package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func int32Ptr(i int32) *int32 { return &i }

func TestReconcileSessionHPA_CreatesHPA(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns, UID: "test-uid"},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas:                   int32Ptr(2),
					MaxReplicas:                   10,
					ScaleDownStabilizationSeconds: int32Ptr(180),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:                    fakeClient,
		Scheme:                    scheme,
		SessionStatefulSetBuilder: NewSessionStatefulSetBuilder(),
		Recorder:                  record.NewFakeRecorder(10),
	}

	stsName := sessionStatefulSetName(spawnerName)
	err := r.reconcileSessionHPA(context.Background(), ts, stsName)
	require.NoError(t, err)

	// Verify HPA was created.
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: ns, Name: sessionHPAName(spawnerName),
	}, &hpa)
	require.NoError(t, err)

	assert.Equal(t, stsName, hpa.Spec.ScaleTargetRef.Name)
	assert.Equal(t, "StatefulSet", hpa.Spec.ScaleTargetRef.Kind)
	assert.Equal(t, int32(2), *hpa.Spec.MinReplicas)
	assert.Equal(t, int32(10), hpa.Spec.MaxReplicas)
	assert.Equal(t, int32(180), *hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds)

	// Verify metric configuration.
	require.Len(t, hpa.Spec.Metrics, 1)
	assert.Equal(t, autoscalingv2.PodsMetricSourceType, hpa.Spec.Metrics[0].Type)
	assert.Equal(t, "kelos_session_tasks_queued", hpa.Spec.Metrics[0].Pods.Metric.Name)
}

func TestReconcileSessionHPA_UpdatesExistingHPA(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns, UID: "test-uid"},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(3),
					MaxReplicas: 15,
				},
			},
		},
	}

	oldStab := int32(300)
	existingHPA := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionHPAName(spawnerName),
			Namespace: ns,
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       sessionStatefulSetName(spawnerName),
			},
			MinReplicas: int32Ptr(1),
			MaxReplicas: 5,
			Behavior: &autoscalingv2.HorizontalPodAutoscalerBehavior{
				ScaleDown: &autoscalingv2.HPAScalingRules{
					StabilizationWindowSeconds: &oldStab,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ts, existingHPA).
		Build()

	r := &TaskSpawnerReconciler{
		Client:                    fakeClient,
		Scheme:                    scheme,
		SessionStatefulSetBuilder: NewSessionStatefulSetBuilder(),
		Recorder:                  record.NewFakeRecorder(10),
	}

	stsName := sessionStatefulSetName(spawnerName)
	err := r.reconcileSessionHPA(context.Background(), ts, stsName)
	require.NoError(t, err)

	// Verify HPA was updated.
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: ns, Name: sessionHPAName(spawnerName),
	}, &hpa)
	require.NoError(t, err)

	assert.Equal(t, int32(3), *hpa.Spec.MinReplicas)
	assert.Equal(t, int32(15), hpa.Spec.MaxReplicas)

	// Verify Metrics are reconciled (overwritten from empty to correct spec).
	require.Len(t, hpa.Spec.Metrics, 1)
	assert.Equal(t, autoscalingv2.PodsMetricSourceType, hpa.Spec.Metrics[0].Type)
	assert.Equal(t, "kelos_session_tasks_queued", hpa.Spec.Metrics[0].Pods.Metric.Name)

	// Verify Behavior is reconciled (scale-down and scale-up rules applied).
	require.NotNil(t, hpa.Spec.Behavior)
	require.NotNil(t, hpa.Spec.Behavior.ScaleDown)
	require.NotNil(t, hpa.Spec.Behavior.ScaleUp)
	assert.Equal(t, int32(300), *hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds)
	require.Len(t, hpa.Spec.Behavior.ScaleUp.Policies, 2)
}

func TestDeleteSessionHPA(t *testing.T) {
	scheme := newTestScheme()
	spawnerName := "my-spawner"
	ns := "default"

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: spawnerName, Namespace: ns},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{},
		},
	}

	existingHPA := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionHPAName(spawnerName),
			Namespace: ns,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ts, existingHPA).
		Build()

	r := &TaskSpawnerReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	err := r.deleteSessionHPA(context.Background(), ts)
	require.NoError(t, err)

	// Verify HPA was deleted.
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: ns, Name: sessionHPAName(spawnerName),
	}, &hpa)
	assert.True(t, err != nil, "HPA should be deleted")
}

func TestReplicaNotOverwrittenWhenAutoscalingEnabled(t *testing.T) {
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "my-spawner", Namespace: "default"},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Replicas: int32Ptr(2),
				Autoscaling: &kelosv1alpha1.SessionAutoscalingConfig{
					MinReplicas: int32Ptr(1),
					MaxReplicas: 8,
				},
			},
		},
	}

	autoscalingEnabled := ts.Spec.SessionConfig != nil && ts.Spec.SessionConfig.Autoscaling != nil
	assert.True(t, autoscalingEnabled)
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

	var updatedPod corev1.Pod
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: "session-pod-0", Namespace: ns,
	}, &updatedPod)
	require.NoError(t, err)

	assert.Empty(t, updatedPod.Annotations[AnnotationAssignedTask])
	assert.Empty(t, updatedPod.Annotations[AnnotationTaskStatus])
	assert.NotEmpty(t, updatedPod.Annotations[AnnotationIdleSince])

	_, parseErr := time.Parse(time.RFC3339, updatedPod.Annotations[AnnotationIdleSince])
	assert.NoError(t, parseErr)
}

func TestSessionReconciler_ClearAssignmentPreservesExistingIdleSince(t *testing.T) {
	scheme := newTestScheme()
	ns := "default"

	existingIdleTime := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: ns,
			Labels: map[string]string{
				"kelos.dev/component": SessionComponentLabel,
			},
			Annotations: map[string]string{
				AnnotationIdleSince: existingIdleTime,
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

	var updatedPod corev1.Pod
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: "session-pod-0", Namespace: ns,
	}, &updatedPod)
	require.NoError(t, err)

	assert.Equal(t, existingIdleTime, updatedPod.Annotations[AnnotationIdleSince])
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

	var updatedPod corev1.Pod
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: "session-pod-0", Namespace: ns,
	}, &updatedPod)
	require.NoError(t, err)

	assert.Equal(t, "queued-task", updatedPod.Annotations[AnnotationAssignedTask])
	assert.Empty(t, updatedPod.Annotations[AnnotationIdleSince])
}
