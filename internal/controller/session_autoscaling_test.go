package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func int32Ptr(i int32) *int32 { return &i }

func newAutoscalingReconciler(scheme *runtime.Scheme, objs ...runtime.Object) *TaskSpawnerReconciler {
	clientObjs := make([]runtime.Object, 0, len(objs))
	clientObjs = append(clientObjs, objs...)

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range clientObjs {
		builder = builder.WithRuntimeObjects(obj)
	}
	// Register status subresources for Tasks.
	var tasks []*kelosv1alpha1.Task
	for _, obj := range clientObjs {
		if t, ok := obj.(*kelosv1alpha1.Task); ok {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) > 0 {
		statusObjs := make([]runtime.Object, 0, len(tasks))
		for _, t := range tasks {
			statusObjs = append(statusObjs, t)
		}
	}

	fakeClient := builder.Build()

	return &TaskSpawnerReconciler{
		Client:                    fakeClient,
		Scheme:                    scheme,
		SessionStatefulSetBuilder: NewSessionStatefulSetBuilder(),
		Recorder:                  record.NewFakeRecorder(10),
	}
}

func makeSessionPod(name, namespace, spawner string, assigned bool, idleSince *time.Time) *corev1.Pod {
	annotations := map[string]string{}
	if assigned {
		annotations[AnnotationAssignedTask] = "some-task"
	}
	if idleSince != nil {
		annotations[AnnotationIdleSince] = idleSince.UTC().Format(time.RFC3339)
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"kelos.dev/taskspawner": spawner,
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: annotations,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

func makeQueuedTask(name, namespace, spawner string) *kelosv1alpha1.Task {
	return &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"kelos.dev/taskspawner": spawner,
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}
}

func TestComputeAutoscaledReplicas_ScaleUp(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(1),
					MaxReplicas: 5,
				},
			},
		},
	}

	// 1 busy pod, 0 idle, 3 queued tasks -> scale up by 3 (to 4).
	pod := makeSessionPod("pod-0", ns, spawnerName, true, nil)
	task1 := makeQueuedTask("task-1", ns, spawnerName)
	task2 := makeQueuedTask("task-2", ns, spawnerName)
	task3 := makeQueuedTask("task-3", ns, spawnerName)

	r := newAutoscalingReconciler(scheme, ts, pod, task1, task2, task3)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(4), replicas)
}

func TestComputeAutoscaledReplicas_ScaleUpCappedAtMax(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(1),
					MaxReplicas: 3,
				},
			},
		},
	}

	// 1 busy pod, 5 queued -> wants 6, capped at 3.
	pod := makeSessionPod("pod-0", ns, spawnerName, true, nil)
	tasks := make([]runtime.Object, 5)
	for i := range tasks {
		tasks[i] = makeQueuedTask("task-"+string(rune('a'+i)), ns, spawnerName)
	}

	objs := append([]runtime.Object{ts, pod}, tasks...)
	r := newAutoscalingReconciler(scheme, objs...)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(3), replicas)
}

func TestComputeAutoscaledReplicas_NoScaleUpWhenIdlePodsAvailable(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(1),
					MaxReplicas: 5,
				},
			},
		},
	}

	// 1 busy pod, 1 idle pod, 2 queued tasks -> no scale up (idle pod available).
	idleTime := time.Now().Add(-10 * time.Second)
	pod1 := makeSessionPod("pod-0", ns, spawnerName, true, nil)
	pod2 := makeSessionPod("pod-1", ns, spawnerName, false, &idleTime)
	task1 := makeQueuedTask("task-1", ns, spawnerName)
	task2 := makeQueuedTask("task-2", ns, spawnerName)

	r := newAutoscalingReconciler(scheme, ts, pod1, pod2, task1, task2)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(2), replicas) // maintain current
}

func TestComputeAutoscaledReplicas_ScaleDown(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas:                   int32Ptr(1),
					MaxReplicas:                   5,
					ScaleDownStabilizationSeconds: int32Ptr(60),
				},
			},
		},
	}

	// 3 pods all idle for > 60s, no queued tasks -> scale down to min (1).
	longIdle := time.Now().Add(-2 * time.Minute)
	pod1 := makeSessionPod("pod-0", ns, spawnerName, false, &longIdle)
	pod2 := makeSessionPod("pod-1", ns, spawnerName, false, &longIdle)
	pod3 := makeSessionPod("pod-2", ns, spawnerName, false, &longIdle)

	r := newAutoscalingReconciler(scheme, ts, pod1, pod2, pod3)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(1), replicas) // clamped to min
}

