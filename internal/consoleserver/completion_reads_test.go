package consoleserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
)

func completionSession() *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a", UID: "uid-1", Annotations: map[string]string{"owner": "platform"}},
		Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
		Status:     kelos.SessionStatus{LastNotification: &kelos.SessionNotification{ID: "journal/1", Type: kelos.SessionNotificationTypeTurnCompleted}},
	}
}

func TestCompletionReadsSharedAcrossServers(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		for _, kind := range []kelos.SessionNotificationType{kelos.SessionNotificationTypeTurnCompleted, kelos.SessionNotificationTypeTurnFailed} {
			t.Run(string(kind)+map[bool]string{false: "/token", true: "/user"}[authenticated], func(t *testing.T) {
				newServer := func() *Server {
					if authenticated {
						return oidcTestServer(t, reviewFunc(allowReview))
					}
					return testServer(t)
				}
				server := newServer()
				session := completionSession()
				session.Status.LastNotification.Type = kind
				if err := server.client.Create(t.Context(), session); err != nil {
					t.Fatal(err)
				}
				request := func(s *Server, method, path, body, username string) *httptest.ResponseRecorder {
					t.Helper()
					r := oidcRequest(method, path, body)
					r.Header.Set("X-Kelos-User", username)
					r.Header.Set("Authorization", "Bearer secret-token")
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					if w.Code != http.StatusOK {
						t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
					}
					return w
				}
				listReceipt := func(s *Server, username string) string {
					t.Helper()
					response := request(s, "GET", "/api/sessions?namespace=team-a", "", username)
					var items []sessionSummary
					if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
						t.Fatal(err)
					}
					if len(items) != 1 {
						t.Fatalf("Sessions = %#v", items)
					}
					return items[0].ReadNotificationID
				}
				if got := listReceipt(server, "alice"); got != "" {
					t.Fatalf("Unread result has receipt %q", got)
				}
				body := `{"uid":"uid-1","notificationId":"journal/1"}`
				response := request(server, "POST", "/api/sessions/team-a/chat/read", body, "alice")
				var summary sessionSummary
				if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
					t.Fatal(err)
				}
				if summary.ReadNotificationID != "journal/1" {
					t.Fatalf("Acknowledgment = %#v", summary)
				}
				// A fresh server has no process memory or browser storage from the reader.
				other := newServer()
				other.client = server.client
				if got := listReceipt(other, "alice"); got != "journal/1" {
					t.Fatalf("Other server receipt = %q", got)
				}
				wantBob := "journal/1"
				if authenticated {
					wantBob = ""
				}
				if got := listReceipt(other, "bob"); got != wantBob {
					t.Fatalf("Other user receipt = %q, want %q", got, wantBob)
				}
				var stored kelos.Session
				if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &stored); err != nil {
					t.Fatal(err)
				}
				version := stored.ResourceVersion
				request(other, "POST", "/api/sessions/team-a/chat/read", body, "alice")
				if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &stored); err != nil {
					t.Fatal(err)
				}
				if stored.ResourceVersion != version || stored.Annotations["owner"] != "platform" {
					t.Fatalf("Repeated read modified Session: %#v", stored.ObjectMeta)
				}
				stored.Status.LastNotification.ID = "journal/2"
				if err := server.client.Update(t.Context(), &stored); err != nil {
					t.Fatal(err)
				}
				request(other, "POST", "/api/sessions/team-a/chat/read", `{"uid":"uid-1","notificationId":"journal/2"}`, "alice")
				if got := listReceipt(server, "alice"); got != "journal/2" {
					t.Fatalf("Latest receipt = %q", got)
				}
				if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &stored); err != nil {
					t.Fatal(err)
				}
				if len(stored.Annotations) != 2 {
					t.Fatalf("Receipts grow with each result: %v", stored.Annotations)
				}
			})
		}
	}
}

func TestCompletionReadRejectsInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		change func(*kelos.Session)
		status int
	}{
		{"empty UID", `{"notificationId":"journal/1"}`, nil, 400},
		{"empty ID", `{"uid":"uid-1"}`, nil, 400},
		{"invalid JSON", `{`, nil, 400},
		{"replaced Session", `{"uid":"replaced","notificationId":"journal/1"}`, nil, 409},
		{"older result", `{"uid":"uid-1","notificationId":"older"}`, nil, 409},
		{"unpublished result", `{"uid":"uid-1","notificationId":"future"}`, nil, 409},
		{"no notification", `{"uid":"uid-1","notificationId":"journal/1"}`, func(s *kelos.Session) { s.Status.LastNotification = nil }, 409},
		{"input request", `{"uid":"uid-1","notificationId":"journal/1"}`, func(s *kelos.Session) { s.Status.LastNotification.Type = kelos.SessionNotificationTypeInputRequested }, 409},
		{"resetting", `{"uid":"uid-1","notificationId":"journal/1"}`, func(s *kelos.Session) { s.Annotations[sessionreset.RequestAnnotation] = "reset" }, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			session := completionSession()
			if test.change != nil {
				test.change(session)
			}
			if err := server.client.Create(t.Context(), session); err != nil {
				t.Fatal(err)
			}
			observed := &observedClient{Client: server.client}
			server.client = observed
			request := httptest.NewRequest("POST", "/api/sessions/team-a/chat/read", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("Response = %d %s", response.Code, response.Body.String())
			}
			for _, call := range observed.calls {
				if call == "patch" {
					t.Fatal("Rejected read patched the Session")
				}
			}
		})
	}
}

func TestCompletionReadPrunesSupersededReceipts(t *testing.T) {
	server := oidcTestServer(t, reviewFunc(allowReview))
	session := completionSession()
	session.Annotations[completionReadAnnotationPrefix+"earlier-reader"] = "journal/0"
	session.Annotations[completionReadAnnotationPrefix+"current-reader"] = "journal/1"
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, oidcRequest("POST", "/api/sessions/team-a/chat/read", `{"uid":"uid-1","notificationId":"journal/1"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("Response = %d %s", response.Code, response.Body.String())
	}
	var stored kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &stored); err != nil {
		t.Fatal(err)
	}
	if _, exists := stored.Annotations[completionReadAnnotationPrefix+"earlier-reader"]; exists {
		t.Fatal("Superseded receipt was retained")
	}
	if len(stored.Annotations) != 3 || stored.Annotations["owner"] != "platform" || stored.Annotations[completionReadAnnotationPrefix+"current-reader"] != "journal/1" {
		t.Fatalf("Current and unrelated annotations were not preserved: %v", stored.Annotations)
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ReadNotificationID != "journal/1" {
		t.Fatalf("Reader acknowledgment = %q", summary.ReadNotificationID)
	}
}

func TestCompletionReadPatchErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"invalid annotations", apierrors.NewInvalid(schema.GroupKind{Group: "kelos.dev", Kind: "Session"}, "chat", field.ErrorList{field.TooLong(field.NewPath("metadata", "annotations"), "", 256*1024)}), http.StatusBadRequest},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: "kelos.dev", Resource: "sessions"}, "chat", errors.New("access denied")), http.StatusForbidden},
		{"temporary failure", errors.New("API unavailable"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			if err := server.client.Create(t.Context(), completionSession()); err != nil {
				t.Fatal(err)
			}
			server.client = &patchErrorClient{Client: server.client, err: test.err}
			request := httptest.NewRequest("POST", "/api/sessions/team-a/chat/read", strings.NewReader(`{"uid":"uid-1","notificationId":"journal/1"}`))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("Response = %d %s, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

type completionRaceClient struct{ client.Client }

func (c completionRaceClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	var session kelos.Session
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(object), &session); err != nil {
		return err
	}
	session.Status.LastNotification.ID = "journal/2"
	session.Annotations["concurrent-update"] = "preserved"
	if err := c.Client.Update(ctx, &session); err != nil {
		return err
	}
	return c.Client.Patch(ctx, object, patch, opts...)
}

func TestCompletionReadConflictsWithConcurrentUpdate(t *testing.T) {
	server := testServer(t)
	session := completionSession()
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	server.client = completionRaceClient{Client: server.client}
	request := httptest.NewRequest("POST", "/api/sessions/team-a/chat/read", strings.NewReader(`{"uid":"uid-1","notificationId":"journal/1"}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("Response = %d %s", response.Code, response.Body.String())
	}
	var stored kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Annotations[completionReadAnnotation(request)] != "" || stored.Status.LastNotification.ID != "journal/2" || stored.Annotations["concurrent-update"] != "preserved" {
		t.Fatalf("Concurrent completion was acknowledged: %#v", stored)
	}
}
