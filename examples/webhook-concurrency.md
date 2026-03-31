# Webhook TaskSpawner Concurrency Control

This document explains how `maxConcurrency` works for webhook-driven TaskSpawners and the improved behavior.

## Overview

Webhook TaskSpawners support concurrency limits via the `maxConcurrency` field, but the behavior differs from polling-based TaskSpawners due to their event-driven nature.

## How It Works

### activeTasks Counting
- **Polling TaskSpawners**: The spawner process continuously counts active tasks
- **Webhook TaskSpawners**: The `kelos-controller` updates `activeTasks` when Tasks change status
- **Eventually Consistent**: There may be brief periods where the count is slightly stale

### Concurrency Enforcement
```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: github-webhook-limited
spec:
  maxConcurrency: 3  # Limit to 3 concurrent tasks
  when:
    githubWebhook:
      events: ["issues", "issue_comment"]
      filters:
        - event: "issues"
          action: "opened"
```

### Behavior When Limit Exceeded

**✅ Improved Behavior (Current)**
- Webhook is **accepted** with HTTP 200 OK
- Event processing continues for other TaskSpawners
- Task creation is **skipped** with clear logging
- GitHub sees successful delivery (no retries)

**❌ Previous Behavior**
- Webhook returned HTTP 503 Service Unavailable
- Could cause unwanted retry storms
- Didn't work reliably with GitHub's retry logic

## Example Scenarios

### Scenario 1: Normal Operation
```
Current activeTasks: 2
maxConcurrency: 3
Incoming webhook: ✅ Creates new task (activeTasks becomes 3)
```

### Scenario 2: At Limit
```
Current activeTasks: 3
maxConcurrency: 3
Incoming webhook: ⚠️ Logs "Max concurrency reached, dropping webhook event"
Response: HTTP 200 OK (webhook accepted but task skipped)
```

### Scenario 3: Eventually Consistent Update
```
1. Task completes → activeTasks should become 2
2. Brief delay while kelos-controller reconciles
3. New webhook arrives → May see stale activeTasks=3 temporarily
4. Controller updates → activeTasks becomes 2
5. Next webhook → Will see correct count
```

## Monitoring Concurrency

### Logs
Look for these log messages:
```
# Normal task creation
level=info msg="Successfully created task from webhook" spawner=my-spawner

# Concurrency limit hit
level=info msg="Max concurrency reached, dropping webhook event"
  activeTasks=3 maxConcurrency=3
  reason="Webhook accepted but task creation skipped due to concurrency limits"
```

### TaskSpawner Status
Check the status for current counts:
```bash
kubectl get taskspawner github-webhook-limited -o yaml
```

```yaml
status:
  phase: Running
  activeTasks: 3
  totalTasksCreated: 15
  message: "Webhook-driven TaskSpawner ready"
```

### Task Labels
All webhook tasks are labeled for easy filtering:
```bash
# Count active tasks for a specific TaskSpawner
kubectl get tasks -l kelos.dev/taskspawner=github-webhook-limited \
  --field-selector=status.phase!=Succeeded,status.phase!=Failed

# View recent webhook tasks
kubectl get tasks -l kelos.dev/source-kind=webhook --sort-by=.metadata.creationTimestamp
```

## Best Practices

### 1. **Choose Appropriate Limits**
```yaml
# For high-traffic repositories
maxConcurrency: 10

# For resource-constrained environments
maxConcurrency: 2

# For development/testing
maxConcurrency: 1
```

### 2. **Monitor Both Sides**
- **Kubernetes**: TaskSpawner status and Task resources
- **GitHub**: Webhook delivery logs for any failures

### 3. **Handle Bursts Gracefully**
```yaml
# Use reasonable concurrency limits
maxConcurrency: 5

# Set task cleanup timeouts
taskTemplate:
  ttlSecondsAfterFinished: 3600  # Clean up after 1 hour
```

### 4. **Filter Judiciously**
```yaml
# Reduce webhook volume with good filtering
githubWebhook:
  events: ["issues"]  # Don't listen to every event type
  filters:
    - event: "issues"
      action: "opened"  # Only new issues
      excludeLabels: ["wontfix", "duplicate"]  # Skip certain labels
```

## Troubleshooting

### Issue: Tasks Not Being Created
**Check**: TaskSpawner may be at concurrency limit
```bash
kubectl describe taskspawner <name>
kubectl logs -l app.kubernetes.io/name=kelos,app.kubernetes.io/component=webhook-github
```

### Issue: activeTasks Count Seems Wrong
**Cause**: Eventually consistent updates
**Solution**: Wait a few seconds for controller reconciliation or trigger manually:
```bash
kubectl patch taskspawner <name> -p '{"spec":{"suspend":false}}'
```

### Issue: Webhook Retries
**Check**: Should not happen with current implementation
**If it does**: Verify webhook server is returning HTTP 200, not 503

## Migration from Old Behavior

If upgrading from a version that returned HTTP 503:
- **No action required** - behavior automatically improves
- **Monitor webhook delivery logs** - should see fewer retries
- **Adjust concurrency limits** if needed - can be more aggressive now
