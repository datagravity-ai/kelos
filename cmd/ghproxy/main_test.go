package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/source"
)

func TestProxy_ServesFreshCacheWithoutRevalidating(t *testing.T) {
	var calls atomic.Int32
	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			t.Error("Did not expect upstream revalidation for a fresh cache entry")
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repos":["a"]}`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL}, time.Minute)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() string {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	// First request hits upstream.
	body := doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	now = now.Add(10 * time.Second)

	// Second request should be served directly from cache.
	body = doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected cached body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected still 1 upstream call after fresh cache hit, got %d", calls.Load())
	}
}

func TestProxy_RevalidatesStaleGETWithETag(t *testing.T) {
	var calls atomic.Int32
	var bodyHits atomic.Int32
	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodyHits.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repos":["a"]}`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL}, time.Second)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() string {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	body := doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	now = now.Add(2 * time.Second)

	body = doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected cached body after revalidation: %s", body)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 upstream calls after stale revalidation, got %d", calls.Load())
	}
	if bodyHits.Load() != 1 {
		t.Fatalf("expected body to be fetched once, got %d", bodyHits.Load())
	}
}

func TestProxy_SeparatesCacheByUpstream(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hits.Add(1)
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Content-Type", "application/json")
		// Differentiate by the upstream header the proxy forwards.
		w.Write([]byte(`{"hit":` + fmt.Sprintf("%d", hits.Load()) + `}`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL + "/public", upstream.URL + "/enterprise"}, time.Minute)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func(upstreamURL string) {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstreamURL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// Two different upstream headers for the same path should produce
	// two separate cache entries (2 upstream hits).
	doGET(upstream.URL + "/public")
	doGET(upstream.URL + "/enterprise")
	if hits.Load() != 2 {
		t.Fatalf("expected 2 upstream hits for different upstreams, got %d", hits.Load())
	}
}

func TestProxy_PassesThroughNonGET(t *testing.T) {
	var method string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL}, time.Minute)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("POST", proxyServer.URL+"/repos/owner/repo/issues", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if method != "POST" {
		t.Fatalf("expected POST, got %s", method)
	}
}

func TestProxy_DefaultUpstream(t *testing.T) {
	// Verify that the cache key includes the default upstream.
	key := cacheKey(defaultUpstream, "/repos/owner/repo", "", "")
	if key != "https://api.github.com|/repos/owner/repo||" {
		t.Fatalf("unexpected cache key: %s", key)
	}
}

func TestProxy_RewritesLinkHeader(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		// Simulate GitHub pagination with absolute upstream URLs.
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next", <%s/repos/owner/repo/issues?page=5>; rel="last"`, upstream.URL, upstream.URL))
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL}, time.Minute)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	link := resp.Header.Get("Link")
	wantLink := fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next", <%s/repos/owner/repo/issues?page=5>; rel="last"`, proxyServer.URL, proxyServer.URL)
	if link != wantLink {
		t.Errorf("Link header not rewritten:\n  got:  %s\n  want: %s", link, wantLink)
	}
}

func TestProxy_CachedResponsePreservesLinkHeader(t *testing.T) {
	var hits atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hits.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next"`, upstream.URL))
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy([]string{upstream.URL}, 0)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() *http.Response {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First request — populates cache.
	resp1 := doGET()
	resp1.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits.Load())
	}

	// Second request — served from cache via 304.
	resp2 := doGET()
	defer resp2.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected still 1 upstream hit, got %d", hits.Load())
	}

	link := resp2.Header.Get("Link")
	wantLink := fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next"`, proxyServer.URL)
	if link != wantLink {
		t.Errorf("Cached Link header not rewritten:\n  got:  %s\n  want: %s", link, wantLink)
	}
}

func TestProxy_RejectsDisallowedUpstream(t *testing.T) {
	p := newProxy([]string{"https://api.github.com"}, time.Minute)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, "https://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed upstream, got %d", resp.StatusCode)
	}
}

func TestProxy_AllowsConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newProxy([]string{"https://api.github.com", upstream.URL}, time.Minute)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for allowed upstream, got %d", resp.StatusCode)
	}
}

func TestCacheKeyVariesByAcceptAndAuthorization(t *testing.T) {
	key1 := cacheKey(defaultUpstream, "/repos/o/r/issues", "application/json", "token one")
	key2 := cacheKey(defaultUpstream, "/repos/o/r/issues", "application/vnd.github.raw+json", "token one")
	key3 := cacheKey(defaultUpstream, "/repos/o/r/issues", "application/json", "token two")

	if key1 == key2 {
		t.Fatal("expected cache key to vary by Accept header")
	}
	if key1 == key3 {
		t.Fatal("expected cache key to vary by Authorization header")
	}
	if key1 == cacheKey(defaultUpstream, "/repos/o/r/issues", "application/json", "token one") {
		return
	}
	t.Fatal("expected cache key to be stable for identical inputs")
}

func TestRewriteLinkHeader(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		upstream string
		proxy    string
		want     string
	}{
		{
			name:     "single next link",
			header:   `<https://api.github.com/repos/owner/repo/issues?page=2>; rel="next"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/owner/repo/issues?page=2>; rel="next"`,
		},
		{
			name:     "next and last links",
			header:   `<https://api.github.com/repos/o/r?page=2>; rel="next", <https://api.github.com/repos/o/r?page=5>; rel="last"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/o/r?page=2>; rel="next", <http://ghproxy:8888/repos/o/r?page=5>; rel="last"`,
		},
		{
			name:     "enterprise upstream",
			header:   `<https://github.example.com/api/v3/repos/o/r?page=2>; rel="next"`,
			upstream: "https://github.example.com/api/v3",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/o/r?page=2>; rel="next"`,
		},
		{
			name:     "no matching upstream — unchanged",
			header:   `<https://other.host/repos/o/r?page=2>; rel="next"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<https://other.host/repos/o/r?page=2>; rel="next"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteLinkHeader(tt.header, tt.upstream, tt.proxy)
			if got != tt.want {
				t.Errorf("rewriteLinkHeader():\n  got:  %s\n  want: %s", got, tt.want)
			}
		})
	}
}
