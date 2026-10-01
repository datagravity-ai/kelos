package consoleserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

type reviewFunc func(context.Context, *authorizationv1.SubjectAccessReview, metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error)

func (f reviewFunc) Create(ctx context.Context, review *authorizationv1.SubjectAccessReview, opts metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
	return f(ctx, review, opts)
}

func allowReview(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
	review.Status.Allowed = true
	return review, nil
}

func oidcTestServer(t *testing.T, reviewer AccessReviewer) *Server {
	t.Helper()
	s := testServer(t)
	server, err := New(Config{
		AuthMode: AuthModeOIDC,
		OIDC: &OIDCConfig{
			ExternalURL: "https://console.example", UsernamePrefix: "oidc:", GroupsPrefix: "oidc:",
			Reviewer: reviewer, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		},
		Client: s.client, Clientset: s.clientset, RESTConfig: s.restConfig, DefaultNamespace: "team-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func oidcRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:4180"
	request.Header.Set("X-Kelos-User", "alice")
	request.Header.Set("X-Kelos-Groups", "developers,system:masters")
	request.Header.Set("Origin", "https://console.example")
	return request
}

func TestOIDCIdentityValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*http.Request)
	}{
		{"non-loopback", func(r *http.Request) { r.RemoteAddr = "10.0.0.1:1234" }},
		{"missing user", func(r *http.Request) { r.Header.Del("X-Kelos-User") }},
		{"empty user", func(r *http.Request) { r.Header.Set("X-Kelos-User", "") }},
		{"duplicate user", func(r *http.Request) { r.Header.Add("X-Kelos-User", "admin") }},
		{"oversized user", func(r *http.Request) { r.Header.Set("X-Kelos-User", strings.Repeat("a", 1025)) }},
		{"user control", func(r *http.Request) { r.Header.Set("X-Kelos-User", "alice\nadmin") }},
		{"user whitespace", func(r *http.Request) { r.Header.Set("X-Kelos-User", " alice") }},
		{"user invalid UTF8", func(r *http.Request) { r.Header.Set("X-Kelos-User", "\xff") }},
		{"duplicate groups", func(r *http.Request) { r.Header.Add("X-Kelos-Groups", "admins") }},
		{"empty groups", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", "") }},
		{"empty group entry", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", "developers,,admins") }},
		{"group control", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", "developers,ad\tmins") }},
		{"group whitespace", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", "developers, admins") }},
		{"oversized group", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", strings.Repeat("a", 257)) }},
		{"oversized groups", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", strings.Repeat("a", 8193)) }},
		{"too many groups", func(r *http.Request) { r.Header.Set("X-Kelos-Groups", strings.Repeat("a,", 128)+"a") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := oidcTestServer(t, reviewFunc(func(context.Context, *authorizationv1.SubjectAccessReview, metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				t.Fatal("invalid identity reached authorization")
				return nil, nil
			}))
			r := oidcRequest(http.MethodGet, "/api/sessions", "")
			r.Header.Set("Authorization", "Bearer secret-token")
			test.change(r)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOIDCPrincipalAndAudit(t *testing.T) {
	var logs bytes.Buffer
	s := oidcTestServer(t, reviewFunc(func(ctx context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("review has no deadline")
		}
		if r.Spec.User != "oidc:alice" || !reflect.DeepEqual(r.Spec.Groups, []string{"oidc:developers", "oidc:system:masters"}) {
			t.Fatalf("principal = %#v", r.Spec)
		}
		r.Status.Allowed = true
		return r, nil
	}))
	s.oidc.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	r := oidcRequest(http.MethodGet, "/api/sessions", "private prompt")
	r.Header.Set("Cookie", "private-cookie")
	r.Header.Set("Authorization", "Bearer private-token")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	for _, expected := range []string{`"username":"oidc:alice"`, `"decision":"allowed"`, `"namespace":"team-a"`, `"resource":"sessions"`, `"action":"list"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Errorf("audit missing %s: %s", expected, logs.String())
		}
	}
	if strings.Contains(logs.String(), "private") {
		t.Fatalf("audit leaked request data: %s", logs.String())
	}
	// User RoleBindings also work for identities with no group claim.
	s.oidc.Reviewer = reviewFunc(allowReview)
	r.Header.Del("X-Kelos-Groups")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("user without groups status = %d", w.Code)
	}
}

func TestOIDCOriginAndStaticModeIsolation(t *testing.T) {
	s := oidcTestServer(t, reviewFunc(allowReview))
	for _, origin := range []string{"", "https://attacker.example", "http://console.example"} {
		for _, target := range []struct{ method, path string }{
			{"POST", "/api/sessions/team-a/chat/attachments"}, {"POST", "/api/logout"}, {"GET", "/api/sessions/team-a/chat/connect"},
		} {
			r := oidcRequest(target.method, target.path, "")
			r.Header.Set("Origin", origin)
			if target.method == "GET" {
				r.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s origin %q status = %d", target.path, origin, w.Code)
			}
		}
	}
	r := oidcRequest("GET", "/api/sessions", "")
	w := httptest.NewRecorder()
	testServer(t).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("static mode trusted headers: %d", w.Code)
	}
	r = oidcRequest("POST", "/api/login", `{"token":"secret-token"}`)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("OIDC accepted static login: %d", w.Code)
	}
	r = oidcRequest("POST", "/api/logout", "")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/oauth2/sign_out?rd=/oauth2/sign_in") {
		t.Fatalf("logout = %d %s", w.Code, w.Body.String())
	}
}

type observedClient struct {
	client.Client
	calls []string
}

func (c *observedClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	c.calls = append(c.calls, "get")
	return c.Client.Get(ctx, key, object, opts...)
}
func (c *observedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.calls = append(c.calls, "list")
	return c.Client.List(ctx, list, opts...)
}
func (c *observedClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	c.calls = append(c.calls, "create")
	return c.Client.Create(ctx, object, opts...)
}
func (c *observedClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.calls = append(c.calls, "patch")
	return c.Client.Patch(ctx, object, patch, opts...)
}
func (c *observedClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	c.calls = append(c.calls, "delete")
	return c.Client.Delete(ctx, object, opts...)
}
func (c *observedClient) Apply(ctx context.Context, object runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	c.calls = append(c.calls, "apply")
	return c.Client.Apply(ctx, object, opts...)
}

func TestOIDCEndpointAuthorization(t *testing.T) {
	getSession := access("get", "sessions", "team-a", "chat")
	manifest := "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\n  namespace: team-a\nspec:\n  worker:\n    type: codex\n"
	for _, test := range []struct {
		name, method, path, body string
		checks                   []authorizationv1.ResourceAttributes
	}{
		{"list", "GET", "/api/sessions", "", []authorizationv1.ResourceAttributes{access("list", "sessions", "team-a", "")}},
		{"options", "GET", "/api/options", "", []authorizationv1.ResourceAttributes{access("list", "sessions", "team-a", ""), access("list", "workspaces", "team-a", ""), access("list", "agentconfigs", "team-a", "")}},
		{"source", "GET", "/api/sessions/team-a/chat", "", []authorizationv1.ResourceAttributes{getSession}},
		{"YAML", "GET", "/api/resources/sessions/team-a/chat", "", []authorizationv1.ResourceAttributes{getSession}},
		{"pipeline YAML", "GET", "/api/resources/taskpipelines/team-a/pipeline", "", []authorizationv1.ResourceAttributes{access("get", "taskpipelines", "team-a", "pipeline")}},
		{"logs", "GET", "/api/resources/tasks/team-a/task/logs", "", []authorizationv1.ResourceAttributes{access("get", "tasks", "team-a", "task"), access("get", "tasks/logs", "team-a", "task")}},
		{"create", "POST", "/api/sessions", `{"name":"created","namespace":"team-a","worker":{"type":"codex"}}`, []authorizationv1.ResourceAttributes{access("create", "sessions", "team-a", "")}},
		{"apply", "POST", "/api/sessions/apply?namespace=team-a", manifest, []authorizationv1.ResourceAttributes{getSession, access("patch", "sessions", "team-a", "chat"), access("create", "sessions", "team-a", "")}},
		{"section", "PATCH", "/api/sessions/team-a/chat/section", `{"section":"Work"}`, []authorizationv1.ResourceAttributes{getSession, access("patch", "sessions", "team-a", "chat")}},
		{"display name", "PATCH", "/api/sessions/team-a/chat/display-name", `{"displayName":"Work"}`, []authorizationv1.ResourceAttributes{getSession, access("patch", "sessions", "team-a", "chat")}},
		{"delete", "DELETE", "/api/sessions/team-a/chat", "", []authorizationv1.ResourceAttributes{access("delete", "sessions", "team-a", "chat")}},
		{"reset", "POST", "/api/sessions/team-a/chat/reset", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/reset", "team-a", "chat")}},
		{"suspend", "POST", "/api/sessions/team-a/chat/suspend", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/suspend", "team-a", "chat")}},
		{"resume", "POST", "/api/sessions/team-a/chat/resume", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/resume", "team-a", "chat")}},
		{"chat", "GET", "/api/sessions/team-a/chat/connect", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/connect", "team-a", "chat")}},
		{"terminal", "GET", "/api/sessions/team-a/chat/exec", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/connect", "team-a", "chat")}},
		{"upload", "POST", "/api/sessions/team-a/chat/attachments", "", []authorizationv1.ResourceAttributes{getSession, access("create", "sessions/attachments", "team-a", "chat")}},
		{"download", "GET", "/api/sessions/team-a/chat/attachments/file", "", []authorizationv1.ResourceAttributes{getSession, access("get", "sessions/attachments", "team-a", "chat")}},
	} {
		for index := range test.checks {
			for _, failure := range []string{"denied", "transport", "evaluation"} {
				t.Run(fmt.Sprintf("%s/%d/%s", test.name, index, failure), func(t *testing.T) {
					var checked []authorizationv1.ResourceAttributes
					s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
						checked = append(checked, *r.Spec.ResourceAttributes)
						r.Status.Allowed = true
						if len(checked)-1 == index {
							switch failure {
							case "denied":
								r.Status.Allowed = false
							case "transport":
								return nil, errors.New("offline")
							case "evaluation":
								r.Status.EvaluationError = "incomplete"
							}
						}
						return r, nil
					}))
					observed := &observedClient{Client: s.client}
					s.client = observed
					r := oidcRequest(test.method, test.path, test.body)
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					want := http.StatusForbidden
					if failure != "denied" {
						want = http.StatusServiceUnavailable
					}
					if w.Code != want {
						t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
					}
					if !reflect.DeepEqual(checked, test.checks[:index+1]) {
						t.Fatalf("checks = %#v, want %#v", checked, test.checks[:index+1])
					}
					if len(observed.calls) != 0 {
						t.Fatalf("denied request accessed resources: %v", observed.calls)
					}
				})
			}
		}
	}
}

func TestOIDCSessionActions(t *testing.T) {
	for _, test := range []struct {
		method, path, body, operation string
		status                        int
	}{
		{"GET", "/api/sessions", "", "list", 200},
		{"GET", "/api/options", "", "list", 200},
		{"GET", "/api/sessions/team-a/chat", "", "get", 200},
		{"GET", "/api/resources/sessions/team-a/chat", "", "get", 200},
		{"POST", "/api/sessions", `{"name":"created","worker":{"type":"codex"}}`, "create", 201},
		{"POST", "/api/sessions/apply", "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: applied\nspec:\n  worker:\n    type: codex\n", "apply", 200},
		{"PATCH", "/api/sessions/team-a/chat/section", `{"section":"Work"}`, "patch", 200},
		{"PATCH", "/api/sessions/team-a/chat/display-name", `{"displayName":"Work"}`, "patch", 200},
		{"DELETE", "/api/sessions/team-a/chat", "", "delete", 204},
		{"POST", "/api/sessions/team-a/chat/reset", "", "patch", 202},
		{"POST", "/api/sessions/team-a/chat/suspend", "", "patch", 202},
		{"POST", "/api/sessions/team-a/chat/resume", "", "patch", 202},
	} {
		t.Run(test.path+test.method, func(t *testing.T) {
			s := oidcTestServer(t, reviewFunc(allowReview))
			session := terminalTestSession()
			session.Spec.Worker.Type = "codex"
			if strings.HasSuffix(test.path, "/resume") {
				suspended := true
				session.Spec.Suspend = &suspended
			}
			if err := s.client.Create(t.Context(), session); err != nil {
				t.Fatal(err)
			}
			observed := &observedClient{Client: s.client}
			s.client = observed
			w := httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest(test.method, test.path, test.body))
			if w.Code != test.status {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(strings.Join(observed.calls, ","), test.operation) {
				t.Fatalf("action was not executed: %v", observed.calls)
			}
		})
	}
}

func TestOIDCInventoryFiltersRelationships(t *testing.T) {
	s := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		review.Status.Allowed = review.Spec.ResourceAttributes.Resource == "tasks"
		return review, nil
	}))
	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "visible-task", Namespace: "team-a", OwnerReferences: []metav1.OwnerReference{{Kind: "TaskPipeline", Name: "hidden-pipeline"}}}}
	if err := s.client.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, oidcRequest("GET", "/api/resources", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "visible-task") || strings.Contains(w.Body.String(), "hidden-pipeline") {
		t.Fatalf("inventory = %d %s", w.Code, w.Body.String())
	}
	for _, failure := range []string{"denied", "error"} {
		s.oidc.Reviewer = reviewFunc(func(context.Context, *authorizationv1.SubjectAccessReview, metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
			if failure == "error" {
				return nil, errors.New("offline")
			}
			return &authorizationv1.SubjectAccessReview{}, nil
		})
		observed := &observedClient{Client: s.client}
		s.client = observed
		w = httptest.NewRecorder()
		s.ServeHTTP(w, oidcRequest("GET", "/api/resources", ""))
		want := 403
		if failure == "error" {
			want = 503
		}
		if w.Code != want || len(observed.calls) != 0 {
			t.Fatalf("inventory %s = %d, resource calls = %v", failure, w.Code, observed.calls)
		}
	}
}

func TestOIDCWebSocketsReauthorizeConnections(t *testing.T) {
	for _, endpoint := range []string{"connect", "exec"} {
		t.Run(endpoint, func(t *testing.T) {
			var revoked atomic.Bool
			var reviews, actions atomic.Int32
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				reviews.Add(1)
				attributes := *r.Spec.ResourceAttributes
				if attributes != access("get", "sessions", "team-a", "chat") && attributes != access("create", "sessions/connect", "team-a", "chat") {
					t.Errorf("unexpected review: %#v", attributes)
				}
				r.Status.Allowed = !revoked.Load()
				return r, nil
			}))
			if err := s.client.Create(t.Context(), terminalTestSession()); err != nil {
				t.Fatal(err)
			}
			var err error
			s.clientset, err = kubernetes.NewForConfig(s.restConfig)
			if err != nil {
				t.Fatal(err)
			}
			s.bridge = func(_ context.Context, socket *sessionSocket, namespace, pod string, _ func() error) error {
				actions.Add(1)
				if namespace != "team-a" || pod != "chat-pod" {
					t.Errorf("chat target = %s/%s", namespace, pod)
				}
				var message map[string]string
				if err := socket.ReadJSON(&message); err != nil {
					return err
				}
				if message["command"] != "echo allowed" {
					t.Errorf("chat command = %v", message)
				}
				return socket.WriteJSON(map[string]string{"type": "shell.result", "stdout": "allowed"})
			}
			s.terminalExecutor = func(_ *rest.Config, _ string, target *url.URL) (remotecommand.Executor, error) {
				actions.Add(1)
				if target.Path != "/api/v1/namespaces/team-a/pods/chat-pod/exec" {
					t.Errorf("terminal target = %s", target.Path)
				}
				return terminalTestExecutor{stream: func(_ context.Context, streams remotecommand.StreamOptions) error {
					_, err := streams.Stdout.Write([]byte("allowed"))
					return err
				}}, nil
			}
			server := httptest.NewServer(s)
			defer server.Close()
			header := oidcRequest("GET", "/", "").Header
			target := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/sessions/team-a/chat/" + endpoint
			for range 2 {
				connection, response, err := websocket.DefaultDialer.Dial(target, header)
				if err != nil {
					t.Fatalf("connect = %v, response = %v", err, response)
				}
				_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
				if endpoint == "connect" {
					if err := connection.WriteJSON(map[string]string{"type": "shell", "command": "echo allowed"}); err != nil {
						t.Fatal(err)
					}
				}
				_, data, err := connection.ReadMessage()
				_ = connection.Close()
				if err != nil || !strings.Contains(string(data), "allowed") {
					t.Fatalf("stream output = %q, error = %v", data, err)
				}
			}
			if reviews.Load() != 4 || actions.Load() != 2 {
				t.Fatalf("reviews = %d, actions = %d", reviews.Load(), actions.Load())
			}
			revoked.Store(true)
			connection, response, err := websocket.DefaultDialer.Dial(target, header)
			if connection != nil {
				_ = connection.Close()
			}
			if response != nil {
				defer response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != 403 || actions.Load() != 2 {
				t.Fatalf("revoked reconnect = %v %v, actions = %d", response, err, actions.Load())
			}
		})
	}
}

func TestOIDCAttachmentsAndTaskLogs(t *testing.T) {
	s := oidcTestServer(t, reviewFunc(allowReview))
	if err := s.client.Create(t.Context(), terminalTestSession()); err != nil {
		t.Fatal(err)
	}
	transfer := &fakeSessionAttachmentTransfer{attachment: sessionruntime.Attachment{ID: "file", Name: "hello.txt", SizeBytes: 5}, downloadStream: io.NopCloser(strings.NewReader("hello"))}
	s.attachments = transfer
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "hello")
	_ = form.Close()
	r := oidcRequest("POST", "/api/sessions/team-a/chat/attachments", body.String())
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := &attachmentResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	s.ServeHTTP(w, r)
	if w.Code != 201 || transfer.uploadedName != "hello.txt" || string(transfer.uploadedData) != "hello" {
		t.Fatalf("upload = %d %s, transfer = %#v", w.Code, w.Body.String(), transfer)
	}
	download := httptest.NewRecorder()
	s.ServeHTTP(download, oidcRequest("GET", "/api/sessions/team-a/chat/attachments/file", ""))
	if download.Code != 200 || download.Body.String() != "hello" {
		t.Fatalf("download = %d %s", download.Code, download.Body.String())
	}
	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "task", Namespace: "team-a"}, Status: kelos.TaskStatus{PodName: "task-pod"}}
	if err := s.client.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	opened := false
	s.taskLogStream = func(_ context.Context, task *kelos.Task, _ int64) (io.ReadCloser, error) {
		opened = true
		if task.Namespace != "team-a" || task.Name != "task" {
			t.Errorf("log target = %s/%s", task.Namespace, task.Name)
		}
		return io.NopCloser(strings.NewReader("task output\n")), nil
	}
	logs := httptest.NewRecorder()
	s.ServeHTTP(logs, oidcRequest("GET", "/api/resources/tasks/team-a/task/logs", ""))
	if logs.Code != 200 || !opened || !strings.Contains(logs.Body.String(), "task output") {
		t.Fatalf("logs = %d %s, opened = %v", logs.Code, logs.Body.String(), opened)
	}
}

func TestOIDCConfigurationValidation(t *testing.T) {
	base := oidcTestServer(t, reviewFunc(allowReview))
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing OIDC config", func(c *Config) { c.OIDC = nil }},
		{"missing reviewer", func(c *Config) { c.OIDC.Reviewer = nil }},
		{"missing logger", func(c *Config) { c.OIDC.Logger = nil }},
		{"empty username prefix", func(c *Config) { c.OIDC.UsernamePrefix = "" }},
		{"reserved group prefix", func(c *Config) { c.OIDC.GroupsPrefix = "system:" }},
		{"partial reserved username prefix", func(c *Config) { c.OIDC.UsernamePrefix = "sys" }},
		{"partial reserved group prefix", func(c *Config) { c.OIDC.GroupsPrefix = "system" }},
		{"empty group prefix", func(c *Config) { c.OIDC.GroupsPrefix = "" }},
		{"HTTP origin", func(c *Config) { c.OIDC.ExternalURL = "http://console.example" }},
		{"origin path", func(c *Config) { c.OIDC.ExternalURL = "https://console.example/path" }},
		{"mixed token", func(c *Config) { c.Token = "secret" }},
		{"mixed mode", func(c *Config) { c.AuthMode = AuthModeStaticToken }},
		{"unknown mode", func(c *Config) { c.AuthMode = "unknown" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			oidc := *base.oidc
			config := Config{AuthMode: AuthModeOIDC, OIDC: &oidc, Client: base.client, Clientset: base.clientset, RESTConfig: base.restConfig, DefaultNamespace: "team-a"}
			test.change(&config)
			if _, err := New(config); err == nil {
				t.Fatal("invalid authentication configuration accepted")
			}
		})
	}
}

func TestOIDCCanonicalOrigin(t *testing.T) {
	for _, externalURL := range []string{"https://Console.Example", "https://console.example:443", "https://Console.Example:443", "https://Console.Example:8443"} {
		t.Run(externalURL, func(t *testing.T) {
			base := oidcTestServer(t, reviewFunc(allowReview))
			oidc := *base.oidc
			oidc.ExternalURL = externalURL
			server, err := New(Config{AuthMode: AuthModeOIDC, OIDC: &oidc, Client: base.client, Clientset: base.clientset, RESTConfig: base.restConfig, DefaultNamespace: base.defaultNamespace})
			if err != nil {
				t.Fatal(err)
			}
			origin := "https://console.example"
			if strings.HasSuffix(externalURL, ":8443") {
				origin += ":8443"
			}
			for _, allowed := range []bool{true, false} {
				r := oidcRequest("POST", "/api/logout", "")
				r.Header.Set("Origin", origin)
				if !allowed {
					r.Header.Set("Origin", "https://other.example")
				}
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				want := http.StatusOK
				if !allowed {
					want = http.StatusForbidden
				}
				if w.Code != want || server.upgrader.CheckOrigin(r) != allowed {
					t.Fatalf("origin %s: status = %d, websocket allowed = %v", r.Header.Get("Origin"), w.Code, server.upgrader.CheckOrigin(r))
				}
			}
		})
	}
}

func TestConsoleBrowserAuthentication(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	command := exec.Command(node, "testdata/authentication_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser authentication: %v\n%s", err, output)
	}
}

func TestOIDCPipelineRelationships(t *testing.T) {
	for _, denied := range []string{"", "taskpipelines", "tasks", "workspaces", "agentconfigs", "workerpools"} {
		t.Run("deny="+denied, func(t *testing.T) {
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				r.Status.Allowed = r.Spec.ResourceAttributes.Resource != denied
				return r, nil
			}))
			pipeline := &kelos.TaskPipeline{ObjectMeta: metav1.ObjectMeta{Name: "pipeline", Namespace: "team-a"}, Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{
				{Name: "inline", TaskTemplate: kelos.PipelineTaskTemplate{Prompt: "work", Worker: &kelos.WorkerSpec{Type: "codex", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}, WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"}, AgentConfigRefs: []kelos.AgentConfigReference{{Name: "agent"}}}}},
				{Name: "pool", TaskTemplate: kelos.PipelineTaskTemplate{Prompt: "work", WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}}},
			}}}
			task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "team-a", OwnerReferences: []metav1.OwnerReference{{Kind: "TaskPipeline", Name: "pipeline"}}}}
			for _, object := range []client.Object{pipeline, task} {
				if err := s.client.Create(t.Context(), object); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest("GET", "/api/resources", ""))
			if w.Code != 200 {
				t.Fatalf("inventory = %d %s", w.Code, w.Body.String())
			}
			var inventory struct {
				Groups        []consoleResourceGroup        `json:"groups"`
				Relationships []consoleResourceRelationship `json:"relationships"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &inventory); err != nil {
				t.Fatal(err)
			}
			for _, group := range inventory.Groups {
				for _, collection := range group.Resources {
					if collection.Resource == denied {
						t.Fatalf("denied collection leaked: %s", denied)
					}
				}
			}
			want := 4
			if denied == "taskpipelines" {
				want = 0
			} else if denied != "" {
				want = 3
			}
			if len(inventory.Relationships) != want {
				t.Fatalf("relationships = %#v, want %d", inventory.Relationships, want)
			}
			for _, relationship := range inventory.Relationships {
				if relationship.Source.Resource == denied || relationship.Target.Resource == denied {
					t.Fatalf("denied relationship leaked: %#v", relationship)
				}
			}
			w = httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest("GET", "/api/resources/taskpipelines/team-a/pipeline", ""))
			status := 200
			if denied == "taskpipelines" {
				status = 403
			}
			if w.Code != status {
				t.Fatalf("pipeline YAML = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOIDCApplyRequiresCreateForExistingAndMissingSessions(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("exists=%t/allow=%t", exists, allow), func(t *testing.T) {
				s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
					r.Status.Allowed = allow || r.Spec.ResourceAttributes.Verb != "create"
					return r, nil
				}))
				if exists {
					if err := s.client.Create(t.Context(), terminalTestSession()); err != nil {
						t.Fatal(err)
					}
				}
				observed := &observedClient{Client: s.client}
				s.client = observed
				manifest := "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\n  labels:\n    source: oidc\nspec:\n  worker:\n    type: codex\n"
				w := httptest.NewRecorder()
				s.ServeHTTP(w, oidcRequest("POST", "/api/sessions/apply", manifest))
				if !allow {
					if w.Code != 403 || len(observed.calls) != 0 {
						t.Fatalf("denied apply = %d, resource calls = %v", w.Code, observed.calls)
					}
					return
				}
				if w.Code != 200 {
					t.Fatalf("apply = %d %s", w.Code, w.Body.String())
				}
				var session kelos.Session
				if err := s.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &session); err != nil {
					t.Fatal(err)
				}
				if session.Labels["source"] != "oidc" {
					t.Fatalf("apply did not update labels: %v", session.Labels)
				}
			})
		}
	}
}
