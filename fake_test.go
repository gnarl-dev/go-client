package gnarl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// UNIT: methods against a fake node.
//
// The conformance suite proves the node accepts what this client sends. These
// prove what the client SENDS — method, path, query and body — and that it
// reads every field of the answer, with no node involved, so a broken method
// fails here in milliseconds rather than only where a node is available.

// seen is one request the fake node received.
type seen struct {
	Method string
	Path   string // escaped, as on the wire
	Query  string
	Body   map[string]any
	Raw    string
	Header http.Header
}

// fakeNode answers every request with reply and records what it was asked.
type fakeNode struct {
	mu       sync.Mutex
	requests []seen
}

func (f *fakeNode) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the fake node received no request")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeNode) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// newFake starts a fake node whose handler decides each answer.
func newFake(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) (*Client, *fakeNode) {
	t.Helper()
	f := &fakeNode{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		s := seen{
			Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			Raw: string(raw), Header: r.Header.Clone(),
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &s.Body)
		}
		f.mu.Lock()
		f.requests = append(f.requests, s)
		n := len(f.requests)
		f.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(EnvToken, "")
	c, err := New(srv.URL, WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: 1, MaxDelay: 1e9}))
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}

// replying is a fake node that always answers status with body.
func replying(t *testing.T, status int, body string) (*Client, *fakeNode) {
	t.Helper()
	return newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// want asserts the request line.
func (s seen) want(t *testing.T, method, path, query string) {
	t.Helper()
	if s.Method != method || s.Path != path || s.Query != query {
		t.Errorf("request = %s %s?%s, want %s %s?%s",
			s.Method, s.Path, s.Query, method, path, query)
	}
}

// field returns a top-level body member, failing if absent.
func (s seen) field(t *testing.T, name string) any {
	t.Helper()
	v, ok := s.Body[name]
	if !ok {
		t.Fatalf("request body has no %q: %s", name, s.Raw)
	}
	return v
}

// absent asserts the body does not carry name, so the node's default applies.
func (s seen) absent(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := s.Body[name]; ok {
			t.Errorf("request body carries %q, which should be omitted: %s", name, s.Raw)
		}
	}
}
