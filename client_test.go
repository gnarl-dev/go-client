package gnarl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewReadsTheAddressAndTokenFromTheEnvironment(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvToken, "tok-from-env")
	c, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok-from-env" {
		t.Errorf("Authorization = %q", auth)
	}

	// An explicit WithToken wins, even an empty one.
	c, _ = New("", WithToken(""))
	_ = c.Ping(context.Background())
	if auth != "" {
		t.Errorf("WithToken(\"\") still sent %q", auth)
	}
}

func TestNewWithNoAddressAnywhereNamesTheVariable(t *testing.T) {
	t.Setenv(EnvURL, "")
	_, err := New("")
	if err == nil || !strings.Contains(err.Error(), EnvURL) {
		t.Errorf("got %v; want an error naming $%s", err, EnvURL)
	}
}

// REGRESSION: WithTimeout set Timeout on the *http.Client the caller passed,
// so building a gnarl client changed the caller's own client — every other
// request in their program inherited a timeout it never asked for.
func TestOptionsDoNotMutateTheCallersHTTPClient(t *testing.T) {
	tr := &http.Transport{}
	mine := &http.Client{Timeout: time.Minute, Transport: tr}
	c, err := New("https://example.invalid",
		WithHTTPClient(mine), WithTimeout(3*time.Second), WithInsecureSkipVerify())
	if err != nil {
		t.Fatal(err)
	}
	if mine.Timeout != time.Minute {
		t.Errorf("caller's Timeout became %v", mine.Timeout)
	}
	if mine.Transport != tr || (tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify) {
		t.Error("caller's transport had TLS verification switched off")
	}
	if c.http.Timeout != 3*time.Second {
		t.Errorf("client Timeout = %v, want 3s", c.http.Timeout)
	}
	if got := c.http.Transport.(*http.Transport); got == tr || !got.TLSClientConfig.InsecureSkipVerify {
		t.Error("the client's own transport is not an insecure clone")
	}
}
