package consoleserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNamespaceDiscoveryFiltersByName(t *testing.T) {
	for _, test := range []struct {
		name, namespace, resource, body string
		status, reviews                 int
		err                             error
	}{
		{name: "sessions", namespace: "team-a", resource: "sessions", body: `{"namespaces":["team-a"]}`, status: http.StatusOK, reviews: 1},
		{name: "custom role", namespace: "team-a", resource: "agentconfigs", body: `{"namespaces":["team-a"]}`, status: http.StatusOK, reviews: len(consoleResourceDefinitions)},
		{name: "denied", namespace: "team-a", body: `{"namespaces":[]}`, status: http.StatusOK, reviews: len(consoleResourceDefinitions)},
		{name: "missing", namespace: "missing", body: `{"namespaces":[]}`, status: http.StatusOK},
		{name: "authorization failure", namespace: "team-a", body: `{"error":"authorization service unavailable"}`, status: http.StatusServiceUnavailable, reviews: 1, err: errors.New("unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := memberTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				calls.Add(1)
				attr := review.Spec.ResourceAttributes
				if attr.Namespace != test.namespace || attr.Group != "kelos.dev" || attr.Verb != "list" || review.Spec.User != "oidc:alice" {
					t.Errorf("unexpected namespace review: %#v", review.Spec)
				}
				review.Status.Allowed = attr.Resource == test.resource
				return review, test.err
			}))
			server.client = fake.NewClientBuilder().WithScheme(server.client.Scheme()).
				WithIndex(&corev1.Namespace{}, "metadata.name", func(object client.Object) []string { return []string{object.GetName()} }).Build()
			for _, namespace := range []string{"team-a", "team-b", "team-c"} {
				if err := server.client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
					t.Fatal(err)
				}
			}
			response := memberRequest(server, http.MethodGet, "/api/namespaces?namespace="+test.namespace, "")
			if response.Code != test.status || strings.TrimSpace(response.Body.String()) != test.body {
				t.Fatalf("namespace discovery: %d %s", response.Code, response.Body.String())
			}
			if got := int(calls.Load()); got != test.reviews {
				t.Fatalf("review count = %d, want %d", got, test.reviews)
			}
		})
	}
}

func TestNamespaceDiscoveryBoundsConcurrentReviews(t *testing.T) {
	started := make(chan struct{}, namespaceReviewConcurrency*2)
	release := make(chan struct{})
	var active, peak atomic.Int32
	server := memberTestServer(t, reviewFunc(func(ctx context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		running := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); running > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, running) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		review.Status.Allowed = true
		return review, nil
	}))
	for i := range namespaceReviewConcurrency * 2 {
		if err := server.client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("team-%02d", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- memberRequest(server, http.MethodGet, "/api/namespaces", "") }()
	for range namespaceReviewConcurrency {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("namespace reviews did not run concurrently")
		}
	}
	close(release)
	select {
	case result := <-response:
		if result.Code != http.StatusOK {
			t.Fatalf("discovery: %d %s", result.Code, result.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("namespace discovery did not finish")
	}
	if peak.Load() != namespaceReviewConcurrency {
		t.Fatalf("peak concurrency = %d, want %d", peak.Load(), namespaceReviewConcurrency)
	}
}

func TestNamespaceDiscoveryCancelsReviews(t *testing.T) {
	started := make(chan struct{}, namespaceReviewConcurrency)
	server := memberTestServer(t, reviewFunc(func(ctx context.Context, _ *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	for i := range namespaceReviewConcurrency * 2 {
		if err := server.client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("team-%02d", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, oidcRequest(http.MethodGet, "/api/namespaces", "").WithContext(ctx))
		done <- response.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("namespace review did not start")
	}
	cancel()
	select {
	case status := <-done:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("cancelled discovery returned %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("namespace reviews did not cancel")
	}
}

func TestNamespaceDiscoveryRejectsCancellationWithoutReviewErrors(t *testing.T) {
	for _, beforeReviews := range []bool{true, false} {
		t.Run(fmt.Sprintf("beforeReviews=%t", beforeReviews), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			server := memberTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				calls.Add(1)
				cancel()
				review.Status.Allowed = true
				return review, nil
			}))
			if err := server.client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}); err != nil {
				t.Fatal(err)
			}
			if beforeReviews {
				cancel()
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, oidcRequest(http.MethodGet, "/api/namespaces", "").WithContext(ctx))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "team-a") {
				t.Fatalf("cancelled discovery: %d %s", response.Code, response.Body.String())
			}
			var wantCalls int32 = 1
			if beforeReviews {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("review calls = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}
