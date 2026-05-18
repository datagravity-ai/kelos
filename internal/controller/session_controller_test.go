package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestSessionReconciler_SkipsNonPersistentTask(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ephemeral-task",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ephemeral-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("Expected no requeue for non-persistent task")
	}
}

func TestSessionReconciler_SkipsTerminalTask(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "done-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode: string(kelosv1alpha1.ExecutionModePersistent),
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseSucceeded,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "done-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("Expected no requeue for terminal task")
	}
}

func TestSessionReconciler_AssignsQueuedTaskToAvailablePod(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "queued-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "queued-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	// Verify pod got the assignment annotation.
	var updatedPod corev1.Pod
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: "default"}, &updatedPod); err != nil {
		t.Fatalf("Failed to get pod: %v", err)
	}
	if updatedPod.Annotations[AnnotationAssignedTask] != "queued-task" {
		t.Errorf("Pod annotation %s: expected 'queued-task', got %q", AnnotationAssignedTask, updatedPod.Annotations[AnnotationAssignedTask])
	}

	// Verify task status was updated.
	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "queued-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhasePending {
		t.Errorf("Task phase: expected Pending, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SessionPodName != pod.Name {
		t.Errorf("Task sessionPodName: expected %q, got %q", pod.Name, updatedTask.Status.SessionPodName)
	}
}

func TestSessionReconciler_RequeuesWhenNoPodAvailable(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "queued-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}

	// Pod exists but already has a task assigned.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "other-task",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "queued-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected requeue when no pod is available")
	}
}

func TestSessionReconciler_DetectsSucceededTask(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "running-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseRunning,
			SessionPodName: "session-my-spawner-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "running-task",
				AnnotationTaskStatus:   "succeeded",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "running-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	// Verify task phase is Succeeded.
	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "running-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded {
		t.Errorf("Task phase: expected Succeeded, got %s", updatedTask.Status.Phase)
	}

	// Verify pod assignment was cleared.
	var updatedPod corev1.Pod
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: "default"}, &updatedPod); err != nil {
		t.Fatalf("Failed to get pod: %v", err)
	}
	if _, exists := updatedPod.Annotations[AnnotationAssignedTask]; exists {
		t.Error("Expected pod assignment annotation to be cleared")
	}
}

func TestSessionReconciler_DetectsFailedTask(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failing-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseRunning,
			SessionPodName: "session-my-spawner-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "failing-task",
				AnnotationTaskStatus:   "failed",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "failing-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "failing-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		t.Errorf("Task phase: expected Failed, got %s", updatedTask.Status.Phase)
	}
}

func TestSessionReconciler_RequeuesTaskWhenPodDeleted(t *testing.T) {
	scheme := newTestScheme()
	startTime := metav1.Now()
	completionTime := metav1.Now()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "orphaned-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseRunning,
			SessionPodName: "session-my-spawner-0",
			StartTime:      &startTime,
			CompletionTime: &completionTime,
			Outputs:        []string{"branch: old-branch"},
			Results:        map[string]string{"branch": "old-branch"},
		},
	}

	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			SessionConfig: &kelosv1alpha1.SessionConfig{},
		},
	}

	// Pod does NOT exist.
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, spawner).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "orphaned-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "orphaned-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseQueued {
		t.Errorf("Task phase: expected Queued, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SessionRetryCount != 1 {
		t.Errorf("SessionRetryCount: expected 1, got %d", updatedTask.Status.SessionRetryCount)
	}
	if updatedTask.Status.SessionPodName != "" {
		t.Errorf("SessionPodName: expected empty, got %q", updatedTask.Status.SessionPodName)
	}
	if updatedTask.Status.LastSessionFailure != "session-my-spawner-0" {
		t.Errorf("LastSessionFailure: expected 'session-my-spawner-0', got %q", updatedTask.Status.LastSessionFailure)
	}
	if updatedTask.Status.StartTime != nil {
		t.Error("StartTime: expected nil after requeue")
	}
	if updatedTask.Status.CompletionTime != nil {
		t.Error("CompletionTime: expected nil after requeue")
	}
	if updatedTask.Status.Outputs != nil {
		t.Errorf("Outputs: expected nil after requeue, got %v", updatedTask.Status.Outputs)
	}
	if updatedTask.Status.Results != nil {
		t.Errorf("Results: expected nil after requeue, got %v", updatedTask.Status.Results)
	}
}

func TestSessionReconciler_FailsTaskWhenRetryLimitExhausted(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "exhausted-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:             kelosv1alpha1.TaskPhaseRunning,
			SessionPodName:    "session-my-spawner-0",
			SessionRetryCount: 3,
		},
	}

	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			SessionConfig: &kelosv1alpha1.SessionConfig{},
		},
	}

	// Pod does NOT exist.
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, spawner).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "exhausted-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "exhausted-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		t.Errorf("Task phase: expected Failed, got %s", updatedTask.Status.Phase)
	}
}

func TestSessionReconciler_FailsTaskWhenRetryDisabled(t *testing.T) {
	scheme := newTestScheme()
	retryDisabled := false
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-retry-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseRunning,
			SessionPodName: "session-my-spawner-0",
		},
	}

	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			SessionConfig: &kelosv1alpha1.SessionConfig{
				RetryOnPodFailure: &retryDisabled,
			},
		},
	}

	// Pod does NOT exist.
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, spawner).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-retry-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "no-retry-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		t.Errorf("Task phase: expected Failed, got %s", updatedTask.Status.Phase)
	}
}

