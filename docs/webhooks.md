# Webhook Support

Kelos supports webhook-based work item discovery as an alternative to API polling. Instead of repeatedly querying external APIs (GitHub, Slack, Linear, etc.), Kelos can receive push notifications via webhooks and process them on-demand.

## Architecture

The webhook system consists of three components:

### 1. WebhookEvent CRD (Queue)

Webhook payloads are stored as `WebhookEvent` custom resources in Kubernetes. This CRD-based queue provides:

- **Persistence**: Events survive pod restarts (stored in etcd)
- **Auditability**: All events are visible via `kubectl get webhookevents`
- **Processing tracking**: Events are marked as processed after discovery

**Alternative approaches considered:**
- In-memory queue with ConfigMap snapshots (faster but loses events on crash)
- External queue like Redis/RabbitMQ (adds infrastructure dependency)

We chose CRD-based queuing because it's consistent with Kelos' existing architecture where all state lives in Kubernetes resources.

### 2. Webhook Receiver (kelos-webhook-receiver)

An HTTP server that:
- Listens on `/webhook/:source` (e.g., `/webhook/github`, `/webhook/slack`)
- Validates webhook signatures (e.g., GitHub HMAC-SHA256)
- Creates `WebhookEvent` CRD instances
- Returns 202 Accepted

Deploy as a Deployment with a LoadBalancer Service to expose it publicly.

### 3. Webhook Source Implementation

Source implementations (e.g., `GitHubWebhookSource`) that:
- List unprocessed `WebhookEvent` resources
- Parse webhook payloads into `WorkItem` format
- Apply filters (labels, state, etc.)
- Mark events as processed

## GitHub Webhook Setup

### 1. Deploy the webhook receiver

```bash
kubectl apply -f examples/taskspawner-github-webhook.yaml
```

This creates:
- `kelos-webhook-receiver` Deployment
- LoadBalancer Service
- RBAC for creating WebhookEvent resources

### 2. Get the external URL

```bash
kubectl get service kelos-webhook-receiver -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

### 3. Configure GitHub webhook

In your GitHub repository settings:
1. Go to Settings → Webhooks → Add webhook
2. **Payload URL**: `http://<your-loadbalancer>/webhook/github`
3. **Content type**: `application/json`
4. **Secret**: Set a secret and store it in the `github-webhook-secret` Secret
5. **Events**: Select "Issues" and "Pull requests"
6. Click "Add webhook"

### 4. Create a TaskSpawner with githubWebhook

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: my-webhook-spawner
spec:
  when:
    githubWebhook:
      namespace: default
      labels:
        - "kelos-task"
  taskTemplate:
    type: claude-code
    credentials:
      type: api-key
      secretRef:
        name: anthropic-api-key
    workspaceRef:
      name: my-workspace
    promptTemplate: |
      {{ .Title }}
      {{ .Body }}
```

## Differences from API Polling

| Aspect | API Polling | Webhooks |
|--------|-------------|----------|
| **Latency** | Poll interval (e.g., 5 minutes) | Near-instant |
| **API Rate Limits** | Consumes API quota | No API calls for discovery |
| **Complexity** | Simple (no external endpoint) | Requires public endpoint + signature validation |
| **Missed Events** | Can miss events between polls | Events queued reliably |
| **Infrastructure** | Just spawner pod | Spawner + webhook receiver + LoadBalancer |

## Webhook Signature Validation

For GitHub webhooks, the receiver validates the `X-Hub-Signature-256` header using HMAC-SHA256.

Set the `GITHUB_WEBHOOK_SECRET` environment variable on the webhook receiver to enable validation:

```yaml
env:
- name: GITHUB_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: github-webhook-secret
      key: secret
```

If the secret is not set, signature validation is skipped (development mode only).

## Future Sources

The webhook architecture is designed to support multiple sources:
- **Slack**: `/webhook/slack` for slash commands or event subscriptions
- **Linear**: `/webhook/linear` for issue events
- **Salesforce**: `/webhook/salesforce` for custom events

Each source requires:
1. A Source implementation (similar to `GitHubWebhookSource`)
2. Payload parsing logic
3. Signature validation (if applicable)
