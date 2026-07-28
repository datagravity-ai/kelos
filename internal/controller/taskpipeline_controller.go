package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	taskPipelineFinalizer = "kelos.dev/pipeline-finalizer"
	gateSourceLabel       = "kelos.dev/gate-source"
)

// TaskPipelineReconciler reconciles TaskPipeline resources.
type TaskPipelineReconciler struct {
	client.Client
	CELEvaluator *PipelineCELEvaluator
	GateChecker  *GateChecker
}

// StageContext provides stage data to CEL expressions and prompt templates.
type StageContext struct {
	Results map[string]string
	Phase   string
}

func (r *TaskPipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pipeline kelos.TaskPipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion.
	if !pipeline.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&pipeline, taskPipelineFinalizer) {
			controllerutil.RemoveFinalizer(&pipeline, taskPipelineFinalizer)
			if err := r.Update(ctx, &pipeline); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if missing.
	if !controllerutil.ContainsFinalizer(&pipeline, taskPipelineFinalizer) {
		controllerutil.AddFinalizer(&pipeline, taskPipelineFinalizer)
		if err := r.Update(ctx, &pipeline); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Terminal phase: enforce TTL.
	if isTerminal(pipeline.Status.Phase) {
		return r.enforceTTL(ctx, &pipeline)
	}

	// Suspended: set Paused phase.
	if pipeline.Spec.Suspend != nil && *pipeline.Spec.Suspend {
		return r.setPaused(ctx, &pipeline)
	}

	// Check timeout.
	if pipeline.Status.StartTime != nil && pipeline.Spec.Timeout != nil {
		elapsed := time.Since(pipeline.Status.StartTime.Time)
		if elapsed > pipeline.Spec.Timeout.Duration {
			logger.Info("Pipeline timed out", "elapsed", elapsed)
			return r.failPipeline(ctx, &pipeline, "Pipeline exceeded timeout")
		}
	}

	// Build stage context from current status.
	stageContexts := r.buildStageContexts(&pipeline)

	// Process stages in dependency order.
	var minRequeue time.Duration
	for i := range pipeline.Spec.Stages {
		stage := &pipeline.Spec.Stages[i]
		status := r.getOrCreateStageStatus(&pipeline, stage.Name)

		if isStageTerminal(status.Phase) {
			continue
		}

		// Check dependencies.
		depsReady, depFailed := r.checkStageDependencies(&pipeline, stage)
		if depFailed {
			status.Phase = kelos.StagePhaseFailed
			status.Message = "Upstream dependency failed"
			now := metav1.Now()
			status.CompletionTime = &now
			continue
		}
		if !depsReady {
			continue
		}

		// Evaluate when predicate.
		if stage.When != "" {
			result, err := r.CELEvaluator.Evaluate(stage.When, stageContexts)
			if err != nil {
				logger.Error(err, "Failed to evaluate when predicate", "stage", stage.Name)
				status.Phase = kelos.StagePhaseFailed
				status.Message = fmt.Sprintf("CEL evaluation error: %v", err)
				now := metav1.Now()
				status.CompletionTime = &now
				continue
			}
			if !result {
				status.Phase = kelos.StagePhaseSkipped
				status.Message = "When predicate evaluated to false"
				now := metav1.Now()
				status.CompletionTime = &now
				stageContexts[stage.Name] = StageContext{Phase: string(kelos.StagePhaseSkipped)}
				continue
			}
		}

		// Set start time on first non-Pending transition.
		if status.StartTime == nil {
			now := metav1.Now()
			status.StartTime = &now
			if pipeline.Status.StartTime == nil {
				pipeline.Status.StartTime = &now
			}
		}

		// Check gate.
		if stage.WaitFor != nil {
			if status.GateStatus == nil {
				status.GateStatus = &kelos.GateStatus{}
			}
			if !status.GateStatus.Cleared {
				requeue, err := r.GateChecker.Check(ctx, &pipeline, stage, status)
				if err != nil {
					logger.Error(err, "Gate check failed", "stage", stage.Name)
				}
				status.Phase = kelos.StagePhaseWaiting
				status.Message = r.gateWaitMessage(stage)
				r.updateGateLabels(ctx, &pipeline, stage)
				if requeue > 0 && (minRequeue == 0 || requeue < minRequeue) {
					minRequeue = requeue
				}
				continue
			}
		}

		// Create child Tasks if not yet created.
		if status.Phase == kelos.StagePhasePending || status.Phase == kelos.StagePhaseWaiting {
			taskNames, err := r.createStageTasks(ctx, &pipeline, stage, stageContexts)
			if err != nil {
				logger.Error(err, "Failed to create tasks for stage", "stage", stage.Name)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			status.TaskNames = taskNames
			status.Phase = kelos.StagePhaseRunning
			r.clearGateLabels(ctx, &pipeline, stage)
		}

		// Check child Task status.
		if status.Phase == kelos.StagePhaseRunning {
			succeeded, failed, running := r.checkChildTasks(ctx, &pipeline, status)
			if running == 0 {
				if failed > 0 {
					status.Phase = kelos.StagePhaseFailed
					status.Message = fmt.Sprintf("%d task(s) failed", failed)
				} else if succeeded == len(status.TaskNames) {
					status.Phase = kelos.StagePhaseSucceeded
					status.Results = r.mergeTaskResults(ctx, status)
					stageContexts[stage.Name] = StageContext{
						Results: status.Results,
						Phase:   string(kelos.StagePhaseSucceeded),
					}
				}
				if isStageTerminal(status.Phase) {
					now := metav1.Now()
					status.CompletionTime = &now
				}
			}
		}
	}

	// Derive pipeline phase.
	pipeline.Status.Phase = r.derivePipelinePhase(&pipeline)
	if isTerminal(pipeline.Status.Phase) && pipeline.Status.CompletionTime == nil {
		now := metav1.Now()
		pipeline.Status.CompletionTime = &now
	}

	if err := r.Status().Update(ctx, &pipeline); err != nil {
		return ctrl.Result{}, err
	}

	if minRequeue > 0 {
		return ctrl.Result{RequeueAfter: minRequeue}, nil
	}
	return ctrl.Result{}, nil
}

func (r *TaskPipelineReconciler) buildStageContexts(pipeline *kelos.TaskPipeline) map[string]StageContext {
	contexts := make(map[string]StageContext)
	for _, s := range pipeline.Status.Stages {
		if isStageTerminal(s.Phase) {
			contexts[s.Name] = StageContext{
				Results: s.Results,
				Phase:   string(s.Phase),
			}
		}
	}
	return contexts
}

func (r *TaskPipelineReconciler) checkStageDependencies(pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage) (ready bool, failed bool) {
	for _, dep := range stage.DependsOn {
		status := r.findStageStatus(pipeline, dep)
		if status == nil {
			return false, false
		}
		switch status.Phase {
		case kelos.StagePhaseFailed:
			return false, true
		case kelos.StagePhaseSucceeded, kelos.StagePhaseSkipped:
			continue
		default:
			return false, false
		}
	}
	return true, false
}

func (r *TaskPipelineReconciler) createStageTasks(
	ctx context.Context,
	pipeline *kelos.TaskPipeline,
	stage *kelos.PipelineStage,
	stageContexts map[string]StageContext,
) ([]string, error) {
	combinations := expandMatrix(stage.Matrix)
	var taskNames []string

	for _, taskTmpl := range stage.Tasks {
		for _, combo := range combinations {
			taskName := r.childTaskName(pipeline, stage, &taskTmpl, combo)

			prompt, err := r.renderPrompt(taskTmpl.PromptTemplate, stageContexts, combo)
			if err != nil {
				return nil, fmt.Errorf("render prompt for %s: %w", taskName, err)
			}

			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      taskName,
					Namespace: pipeline.Namespace,
					Labels: map[string]string{
						"kelos.dev/pipeline":       pipeline.Name,
						"kelos.dev/pipeline-stage": stage.Name,
					},
				},
				Spec: kelos.TaskSpec{
					Worker: taskTmpl.Worker,
					Prompt: prompt,
					Branch: taskTmpl.Branch,
					TTLSecondsAfterFinished: taskTmpl.TTLSecondsAfterFinished,
					PodOverrides:            taskTmpl.PodOverrides,
				},
			}

			if err := controllerutil.SetControllerReference(pipeline, task, r.Scheme()); err != nil {
				return nil, fmt.Errorf("set owner reference for %s: %w", taskName, err)
			}

			if err := r.Create(ctx, task); err != nil {
				return nil, fmt.Errorf("create task %s: %w", taskName, err)
			}
			taskNames = append(taskNames, taskName)
		}
	}
	return taskNames, nil
}

