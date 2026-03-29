package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/source"
)

const (
	// defaultUpstream is used when no header is provided.
	defaultUpstream = "https://api.github.com"

	// defaultCacheTTL is the default freshness window for cached GET responses.
	defaultCacheTTL = 15 * time.Second
)

// cacheEntry stores a cached response body together with the ETag returned
// by the upstream so it can be served directly while fresh and revalidated
// later with conditional requests.
type cacheEntry struct {
	etag        string
	body        []byte
	contentType string
	link        string
	status      int
	freshUntil  time.Time
}

type proxy struct {
	mu               sync.RWMutex
	cache            map[string]*cacheEntry
	upstream         *http.Client
	allowedUpstreams map[string]bool
	cacheTTL         time.Duration
	now              func() time.Time
}

func newProxy(allowed []string, cacheTTL time.Duration) *proxy {
	m := make(map[string]bool, len(allowed))
	for _, u := range allowed {
		m[strings.TrimSuffix(strings.TrimSpace(u), "/")] = true
	}
	return &proxy{
		cache: make(map[string]*cacheEntry),
		upstream: &http.Client{
			Timeout: 30 * time.Second,
		},
		allowedUpstreams: m,
		cacheTTL:         cacheTTL,
		now:              time.Now,
	}
}

// cacheKey returns a key that includes the upstream and the request path+query
// and varies by headers that can affect the upstream response.
func cacheKey(upstream, pathAndQuery, accept, authorization string) string {
	return strings.Join([]string{
		upstream,
		pathAndQuery,
		accept,
		authorizationKey(authorization),
	}, "|")
}

func authorizationKey(authorization string) string {
	if authorization == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])
}

// rewriteLinkHeader rewrites absolute URLs in a Link header, replacing the
// upstream base with the proxy's own base so clients follow pagination
// links back through the proxy.
func rewriteLinkHeader(header, upstreamBase, proxyBase string) string {
	return strings.ReplaceAll(header, upstreamBase, proxyBase)
}

func (p *proxy) nextFreshUntil() time.Time {
	if p.cacheTTL <= 0 {
		return time.Time{}
	}
	return p.now().Add(p.cacheTTL)
}

func (p *proxy) isFresh(entry *cacheEntry) bool {
	return entry != nil && !entry.freshUntil.IsZero() && p.now().Before(entry.freshUntil)
}

