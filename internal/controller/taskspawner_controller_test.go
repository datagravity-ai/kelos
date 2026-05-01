package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

func TestIsWebhookBased(t *testing.T) {
	tests := []struct {
		name string
		ts   *kelosv1alpha1.TaskSpawner
		want bool
	}{
		{
			name: "GitHub webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "Linear webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						LinearWebhook: &kelosv1alpha1.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "polling TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubIssues: &kelosv1alpha1.GitHubIssues{},
					},
				},
			},
			want: false,
		},
		{
			name: "cron TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						Cron: &kelosv1alpha1.Cron{
							Schedule: "0 9 * * 1",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "generic webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GenericWebhook: &kelosv1alpha1.GenericWebhook{
							Source: "notion",
							FieldMapping: map[string]string{
								"id": "$.data.id",
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWebhookBased(tt.ts)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReconcileWebhook(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelosv1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		ts             *kelosv1alpha1.TaskSpawner
		existingObjs   []client.Object
		isSuspended    bool
		wantPhase      kelosv1alpha1.TaskSpawnerPhase
		wantMessage    string
		wantDeployment bool
		wantCronJob    bool
	}{
		{
			name: "active webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseRunning,
			wantMessage: "Webhook-driven TaskSpawner ready",
		},
		{
			name: "suspended GitHub webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "suspended Linear webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook-linear",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						LinearWebhook: &kelosv1alpha1.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "webhook TaskSpawner with stale deployment",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelosv1alpha1.TaskSpawnerStatus{
					DeploymentName: "test-webhook",
				},
			},
			existingObjs: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-webhook",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kelos.dev/v1alpha1",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended:    false,
			wantPhase:      kelosv1alpha1.TaskSpawnerPhaseRunning,
			wantMessage:    "Webhook-driven TaskSpawner ready",
			wantDeployment: false, // Should be deleted
		},
		{
			name: "webhook TaskSpawner with stale cronjob",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelosv1alpha1.TaskSpawnerStatus{
					CronJobName: "test-webhook",
				},
			},
			existingObjs: []client.Object{
				&batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-webhook",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kelos.dev/v1alpha1",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseRunning,
			wantMessage: "Webhook-driven TaskSpawner ready",
			wantCronJob: false, // Should be deleted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := append([]client.Object{tt.ts}, tt.existingObjs...)
			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&kelosv1alpha1.TaskSpawner{}).
				Build()

			reconciler := &TaskSpawnerReconciler{
				Client: client,
				Scheme: scheme,
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.ts.Name,
					Namespace: tt.ts.Namespace,
				},
			}

			_, err := reconciler.reconcileWebhook(context.Background(), req, tt.ts, tt.isSuspended)
			require.NoError(t, err)

			// Check final TaskSpawner status
			var finalTs kelosv1alpha1.TaskSpawner
			err = client.Get(context.Background(), req.NamespacedName, &finalTs)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPhase, finalTs.Status.Phase)
			assert.Equal(t, tt.wantMessage, finalTs.Status.Message)
			assert.Empty(t, finalTs.Status.DeploymentName, "DeploymentName should be cleared")
			assert.Empty(t, finalTs.Status.CronJobName, "CronJobName should be cleared")

			// Check that stale resources are deleted
			var deployment appsv1.Deployment
			err = client.Get(context.Background(), req.NamespacedName, &deployment)
			if tt.wantDeployment {
				assert.NoError(t, err, "Deployment should exist")
			} else {
				assert.True(t, apierrors.IsNotFound(err), "Deployment should not exist")
			}

			var cronJob batchv1.CronJob
			err = client.Get(context.Background(), req.NamespacedName, &cronJob)
			if tt.wantCronJob {
				assert.NoError(t, err, "CronJob should exist")
			} else {
				assert.True(t, apierrors.IsNotFound(err), "CronJob should not exist")
			}
		})
	}
}

func TestResolveSessionGitHubAppToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_test_session_token",
			"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-creds",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	tc := &githubapp.TokenClient{
		BaseURL: server.URL,
		Client:  server.Client(),
	}

	r := &TaskSpawnerReconciler{
		Client:      cl,
		Scheme:      scheme,
		TokenClient: tc,
	}

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	result, requeueAfter, err := r.resolveSessionGitHubAppToken(context.Background(), ts, workspace)
	require.NoError(t, err)

	assert.Equal(t, "session-test-spawner-github-token", result.SecretRef.Name)
	assert.True(t, requeueAfter > 0, "requeueAfter should be positive")

	// Verify the derived secret was created with the correct token.
	var tokenSecret corev1.Secret
	err = cl.Get(context.Background(), types.NamespacedName{
		Name:      "session-test-spawner-github-token",
		Namespace: "default",
	}, &tokenSecret)
	require.NoError(t, err)
	token := string(tokenSecret.Data["GITHUB_TOKEN"])
	if token == "" {
		token = tokenSecret.StringData["GITHUB_TOKEN"]
	}
	assert.Equal(t, "ghs_test_session_token", token)
}

func TestResolveSessionGitHubAppToken_PATSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pat-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"GITHUB_TOKEN": []byte("ghp_test"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	r := &TaskSpawnerReconciler{
		Client: cl,
		Scheme: scheme,
	}

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "pat-secret",
		},
	}

	result, requeueAfter, err := r.resolveSessionGitHubAppToken(context.Background(), ts, workspace)
	require.NoError(t, err)
	assert.Equal(t, "pat-secret", result.SecretRef.Name, "PAT secret should pass through unchanged")
	assert.Zero(t, requeueAfter, "no requeue needed for PAT secrets")
}
