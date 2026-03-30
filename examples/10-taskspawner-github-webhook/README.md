# GitHub Webhook TaskSpawner

This example shows how to configure a TaskSpawner that reacts to GitHub webhook
events in real time instead of polling the GitHub API.

## How It Works

A global webhook server (`kelos-webhook-server --source=github`) watches all
TaskSpawners with a `githubWebhook` source. When GitHub sends a webhook, the
server matches it against configured filters and creates Tasks directly.

## Setup

### 1. Enable the webhook server in your Helm values

```yaml
webhookServer:
  sources:
    github:
      enabled: true
      secretName: github-webhook-secret
  ingress:
    enabled: true
    host: webhooks.example.com
```

### 2. Create the webhook secret

```bash
kubectl create secret generic github-webhook-secret \
  --namespace kelos-system \
  --from-literal=webhook-secret=YOUR_SECRET_HERE
```

### 3. Configure the GitHub webhook

In your GitHub repository settings, add a webhook:
- **Payload URL**: `https://webhooks.example.com/webhook/github`
- **Content type**: `application/json`
- **Secret**: The same value used in step 2
- **Events**: Select the events your TaskSpawners listen for (e.g., Issue comments, Pull request reviews)

### 4. Apply the TaskSpawner

```bash
kubectl apply -f taskspawner.yaml
```

## Filter Reference

Filters use OR semantics — if any filter matches, a task is created.

| Field | Description | Example |
|-------|-------------|---------|
| `event` | GitHub event type (required) | `issue_comment` |
| `action` | Webhook action | `created`, `opened`, `submitted` |
| `bodyContains` | Substring match on comment/review body | `/fix` |
| `labels` | Require all listed labels | `["bug", "priority/high"]` |
| `state` | Issue/PR state | `open`, `closed` |
| `branch` | Push event branch (supports globs) | `main`, `release-*` |
| `draft` | PR draft status | `true`, `false` |
| `author` | Event sender username | `admin` |

## Template Variables

In addition to the standard template variables, webhook-sourced tasks have:

| Variable | Description |
|----------|-------------|
| `{{.Event}}` | GitHub event type (e.g., `issue_comment`) |
| `{{.Action}}` | Webhook action (e.g., `created`) |
| `{{.Sender}}` | Username who triggered the event |
| `{{.Ref}}` | Git ref (push events) |
| `{{.Payload.field.sub}}` | Access any field in the raw webhook payload |