func TestSessionReconciler_RequeuesTaskWhenPodFailed(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-failed-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseRunning,
			SessionPodName: "session-my-spawner-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "pod-failed-task",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
		},
	}

	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			SessionConfig: &kelosv1alpha1.SessionConfig{},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod, spawner).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pod-failed-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "pod-failed-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseQueued {
		t.Errorf("Task phase: expected Queued, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SessionRetryCount != 1 {
		t.Errorf("SessionRetryCount: expected 1, got %d", updatedTask.Status.SessionRetryCount)
	}
}

func TestSessionReconciler_WaitsGracePeriodBeforeFailingOnMissingAnnotation(t *testing.T) {
	scheme := newTestScheme()
	completionTime := metav1.Now()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recent-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhasePending,
			SessionPodName: "session-my-spawner-0",
			CompletionTime: &completionTime,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "recent-task",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "recent-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected RequeueAfter > 0 during grace period")
	}

	// Task should still be Pending (not Failed).
	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "recent-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhasePending {
		t.Errorf("Task phase: expected Pending during grace period, got %s", updatedTask.Status.Phase)
	}
}

func TestSessionReconciler_FailsTaskWhenCompletionTimeSetButNoAnnotation(t *testing.T) {
	scheme := newTestScheme()
	completionTime := metav1.NewTime(time.Now().Add(-30 * time.Second))
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stuck-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhasePending,
			SessionPodName: "session-my-spawner-0",
			CompletionTime: &completionTime,
			Results: map[string]string{
				"branch": "some-branch",
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "stuck-task",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "stuck-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "stuck-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		t.Errorf("Task phase: expected Failed, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.Message != "Session runner completed but did not set status annotation" {
		t.Errorf("Unexpected message: %s", updatedTask.Status.Message)
	}
	if updatedTask.Status.CompletionTime != nil {
		t.Error("CompletionTime: expected nil after marking failed")
	}

	// Verify pod assignment was cleared.
	var updatedPod corev1.Pod
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "session-my-spawner-0", Namespace: "default"}, &updatedPod); err != nil {
		t.Fatalf("Failed to get pod: %v", err)
	}
	if _, assigned := updatedPod.Annotations[AnnotationAssignedTask]; assigned {
		t.Error("Expected pod assignment annotation to be cleared")
	}
}

func TestSessionReconciler_SkipsNonRunningPods(t *testing.T) {
	scheme := newTestScheme()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "queued-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}

	// Pod exists but is in Pending phase.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "queued-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected requeue when no Running pod is available")
	}
}

func TestSessionReconciler_RaceConditionRollsBackPodAnnotation(t *testing.T) {
	scheme := newTestScheme()
	// Task is already assigned (Pending) by a prior reconcile.
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "already-assigned-task",
			Namespace: "default",
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhasePending,
			SessionPodName: "session-my-spawner-0",
		},
	}

	// A second pod that the losing reconcile would pick.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-1",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, pod).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Simulate the losing reconcile calling assignTask on a task that's already
	// been moved past Queued. We call assignTask directly with a stale copy.
	staleTask := task.DeepCopy()
	staleTask.Status.Phase = kelosv1alpha1.TaskPhaseQueued
	staleTask.Status.SessionPodName = ""

	_, err := r.assignTask(context.Background(), staleTask)
	if err != nil {
		t.Fatalf("assignTask() returned error: %v", err)
	}

	// The pod annotation should have been rolled back (cleared).
	var updatedPod corev1.Pod
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: "default"}, &updatedPod); err != nil {
		t.Fatalf("Failed to get pod: %v", err)
	}
	if _, exists := updatedPod.Annotations[AnnotationAssignedTask]; exists {
		t.Error("Expected pod assignment annotation to be rolled back after race condition")
	}

	// The task should remain in Pending with its original assignment.
	var updatedTask kelosv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "already-assigned-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhasePending {
		t.Errorf("Task phase: expected Pending (unchanged), got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SessionPodName != "session-my-spawner-0" {
		t.Errorf("Task sessionPodName: expected 'session-my-spawner-0' (unchanged), got %q", updatedTask.Status.SessionPodName)
	}
}

func TestFindOldestQueuedTask(t *testing.T) {
	scheme := newTestScheme()
	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-10 * 1e9))

	task1 := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "task-newer",
			Namespace:         "default",
			CreationTimestamp: now,
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseQueued},
	}
	task2 := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "task-older",
			Namespace:         "default",
			CreationTimestamp: earlier,
			Labels: map[string]string{
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
				"kelos.dev/taskspawner": "my-spawner",
			},
		},
		Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseQueued},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task1, task2).
		WithStatusSubresource(task1, task2).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-my-spawner-0",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "my-spawner",
				"kelos.dev/component":   SessionComponentLabel,
			},
		},
	}

	requests := r.findOldestQueuedTask(context.Background(), pod)
	if len(requests) != 1 {
		t.Fatalf("Expected 1 request, got %d", len(requests))
	}
	if requests[0].Name != "task-older" {
		t.Errorf("Expected oldest task 'task-older', got %q", requests[0].Name)
	}
}
