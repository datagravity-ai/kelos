# Per-Type Webhook Servers — Implementation Plan

## Architecture

```
                        Ingress (single host)
                       /webhook/github  →  kelos-webhook-server --source=github
                       /webhook/linear  →  kelos-webhook-server --source=linear
                              ↓                       ↓
                  Watches all TaskSpawners    Watches all TaskSpawners
                  with spec.when.githubWebhook  with spec.when.linearWebhook
                              ↓                       ↓
                  Matches event → filters     Matches event → filters
                              ↓                       ↓
                        Creates Tasks           Creates Tasks
```

Single binary, single image, `--source` flag selects behavior. One Deployment per source type.

### Design Decisions

- **Per-type receivers** (not singleton): fault isolation, independent scaling, clean secret boundaries
- **maxConcurrency exceeded**: return 503 + `Retry-After` header (rely on GitHub/Linear retry)
- **Secrets**: deployment-level, one `WEBHOOK_SECRET` env var per receiver deployment
- **Template variables**: support full payload access via `{{.Payload.field.sub}}`
- **GitHub SDK**: Use `github.com/google/go-github/v66` for complete type safety and field coverage

---

## API Design

### TaskSpawner Types

```go
// When defines trigger sources for task spawning.
type When struct {
    GitHubIssues  *GitHubIssues  `json:"githubIssues,omitempty"`
    Cron          *Cron          `json:"cron,omitempty"`
    GitHubWebhook *GitHubWebhook `json:"githubWebhook,omitempty"`
    LinearWebhook *LinearWebhook `json:"linearWebhook,omitempty"`
}

// GitHubWebhook configures webhook-driven task spawning from GitHub events.
type GitHubWebhook struct {
    // Events is the list of GitHub event types to listen for.
    // e.g., "issue_comment", "pull_request_review", "push", "issues"
    Events []string `json:"events"`

    // Filters refine which events trigger tasks. If multiple filters match
    // the same event type, any match triggers a task (OR semantics).
    // If empty, all events in the Events list trigger tasks.
    Filters []GitHubWebhookFilter `json:"filters,omitempty"`
}

type GitHubWebhookFilter struct {
    // Event is the GitHub event type this filter applies to.
    Event string `json:"event"`
    // Action filters by webhook action (e.g., "created", "opened", "submitted").
    Action string `json:"action,omitempty"`
    // BodyContains filters by substring match on the comment/review body.
    BodyContains string `json:"bodyContains,omitempty"`
    // Labels requires the issue/PR to have all of these labels.
    Labels []string `json:"labels,omitempty"`
    // State filters by issue/PR state ("open", "closed").
    State string `json:"state,omitempty"`
    // Branch filters push events by branch name (exact match or glob).
    Branch string `json:"branch,omitempty"`
    // Draft filters PRs by draft status. nil = don't filter.
    Draft *bool `json:"draft,omitempty"`
    // Author filters by the event sender's username.
    Author string `json:"author,omitempty"`
}

// LinearWebhook configures webhook-driven task spawning from Linear events.
type LinearWebhook struct {
    // Types is the list of Linear resource types to listen for.
    // e.g., "Issue", "Comment", "Project"
    Types []string `json:"types"`

    // Filters refine which events trigger tasks (OR semantics within same type).
    Filters []LinearWebhookFilter `json:"filters,omitempty"`
}

type LinearWebhookFilter struct {
    // Type is the Linear resource type this filter applies to.
    Type string `json:"type"`
    // Action filters by webhook action ("create", "update", "remove").
    Action string `json:"action,omitempty"`
    // States filters by Linear workflow state names (e.g., "Todo", "In Progress").
    States []string `json:"states,omitempty"`
    // Labels requires the issue to have all of these labels.
    Labels []string `json:"labels,omitempty"`
    // ExcludeLabels excludes issues with any of these labels.
    ExcludeLabels []string `json:"excludeLabels,omitempty"`
}
```

### Template Variables

All existing template variables remain. New variables available for webhook-sourced tasks:

```
{{.Event}}              — GitHub event type or Linear resource type
{{.Action}}             — Webhook action (e.g., "created", "submitted")
{{.Sender}}             — Username of the person who triggered the event
{{.Ref}}                — Git ref for push events
{{.Payload.field.sub}}  — Access any field in the raw webhook payload
```

---

## Implementation Components

### Core Webhook Infrastructure

#### API Types
- Updated `api/v1alpha1/taskspawner_types.go` with GitHubWebhook and LinearWebhook fields
- Added comprehensive filter types with validation tags
- Generated deepcopy methods and CRD manifests