func writeCachedResponse(w http.ResponseWriter, proxyBase, upstream string, entry *cacheEntry) {
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("ETag", entry.etag)
	if entry.link != "" {
		w.Header().Set("Link", rewriteLinkHeader(entry.link, upstream, proxyBase))
	}
	w.WriteHeader(entry.status)
	w.Write(entry.body)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := ctrl.Log.WithName("ghproxy")
	resource := source.ClassifyResource(r.URL.Path)
	statusCode := http.StatusBadGateway
	cacheResult := "skip"
	defer func() {
		githubAPIRequestsTotal.WithLabelValues(r.Method, strconv.Itoa(statusCode), resource, cacheResult).Inc()
	}()

	upstream := r.Header.Get(source.UpstreamBaseURLHeader)
	if upstream == "" {
		upstream = defaultUpstream
	}
	upstream = strings.TrimSuffix(upstream, "/")

	if !p.allowedUpstreams[upstream] {
		statusCode = http.StatusForbidden
		http.Error(w, "Upstream not allowed", http.StatusForbidden)
		log.Info("Rejected request for disallowed upstream", "upstream", upstream)
		return
	}

	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}
	proxyBase := scheme + "://" + r.Host
	key := cacheKey(upstream, r.URL.RequestURI(), r.Header.Get("Accept"), r.Header.Get("Authorization"))
	var entry *cacheEntry
	if r.Method == http.MethodGet {
		p.mu.RLock()
		entry = p.cache[key]
		p.mu.RUnlock()
		if p.isFresh(entry) {
			statusCode = entry.status
			cacheResult = "fresh_hit"
			writeCachedResponse(w, proxyBase, upstream, entry)
			return
		}
	}

	// Build the upstream URL by combining the upstream base with the
	// incoming request path and query string.
	target, err := url.Parse(upstream + r.URL.RequestURI())
	if err != nil {
		http.Error(w, "Bad upstream URL", http.StatusBadGateway)
		log.Error(err, "Failed to parse upstream URL", "upstream", upstream, "path", r.URL.RequestURI())
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		log.Error(err, "Failed to create upstream request")
		return
	}

	// Copy relevant headers from the original request.
	for _, h := range []string{"Accept", "Authorization", "User-Agent"} {
		if v := r.Header.Get(h); v != "" {
			outReq.Header.Set(h, v)
		}
	}

	// For GET requests, attach a cached ETag as If-None-Match so the
	// upstream can return 304 when nothing changed.
	if r.Method == http.MethodGet && entry != nil {
		outReq.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := p.upstream.Do(outReq)
	if err != nil {
		http.Error(w, "Upstream request failed", http.StatusBadGateway)
		log.Error(err, "Upstream request failed", "url", target.String())
		return
	}
	defer resp.Body.Close()

	// On 304, serve the cached body directly.
	if resp.StatusCode == http.StatusNotModified {
		if entry != nil {
			refreshed := *entry
			refreshed.freshUntil = p.nextFreshUntil()
			p.mu.Lock()
			p.cache[key] = &refreshed
			p.mu.Unlock()

			statusCode = entry.status
			cacheResult = "revalidated_hit"
			writeCachedResponse(w, proxyBase, upstream, &refreshed)
			return
		}
		// Cache miss after 304 — should not happen, but fall through
		// and treat it as an error by returning the 304 as-is.
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Failed to read upstream response", http.StatusBadGateway)
		log.Error(err, "Failed to read upstream response body")
		return
	}

	// Cache successful GET responses that include an ETag.
	if r.Method == http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if etag := resp.Header.Get("ETag"); etag != "" {
			p.mu.Lock()
			p.cache[key] = &cacheEntry{
				etag:        etag,
				body:        body,
				contentType: resp.Header.Get("Content-Type"),
				link:        resp.Header.Get("Link"),
				status:      resp.StatusCode,
				freshUntil:  p.nextFreshUntil(),
			}
			p.mu.Unlock()
		}
	}

	// Copy response headers.
	for _, h := range []string{"Content-Type", "ETag", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if link := resp.Header.Get("Link"); link != "" {
		w.Header().Set("Link", rewriteLinkHeader(link, upstream, proxyBase))
	}
	statusCode = resp.StatusCode
	if r.Method == http.MethodGet {
		cacheResult = "miss"
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func main() {
	var listenAddr string
	var metricsAddr string
	var allowedUpstreamsFlag string
	var cacheTTL time.Duration
	flag.StringVar(&listenAddr, "listen-address", ":8888", "Address to listen on")
	flag.StringVar(&metricsAddr, "metrics-address", ":9090", "Address to serve Prometheus metrics on")
	flag.StringVar(&allowedUpstreamsFlag, "allowed-upstreams", defaultUpstream, "Comma-separated list of allowed upstream base URLs")
	flag.DurationVar(&cacheTTL, "cache-ttl", defaultCacheTTL, "Duration to serve cached GET responses without upstream revalidation")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))
	log := ctrl.Log.WithName("ghproxy")

	allowed := strings.Split(allowedUpstreamsFlag, ",")
	p := newProxy(allowed, cacheTTL)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      p,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:    metricsAddr,
		Handler: metricsMux,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "Metrics server failed")
			os.Exit(1)
		}
	}()

	log.Info("Starting ghproxy", "address", listenAddr, "metricsAddress", metricsAddr, "allowedUpstreams", allowed, "cacheTTL", cacheTTL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "Server failed")
		os.Exit(1)
	}
}
