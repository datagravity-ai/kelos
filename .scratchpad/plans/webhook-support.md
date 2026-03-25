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
- [ ] Define `WebhookEvent` CRD in `api/v1alpha1/webhookevent_types.go`
  - Source (github, slack, linear, etc.)
  - Payload (raw JSON bytes)
  - ReceivedAt timestamp
  - Processed bool status
- [ ] Generate CRD manifests with `make manifests`
- [ ] Add to scheme registration

### Phase 2: Webhook Receiver HTTP Server
- [ ] Create `internal/webhook/receiver.go`
  - HTTP server listening on `/webhook/:source` (e.g., `/webhook/github`)
  - Validates webhook signatures (GitHub HMAC-SHA256)
  - Creates WebhookEvent CRD instances
  - Returns 202 Accepted
- [ ] Create `cmd/kelos-webhook-receiver/main.go`
  - Standalone binary for webhook server
  - Similar structure to `cmd/kelos-spawner`

### Phase 3: GitHub Webhook Source
- [ ] Create `internal/source/github_webhook.go`
  - Implements `Source` interface
  - `Discover()` lists unprocessed WebhookEvent resources with source=github
  - Parses GitHub webhook payloads into WorkItem
  - Marks events as processed after discovery
- [ ] Update `api/v1alpha1/taskspawner_types.go`
  - Add `GitHubWebhook` field to `When` struct
  - Include repo, secret ref for webhook validation

### Phase 4: Integration & Controller Updates
- [ ] Update `cmd/kelos-spawner/main.go`
  - Add webhook source creation logic
- [ ] Add RBAC permissions for WebhookEvent resources
- [ ] Update deployment manifests to include webhook receiver

### Phase 5: Documentation & Examples
- [ ] Add example TaskSpawner YAML using GitHub webhooks
- [ ] Document webhook setup instructions
- [ ] Update README with webhook architecture

### Phase 6: Tests
- [ ] Unit tests for webhook receiver
- [ ] Unit tests for GitHub webhook source
- [ ] Integration test for end-to-end flow

## Success Criteria

- Can receive GitHub webhooks and create WebhookEvent CRDs
- TaskSpawner with `when.githubWebhook` discovers work items from webhooks
- Compatible with `main` branch for upstream PR
- Tests pass