#### GitHub Filter Package with go-github SDK

`internal/webhook/github_filter.go`:
- **Uses `github.com/google/go-github/v66` SDK** for complete GitHub webhook coverage
- `ParseGitHubWebhook(eventType string, payload []byte) (*GitHubEventData, error)` — parses using `github.ParseWebHook()` for type safety
- `MatchesGitHubEvent(spawner *GitHubWebhook, eventType string, payload []byte) bool` — evaluates filters against typed structs
- **Supports all GitHub event types** with full field access (issues, pull_requests, push, reviews, etc.)
- OR semantics across filters for the same event type
- **Forward compatible** — new GitHub fields automatically available via SDK updates

#### Linear Filter Package

`internal/webhook/linear_filter.go`:
- `MatchesLinearEvent(spawner *LinearWebhook, payload []byte) bool`
- Manual JSON parsing for Linear webhooks (no official Linear Go SDK)

#### Signature Validation

`internal/webhook/signature.go`:
- `ValidateGitHubSignature(payload []byte, signature string, secret []byte) error` — HMAC-SHA256 with `sha256=` prefix parsing
- `ValidateLinearSignature(payload []byte, signature string, secret []byte) error` — Raw HMAC-SHA256 hex digest
- Uses standard crypto/hmac for reliable validation

#### Task Creation Package
`internal/taskbuilder/builder.go`:
- Shared task creation logic usable by both the existing spawner and the webhook server
- Renders `promptTemplate` with webhook-specific template variables
- Stores event metadata as Task annotations for auditability

#### Webhook Server Binary
`cmd/kelos-webhook-server/main.go`:
- HTTP server with controller-runtime manager
- Watches TaskSpawner CRs by source type
- Handles webhook POST requests with signature validation, filtering, and Task creation

#### Helm Chart Templates
- Per-source-type Deployment + Service + RBAC
- Single Ingress with path-based routing
- Configurable via values.yaml

#### Tests
- Unit tests for GitHub filter evaluation using go-github parsed structs
- Unit tests for Linear filter evaluation with manual JSON parsing
- Unit tests for signature validation (GitHub HMAC-SHA256, Linear raw hex)
- Integration tests: webhook POST → Task creation

#### Build Infrastructure
- Dockerfile + Makefile + CI updates

---

## Key Benefits of go-github SDK Integration

### 1. **Complete Field Coverage**
Instead of manually defining JSON structs for a subset of fields, we get every field GitHub sends:
```go
// Before: Limited manual struct
type githubWebhookPayload struct {
    Action string `json:"action"`
    // ... only ~10 fields
}

// After: Full go-github event structs
event := data.RawEvent.(*github.PullRequestEvent)
// Access to ALL fields: event.PullRequest.Assignees, .RequestedReviewers, .Milestone, etc.
```

### 2. **Forward Compatibility**
When GitHub adds new webhook fields:
- **Before**: Manual updates required to our JSON structs
- **After**: Automatic via `go mod update github.com/google/go-github/v66`

### 3. **Type Safety**
- **Before**: `payload["pull_request"]["draft"]` (runtime errors possible)
- **After**: `event.PullRequest.Draft` (compile-time type checking)

### 4. **Rich Template Access**
Users can access any field via `{{.Payload.*}}` while still having backward-compatible variables like `{{.Number}}`, `{{.Title}}`, etc.

---

## Implementation Files

```
go.mod                                           — github.com/google/go-github/v66 dependency
go.sum                                           — updated dependencies
api/v1alpha1/taskspawner_types.go                — GitHubWebhook, LinearWebhook types
api/v1alpha1/zz_generated.deepcopy.go            — generated deepcopy methods
internal/webhook/signature.go                    — HMAC signature validation
internal/webhook/github_filter.go                — GitHub event parsing & filtering
internal/webhook/linear_filter.go                — Linear event parsing & filtering
internal/taskbuilder/builder.go                  — shared task creation logic
internal/webhook/handler.go                      — HTTP webhook handler
cmd/kelos-webhook-server/main.go                 — webhook server binary
cmd/kelos-webhook-server/Dockerfile              — container image
internal/manifests/charts/kelos/templates/webhook-server.yaml   — Helm deployment
internal/manifests/charts/kelos/templates/webhook-ingress.yaml  — Helm ingress
internal/manifests/charts/kelos/values.yaml      — Helm configuration
examples/10-taskspawner-github-webhook/          — GitHub webhook example
examples/11-taskspawner-linear-webhook/           — Linear webhook example
test/integration/webhook_handler_test.go          — integration tests
internal/webhook/*_test.go                        — unit tests
```