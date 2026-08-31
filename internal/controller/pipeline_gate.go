package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	defaultPollInterval     = 30 * time.Second
	minPollInterval         = 10 * time.Second
	webhookRequeueInterval  = 5 * time.Minute
	approvalRequeueInterval = 5 * time.Minute
)

// GitHubCheckClient abstracts GitHub API calls for testability.
type GitHubCheckClient interface {
	GetCheckConclusion(ctx context.Context, owner, repo, ref, checkName string) (conclusion string, found bool, err error)
}

// GateChecker evaluates pipeline gate conditions and returns the requeue
// interval when a gate is not yet cleared.
type GateChecker struct {
	GitHubClient GitHubCheckClient
}

// Check evaluates the gate condition for a stage. Returns the recommended
// requeue duration (>0 means gate not cleared). Updates gateStatus in place.
func (g *GateChecker) Check(ctx context.Context, pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage, status *kelos.StageStatus) (time.Duration, error) {
	gate := stage.WaitFor
	if gate == nil {
		return 0, nil
	}

	switch {
	case gate.Duration != nil:
		return g.checkDuration(status, gate.Duration.Duration)
	case gate.GitHubCheck != nil:
		return g.checkGitHub(ctx, pipeline, stage, status)
	case gate.Webhook != nil:
		return g.checkWebhook(status)
	case gate.Approval != nil:
		return g.checkApproval(status)
	default:
		return 0, fmt.Errorf("no gate type specified")
	}
}

func (g *GateChecker) checkDuration(status *kelos.StageStatus, duration time.Duration) (time.Duration, error) {
	if status.StartTime == nil {
		return duration, nil
	}
	deadline := status.StartTime.Time.Add(duration)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		now := metav1.Now()
		status.GateStatus.Cleared = true
		status.GateStatus.ClearedAt = &now
		status.GateStatus.ClearedBy = "timer"
		return 0, nil
	}
	return remaining, nil
}

func (g *GateChecker) checkGitHub(ctx context.Context, pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage, status *kelos.StageStatus) (time.Duration, error) {
	check := stage.WaitFor.GitHubCheck
	if g.GitHubClient == nil {
		return defaultPollInterval, fmt.Errorf("GitHub client not configured")
	}

	// Record poll time.
	now := metav1.Now()
	status.GateStatus.LastPollTime = &now

	// Resolve ref (may be a template — already rendered by the time we get here
	// for the gate message, but for the actual API call we use the raw value).
	conclusion, found, err := g.GitHubClient.GetCheckConclusion(ctx, check.Owner, check.Repo, check.Ref, check.CheckName)
	if err != nil {
		return g.pollInterval(check), fmt.Errorf("poll GitHub check: %w", err)
	}

	if !found {
		status.Message = fmt.Sprintf("Check %q not found on ref %s", check.CheckName, check.Ref)
		return g.pollInterval(check), nil
	}

	target := check.TargetConclusion
	if target == "" {
		target = "success"
	}

	if conclusion == target {
		status.GateStatus.Cleared = true
		status.GateStatus.ClearedAt = &now
		status.GateStatus.ClearedBy = fmt.Sprintf("check:%s=%s", check.CheckName, conclusion)
		return 0, nil
	}

	// Check failed with a terminal non-matching conclusion.
	if conclusion != "" && conclusion != target {
		status.Message = fmt.Sprintf("Check %q concluded with %q, expected %q", check.CheckName, conclusion, target)
	}

	return g.pollInterval(check), nil
}

func (g *GateChecker) checkWebhook(status *kelos.StageStatus) (time.Duration, error) {
	// Webhook gates are cleared externally via status patch.
	// The controller just waits and requeues periodically as a safety net.
	if status.GateStatus.Cleared {
		return 0, nil
	}
	return webhookRequeueInterval, nil
}

func (g *GateChecker) checkApproval(status *kelos.StageStatus) (time.Duration, error) {
	// Approval gates are cleared externally via status patch.
	if status.GateStatus.Cleared {
		return 0, nil
	}
	return approvalRequeueInterval, nil
}

func (g *GateChecker) pollInterval(check *kelos.GitHubCheckGate) time.Duration {
	if check.PollInterval != nil && check.PollInterval.Duration >= minPollInterval {
		return check.PollInterval.Duration
	}
	return defaultPollInterval
}
