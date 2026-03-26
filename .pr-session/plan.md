## Prompt

Now, I want you to check out a AIE-14 branch, OFF FROM the previous webhook branch. This is for linear ticket AIE-14. The goal is to support linear webhooks in Kelos. NOT POLLING. Look through open Kelos issues (https://github.com/kelos-dev/kelos/issues?q=is%3Aissue%20state%3Aopen%20linear) for linear. There are likely some relevant issues, but make sure they are actually very relevant / overlapping. That could be closed out by doing this. Ask any clarifying questions, and let's start. And to be clear, this PR will be going onto our own fork, and will point into the previous webhook branch (tim-aie-13) instead of `prod`.

## Architecture Decision

Linear webhook support will follow the same CRD-based queue pattern as GitHub webhooks (from tim-aie-13). Webhook payloads are stored as WebhookEvent CRDs, then processed by a LinearWebhookSource that implements the Source interface.

**Key decisions:**
- Reuse existing WebhookEvent CRD (source field will be "linear")
- Reuse existing webhook receiver with Linear signature validation support
- Optional signature validation (skip if LINEAR_WEBHOOK_SECRET not set)
- Process Issue create/update events only
- State filtering is opt-in (if not configured, accept non-terminal states)
- Map Linear issues to existing WorkItem struct

**Related upstream issues (polling-based, not closed by this PR):**
- #764 - Linear polling integration
- #391 - Linear polling integration
(Our webhook-based approach is complementary to future polling support)

## Plan

### Phase 1: Extend WebhookEvent Support for Linear
- [x] Update webhook receiver to handle Linear signature validation
  - Add `validateLinearSignature()` function in `cmd/kelos-webhook-receiver/main.go`
  - Check for `LINEAR_WEBHOOK_SECRET` env var (optional, skip if not set)
  - Validate `X-Linear-Signature` header using HMAC-SHA256
  - Update `handle()` to call validator when source is "linear"
- [x] Add unit tests for Linear signature validation

### Phase 2: Linear Webhook Source Implementation
- [x] Create `internal/source/linear_webhook.go`
  - Implement `LinearWebhookSource` struct with Client, Namespace, filters
  - Add `LinearWebhookPayload` struct for Linear webhook format
  - Implement `Discover()` to list unprocessed WebhookEvent with source=linear
  - Parse Linear webhook payloads (Issue events only)
  - Filter by states (if configured), exclude terminal states (Done, Canceled)
  - Convert to WorkItem (ID format: "TEAM-123", Kind = state name)
  - Mark events as processed using DeepCopy pattern
- [x] Create `internal/source/linear_webhook_test.go`
  - Unit tests for payload parsing (Issue create/update)
  - Tests for state filtering
  - Tests for label filtering
  - End-to-end Discover() test with fake client

### Phase 3: TaskSpawner CRD Updates
- [ ] Update `api/v1alpha1/taskspawner_types.go`
  - Add `LinearWebhook` struct with filters (namespace, states, labels, excludeLabels)
  - Add `When.LinearWebhook` field
  - Mirror GitHub webhook pattern for consistency

### Phase 4: Spawner Integration
- [ ] Update `cmd/kelos-spawner/main.go`
  - Extend `buildSource()` to handle `LinearWebhook` case
  - Create `LinearWebhookSource` instance with k8s client

### Phase 5: Integration Tests
- [ ] Create `test/integration/linear_webhook_test.go`
  - Test Linear webhook discovery and processing
  - Test state filtering (include/exclude based on config)
  - Test label filtering
  - Test event marking as processed
  - Test multiple Linear webhooks in same namespace

### Phase 6: Documentation & Examples
- [ ] Update `docs/webhooks.md`
  - Add Linear webhook section
  - Document signature validation setup
  - Document event types (Issue create/update)
  - Document state filtering behavior
- [ ] Create `examples/taskspawner-linear-webhook.yaml`
  - Example TaskSpawner with linearWebhook config
  - Webhook receiver deployment with LINEAR_WEBHOOK_SECRET
  - RBAC configuration
  - Instructions for Linear webhook setup

### Phase 7: RBAC & Generated Code
- [ ] RBAC permissions already exist from Phase 1 (WebhookEvent resources)
- [ ] Run `make update` to regenerate CRD manifests and deepcopy code
- [ ] Verify all tests pass (`make test` and `make test-integration`)

## Success Criteria

- [ ] Can receive Linear webhooks and create WebhookEvent CRDs
- [ ] TaskSpawner with `when.linearWebhook` discovers work items from Linear webhooks
- [ ] Optional signature validation works (validates if secret set, skips if not)
- [ ] State filtering works correctly
- [ ] All unit and integration tests pass
- [ ] Compatible with tim-aie-13 branch (builds on GitHub webhook foundation)
- [ ] Ready for PR to datagravity-ai/kelos targeting tim-aie-13 base branch
