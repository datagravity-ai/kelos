# Webhook Support for Kelos (AIE-13)

## Prompt

Add webhook support to Kelos as an alternative to API polling. Start with GitHub webhooks, allowing TaskSpawner to receive push events instead of polling the GitHub API.

## Architecture Decision: CRD-Based Queue

We'll use a **CRD-based queue** (WebhookEvent custom resources) to store incoming webhooks. This is consistent with Kelos' existing architecture where all state lives in Kubernetes CRDs backed by etcd.

**Alternative approaches considered:**
- In-memory queue with ConfigMap snapshots (faster but loses events on crash)
- External queue like Redis/RabbitMQ (adds infrastructure dependency)

## Plan

### Phase 1: WebhookEvent CRD
- [x] Define `WebhookEvent` CRD in `api/v1alpha1/webhookevent_types.go`
  - Source (github, slack, linear, etc.)
  - Payload (raw JSON bytes)
  - ReceivedAt timestamp
  - Processed bool status
- [x] Add to scheme registration
- [ ] Generate CRD manifests with `make manifests` (requires Go environment)

### Phase 2: Webhook Receiver HTTP Server
- [x] Create `cmd/kelos-webhook-receiver/main.go`
  - HTTP server listening on `/webhook/:source` (e.g., `/webhook/github`)
  - Validates webhook signatures (GitHub HMAC-SHA256)
  - Creates WebhookEvent CRD instances
  - Returns 202 Accepted
  - Standalone binary for webhook server

### Phase 3: GitHub Webhook Source
- [x] Create `internal/source/github_webhook.go`
  - Implements `Source` interface
  - `Discover()` lists unprocessed WebhookEvent resources with source=github
  - Parses GitHub webhook payloads into WorkItem
  - Marks events as processed after discovery
- [x] Update `api/v1alpha1/taskspawner_types.go`
  - Add `GitHubWebhook` field to `When` struct
  - Include namespace, labels, excludeLabels filters

### Phase 4: Integration & Controller Updates
- [x] Update `cmd/kelos-spawner/main.go`
  - Add webhook source creation logic in `buildSource()`
  - Pass k8s client to GitHubWebhookSource
- [x] Add RBAC permissions for WebhookEvent resources (in deployment manifests)

### Phase 5: Documentation & Examples
- [x] Add example TaskSpawner YAML using GitHub webhooks
- [x] Document webhook setup instructions
- [x] Document webhook architecture and design decisions
- [x] Include webhook receiver deployment manifests
- [x] Comparison with API polling approach

### Phase 6: Tests
- [x] Unit tests for webhook receiver
- [x] Unit tests for GitHub webhook source
- [x] Integration test for end-to-end flow (uses envtest)

## Completed

✅ **Draft PR #15 created**: https://github.com/datagravity-ai/kelos/pull/15

### What was built:
1. **WebhookEvent CRD** - Stores webhook payloads as Kubernetes resources
2. **Webhook receiver** - HTTP server that creates WebhookEvent CRDs
3. **GitHubWebhookSource** - Source implementation that discovers WorkItems from webhooks
4. **TaskSpawner integration** - Spawner supports `githubWebhook` in `When` struct
5. **Documentation** - Complete setup guide and examples

### Completed:
- ✅ Generate CRD manifests (`make update`)
- ✅ Generate deep copy code
- ✅ Unit tests
- ✅ Integration tests
- ✅ RBAC permissions

## Success Criteria

- ✅ Can receive GitHub webhooks and create WebhookEvent CRDs
- ✅ TaskSpawner with `when.githubWebhook` discovers work items from webhooks
- ✅ Compatible with `main` branch for upstream PR
- ✅ Unit and integration tests passing
- ✅ All CI checks should pass
