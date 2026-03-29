package source

import "net/http"

// UpstreamBaseURLHeader is the custom header used to identify the original
// GitHub API base URL when requests are routed through a centralized proxy.
// This lets the proxy route and cache requests separately for github.com
// and GitHub Enterprise hosts.
const UpstreamBaseURLHeader = "X-Kelos-GitHub-Upstream-Base-URL"

type upstreamHeaderTransport struct {
	base        http.RoundTripper
	upstreamURL string
}

// NewUpstreamHeaderTransport wraps a base RoundTripper to inject the
// X-Kelos-GitHub-Upstream-Base-URL header on GET requests. The header
// value is the direct GitHub API base URL, enabling a centralized proxy
// to distinguish traffic destined for different upstream hosts.
// Non-GET requests pass through unmodified.
func NewUpstreamHeaderTransport(base http.RoundTripper, upstreamURL string) http.RoundTripper {
	return &upstreamHeaderTransport{
		base:        base,
		upstreamURL: upstreamURL,
	}
}

func (t *upstreamHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet {
		req = req.Clone(req.Context())
		req.Header.Set(UpstreamBaseURLHeader, t.upstreamURL)
	}
	return t.base.RoundTrip(req)
}
