package controller

import (
	"context"
	"testing"

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

func TestSessionReconciler_MarksTaskFailedWhenPodDeleted(t *testing.T) {
	scheme := newTestScheme()
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
		},
	}

	// Pod does NOT exist.
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task).
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
	if updatedTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		t.Errorf("Task phase: expected Failed, got %s", updatedTask.Status.Phase)
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
