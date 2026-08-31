# Proposal: TaskPipeline CRD

**Issue:** https://github.com/kelos-dev/kelos/issues/983
**Status:** Draft
**Authors:** @gravity-agent

## Summary

Introduce a **TaskPipeline** Custom Resource Definition that makes multi-stage
agent workflows a first-class concept in Kelos. A TaskPipeline declares an
ordered DAG of stages, where each stage can contain parallel Tasks, conditional
execution predicates, and external gates that pause without consuming compute.

## Motivation

Today, pipelines are expressed as loose collections of Tasks connected by
`dependsOn` references (see `examples/07-task-pipeline/`). This pattern:

- Has no unified observability (operators must inspect each Task individually)
- Provides no pipeline-level lifecycle (timeout, retry-from-failed, atomic cleanup)
- Cannot express conditional branching (run stage X only if stage Y produced result Z)
- Cannot wait for external signals without burning compute (CI pass, approval, webhook)
- Cannot fan-out dynamically (matrix expansion over N targets)

## Design Goals

1. **Declarative pipeline definition** as a single Kubernetes resource
2. **Conditional execution** via CEL predicates on prior stage results
3. **No-compute waits** for external signals (gates) without Pods running
4. **External status triggers** via webhooks (push) and polling (pull)
5. **Fan-out** via matrix parameter expansion
6. **Backward compatible** — existing `dependsOn` pipelines continue working

## Non-Goals (for initial implementation)

- TaskSpawner `pipelineTemplate` integration (follow-up)
- Cross-pipeline dependencies
- Pipeline-of-pipelines nesting
- GUI/dashboard (rely on `kubectl get taskpipeline`)

---

## API Design

### TaskPipelineSpec

```yaml
apiVersion: kelos.dev/v1alpha2
kind: TaskPipeline
metadata:
  name: example
spec:
  timeout: 2h                      # Pipeline-level deadline
  ttlSecondsAfterFinished: 3600    # Auto-cleanup
  suspend: false                   # Pause advancement

  stages:
    - name: build
      tasks:
        - name: build
          worker: { ... }
          promptTemplate: "Build the project..."

    - name: test
      dependsOn: [build]
      when: 'stages.build.results.exitCode == "0"'  # CEL conditional
      tasks:
        - name: unit-tests
          promptTemplate: "Run unit tests..."

    - name: wait-for-ci
      dependsOn: [test]
      waitFor:                     # No-compute gate
        githubCheck:
          owner: myorg
          repo: myapp
          ref: '{{.Stages.build.Results.commit}}'
          checkName: "e2e-suite"
          pollInterval: 30s
      tasks:
        - name: ack
          promptTemplate: "CI passed."

    - name: deploy
      dependsOn: [wait-for-ci]
      waitFor:
        approval:
          approvers: [oncall-team]
          message: "Approve production deploy?"
      tasks:
        - name: deploy
          promptTemplate: "Deploy to production."
```

### Stages

Each stage declares:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique identifier within the pipeline |
| `dependsOn` | []string | Stage names that must reach terminal (Succeeded/Skipped) |
| `when` | string | CEL expression; if false, stage is Skipped |
| `waitFor` | Gate | External gate; pipeline pauses without compute |
| `tasks` | []PipelineTaskTemplate | Parallel tasks within the stage |
| `matrix` | MatrixSpec | Fan-out over parameter combinations |

### Conditionals (`when`)

CEL expressions evaluated against a `stages` map:

```cel
stages.test.results.coverage > "80"
stages.test.phase == "Succeeded"
stages.analyze.results.severity in ["low", "medium"]
int(stages.lint.results.errorCount) == 0
```

**Why CEL over Go templates:**
- CEL is already a transitive dependency (kubebuilder XValidation)
- Purpose-built for boolean predicates with bounded execution
- Type-safe with a proper boolean return type
- Kubernetes ecosystem standard for policy expressions

When a `when` predicate evaluates to `false`, the stage is marked `Skipped`.
Skipped stages count as resolved for downstream `dependsOn` references,
allowing the pipeline to continue.

### Gates (`waitFor`)

Gates pause a stage **without creating any Pods**. The pipeline controller
handles the wait via its reconcile loop (same pattern as Task's
`checkDependencies` / `checkBudgetAdmission`).

| Gate Type | Mechanism | Resolution |
|-----------|-----------|------------|
| `webhook` | Push: incoming webhook patches status | Webhook handler finds waiting pipelines via label index |
| `githubCheck` | Pull: controller polls GitHub Checks API | RequeueAfter at pollInterval |
| `approval` | Push: user patches status or sends webhook | Status subresource PATCH or annotation |
| `duration` | Timer: controller computes deadline | RequeueAfter with remaining duration |

#### Webhook Gate

