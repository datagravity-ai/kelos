# Linear Webhook TaskSpawner

This example shows how to configure a TaskSpawner that reacts to Linear webhook
events in real time, creating tasks when new issues match specific criteria.

## How It Works

A global webhook server (`kelos-webhook-server --source=linear`) watches all
TaskSpawners with a `linearWebhook` source. When Linear sends a webhook, the
server matches it against configured filters and creates Tasks directly.

## Setup

### 1. Enable the webhook server in your Helm values

```yaml
webhookServer:
  sources:
    linear:
      enabled: true
      secretName: linear-webhook-secret
  ingress:
    enabled: true
    host: webhooks.example.com
```

### 2. Create the webhook secret

```bash
kubectl create secret generic linear-webhook-secret \
  --namespace kelos-system \
  --from-literal=webhook-secret=YOUR_SECRET_HERE
```

### 3. Configure the Linear webhook

In your Linear team settings, add a webhook:
- **URL**: `https://webhooks.example.com/webhook/linear`
- **Secret**: The same value used in step 2
- **Resource types**: Select the types your TaskSpawners listen for (e.g., Issue)
- **Events**: Create, Update

### 4. Apply the TaskSpawner

```bash
kubectl apply -f taskspawner.yaml
```

## Filter Reference

Filters use OR semantics — if any filter matches, a task is created.

| Field | Description | Example |
|-------|-------------|---------|
| `type` | Linear resource type (required) | `Issue`, `Comment`, `Project` |
| `action` | Webhook action | `create`, `update`, `remove` |
| `states` | Issue workflow states | `["Todo", "In Progress"]` |
| `labels` | Require all listed labels | `["bug", "urgent"]` |
| `excludeLabels` | Exclude issues with any of these labels | `["wontfix"]` |

## Template Variables

In addition to the standard template variables, webhook-sourced tasks have:

| Variable | Description |
|----------|-------------|
| `{{.Event}}` | Linear resource type (e.g., `Issue`) |
| `{{.Action}}` | Webhook action (e.g., `create`) |
| `{{.ID}}` | Linear issue ID |
| `{{.Title}}` | Issue title |
| `{{.Body}}` | Issue description |
| `{{.URL}}` | Linear issue URL |
| `{{.State}}` | Current workflow state |
| `{{.Labels}}` | Comma-separated labels |
| `{{.Payload.field.sub}}` | Access any field in the raw webhook payload |