func (r *TaskPipelineReconciler) checkChildTasks(
	ctx context.Context,
	pipeline *kelos.TaskPipeline,
	status *kelos.StageStatus,
) (succeeded, failed, running int) {
	for _, name := range status.TaskNames {
		var task kelos.Task
		key := types.NamespacedName{Name: name, Namespace: pipeline.Namespace}
		if err := r.Get(ctx, key, &task); err != nil {
			running++
			continue
		}
		switch task.Status.Phase {
		case kelos.TaskPhaseSucceeded:
			succeeded++
		case kelos.TaskPhaseFailed:
			failed++
		default:
			running++
		}
	}
	return
}

func (r *TaskPipelineReconciler) mergeTaskResults(ctx context.Context, status *kelos.StageStatus) map[string]string {
	results := make(map[string]string)
	for _, name := range status.TaskNames {
		var task kelos.Task
		key := types.NamespacedName{Name: name, Namespace: ""}
		if err := r.Get(ctx, key, &task); err != nil {
			continue
		}
		for k, v := range task.Status.Results {
			results[k] = v
		}
	}
	return results
}

func (r *TaskPipelineReconciler) derivePipelinePhase(pipeline *kelos.TaskPipeline) kelos.TaskPipelinePhase {
	allTerminal := true
	anyFailed := false
	anyRunning := false

	for _, s := range pipeline.Status.Stages {
		switch s.Phase {
		case kelos.StagePhaseSucceeded, kelos.StagePhaseSkipped:
			continue
		case kelos.StagePhaseFailed:
			anyFailed = true
		case kelos.StagePhaseRunning, kelos.StagePhaseWaiting:
			anyRunning = true
			allTerminal = false
		default:
			allTerminal = false
		}
	}

	if allTerminal && !anyFailed {
		return kelos.TaskPipelinePhaseSucceeded
	}
	if anyFailed && !anyRunning {
		return kelos.TaskPipelinePhaseFailed
	}
	return kelos.TaskPipelinePhaseRunning
}