```yaml
waitFor:
  webhook:
    source: "deploy-notify"          # Matches GenericWebhook source path
    filters:
      - field: "$.status"
        value: "success"
      - field: "$.environment"
        value: "production"
```

The webhook server (`kelos-webhook-server`) is extended to check for waiting
pipeline gates after its existing task-creation logic. Discovery uses a label
(`kelos.dev/gate-source=<source>`) added to pipelines with active webhook gates.

#### GitHub Check Gate

```yaml
waitFor:
  githubCheck:
    owner: myorg
    repo: myapp
    ref: '{{.Stages.build.Results.commit}}'
    checkName: "e2e-suite"
    targetConclusion: success       # Default
    pollInterval: 30s               # Default
```

The pipeline controller calls the GitHub Checks API on each reconcile and
requeues at `pollInterval` until the check reaches the target conclusion.

#### Approval Gate

```yaml
waitFor:
  approval:
    approvers: [alice, bob, oncall-team]
    message: "Approve production deployment?"
```

Clearable via:
1. `kubectl patch taskpipeline <name> --type=merge --subresource=status -p '{"status":{"stages":[{"name":"deploy","gateStatus":{"cleared":true}}]}}'`
2. Webhook delivery matching the pipeline
3. Future: CLI command (`kelos approve <pipeline> <stage>`)

#### Duration Gate

```yaml
waitFor:
  duration: 5m    # Wait 5 minutes before proceeding
```

Simple timer. The controller computes `stageStartTime + duration` and requeues
with the remaining time.

### Matrix Fan-Out

```yaml
stages:
  - name: scan
    matrix:
      params:
        repo: [auth-service, billing-service, gateway-service]
        env: [staging, prod]
    tasks:
      - name: scan
        promptTemplate: |
          Scan {{.Matrix.repo}} in {{.Matrix.env}} for vulnerabilities.
```

This creates 6 tasks (3 repos x 2 envs). Each task receives its matrix
parameter values in the template context. The stage succeeds when all
matrix-expanded tasks succeed.

### Status

```yaml
status:
  phase: Running
  startTime: "2026-07-28T10:00:00Z"
  stages:
    - name: build
      phase: Succeeded
      taskNames: [example-build-build]
      results:
        commit: "abc123"
        image: "myorg/myapp:abc123"
      startTime: "2026-07-28T10:00:00Z"
      completionTime: "2026-07-28T10:05:00Z"
    - name: wait-for-ci
      phase: Waiting
      gateStatus:
        cleared: false
        lastPollTime: "2026-07-28T10:05:30Z"
      message: "Waiting for check 'e2e-suite' on abc123"
  conditions:
    - type: Progressing
      status: "True"
      reason: StageWaiting
      message: "Stage 'wait-for-ci' waiting for GitHub check"
```

---

## Controller Design

### Reconcile Loop

```
Reconcile(pipeline):
  1. Terminal? → enforce TTL → return
  2. Suspended? → set Paused → return
  3. Timeout exceeded? → fail running tasks → fail pipeline → return
  4. For each stage (topological order):
     a. Already terminal? → skip
     b. Dependencies not resolved? → stay Pending
        - Any dep Failed (and no when-skip path)? → fail this stage
     c. Evaluate `when` CEL predicate:
        - false → Skipped (resolved for downstream)
     d. Check `waitFor` gate:
        - Not cleared → Waiting, compute RequeueAfter
     e. Create child Tasks (expand matrix, render templates)
        - SetControllerReference → owner is pipeline
        - Set stage to Running
     f. Running? → check child Tasks:
        - All Succeeded → merge results → Succeeded
        - Any Failed → Failed
  5. Derive pipeline phase from stage statuses
  6. Update status → return RequeueAfter
```

### Watch Setup

```go
ctrl.NewControllerManagedBy(mgr).
    For(&kelos.TaskPipeline{}).
    Owns(&kelos.Task{}).
    Complete(r)
```

`Owns(&kelos.Task{})` provides:
- Automatic reconcile when any child Task status changes
- Cascading garbage collection when pipeline is deleted

### No-Compute Pattern

The same pattern used by `TaskReconciler` for dependencies/budget:

```
Stage enters Waiting → no Tasks created → no Jobs → no Pods → zero compute
Controller sets RequeueAfter or awaits watch trigger
Gate clears → next reconcile creates Tasks → compute starts
```

---

## Webhook Integration

### Flow

```
Webhook arrives at kelos-webhook-server
  ↓
Existing: processWebhook() → create Tasks for matching TaskSpawners
  ↓
New: clearMatchingGates() → find pipelines waiting for this source
  ↓
For each matching pipeline/stage:
  Status PATCH: stage.gateStatus.cleared = true
  ↓
Watch triggers reconcile → pipeline advances
```

### Pipeline Discovery

When the pipeline controller sets a stage to `Waiting` with a webhook gate,
it adds a label to the TaskPipeline:

