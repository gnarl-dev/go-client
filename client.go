// Package gnarl is the Go client for a Gnarl node.
//
// A node is a peer in a decentralized search fabric rather than a coordinator,
// so there is no cluster endpoint to point at: you talk to a node, and it
// answers for the mesh. Any node will do.
//
//	c, err := gnarl.New("http://localhost:8080")
//	res, err := c.Search(ctx, "places", gnarl.SearchRequest{
//	    Query: gnarl.GeoDistance("location", -33.8688, 151.2093, 1_000_000),
//	})
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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout applies when no per-request deadline is set on the context.
const DefaultTimeout = 30 * time.Second

// Client talks to one Gnarl node. It is safe for concurrent use.
type Client struct {
	baseURL   *url.URL
	http      *http.Client
	token     string
	userAgent string
}

// Option configures a Client.
type Option func(*Client) error

// WithToken sends an RBAC capability token as a bearer token.
//
// A node with RBAC enabled exempts loopback callers, so a local node usually
// needs no token; a remote one always does.
func WithToken(token string) Option {
	return func(c *Client) error {
		c.token = token
		return nil
	}
}

// WithHTTPClient supplies your own *http.Client — for a custom transport,
// proxy, connection pool or tracing hook.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) error {
		if h == nil {
			return fmt.Errorf("gnarl: WithHTTPClient(nil)")
		}
		c.http = h
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
		tr, ok := c.http.Transport.(*http.Transport)
		if !ok || tr == nil {
			tr = http.DefaultTransport.(*http.Transport).Clone()
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
// wrote "search.example.com".
func New(addr string, opts ...Option) (*Client, error) {
	if addr == "" {
		return nil, fmt.Errorf("gnarl: New: empty address")
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
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// do performs one request and decodes a JSON response into out.
//
// out may be nil to discard the body. A non-2xx always yields an *Error.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gnarl: encoding %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, rdr)
	if err != nil {
		return fmt.Errorf("gnarl: building %s %s: %w", method, path, err)
	}
	if body != nil {
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

// pathEscape escapes a single path segment. Index and document names reach the
// URL directly, and a document id is caller data that may contain a slash.
func pathEscape(s string) string { return url.PathEscape(s) }
