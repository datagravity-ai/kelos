package consoleserver

import (
	"context"
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
)

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