```
kelos.dev/gate-source: <source-name>
```

The webhook handler uses this label for efficient lookup:

```go
client.List(ctx, &pipelines, client.MatchingLabels{
    "kelos.dev/gate-source": source,
})
```

The label is removed when the gate clears.

---

## Implementation Phases

### Phase 1: Core Pipeline

- CRD types (`api/v1alpha2/taskpipeline_types.go`)
- Basic reconciler: stage ordering, Task creation, result merging
- Owner references, cascading delete
- Template rendering with `{{.Stages.X.Results.Y}}`
- `make update` for CRD generation
- Integration tests

### Phase 2: Conditionals

- CEL environment + evaluation (`internal/controller/pipeline_cel.go`)
- `when` predicate in reconcile loop
- `Skipped` phase handling
- Unit tests

### Phase 3: Duration + GitHubCheck Gates

- Gate evaluation logic (`internal/controller/pipeline_gate.go`)
- Duration gate (timer)
- GitHubCheck gate (API polling)
- `GateStatus` tracking
- Tests with mock GitHub API

### Phase 4: Webhook Push Gates

- Webhook handler extension (`internal/webhook/pipeline_gate.go`)
- Label-based pipeline discovery
- Filter matching + status patching
- Integration tests

### Phase 5: Lifecycle + Matrix

- Timeout enforcement
- TTL cleanup
- Suspend/resume
- Matrix expansion
- Approval gate

---

## File Layout

| File | Purpose |
|------|---------|
| `api/v1alpha2/taskpipeline_types.go` | CRD type definitions |
| `internal/controller/taskpipeline_controller.go` | Main reconciler |
| `internal/controller/pipeline_cel.go` | CEL predicate evaluation |
| `internal/controller/pipeline_gate.go` | Gate evaluation (poll, duration) |
| `internal/webhook/pipeline_gate.go` | Webhook push gate clearing |
| `examples/08-task-pipeline-crd/` | Example manifests |

---

## Relationship to Other Proposals

| Issue | Relationship |
|-------|-------------|
| #747 (conditional deps) | Subsumed by `when` predicates |
| #816 (approvalPolicy) | Complementary; approval gates within pipelines |
| #829 (parent-child tasks) | Complementary; TaskPipeline is declarative, #829 is imperative |
| #835 (taskCompletions source) | Different scope; could trigger pipelines |
| #730 (retryStrategy) | Complementary; stage-level retry as future extension |

## Example: Complete Pipeline with All Features

```yaml
apiVersion: kelos.dev/v1alpha2
kind: TaskPipeline
metadata:
  name: feature-delivery
spec:
  timeout: 4h
  ttlSecondsAfterFinished: 86400
  stages:
    - name: plan
      tasks:
        - name: plan
          worker:
            type: claude-code
            credentials: { type: api-key, secretRef: { name: claude } }
            workspaceRef: { name: my-workspace }
          promptTemplate: |
            Read the requirements and create an implementation plan.
            Output key decisions as results.

    - name: implement
      dependsOn: [plan]
      tasks:
        - name: code
          worker:
            type: claude-code
            credentials: { type: api-key, secretRef: { name: claude } }
            workspaceRef: { name: my-workspace }
          branch: feature/auto
          promptTemplate: |
            Implement the plan: {{index .Stages "plan" "Results" "plan"}}
            Commit and push. Report the branch and commit SHA.

    - name: test
      dependsOn: [implement]
      matrix:
        params:
          suite: [unit, integration, e2e]
      tasks:
        - name: test
          worker:
            type: claude-code
            credentials: { type: api-key, secretRef: { name: claude } }
            workspaceRef: { name: my-workspace }
          branch: feature/auto
          promptTemplate: |
            Run the {{.Matrix.suite}} test suite. Report pass/fail.

    - name: wait-ci
      dependsOn: [test]
      when: 'stages.test.phase == "Succeeded"'
      waitFor:
        githubCheck:
          owner: myorg
          repo: myapp
          ref: '{{index .Stages "implement" "Results" "commit"}}'
          checkName: ci-full
          pollInterval: 60s
      tasks:
        - name: ack
          worker:
            type: claude-code
            credentials: { type: api-key, secretRef: { name: claude } }
          promptTemplate: "CI passed for all test suites."

    - name: deploy
      dependsOn: [wait-ci]
      waitFor:
        approval:
          approvers: [platform-team]
          message: "All tests and CI passed. Approve production deploy?"
      tasks:
        - name: deploy
          worker:
            type: claude-code
            credentials: { type: api-key, secretRef: { name: claude } }
            workspaceRef: { name: my-workspace }
          branch: feature/auto
          promptTemplate: |
            Open a PR from branch {{index .Stages "implement" "Results" "branch"}}
            and deploy to production once merged.
```
