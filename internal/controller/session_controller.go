/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// AnnotationAssignedTask is set on session pods to indicate which Task is assigned.
	AnnotationAssignedTask = "kelos.dev/assigned-task"

	// AnnotationTaskStatus is set on session pods by the session runner to report task status.
	AnnotationTaskStatus = "kelos.dev/task-status"

	// AnnotationTasksCompleted tracks the number of tasks completed by a session pod.
	AnnotationTasksCompleted = "kelos.dev/tasks-completed"

	// AnnotationSessionStartTime records when the session pod started processing tasks.
	AnnotationSessionStartTime = "kelos.dev/session-start-time"

	// LabelExecutionMode is set on Tasks to indicate their execution mode.
	LabelExecutionMode = "kelos.dev/execution-mode"
)

// SessionReconciler assigns queued Tasks to available persistent session pods
// and monitors session pod annotations for task completion signals.
type SessionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch

// Reconcile handles session-related reconciliation for persistent-mode Tasks.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task kelosv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Only handle persistent-mode tasks.
	if task.Labels[LabelExecutionMode] != string(kelosv1alpha1.ExecutionModePersistent) {
		return ctrl.Result{}, nil
	}

	// Skip terminal tasks.
	if task.Status.Phase == kelosv1alpha1.TaskPhaseSucceeded || task.Status.Phase == kelosv1alpha1.TaskPhaseFailed {
		// If this task was assigned to a pod, clear the assignment.
		if task.Status.SessionPodName != "" {
			if err := r.clearPodAssignment(ctx, task.Namespace, task.Status.SessionPodName); err != nil {
				logger.Error(err, "Failed to clear pod assignment", "pod", task.Status.SessionPodName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
		return ctrl.Result{}, nil
	}

	// If task is Queued, try to assign it to an available pod.
	if task.Status.Phase == kelosv1alpha1.TaskPhaseQueued {
		return r.assignTask(ctx, &task)
	}

	// If task has a session pod assigned, check for completion signals.
	// Handle both Pending (waiting for runner to start) and Running phases.
	if task.Status.SessionPodName != "" &&
		(task.Status.Phase == kelosv1alpha1.TaskPhasePending || task.Status.Phase == kelosv1alpha1.TaskPhaseRunning) {
		return r.checkTaskCompletion(ctx, &task)
	}

	return ctrl.Result{}, nil
}

// assignTask tries to assign a Queued task to an available session pod.
func (r *SessionReconciler) assignTask(ctx context.Context, task *kelosv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	spawnerName := task.Labels["kelos.dev/taskspawner"]
	if spawnerName == "" {
		return ctrl.Result{}, fmt.Errorf("task %s missing kelos.dev/taskspawner label", task.Name)
	}

	// List session pods for this spawner.
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(task.Namespace),
		client.MatchingLabels{
			"kelos.dev/taskspawner": spawnerName,
			"kelos.dev/component":   SessionComponentLabel,
		},
	); err != nil {
		return ctrl.Result{}, err
	}

	// Find an available pod (Running, no assigned task).
	var availablePod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		if _, assigned := pod.Annotations[AnnotationAssignedTask]; assigned {
			continue
		}
		availablePod = pod
		break
	}

	if availablePod == nil {
		// No available pod, requeue after a short delay.
		logger.V(1).Info("No available session pod for task, requeuing", "task", task.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Assign the task to the pod.
	logger.Info("Assigning task to session pod", "task", task.Name, "pod", availablePod.Name)

	// Patch pod annotation.
	if availablePod.Annotations == nil {
		availablePod.Annotations = make(map[string]string)
	}
	availablePod.Annotations[AnnotationAssignedTask] = task.Name
	if err := r.Update(ctx, availablePod); err != nil {
		logger.Error(err, "Failed to assign task to pod", "pod", availablePod.Name)
		return ctrl.Result{}, err
	}

	// Update Task status. If this fails, roll back the pod annotation to avoid
	// leaving the pod marked as assigned while the task remains Queued.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.SessionPodName = availablePod.Name
		task.Status.Phase = kelosv1alpha1.TaskPhasePending
		task.Status.Message = fmt.Sprintf("Assigned to session pod %s", availablePod.Name)
		return r.Status().Update(ctx, task)
	}); err != nil {
		if rollbackErr := r.clearPodAssignment(ctx, task.Namespace, availablePod.Name); rollbackErr != nil {
			logger.Error(rollbackErr, "Failed to roll back pod assignment after task status update failure", "pod", availablePod.Name)
		}
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(task, corev1.EventTypeNormal, "SessionAssigned", "Task assigned to session pod %s", availablePod.Name)
	return ctrl.Result{}, nil
}

// checkTaskCompletion reads the session pod's annotations to detect task
// completion signals from the session runner.
func (r *SessionReconciler) checkTaskCompletion(ctx context.Context, task *kelosv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: task.Namespace,
		Name:      task.Status.SessionPodName,
	}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Pod is gone, mark task as failed.
			logger.Info("Session pod not found, marking task as failed", "task", task.Name, "pod", task.Status.SessionPodName)
			return r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed, "Session pod was deleted")
		}
		return ctrl.Result{}, err
	}

	// Check the task status annotation set by the session runner.
	taskStatus := pod.Annotations[AnnotationTaskStatus]
	switch taskStatus {
	case "succeeded":
		logger.Info("Task completed successfully via session runner", "task", task.Name)
		if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseSucceeded, "Task completed successfully"); err != nil {
			return result, err
		}
		if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
			logger.Error(err, "Failed to clear pod assignment after success")
		}
		return ctrl.Result{}, nil

	case "failed":
		logger.Info("Task failed via session runner", "task", task.Name)
		if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed, "Task failed"); err != nil {
			return result, err
		}
		if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
			logger.Error(err, "Failed to clear pod assignment after failure")
		}
		return ctrl.Result{}, nil

	case "running":
		if task.Status.Phase != kelosv1alpha1.TaskPhaseRunning {
			return r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseRunning, "Task is running on session pod")
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	default:
		if pod.Status.Phase == corev1.PodFailed {
			logger.Info("Session pod failed without reporting status, marking task failed",
				"task", task.Name, "pod", pod.Name)
			if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed, "Session pod failed"); err != nil {
				return result, err
			}
			if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
				logger.Error(err, "Failed to clear pod assignment after pod failure")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
}

