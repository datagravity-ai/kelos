# Session Runner Pod Autoscaling

## Summary

Add native autoscaling support for persistent execution mode session runner pods.
The TaskSpawner controller dynamically adjusts StatefulSet replicas based on queued
task count and idle pod availability, eliminating the need for external HPAs or
custom scaling scripts.

## Problem

The current `sessionConfig.replicas` field is static. When task volume fluctuates,
users must manually patch the TaskSpawner to adjust capacity. This leads to either
wasted resources (over-provisioned idle pods) or queued tasks waiting for pods
(under-provisioned). External HPAs cannot solve this because the TaskSpawner
controller overwrites `spec.replicas` every reconciliation cycle.

## API Changes

### New Types (`api/v1alpha1/taskspawner_types.go`)

```go
// SessionAutoscalingConfig configures dynamic replica scaling for persistent
// session pods based on task queue depth.
type SessionAutoscalingConfig struct {
    // MinReplicas is the minimum number of session pods to maintain.
    // The autoscaler will not scale below this value.
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:default=1
    MinReplicas *int32 `json:"minReplicas,omitempty"`

    // MaxReplicas is the maximum number of session pods allowed.
    // The autoscaler will not scale above this value.
    // +kubebuilder:validation:Minimum=1
    MaxReplicas int32 `json:"maxReplicas"`

    // ScaleDownStabilizationSeconds is the duration a pod must be idle
    // before it becomes eligible for scale-down. Prevents flapping
    // when tasks arrive in bursts. Defaults to 300 (5 minutes).
    // +optional
    // +kubebuilder:default=300
    ScaleDownStabilizationSeconds *int32 `json:"scaleDownStabilizationSeconds,omitempty"`
}
```

### SessionConfig Addition

```go
type SessionConfig struct {
    // ... existing fields ...

    // Autoscaling configures dynamic replica scaling based on task queue depth.
    // When set, overrides the static Replicas field for scaling decisions.
    // +optional
    Autoscaling *SessionAutoscalingConfig `json:"autoscaling,omitempty"`
}
```

### Validation Rules

- `maxReplicas` must be >= `minReplicas`
- `minReplicas` defaults to 1 if unset
- `scaleDownStabilizationSeconds` defaults to 300 if unset
- When `autoscaling` is set, the static `replicas` field is ignored
- `maxReplicas` is required (no default — users must choose their upper bound)

## Scaling Logic

### Location

Inside `reconcileSessionStatefulSet` in `internal/controller/taskspawner_controller.go`.

### Scale-Up Algorithm

On each reconciliation when autoscaling is configured:

1. Count tasks in `Queued` phase matching this TaskSpawner (label selector)
2. Count idle session pods (Running, no `kelos.dev/assigned-task` annotation)
3. If `queuedTasks > 0 && idlePods == 0`:
   - `scaleUp = min(queuedTasks, maxReplicas - currentReplicas)`
   - Set `desiredReplicas = currentReplicas + scaleUp`
4. Otherwise, maintain current replica count

### Scale-Down Algorithm

On each reconciliation when autoscaling is configured:

1. List session pods that are idle (no assigned task annotation)
2. For each idle pod, check the `kelos.dev/idle-since` annotation (RFC3339 timestamp)
3. If `time.Since(idleSince) > scaleDownStabilizationSeconds` and
   `currentReplicas > minReplicas`:
   - Decrement `desiredReplicas` by 1 (one pod per reconcile cycle)
4. The StatefulSet controller removes the highest-ordinal pod

### Idle Tracking

The session controller (`session_controller.go`) sets a `kelos.dev/idle-since`
annotation on a pod when it clears `kelos.dev/assigned-task` after task completion.
The annotation is removed when a new task is assigned.

### Suspension

When `spec.suspend: true`, scale to 0 regardless of autoscaling config (existing
behavior preserved).

## New Prometheus Metrics

Registered in `internal/controller/metrics.go`:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kelos_session_pods_ready` | Gauge | namespace, spawner | Ready session pods |
| `kelos_session_pods_busy` | Gauge | namespace, spawner | Pods with assigned tasks |
| `kelos_session_pods_idle` | Gauge | namespace, spawner | Pods without assigned tasks |
| `kelos_tasks_queued` | Gauge | namespace, spawner | Tasks in Queued phase |
| `kelos_session_desired_replicas` | Gauge | namespace, spawner | Computed desired replica count |

Metrics are recorded during each `reconcileSessionStatefulSet` call.

## Example Configuration

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: my-agent
spec:
  executionMode: persistent
  sessionConfig:
    storageSize: 20Gi
    idleTimeout: 30m
    autoscaling:
      minReplicas: 1
      maxReplicas: 8
      scaleDownStabilizationSeconds: 300
```

## Files to Modify

1. `api/v1alpha1/taskspawner_types.go` — Add `SessionAutoscalingConfig` struct and
   field on `SessionConfig`
2. `api/v1alpha1/zz_generated.deepcopy.go` — Regenerated
3. `config/crd/` — Regenerated CRD manifests
4. `internal/controller/taskspawner_controller.go` — Add autoscaling logic in
   `reconcileSessionStatefulSet`
5. `internal/controller/session_controller.go` — Set `kelos.dev/idle-since`
   annotation on task completion
6. `internal/controller/metrics.go` — Register new gauge metrics
7. `internal/controller/taskspawner_controller_test.go` — Unit tests for scaling
   logic
8. `test/integration/session_test.go` — Integration test for autoscaling behavior

## Backward Compatibility

- When `autoscaling` is nil, behavior is unchanged (static `replicas` field used)
- No migration needed for existing TaskSpawners
- The `replicas` field remains valid and functional without autoscaling

## Edge Cases

- **All pods busy, queue grows beyond maxReplicas:** Tasks wait. Desired replicas
  capped at maxReplicas. No special handling needed — existing requeue behavior
  handles this.
- **Rapid burst then silence:** Scale-up is immediate. Scale-down waits for
  stabilization window. Prevents flapping.
- **Pod crash during scale-down:** StatefulSet controller handles pod recreation.
  If replicas were being reduced, the terminated pod simply isn't replaced.
- **minReplicas: 0:** Valid. All pods can be removed when idle. First incoming task
  triggers scale-up, but will experience cold-start delay while pod starts.
