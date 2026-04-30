package contextfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(v1alpha1.AddToScheme(s))
	return s
}

func int32Ptr(v int32) *int32 { return &v }

func newFetcher(opts ...func(*Fetcher)) *Fetcher {
	f := &Fetcher{
		HTTPClient: http.DefaultClient,
		Namespace:  "default",
		Logger:     zap.New(zap.UseDevMode(true)),
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

func TestFetchAll_BasicGET(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "test",
		URL:           srv.URL,
		AllowInsecure: true,
	}}
	vars := map[string]interface{}{"Number": 42}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["test"]; got != `{"status":"ok"}` {
		t.Errorf("expected {\"status\":\"ok\"}, got %v", got)
	}
}

func TestFetchAll_POSTWithBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Write([]byte("created"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "post",
		URL:           srv.URL,
		Method:        "POST",
		Body:          `{"id":{{.Number}}}`,
		AllowInsecure: true,
	}}
	vars := map[string]interface{}{"Number": 42}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["post"]; got != "created" {
		t.Errorf("expected 'created', got %v", got)
	}
}

func TestFetchAll_URLTemplateRendering(t *testing.T) {
	var requestedPath string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "url",
		URL:           srv.URL + "/items/{{.Number}}",
		AllowInsecure: true,
	}}
	vars := map[string]interface{}{"Number": 99}

	_, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestedPath != "/items/99" {
		t.Errorf("expected path /items/99, got %s", requestedPath)
	}
}

func TestFetchAll_JSONPathFilter(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"value": "extracted",
			},
		})
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:           "filtered",
		URL:            srv.URL,
		JSONPathFilter: "$.data.value",
		AllowInsecure:  true,
	}}
	vars := map[string]interface{}{}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["filtered"]; got != "extracted" {
		t.Errorf("expected 'extracted', got %v", got)
	}
}

func TestFetchAll_JSONPathFilter_ComplexValue(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []interface{}{"a", "b"},
		})
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:           "arr",
		URL:            srv.URL,
		JSONPathFilter: "$.items",
		AllowInsecure:  true,
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["arr"]; got != `["a","b"]` {
		t.Errorf("expected [\"a\",\"b\"], got %v", got)
	}
}

func TestFetchAll_HeadersFromSecret(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("Bearer s3cret")},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(secret).Build()

	f := newFetcher(func(f *Fetcher) {
		f.HTTPClient = srv.Client()
		f.Client = cl
	})
	sources := []v1alpha1.ContextSource{{
		Name: "auth",
		URL:  srv.URL,
		HeadersFrom: []v1alpha1.HeaderFromSecret{{
			Header:    "Authorization",
			SecretRef: v1alpha1.SecretReference{Name: "my-secret"},
			Key:       "token",
		}},
		AllowInsecure: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("expected 'Bearer s3cret', got %q", gotAuth)
	}
}

func TestFetchAll_StaticHeaders(t *testing.T) {
	var gotAccept string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "hdrs",
		URL:           srv.URL,
		Headers:       map[string]string{"Accept": "application/json"},
		AllowInsecure: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAccept != "application/json" {
		t.Errorf("expected application/json, got %q", gotAccept)
	}
}

func TestFetchAll_HTTPSRequired(t *testing.T) {
	f := newFetcher()
	sources := []v1alpha1.ContextSource{{
		Name:     "insecure",
		URL:      "http://example.com/data",
		Required: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for HTTP URL without allowInsecure")
	}
	if !strings.Contains(err.Error(), "HTTP URLs are not allowed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_HTTPAllowInsecure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher()
	sources := []v1alpha1.ContextSource{{
		Name:          "insecure",
		URL:           srv.URL,
		AllowInsecure: true,
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["insecure"]; got != "ok" {
		t.Errorf("expected 'ok', got %v", got)
	}
}

func TestFetchAll_RequiredSourceFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "req",
		URL:           srv.URL,
		Required:      true,
		AllowInsecure: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for required source failure")
	}
	if !strings.Contains(err.Error(), "required context source") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_OptionalSourceFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:          "opt",
		URL:           srv.URL,
		Required:      false,
		AllowInsecure: true,
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["opt"]; got != "" {
		t.Errorf("expected empty string for failed optional source, got %v", got)
	}
}

func TestFetchAll_OptionalSourceCancelledByRequired(t *testing.T) {
	// When a required source fails, errgroup cancels the context. In-flight
	// optional sources should NOT log "Context source fetch failed" — verify
	// by capturing log output.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow enough that the required source fails first.
		time.Sleep(2 * time.Second)
		w.Write([]byte("slow"))
	}))
	defer slowSrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	var logBuf strings.Builder
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	f := newFetcher(func(f *Fetcher) { f.Logger = logger })
	sources := []v1alpha1.ContextSource{
		{Name: "optional-slow", URL: slowSrv.URL, Required: false, AllowInsecure: true, TimeoutSeconds: int32Ptr(5)},
		{Name: "required-fail", URL: failSrv.URL, Required: true, AllowInsecure: true},
	}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("Expected error from required source failure")
	}
	if strings.Contains(logBuf.String(), "Context source fetch failed") {
		t.Error("Optional source logged misleading 'fetch failed' when it was actually cancelled by required source failure")
	}
}

