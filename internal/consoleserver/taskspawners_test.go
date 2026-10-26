package consoleserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func consoleTestTaskSpawner() *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "daily", Namespace: "team-a", UID: "daily-uid"},
		Spec: kelos.TaskSpawnerSpec{
			When:    kelos.When{Cron: &kelos.Cron{Schedule: "0 9 * * *"}},
			Suspend: ptr.To(true),
			TaskTemplate: kelos.TaskTemplate{
				Worker:         &kelos.WorkerSpec{Type: "claude-code", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}},
				PromptTemplate: "{{.TriggerType}}: {{.Time}} / {{.Schedule}}",
				NameTemplate:   "{{.Unavailable}}",
			},
		},
	}
}

func TestRunTaskSpawner(t *testing.T) {
	for _, test := range []struct {
		name, values, prompt string
		configure            func(*kelos.TaskSpawner)
	}{
		{"cron defaults", "", "", nil},
		{"YAML values", "Number: 42\nTitle: Fix login", "42: Fix login", nil},
		{"JSON values", `{"Number":42,"Title":"Fix login"}`, "42: Fix login", nil},
		{"source filters", "Title: Operator request\nLabels: []", "manual: Operator request", func(spawner *kelos.TaskSpawner) {
			spawner.Spec.Suspend = ptr.To(false)
			spawner.Spec.When = kelos.When{GitHubIssues: &kelos.GitHubIssues{Repo: "owner/repo", Labels: []string{"automatic-only"}}}
			spawner.Spec.TaskTemplate.PromptTemplate = "{{.TriggerType}}: {{.Title}}"
			spawner.Spec.TaskTemplate.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "repository"}
		}},
		{"concurrency limit", "", "", func(spawner *kelos.TaskSpawner) {
			spawner.Spec.Suspend = ptr.To(false)
			spawner.Spec.MaxConcurrency = ptr.To(int32(1))
			spawner.Status.ActiveTasks = 1
		}},
		{"total task limit", "", "", func(spawner *kelos.TaskSpawner) {
			spawner.Spec.Suspend = ptr.To(false)
			spawner.Spec.MaxTotalTasks = ptr.To(int32(1))
			spawner.Status.TotalTasksCreated = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t)
			spawner := consoleTestTaskSpawner()
			if test.prompt != "" {
				spawner.Spec.TaskTemplate.PromptTemplate = `{{printf "%d" .Number}}: {{.Title}}`
			}
			if test.configure != nil {
				test.configure(spawner)
			}
			if err := s.client.Create(t.Context(), spawner); err != nil {
				t.Fatal(err)
			}
			if spawner.Spec.MaxConcurrency != nil || spawner.Spec.MaxTotalTasks != nil {
				phase := kelos.TaskPhaseRunning
				if spawner.Spec.MaxTotalTasks != nil {
					phase = kelos.TaskPhaseSucceeded
				}
				existing := &kelos.Task{
					ObjectMeta: metav1.ObjectMeta{
						Name: "automatic-task", Namespace: spawner.Namespace,
						Labels:          map[string]string{"kelos.dev/taskspawner": spawner.Name},
						OwnerReferences: []metav1.OwnerReference{{APIVersion: kelos.GroupVersion.String(), Kind: "TaskSpawner", Name: spawner.Name, UID: spawner.UID}},
					},
					Spec:   kelos.TaskSpec{Worker: spawner.Spec.TaskTemplate.Worker, Prompt: "Automated task"},
					Status: kelos.TaskStatus{Phase: phase},
				}
				if err := s.client.Create(t.Context(), existing); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(map[string]string{"values": test.values})
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for range 2 {
				r := httptest.NewRequest(http.MethodPost, "/api/resources/taskspawners/team-a/daily/run", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer secret-token")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusCreated {
					t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
				}
				var result consoleResourceSummary
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Namespace != "team-a" || !strings.HasPrefix(result.Name, "daily-manual-") || names[result.Name] {
					t.Fatalf("created Task = %#v", result)
				}
				names[result.Name] = true
				var task kelos.Task
				if err := s.client.Get(t.Context(), client.ObjectKey{Namespace: result.Namespace, Name: result.Name}, &task); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(task.Spec.Worker, spawner.Spec.TaskTemplate.Worker) || len(task.OwnerReferences) != 0 || task.Labels["kelos.dev/taskspawner"] != "" {
					t.Fatalf("Task does not preserve standalone template behavior: %#v", task)
				}
				triggerTime := task.Annotations["kelos.dev/trigger-time"]
				if _, err := time.Parse(time.RFC3339, triggerTime); err != nil {
					t.Fatal(err)
				}
				if task.Annotations["kelos.dev/created-from-taskspawner"] != "daily" || task.Annotations["kelos.dev/trigger-type"] != "manual" {
					t.Fatalf("annotations = %#v", task.Annotations)
				}
				wantPrompt := test.prompt
				if wantPrompt == "" {
					wantPrompt = "manual: " + triggerTime + " / 0 9 * * *"
				}
				if task.Spec.Prompt != wantPrompt {
					t.Errorf("prompt = %q, want %q", task.Spec.Prompt, wantPrompt)
				}
			}
			var unchanged kelos.TaskSpawner
			if err := s.client.Get(t.Context(), client.ObjectKeyFromObject(spawner), &unchanged); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(unchanged.Spec, spawner.Spec) || !reflect.DeepEqual(unchanged.Status, spawner.Status) {
				t.Fatal("manual run modified TaskSpawner spec or status")
			}
		})
	}
}

