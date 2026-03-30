package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/webhook"
)

func signPayload(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var _ = Describe("Webhook Handler", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("GitHub webhook triggers task creation", func() {
		var (
			ns      *corev1.Namespace
			handler *webhook.Handler
			secret  = []byte("test-webhook-secret")
		)

		BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-webhook-%d", rand.Intn(1000000)),
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			handler = &webhook.Handler{
				Client: k8sClient,
				Log:    logf.Log.WithName("test-webhook-handler"),
				Source: "github",
				Secret: secret,
			}
		})

		It("Should create a Task when a matching webhook arrives", func() {
			By("Creating a Workspace")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ws",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with githubWebhook")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "webhook-test-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issue_comment"},
							Filters: []kelosv1alpha1.GitHubWebhookFilter{
								{
									Event:        "issue_comment",
									Action:       "created",
									BodyContains: "/fix",
								},
							},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeNone,
						},
						WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
							Name: "test-ws",
						},
						PromptTemplate: `Fix request from {{.Sender}} on #{{.Number}}: {{.Body}}`,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Sending a matching webhook")
			payload := map[string]interface{}{
				"action": "created",
				"sender": map[string]interface{}{"login": "testuser"},
				"issue": map[string]interface{}{
					"number":   42,
					"state":    "open",
					"title":    "Bug report",
					"body":     "Something is broken",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels":   []interface{}{},
				},
				"comment": map[string]interface{}{
					"body": "/fix please address this",
				},
			}
			payloadBytes, err := json.Marshal(payload)
			Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req.Header.Set("X-GitHub-Event", "issue_comment")
			req.Header.Set("X-GitHub-Delivery", "test-delivery-123")
			req.Header.Set("X-Hub-Signature-256", signPayload(payloadBytes, secret))

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			Expect(rr.Code).To(Equal(http.StatusOK))

			By("Verifying the Task was created")
			Eventually(func() error {
				var taskList kelosv1alpha1.TaskList
				if err := k8sClient.List(ctx, &taskList,
					client.InNamespace(ns.Name),
					client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
				); err != nil {
					return err
				}
				if len(taskList.Items) == 0 {
					return Errorf("no tasks found")
				}
				return nil
			}, timeout, interval).Should(Succeed())

			var taskList kelosv1alpha1.TaskList
			Expect(k8sClient.List(ctx, &taskList,
				client.InNamespace(ns.Name),
				client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
			)).Should(Succeed())

			task := taskList.Items[0]
			Expect(task.Annotations["kelos.dev/webhook-source"]).To(Equal("github"))
			Expect(task.Annotations["kelos.dev/webhook-delivery"]).To(Equal("test-delivery-123"))
			Expect(task.Annotations["kelos.dev/webhook-event"]).To(Equal("issue_comment"))
			Expect(task.Spec.Prompt).To(ContainSubstring("testuser"))
			Expect(task.Spec.Prompt).To(ContainSubstring("/fix please address this"))
		})

		It("Should reject requests with invalid signatures", func() {
			payload := []byte(`{"action":"created"}`)

			req := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payload))
			req.Header.Set("X-GitHub-Event", "issue_comment")
			req.Header.Set("X-Hub-Signature-256", "sha256=invalid")

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			Expect(rr.Code).To(Equal(http.StatusUnauthorized))
		})

		It("Should not create duplicate tasks for the same delivery ID", func() {
			By("Creating a TaskSpawner")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "webhook-dedup-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"push"},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeNone,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			payload := map[string]interface{}{
				"ref":    "refs/heads/main",
				"action": "",
				"sender": map[string]interface{}{"login": "user"},
			}
			payloadBytes, _ := json.Marshal(payload)
			sig := signPayload(payloadBytes, secret)

			By("Sending first webhook")
			req1 := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req1.Header.Set("X-GitHub-Event", "push")
			req1.Header.Set("X-GitHub-Delivery", "dedup-delivery-456")
			req1.Header.Set("X-Hub-Signature-256", sig)

			rr1 := httptest.NewRecorder()
			handler.ServeHTTP(rr1, req1)
			Expect(rr1.Code).To(Equal(http.StatusOK))

			By("Sending duplicate webhook with same delivery ID")
			req2 := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req2.Header.Set("X-GitHub-Event", "push")
			req2.Header.Set("X-GitHub-Delivery", "dedup-delivery-456")
			req2.Header.Set("X-Hub-Signature-256", sig)

			rr2 := httptest.NewRecorder()
			handler.ServeHTTP(rr2, req2)
			Expect(rr2.Code).To(Equal(http.StatusOK))

			By("Verifying only one task was created")
			var taskList kelosv1alpha1.TaskList
			Expect(k8sClient.List(ctx, &taskList,
				client.InNamespace(ns.Name),
				client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
			)).Should(Succeed())
			Expect(taskList.Items).To(HaveLen(1))
		})

		It("Should return 503 when at maxConcurrency", func() {
			maxConc := int32(1)
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "webhook-maxconc-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					MaxConcurrency: &maxConc,
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"pull_request_review"},
							Filters: []kelosv1alpha1.GitHubWebhookFilter{
								{Event: "pull_request_review", Action: "submitted"},
							},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeNone,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			payload := map[string]interface{}{
				"action": "submitted",
				"sender": map[string]interface{}{"login": "user"},
				"review": map[string]interface{}{"body": "changes requested", "state": "changes_requested"},
				"pull_request": map[string]interface{}{
					"number": 99,
					"state":  "open",
					"title":  "Test PR",
				},
			}
			payloadBytes, _ := json.Marshal(payload)
			sig := signPayload(payloadBytes, secret)

			By("Sending first webhook to fill capacity")
			req1 := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req1.Header.Set("X-GitHub-Event", "pull_request_review")
			req1.Header.Set("X-GitHub-Delivery", "maxconc-review-1")
			req1.Header.Set("X-Hub-Signature-256", sig)

			rr1 := httptest.NewRecorder()
			handler.ServeHTTP(rr1, req1)
			Expect(rr1.Code).To(Equal(http.StatusOK))

			By("Sending second webhook that should be rejected")
			req2 := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req2.Header.Set("X-GitHub-Event", "pull_request_review")
			req2.Header.Set("X-GitHub-Delivery", "maxconc-review-2")
			req2.Header.Set("X-Hub-Signature-256", sig)

			rr2 := httptest.NewRecorder()
			handler.ServeHTTP(rr2, req2)
			Expect(rr2.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(rr2.Header().Get("Retry-After")).To(Equal("30"))
		})

		It("Should not create tasks when filters don't match", func() {
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "webhook-nomatch-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issue_comment"},
							Filters: []kelosv1alpha1.GitHubWebhookFilter{
								{Event: "issue_comment", BodyContains: "/deploy"},
							},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeNone,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			payload := map[string]interface{}{
				"action":  "created",
				"sender":  map[string]interface{}{"login": "user"},
				"comment": map[string]interface{}{"body": "looks good"},
			}
			payloadBytes, _ := json.Marshal(payload)
			sig := signPayload(payloadBytes, secret)

			req := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payloadBytes))
			req.Header.Set("X-GitHub-Event", "issue_comment")
			req.Header.Set("X-GitHub-Delivery", "nomatch-1")
			req.Header.Set("X-Hub-Signature-256", sig)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			Expect(rr.Code).To(Equal(http.StatusOK))

			var taskList kelosv1alpha1.TaskList
			Expect(k8sClient.List(ctx, &taskList,
				client.InNamespace(ns.Name),
				client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
			)).Should(Succeed())
			Expect(taskList.Items).To(BeEmpty())
		})
	})
})

func Errorf(format string, args ...interface{}) error {
	return &errorf{msg: format}
}

type errorf struct{ msg string }

func (e *errorf) Error() string { return e.msg }
