# Example 08: TaskPipeline CRD

This example demonstrates the TaskPipeline CRD, which orchestrates multi-stage
agent workflows as a single Kubernetes resource.

## Key Features

### Declarative Stage DAG

Stages form a directed acyclic graph via `dependsOn`. The pipeline controller
advances stages in dependency order, creating child Tasks only when all
prerequisites are met.

### Conditionals (`when`)

CEL expressions gate stage execution based on prior results:

```yaml
when: 'stages.test.phase == "Succeeded"'
when: 'int(stages.lint.results.errorCount) == 0'
```

If the predicate evaluates to `false`, the stage is marked `Skipped` and
downstream stages treat it as resolved.

### No-Compute Gates (`waitFor`)

Gates pause stages without running any Pods. The pipeline controller handles
the wait via its reconcile loop:

| Gate | Mechanism | How it clears |
|------|-----------|---------------|
| `duration` | Timer | Controller requeues at deadline |
| `githubCheck` | Poll | Controller calls GitHub Checks API |
| `webhook` | Push | Webhook server patches status |
| `approval` | Push | User patches status or sends webhook |

### Matrix Fan-Out

```yaml
matrix:
  params:
    suite: [unit, integration, e2e]
```

Creates one Task per parameter combination. The stage succeeds when all
expanded tasks succeed.

## Files

- `pipeline.yaml` — Full pipeline with conditionals, matrix, and gates
- `pipeline-webhook-gate.yaml` — Pipeline with webhook-triggered gate

## Usage

```bash
# Create the pipeline
kubectl apply -f pipeline.yaml

# Watch progress
kubectl get taskpipeline feature-delivery -w

# View stage details
kubectl get taskpipeline feature-delivery -o jsonpath='{.status.stages}' | jq .

# View child tasks
kubectl get tasks -l kelos.dev/pipeline=feature-delivery

# Approve a gate (via status patch)
kubectl patch taskpipeline feature-delivery --type=merge --subresource=status \
  -p '{"status":{"stages":[{"name":"approve-deploy","gateStatus":{"cleared":true,"clearedBy":"manual"}}]}}'

# Clean up
kubectl delete taskpipeline feature-delivery
```

## Comparison with Example 07

Example 07 uses three separate Task objects connected by `dependsOn`:
- No unified status view
- Manual cleanup of each Task
- No conditional execution
- No external gates

TaskPipeline provides all of this as a single resource with atomic lifecycle
management.