func (r *TaskPipelineReconciler) enforceTTL(ctx context.Context, pipeline *kelos.TaskPipeline) (ctrl.Result, error) {
	if pipeline.Spec.TTLSecondsAfterFinished == nil || pipeline.Status.CompletionTime == nil {
		return ctrl.Result{}, nil
	}
	ttl := time.Duration(*pipeline.Spec.TTLSecondsAfterFinished) * time.Second
	elapsed := time.Since(pipeline.Status.CompletionTime.Time)
	if elapsed >= ttl {
		return ctrl.Result{}, r.Delete(ctx, pipeline)
	}
	return ctrl.Result{RequeueAfter: ttl - elapsed}, nil
}

func (r *TaskPipelineReconciler) setPaused(ctx context.Context, pipeline *kelos.TaskPipeline) (ctrl.Result, error) {
	if pipeline.Status.Phase != kelos.TaskPipelinePhasePaused {
		pipeline.Status.Phase = kelos.TaskPipelinePhasePaused
		if err := r.Status().Update(ctx, pipeline); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *TaskPipelineReconciler) failPipeline(ctx context.Context, pipeline *kelos.TaskPipeline, message string) (ctrl.Result, error) {
	pipeline.Status.Phase = kelos.TaskPipelinePhaseFailed
	pipeline.Status.Conditions = append(pipeline.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		Reason:             "Timeout",
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
	now := metav1.Now()
	pipeline.Status.CompletionTime = &now
	return ctrl.Result{}, r.Status().Update(ctx, pipeline)
}

func (r *TaskPipelineReconciler) getOrCreateStageStatus(pipeline *kelos.TaskPipeline, name string) *kelos.StageStatus {
	for i := range pipeline.Status.Stages {
		if pipeline.Status.Stages[i].Name == name {
			return &pipeline.Status.Stages[i]
		}
	}
	pipeline.Status.Stages = append(pipeline.Status.Stages, kelos.StageStatus{
		Name:  name,
		Phase: kelos.StagePhasePending,
	})
	return &pipeline.Status.Stages[len(pipeline.Status.Stages)-1]
}

func (r *TaskPipelineReconciler) findStageStatus(pipeline *kelos.TaskPipeline, name string) *kelos.StageStatus {
	for i := range pipeline.Status.Stages {
		if pipeline.Status.Stages[i].Name == name {
			return &pipeline.Status.Stages[i]
		}
	}
	return nil
}

func (r *TaskPipelineReconciler) gateWaitMessage(stage *kelos.PipelineStage) string {
	if stage.WaitFor.Webhook != nil {
		return fmt.Sprintf("Waiting for webhook from source %q", stage.WaitFor.Webhook.Source)
	}
	if stage.WaitFor.GitHubCheck != nil {
		return fmt.Sprintf("Waiting for check %q on %s/%s", stage.WaitFor.GitHubCheck.CheckName, stage.WaitFor.GitHubCheck.Owner, stage.WaitFor.GitHubCheck.Repo)
	}
	if stage.WaitFor.Approval != nil {
		msg := "Waiting for approval"
		if stage.WaitFor.Approval.Message != "" {
			msg = stage.WaitFor.Approval.Message
		}
		return msg
	}
	if stage.WaitFor.Duration != nil {
		return fmt.Sprintf("Waiting for %s", stage.WaitFor.Duration.Duration)
	}
	return "Waiting for gate"
}

func (r *TaskPipelineReconciler) updateGateLabels(ctx context.Context, pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage) {
	if stage.WaitFor.Webhook != nil {
		if pipeline.Labels == nil {
			pipeline.Labels = make(map[string]string)
		}
		pipeline.Labels[gateSourceLabel] = stage.WaitFor.Webhook.Source
	}
}

func (r *TaskPipelineReconciler) clearGateLabels(ctx context.Context, pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage) {
	if pipeline.Labels != nil {
		delete(pipeline.Labels, gateSourceLabel)
	}
}

func (r *TaskPipelineReconciler) renderPrompt(tmpl string, stages map[string]StageContext, matrix map[string]string) (string, error) {
	t, err := template.New("prompt").Funcs(template.FuncMap{}).Parse(tmpl)
	if err != nil {
		return "", err
	}

	data := map[string]interface{}{
		"Stages": stages,
		"Matrix": matrix,
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *TaskPipelineReconciler) childTaskName(pipeline *kelos.TaskPipeline, stage *kelos.PipelineStage, task *kelos.PipelineTaskTemplate, matrix map[string]string) string {
	name := fmt.Sprintf("%s-%s-%s", pipeline.Name, stage.Name, task.Name)
	if len(matrix) > 0 {
		keys := make([]string, 0, len(matrix))
		for k := range matrix {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name += "-" + matrix[k]
		}
	}
	// Truncate to Kubernetes name limit.
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

// expandMatrix returns all combinations of matrix parameters.
// Returns a single empty map if matrix is nil (one task, no matrix values).
func expandMatrix(matrix *kelos.MatrixSpec) []map[string]string {
	if matrix == nil || len(matrix.Params) == 0 {
		return []map[string]string{{}}
	}

	keys := make([]string, 0, len(matrix.Params))
	for k := range matrix.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var results []map[string]string
	results = append(results, map[string]string{})

	for _, key := range keys {
		values := matrix.Params[key]
		var expanded []map[string]string
		for _, existing := range results {
			for _, val := range values {
				combo := make(map[string]string, len(existing)+1)
				for k, v := range existing {
					combo[k] = v
				}
				combo[key] = val
				expanded = append(expanded, combo)
			}
		}
		results = expanded
	}
	return results
}

func isTerminal(phase kelos.TaskPipelinePhase) bool {
	return phase == kelos.TaskPipelinePhaseSucceeded || phase == kelos.TaskPipelinePhaseFailed
}

func isStageTerminal(phase kelos.StagePhase) bool {
	return phase == kelos.StagePhaseSucceeded || phase == kelos.StagePhaseFailed || phase == kelos.StagePhaseSkipped
}

// SetupWithManager registers the TaskPipeline controller.
func (r *TaskPipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.TaskPipeline{}).
		Owns(&kelos.Task{}).
		Complete(r)
}