func TestFetchAll_Timeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Write([]byte("late"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:           "slow",
		URL:            srv.URL,
		Required:       true,
		TimeoutSeconds: int32Ptr(1),
		AllowInsecure:  true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestFetchAll_ResponseSizeLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than 64 bytes
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []v1alpha1.ContextSource{{
		Name:             "big",
		URL:              srv.URL,
		Required:         true,
		MaxResponseBytes: int32Ptr(64),
		AllowInsecure:    true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "maxResponseBytes") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_MultipleSources(t *testing.T) {
	srv1 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data1"))
	}))
	defer srv1.Close()

	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data2"))
	}))
	defer srv2.Close()

	// TLS tests need individual clients; use AllowInsecure + plain HTTP instead
	plainSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data1"))
	}))
	defer plainSrv1.Close()

	plainSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data2"))
	}))
	defer plainSrv2.Close()

	f := newFetcher()
	sources := []v1alpha1.ContextSource{
		{Name: "src1", URL: plainSrv1.URL, AllowInsecure: true},
		{Name: "src2", URL: plainSrv2.URL, AllowInsecure: true},
	}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["src1"]; got != "data1" {
		t.Errorf("src1: expected 'data1', got %v", got)
	}
	if got := result["src2"]; got != "data2" {
		t.Errorf("src2: expected 'data2', got %v", got)
	}
}

func TestFetchAll_SecretNotFound(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	f := newFetcher(func(f *Fetcher) { f.Client = cl })
	sources := []v1alpha1.ContextSource{{
		Name: "missing",
		URL:  "https://example.com",
		HeadersFrom: []v1alpha1.HeaderFromSecret{{
			Header:    "Authorization",
			SecretRef: v1alpha1.SecretReference{Name: "nonexistent"},
			Key:       "token",
		}},
		Required: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing Secret")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_SecretKeyNotFound(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"other-key": []byte("val")},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(secret).Build()

	f := newFetcher(func(f *Fetcher) { f.Client = cl })
	sources := []v1alpha1.ContextSource{{
		Name: "badkey",
		URL:  "https://example.com",
		HeadersFrom: []v1alpha1.HeaderFromSecret{{
			Header:    "Authorization",
			SecretRef: v1alpha1.SecretReference{Name: "my-secret"},
			Key:       "token",
		}},
		Required: true,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing key in Secret")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_MissingTemplateVariable(t *testing.T) {
	f := newFetcher()
	sources := []v1alpha1.ContextSource{{
		Name:          "bad",
		URL:           "https://api.example.com/items/{{.MissingVar}}",
		Required:      true,
		AllowInsecure: true,
	}}
	vars := map[string]interface{}{"Number": 42}

	_, err := f.FetchAll(context.Background(), sources, vars)
	if err == nil {
		t.Fatal("Expected error for missing template variable")
	}
	if !strings.Contains(err.Error(), "MissingVar") {
		t.Errorf("Expected error to mention missing variable, got: %v", err)
	}
}

func TestValidateURLScheme(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https allowed", "https://example.com", false, false},
		{"http blocked", "http://example.com", false, true},
		{"http allowed with flag", "http://example.com", true, false},
		{"ftp blocked", "ftp://example.com", false, true},
		{"ftp blocked even with flag", "ftp://example.com", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURLScheme(tt.url, tt.allowInsecure)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateURLScheme(%q, %v) error = %v, wantErr %v", tt.url, tt.allowInsecure, err, tt.wantErr)
			}
		})
	}
}

func TestApplyJSONPathFilter(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		expr    string
		want    string
		wantErr bool
	}{
		{
			name: "string value",
			body: `{"a":"b"}`,
			expr: "$.a",
			want: "b",
		},
		{
			name: "numeric value",
			body: `{"a":42}`,
			expr: "$.a",
			want: "42",
		},
		{
			name: "nested object",
			body: `{"a":{"b":"c"}}`,
			expr: "$.a",
			want: `{"b":"c"}`,
		},
		{
			name:    "missing field",
			body:    `{"a":"b"}`,
			expr:    "$.missing",
			wantErr: true,
		},
		{
			name:    "invalid json",
			body:    `not json`,
			expr:    "$.a",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyJSONPathFilter([]byte(tt.body), tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