func TestRunTaskSpawnerFetchesContextWithSecret(t *testing.T) {
	contextServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer context-token" {
			t.Errorf("context authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("context data"))
	}))
	defer contextServer.Close()
	s := testServer(t)
	s.httpClient = contextServer.Client()
	spawner := consoleTestTaskSpawner()
	spawner.Spec.TaskTemplate.PromptTemplate = "{{.Context.details}}"
	spawner.Spec.TaskTemplate.ContextSources = []kelos.ContextSource{{
		Name: "details", HTTP: &kelos.HTTPContextSource{
			URL:         contextServer.URL,
			HeadersFrom: []kelos.HTTPHeaderSource{{Header: "Authorization", SecretName: "context-auth", SecretKey: "Authorization"}},
		},
	}}
	for _, object := range []client.Object{spawner, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "context-auth", Namespace: "team-a"},
		Data:       map[string][]byte{"Authorization": []byte("Bearer context-token")},
	}} {
		if err := s.client.Create(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/resources/taskspawners/team-a/daily/run", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var tasks kelos.TaskList
	if err := s.client.List(t.Context(), &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 || tasks.Items[0].Spec.Prompt != "context data" {
		t.Fatalf("Tasks = %#v", tasks.Items)
	}
}

func TestRunTaskSpawnerRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"missing", `{}`, http.StatusNotFound},
		{"invalid request", `{`, http.StatusBadRequest},
		{"trailing JSON", `{} {}`, http.StatusBadRequest},
		{"trailing data", `{} trailing`, http.StatusBadRequest},
		{"invalid values", `{"values":"Title: ["}`, http.StatusBadRequest},
		{"array values", `{"values":"[one, two]"}`, http.StatusBadRequest},
		{"null values", `{"values":"null"}`, http.StatusBadRequest},
		{"missing template value", `{}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t)
			if test.name != "missing" {
				spawner := consoleTestTaskSpawner()
				spawner.Spec.TaskTemplate.PromptTemplate = "Manual request"
				if test.name == "missing template value" {
					spawner.Spec.TaskTemplate.PromptTemplate = "{{.Missing}}"
				}
				if err := s.client.Create(t.Context(), spawner); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "/api/resources/taskspawners/team-a/daily/run", strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer secret-token")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.status || !strings.Contains(w.Body.String(), "daily") {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var tasks kelos.TaskList
			if err := s.client.List(t.Context(), &tasks); err != nil || len(tasks.Items) != 0 {
				t.Fatalf("Tasks = %#v, error = %v", tasks.Items, err)
			}
		})
	}
}

func TestRunTaskSpawnerAuthorization(t *testing.T) {
	for _, test := range []struct {
		name, deniedResource string
		status               int
	}{
		{"allowed", "", http.StatusCreated},
		{"cannot read spawner", "taskspawners", http.StatusForbidden},
		{"cannot create task", "tasks", http.StatusForbidden},
		{"review unavailable", "error", http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var checks []authorizationv1.ResourceAttributes
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				if test.deniedResource == "error" {
					return nil, errors.New("unavailable")
				}
				checks = append(checks, *r.Spec.ResourceAttributes)
				r.Status.Allowed = r.Spec.ResourceAttributes.Resource != test.deniedResource
				return r, nil
			}))
			if err := s.client.Create(t.Context(), consoleTestTaskSpawner()); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest(http.MethodPost, "/api/resources/taskspawners/team-a/daily/run", `{}`))
			if w.Code != test.status {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if test.status == http.StatusCreated {
				want := []authorizationv1.ResourceAttributes{access("get", "taskspawners", "team-a", "daily"), access("create", "tasks", "team-a", "")}
				if !reflect.DeepEqual(checks, want) {
					t.Fatalf("access checks = %#v, want %#v", checks, want)
				}
			}
			var tasks kelos.TaskList
			if err := s.client.List(t.Context(), &tasks); err != nil {
				t.Fatal(err)
			}
			if test.status != http.StatusCreated && len(tasks.Items) != 0 {
				t.Fatal("unauthorized run created a Task")
			}
		})
	}
}