// updateTaskPhase updates a Task's phase and message.
func (r *SessionReconciler) updateTaskPhase(ctx context.Context, task *kelosv1alpha1.Task, phase kelosv1alpha1.TaskPhase, message string) (ctrl.Result, error) {
	return ctrl.Result{}, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = phase
		task.Status.Message = message
		return r.Status().Update(ctx, task)
	})
}

// clearPodAssignment removes the task assignment annotations from a session pod.
func (r *SessionReconciler) clearPodAssignment(ctx context.Context, namespace, podName string) error {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if pod.Annotations == nil {
		return nil
	}

	delete(pod.Annotations, AnnotationAssignedTask)
	delete(pod.Annotations, AnnotationTaskStatus)
	return r.Update(ctx, &pod)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelosv1alpha1.Task{},
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[LabelExecutionMode] == string(kelosv1alpha1.ExecutionModePersistent)
			}))).
		// Watch session pods for annotation changes (task completion signals).
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findTaskForSessionPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()["kelos.dev/component"] == SessionComponentLabel
			}))).
		Complete(r)
}

// findTaskForSessionPod maps a session pod change to the Task assigned to it.
func (r *SessionReconciler) findTaskForSessionPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	taskName := pod.Annotations[AnnotationAssignedTask]
	if taskName == "" {
		// Pod has no task assigned. Check if there are Queued tasks that need
		// assignment - trigger reconciliation of the oldest one.
		return r.findOldestQueuedTask(ctx, pod)
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      taskName,
		},
	}}
}

// findOldestQueuedTask returns a reconcile request for the oldest Queued task
// belonging to the same TaskSpawner as the given session pod.
func (r *SessionReconciler) findOldestQueuedTask(ctx context.Context, pod *corev1.Pod) []reconcile.Request {
	spawnerName := pod.Labels["kelos.dev/taskspawner"]
	if spawnerName == "" {
		return nil
	}

	var taskList kelosv1alpha1.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(pod.Namespace),
		client.MatchingLabels{
			"kelos.dev/taskspawner": spawnerName,
			LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
		},
	); err != nil {
		return nil
	}

	// Filter to Queued tasks and sort by creation time.
	var queued []kelosv1alpha1.Task
	for _, t := range taskList.Items {
		if t.Status.Phase == kelosv1alpha1.TaskPhaseQueued {
			queued = append(queued, t)
		}
	}

	if len(queued) == 0 {
		return nil
	}

	sort.Slice(queued, func(i, j int) bool {
		return queued[i].CreationTimestamp.Before(&queued[j].CreationTimestamp)
	})

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: queued[0].Namespace,
			Name:      queued[0].Name,
		},
	}}
}
