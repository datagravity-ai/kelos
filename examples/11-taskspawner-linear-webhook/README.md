# Linear Webhook TaskSpawner Example

This example demonstrates using a TaskSpawner triggered by Linear webhook events.

## Configuration

The TaskSpawner listens for Linear Issue events with specific filtering:

- **Types**: `["Issue"]` - Only issue events
- **Actions**: `["create", "update"]` - Issue creation and updates
- **States**: `["Todo", "In Progress"]` - Only issues in these workflow states
- **Labels**: Must have `["agent-task"]` label
- **Exclude Labels**: Excludes issues with `["no-automation"]` label

## Webhook Setup

1. **Deploy webhook server** (if not already done):
   ```bash
   # Enable Linear webhook server in your Helm values
   webhookServer:
     sources:
       linear:
         enabled: true
         replicas: 1
         secretName: linear-webhook-secret
   ```

2. **Create webhook secret**:
   ```bash
   kubectl create secret generic linear-webhook-secret \
     --from-literal=WEBHOOK_SECRET=your-linear-webhook-secret
   ```

3. **Configure Linear webhook**:
   - In Linear Settings → API → Webhooks
   - URL: `https://your-webhook-domain/webhook/linear`
   - Secret: Use the same secret as above
   - Events: Select "Issues" and "Comments" as needed

## Template Variables

Linear webhook TaskSpawners have access to these template variables:

- `{{.ID}}` - Linear issue/resource ID
- `{{.Title}}` - Issue title
- `{{.Type}}` - Resource type (e.g., "Issue", "Comment")
- `{{.Action}}` - Webhook action (e.g., "create", "update")
- `{{.State}}` - Current workflow state name
- `{{.Labels}}` - Comma-separated list of labels
- `{{.Payload}}` - Full webhook payload for advanced templating

## Usage

```bash
kubectl apply -f taskspawner.yaml
```

When matching Linear issues are created or updated, this TaskSpawner will automatically create Claude Code tasks to process them.