// Package gnarl is the Go client for a Gnarl node.
//
// A node is a peer in a decentralized search fabric rather than a coordinator,
// so there is no cluster endpoint to point at: you talk to a node, and it
// answers for the mesh. Any node will do.
//
//	// A node you started yourself serves https from a self-signed
//	// certificate, so skip verification for it — and only for it.
//	c, err := gnarl.New("https://localhost:8080", gnarl.WithInsecureSkipVerify())
//	res, err := c.Search(ctx, "places", gnarl.SearchRequest{
//	    Query: gnarl.GeoDistance("location", -33.8688, 151.2093, 1_000_000),
//	})
//
// New("") reads the address from $GNARL_URL, and a client given no WithToken
// reads $GNARL_TOKEN, so the same program runs against a laptop node and a
// remote one without a code change.
//
// The payload types in internal/oas are generated from the node's OpenAPI
// description and cannot drift from it. Everything in this package is written
// by hand, so it can be idiomatic.
package gnarl

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultTimeout applies when no per-request deadline is set on the context.
const DefaultTimeout = 30 * time.Second

// Environment variables New consults when the caller does not say.
const (
	// EnvURL is the node address New uses when given "".
	EnvURL = "GNARL_URL"
	// EnvToken is the bearer token used when no WithToken option is given.
	EnvToken = "GNARL_TOKEN"
)

// Client talks to one Gnarl node. It is safe for concurrent use.
type Client struct {
	baseURL   *url.URL
	http      *http.Client
	token     string
	tokenSet  bool
	userAgent string
	retry     RetryPolicy
}

// Option configures a Client.
type Option func(*Client) error

// WithToken sends an RBAC capability token as a bearer token.
//
// A node with RBAC enabled exempts loopback callers, so a local node usually
// needs no token; a remote one always does.
//
// Passing it — even with "" — overrides $GNARL_TOKEN, so a caller can opt out
// of the environment explicitly.
func WithToken(token string) Option {
	return func(c *Client) error {
		c.token = token
		c.tokenSet = true
		return nil
	}
}

// WithHTTPClient supplies your own *http.Client — for a custom transport,
// proxy, connection pool or tracing hook.
//
// The client is copied, not adopted: WithTimeout and WithInsecureSkipVerify
// change the copy, so a *http.Client you share with the rest of your program
// does not have its timeout or TLS settings changed underneath it.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) error {
		if h == nil {
			return fmt.Errorf("gnarl: WithHTTPClient(nil)")
		}
		cp := *h
		c.http = &cp
		return nil
	}
}

// WithTimeout sets the client-wide timeout. A deadline on the request context
// still wins when it is shorter.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d <= 0 {
			return fmt.Errorf("gnarl: WithTimeout(%v): must be positive", d)
		}
		c.http.Timeout = d
		return nil
	}
}

// WithInsecureSkipVerify disables TLS certificate verification.
//
// A node generates a self-signed certificate on first run, so this is the
// switch you reach for against a development node. It disables the protection
// TLS exists to provide — never set it against a node you did not start
// yourself.
func WithInsecureSkipVerify() Option {
	return func(c *Client) error {
		// Clone even a transport we were handed. Setting InsecureSkipVerify
		// on the caller's own transport would switch verification off for
		// every other client sharing it.
		var tr *http.Transport
		if own, ok := c.http.Transport.(*http.Transport); ok && own != nil {
			tr = own.Clone()
		} else if c.http.Transport == nil {
			tr = http.DefaultTransport.(*http.Transport).Clone()
		} else {
			return fmt.Errorf("gnarl: WithInsecureSkipVerify: the supplied transport is a %T, "+
				"not an *http.Transport, so its TLS settings cannot be changed here", c.http.Transport)
		}
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
		tr.TLSClientConfig.InsecureSkipVerify = true
		c.http.Transport = tr
		return nil
	}
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) error {
		c.userAgent = ua
		return nil
	}
}

// New returns a Client for the node at addr.
//
// addr may omit the scheme, in which case https is assumed: a node serves TLS
// by default, and defaulting to http would silently downgrade a caller who
// wrote "search.example.com". An empty addr reads $GNARL_URL.
func New(addr string, opts ...Option) (*Client, error) {
	if addr == "" {
		addr = os.Getenv(EnvURL)
	}
	if addr == "" {
		return nil, fmt.Errorf("gnarl: New: empty address, and $%s is not set", EnvURL)
	}
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("gnarl: New(%q): %w", addr, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("gnarl: New(%q): no host", addr)
	}
	u.Path = strings.TrimRight(u.Path, "/")

	c := &Client{
		baseURL:   u,
		http:      &http.Client{Timeout: DefaultTimeout},
		userAgent: "gnarl-go",
		retry:     DefaultRetryPolicy,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if !c.tokenSet {
		c.token = os.Getenv(EnvToken)
	}
	return c, nil
}

// do performs one request and decodes a JSON response into out.
//
// out may be nil to discard the body. A non-2xx always yields an *Error.
// Whether a 429 or 503 is retried follows from the method: GET, HEAD, PUT and
// DELETE are idempotent by definition, POST is not.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.call(ctx, method, path, body, out, idempotentMethod(method))
}

// doRead is do for a POST that only reads — a search carries its query in a
// body, but running it twice changes nothing, so it is as safe to retry as a
// GET.
func (c *Client) doRead(ctx context.Context, path string, body, out any) error {
	return c.call(ctx, http.MethodPost, path, body, out, true)
}

func idempotentMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

func (c *Client) call(ctx context.Context, method, path string, body, out any, idempotent bool) error {
	var payload []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gnarl: encoding %s %s: %w", method, path, err)
		}
		payload = buf
	}

	attempts := 1
	if idempotent && c.retry.MaxAttempts > 1 {
		attempts = c.retry.MaxAttempts
	}
	for attempt := 1; ; attempt++ {
		err := c.once(ctx, method, path, payload, body != nil, out)
		if err == nil || attempt >= attempts {
			return err
		}
		wait, ok := c.retry.delay(err, attempt)
		if !ok {
			return err
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			// The caller's deadline ended the wait; what they need to see
			// is why the request failed, not only that time ran out.
			return fmt.Errorf("%w (retry abandoned: %w)", err, ctx.Err())
		case <-t.C:
		}
	}
}

// once is a single attempt. The body is rebuilt from bytes each time, because
// a reader consumed by the first attempt would send an empty second one.
func (c *Client) once(ctx context.Context, method, path string, payload []byte, hasBody bool, out any) error {
	var rdr io.Reader
	if hasBody {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, rdr)
	if err != nil {
		return fmt.Errorf("gnarl: building %s %s: %w", method, path, err)
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gnarl: %s %s: %w", method, path, err)
	}
	defer func() {
		// Drain before closing so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("gnarl: decoding %s %s response: %w", method, path, err)
	}
	return nil
}

// retryable reports whether err is a response the node expects to be retried.
func retryable(err error) (*Error, bool) {
	var e *Error
	if !errors.As(err, &e) {
		return nil, false
	}
	return e, e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable
}

// pathEscape escapes a single path segment. Index and document names reach the
// URL directly, and a document id is caller data that may contain a slash.
func pathEscape(s string) string { return url.PathEscape(s) }
