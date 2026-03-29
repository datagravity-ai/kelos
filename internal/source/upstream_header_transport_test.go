package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpstreamHeaderTransport_InjectsOnGET(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(UpstreamBaseURLHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: NewUpstreamHeaderTransport(http.DefaultTransport, "https://api.github.com"),
	}

	resp, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotHeader != "https://api.github.com" {
		t.Errorf("expected header %q, got %q", "https://api.github.com", gotHeader)
	}
}

func TestUpstreamHeaderTransport_SkipsNonGET(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(UpstreamBaseURLHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: NewUpstreamHeaderTransport(http.DefaultTransport, "https://api.github.com"),
	}

	resp, err := client.Post(srv.URL+"/repos/owner/repo/issues/1/comments", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotHeader != "" {
		t.Errorf("expected no header on POST, got %q", gotHeader)
	}
}

func TestUpstreamHeaderTransport_EnterpriseURL(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(UpstreamBaseURLHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: NewUpstreamHeaderTransport(http.DefaultTransport, "https://github.example.com/api/v3"),
	}

	resp, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotHeader != "https://github.example.com/api/v3" {
		t.Errorf("expected header %q, got %q", "https://github.example.com/api/v3", gotHeader)
	}
}