func TestComputeAutoscaledReplicas_NoScaleDownBeforeStabilization(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas:                   int32Ptr(1),
					MaxReplicas:                   5,
					ScaleDownStabilizationSeconds: int32Ptr(300),
				},
			},
		},
	}

	// 2 idle pods but only idle for 10 seconds (< 300s) -> no scale down.
	recentIdle := time.Now().Add(-10 * time.Second)
	pod1 := makeSessionPod("pod-0", ns, spawnerName, false, &recentIdle)
	pod2 := makeSessionPod("pod-1", ns, spawnerName, false, &recentIdle)

	r := newAutoscalingReconciler(scheme, ts, pod1, pod2)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(2), replicas) // maintain current (no eligible pods)
}

func TestComputeAutoscaledReplicas_MinReplicasZero(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas:                   int32Ptr(0),
					MaxReplicas:                   5,
					ScaleDownStabilizationSeconds: int32Ptr(60),
				},
			},
		},
	}

	// 1 idle pod past stabilization, no queued tasks -> scale to 0.
	longIdle := time.Now().Add(-2 * time.Minute)
	pod := makeSessionPod("pod-0", ns, spawnerName, false, &longIdle)

	r := newAutoscalingReconciler(scheme, ts, pod)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(0), replicas)
}

func TestComputeAutoscaledReplicas_NoPods(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(1),
					MaxReplicas: 5,
				},
			},
		},
	}

	// No pods running yet, 2 queued tasks -> scale to 2.
	task1 := makeQueuedTask("task-1", ns, spawnerName)
	task2 := makeQueuedTask("task-2", ns, spawnerName)

	r := newAutoscalingReconciler(scheme, ts, task1, task2)

	replicas, err := r.computeAutoscaledReplicas(context.Background(), ts)
	require.NoError(t, err)
	assert.Equal(t, int32(2), replicas)
}

func TestSessionReconciler_SetsIdleSinceOnClearAssignment(t *testing.T) {
	scheme := newTestScheme()
	ns := "default"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: ns,
			Labels: map[string]string{
				"kelos.dev/component": SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationAssignedTask: "my-task",
				AnnotationTaskStatus:   "succeeded",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	err := r.clearPodAssignment(context.Background(), ns, "session-pod-0")
	require.NoError(t, err)

	// Verify the pod has idle-since annotation set and assigned-task removed.
	var updatedPod corev1.Pod
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: "session-pod-0", Namespace: ns,
	}, &updatedPod)
	require.NoError(t, err)

	assert.Empty(t, updatedPod.Annotations[AnnotationAssignedTask])
	assert.Empty(t, updatedPod.Annotations[AnnotationTaskStatus])
	assert.NotEmpty(t, updatedPod.Annotations[AnnotationIdleSince])

	// Verify it parses as valid RFC3339.
	_, parseErr := time.Parse(time.RFC3339, updatedPod.Annotations[AnnotationIdleSince])
	assert.NoError(t, parseErr)
}

func TestSessionReconciler_RemovesIdleSinceOnAssignment(t *testing.T) {
	scheme := newTestScheme()
	ns := "default"
	spawnerName := "my-spawner"

	idleTime := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: ns,
			Labels: map[string]string{
				"kelos.dev/taskspawner": spawnerName,
				"kelos.dev/component":   SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationIdleSince: idleTime,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "queued-task",
			Namespace: ns,
			Labels: map[string]string{
				"kelos.dev/taskspawner": spawnerName,
				LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseQueued,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod, task).
		WithStatusSubresource(task).
		Build()

	r := &SessionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.assignTask(context.Background(), task)
	require.NoError(t, err)

	// Verify idle-since is removed and assigned-task is set.
	var updatedPod corev1.Pod
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: "session-pod-0", Namespace: ns,
	}, &updatedPod)
	require.NoError(t, err)

	assert.Equal(t, "queued-task", updatedPod.Annotations[AnnotationAssignedTask])
	assert.Empty(t, updatedPod.Annotations[AnnotationIdleSince])
}
