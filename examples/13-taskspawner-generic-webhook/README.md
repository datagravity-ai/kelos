# Generic Webhook TaskSpawner Example

This example demonstrates how to drive a TaskSpawner from an arbitrary HTTP
POST source — anything that can deliver a JSON payload (Sentry, Notion,
Slack, Drata, PagerDuty, internal services). Unlike the GitHub and Linear
webhook sources, the generic webhook has no built-in knowledge of any
particular schema; you describe how to extract fields and what to filter on
using JSONPath expressions.

This example wires up Sentry error events: every `error`-level event from a
Python, Go, or Node project triggers a Claude Code Task that investigates
the stack trace and opens a PR with a fix.

## Prerequisites

1. **Webhook server**: deploy `kelos-webhook-server` with the generic source enabled
2. **Webhook secret**: a Kubernetes Secret containing `SENTRY_WEBHOOK_SECRET`
3. **Sender configuration**: a Sentry (or other system's) webhook integration
   pointed at `/webhook/sentry`

## Setup

### 1. Enable the generic source

Enable `webhookServer.sources.generic` in your Helm values and reference a
Secret holding one HMAC key per source. Each key must be named
`<SOURCE>_WEBHOOK_SECRET` (uppercased), so this example uses
`SENTRY_WEBHOOK_SECRET`.

```yaml
# Helm values
webhookServer:
  sources:
    generic:
      enabled: true
      replicas: 1
      secretName: generic-webhook-secrets
```

### 2. Create the webhook secret

```bash
kubectl create secret generic generic-webhook-secrets \
  --from-literal=SENTRY_WEBHOOK_SECRET=<your-sentry-webhook-secret>
```

To support more sources from the same webhook server, add additional keys
to the same Secret (e.g., `--from-literal=NOTION_WEBHOOK_SECRET=...`).

### 3. Configure the sender

Point the upstream system at `https://your-webhook-domain/webhook/sentry`
and configure it to sign requests with HMAC-SHA256 using the secret above.
The webhook server expects the signature in the `X-Hub-Signature-256`
header with a `sha256=` prefix (the same scheme GitHub uses).

For Sentry: Settings → Integrations → Custom Webhook, with the URL above
and the same secret.

### 4. Deploy the TaskSpawner

```bash
kubectl apply -f taskspawner.yaml
```

## Configuration Details

### `source`

Lowercase alphanumeric identifier (with optional hyphens). Determines:

- The webhook URL path: `/webhook/<source>`
- The env var the server looks up for HMAC validation: `<SOURCE>_WEBHOOK_SECRET`

Each TaskSpawner declares one `source`; multiple TaskSpawners can share a
source to fan out a single event into different work streams.

### `fieldMapping`

A map of template variable name → JSONPath expression evaluated against
the request body. Each key becomes `{{.Key}}` in `promptTemplate` and
`branch`. Lowercase `id`, `title`, `body`, and `url` are also exposed under
their canonical uppercase aliases (`{{.ID}}`, `{{.Title}}`, `{{.Body}}`,
`{{.URL}}`) for compatibility with templates written for the GitHub or
Linear sources.

The **`id` key is required** — it is used to derive a stable delivery ID
for deduplication and to name the spawned Task. Without it, retries of the
same logical event hash to the same body and may dedupe inconsistently.

Missing fields in the payload produce empty strings rather than errors, so
optional mappings (like `level` here) do not block Task creation. Malformed
JSONPath expressions surface as errors so that broken specs are visible.

### `filters`

A list of conditions that **all** must match for a delivery to trigger a
Task (AND semantics). Each filter has a `field` (JSONPath) and exactly one
of:

- `value` — exact string match against the extracted field value
- `pattern` — Go [regexp](https://pkg.go.dev/regexp/syntax) against the
  extracted field value

When `filters` is empty, every delivery triggers a Task. A filter whose
`field` is missing in the payload fails (the delivery is skipped).

## Template Variables

Generic webhook TaskSpawners have access to:

- `{{.ID}}` / `{{.id}}` — value of the mapped `id` field (required)
- `{{.Title}}` / `{{.title}}` — mapped `title` field (if present)
- `{{.Body}}` / `{{.body}}` — mapped `body` field (if present)
- `{{.URL}}` / `{{.url}}` — mapped `url` field (if present)
- `{{.Kind}}` — always `"GenericWebhook"`
- `{{.Payload}}` — the full parsed JSON body (use it for advanced
  templating: `{{.Payload.data.event.platform}}`)
- Any additional key declared in `fieldMapping` — for example, the
  `level` and `platform` keys in this example are available as
  `{{.level}}` and `{{.platform}}`

## Sample Payload

The example matches Sentry error payloads of this shape:

```json
{
  "action": "created",
  "data": {
    "event": {
      "event_id": "abc123def456",
      "title": "ZeroDivisionError: integer division by zero",
      "level": "error",
      "platform": "python"
    },
    "url": "https://sentry.io/organizations/acme/issues/789/"
  }
}
```

With the configured `fieldMapping`, the spawned Task gets:

- `{{.ID}}` = `"abc123def456"`
- `{{.Title}}` = `"ZeroDivisionError: integer division by zero"`
- `{{.URL}}` = `"https://sentry.io/organizations/acme/issues/789/"`
- `{{.level}}` = `"error"`
- `{{.platform}}` = `"python"`

And both `filters` match (level == "error" and platform matches the
regex), so the Task is created.

## Webhook Security

- HMAC-SHA256 signature in `X-Hub-Signature-256` (`sha256=<hex>`)
- The server selects the per-source secret from the `<SOURCE>_WEBHOOK_SECRET`
  env var at request time, so one server can validate many sources from
  one Secret
- Invalid signatures return HTTP 401
- Senders that emit a different signature header are not currently
  supported

## Troubleshooting

- **Tasks not being created** — check the webhook server logs for
  signature failures, missing-secret errors, or filter mismatches.
- **`fieldMapping must include an 'id' key`** — the CRD enforces an `id`
  key in `fieldMapping`. Add one whose JSONPath produces a stable,
  unique identifier per logical event.
- **Same event triggering twice** — verify your `id` mapping resolves to
  a stable string. Falling back to body hashing means JSON encoding
  differences (key order, whitespace) defeat dedup.
- **Filter never matches** — if the field in `filter.field` is missing
  from the payload, the filter fails (silent skip). Use `{{.Payload}}`
  in a debug template to see the actual structure.

## Cleanup

```bash
kubectl delete -f taskspawner.yaml
kubectl delete secret generic-webhook-secrets
```
