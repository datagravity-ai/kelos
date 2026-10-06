package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/kelos-dev/kelos/internal/consoleserver"
)

func TestAuthorizationClient(t *testing.T) {
	received := make(chan authorizationv1.SubjectAccessReviewSpec, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(request.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		received <- review.Spec
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SubjectAccessReview","status":{"allowed":true}}`))
	}))
	defer server.Close()
	config := &rest.Config{Host: server.URL, QPS: 5, Burst: 10, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	client, err := newAuthorizationClient(config)
	if err != nil {
		t.Fatal(err)
	}
	if got := client.RESTClient().GetRateLimiter().QPS(); got != 50 {
		t.Fatalf("authorization QPS = %v, want 50", got)
	}
	if config.QPS != 5 || config.Burst != 10 {
		t.Fatal("authorization client changed the shared REST configuration")
	}
	spec := authorizationv1.SubjectAccessReviewSpec{
		User: "oidc:alice", Groups: []string{"oidc:developers"},
		ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "kelos.dev", Resource: "sessions", Verb: "list", Namespace: "team-a"},
	}
	review, err := client.SubjectAccessReviews().Create(t.Context(), &authorizationv1.SubjectAccessReview{Spec: spec}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-received; !review.Status.Allowed || !reflect.DeepEqual(got, spec) {
		t.Fatalf("unexpected review: request=%#v response=%#v", got, review.Status)
	}
}

func TestReadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readToken(path)
	if err != nil {
		t.Fatalf("readToken() error = %v", err)
	}
	if value != "secret" {
		t.Fatalf("readToken() = %q, want %q", value, "secret")
	}
}

func TestValidateAuthFlags(t *testing.T) {
	for _, test := range []struct {
		mode, address, token string
		secure, valid        bool
	}{
		{"staticToken", ":8080", "token", false, true},
		{"staticToken", ":8080", "", false, false},
		{"oidc", "127.0.0.1:8080", "", false, true},
		{"oidc", "[::1]:8080", "", false, true},
		{"oidc", ":8080", "", false, false},
		{"oidc", "0.0.0.0:8080", "", false, false},
		{"oidc", "localhost:8080", "", false, false},
		{"oidc", "127.0.0.1:8080", "token", false, false},
		{"oidc", "127.0.0.1:8080", "", true, false},
		{"github", "127.0.0.1:8080", "", false, true},
		{"github", "[::1]:8080", "", false, true},
		{"github", ":8080", "", false, false},
		{"github", "localhost:8080", "", false, false},
		{"github", "127.0.0.1:8080", "token", false, false},
		{"github", "127.0.0.1:8080", "", true, false},
		{"unknown", ":8080", "token", false, false},
	} {
		err := validateAuthFlags(test.mode, test.address, test.token, test.secure, consoleserver.ProxyAuthConfig{})
		if (err == nil) != test.valid {
			t.Errorf("%#v: error = %v", test, err)
		}
	}
	for _, oidc := range []consoleserver.ProxyAuthConfig{
		{ExternalURL: "https://console.example"},
		{UsernamePrefix: "oidc:"},
		{GroupsPrefix: "oidc:"},
	} {
		if err := validateAuthFlags("staticToken", ":8080", "token", false, oidc); err == nil {
			t.Fatalf("static mode accepted OIDC flags: %#v", oidc)
		}
	}
}

func TestReadTokenRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}

func TestReadTokenRejectsWhitespaceOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}
