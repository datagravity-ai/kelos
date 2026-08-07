# Actions Gateway Reviewers

This directory configures on-demand code and Kubernetes API reviewers for
[`gjkim42/actions-gateway`](https://github.com/gjkim42/actions-gateway). The
reviewers are read-only: they inspect pull requests or issues and publish
feedback without modifying repository files or branches.

Both TaskSpawners use the `actions-gateway-session-agent` Workspace from
[`../session-workspaces.yaml`](../session-workspaces.yaml) and the shared
`base-agent` AgentConfig from [`../base-agent.yaml`](../base-agent.yaml).

## Spawners

| Spawner | Trigger | Agent | Scope |
|---|---|---|---|
| **actions-gateway-reviewer** | PR comment or review `/kelos review` | Codex | General correctness, tests, security, and project conventions |
| **actions-gateway-api-reviewer** | Issue/PR comment or review `/kelos api-review` | Codex | Kubernetes API design, compatibility, schemas, and documentation |

The general reviewer creates or updates one sticky pull request comment. The
API reviewer does the same for pull requests and posts a normal comment for
issue-based design proposals. Each trigger accepts commands from `gjkim42`;
comments from `kelos-bot[bot]` are also accepted for automated handoffs.

The review prompts use Actions Gateway's Makefile targets and treat CRD types,
generated schemas, samples, labels, annotations, flags, configuration, and
webhook contracts as user-facing API surfaces. API reviews also check
`docs/api-design.md` and the repository's schema tests.

## Deploy

Apply the shared prerequisites once:

```bash
kubectl apply -f self-development/base-agent.yaml
kubectl apply -f self-development/session-workspaces.yaml
```

Apply the reviewers:

```bash
kubectl apply -f self-development/actions-gateway/actions-gateway-reviewer.yaml
kubectl apply -f self-development/actions-gateway/actions-gateway-api-reviewer.yaml
```
