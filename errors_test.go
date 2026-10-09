package gnarl

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// UNIT: error parsing, with no node involved.
//
// These feed bodies straight to the parser. The conformance suite proves the
// server emits the shape; this proves the client reads every shape it might
// meet — including the ones the server no longer produces but an older node
// still might, and the ones that never came from the server at all.

func respFor(status int, body string, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestParsesTheCanonicalEnvelope(t *testing.T) {
	err := parseError(respFor(404,
		`{"error":{"type":"index_not_found","reason":"index 'x' not found"}}`, nil))

	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T, want *Error", err)
	}
	if e.Type != "index_not_found" {
		t.Errorf("Type = %q", e.Type)
	}
	if e.Reason != "index 'x' not found" {
		t.Errorf("Reason = %q", e.Reason)
	}
	if e.Status != 404 {
		t.Errorf("Status = %d", e.Status)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Error("index_not_found does not satisfy errors.Is(err, ErrNotFound)")
	}
}

// REGRESSION: a node older than the envelope unification sends `message` and
// no `reason`. Dropping support for the alias would make this client fail
// against a node it is supposed to work with.
func TestFallsBackToTheDeprecatedMessageAlias(t *testing.T) {
	err := parseError(respFor(429,
		`{"error":{"type":"rate_limited","message":"slow down"}}`, nil))

	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T", err)
	}
	if e.Reason != "slow down" {
		t.Errorf("Reason = %q — `message` is the only text an older node "+
			"sends, so ignoring it loses the explanation entirely", e.Reason)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Error("rate_limited does not satisfy errors.Is(err, ErrRateLimited)")
	}
}

// The framework's 422 for a malformed body is text/plain, emitted by the
// extractor before any handler runs. It must still produce a usable error
// rather than a nil or a panic.
func TestANonJSONBodyStillProducesAnError(t *testing.T) {
	body := "Failed to deserialize the JSON body into the target type"
	err := parseError(respFor(422, body, nil))

	if err == nil {
		t.Fatal("parseError returned nil for a failed response")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T", err)
	}
	if e.Status != 422 {
		t.Errorf("Status = %d, want 422 — the status is all a caller has "+
			"when the body is not JSON", e.Status)
	}
	if !strings.Contains(e.Reason, "deserialize") {
		t.Errorf("Reason = %q, want the body text preserved", e.Reason)
	}
}

// A proxy or load balancer answers with its own body, not ours.
func TestAForeignErrorBodyKeepsItsStatus(t *testing.T) {
	err := parseError(respFor(503, "<html>503 Service Unavailable</html>", nil))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T", err)
	}
	if e.Status != 503 {
		t.Errorf("Status = %d, want 503", e.Status)
	}
	if e.Type != "" {
		t.Errorf("Type = %q — inventing a type for a body we did not "+
			"recognise tells the caller something we do not know", e.Type)
	}
}

func TestRetryAfterIsRead(t *testing.T) {
	err := parseError(respFor(429,
		`{"error":{"type":"rate_limited","reason":"slow down"}}`,
		map[string]string{"Retry-After": "7"}))

	var e *Error
	errors.As(err, &e)
	if e.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", e.RetryAfter)
	}
	if got := e.RetryAfterOrDefault(time.Minute); got != 7*time.Second {
		t.Errorf("RetryAfterOrDefault = %v, want the server's hint", got)
	}

	// And the default applies when the server gave none.
	err2 := parseError(respFor(429, `{"error":{"type":"rate_limited","reason":"x"}}`, nil))
	var e2 *Error
	errors.As(err2, &e2)
	if got := e2.RetryAfterOrDefault(time.Minute); got != time.Minute {
		t.Errorf("RetryAfterOrDefault = %v, want the caller's default", got)
	}
}

// An error type this client has never seen must still arrive intact. Mapping
// it onto a familiar sentinel would be worse than not mapping it: the caller
// would branch on a guess. (A 400 says nothing a sentinel could: only the
// statuses that do — 404, 409, 429 and the rest — match by status alone.)
func TestAnUnknownTypeIsNotRemapped(t *testing.T) {
	err := parseError(respFor(400,
		`{"error":{"type":"some_future_type","reason":"a new failure"}}`, nil))

	var e *Error
	errors.As(err, &e)
	if e.Type != "some_future_type" {
		t.Errorf("Type = %q, want it preserved verbatim", e.Type)
	}
	for _, sentinel := range []error{
		ErrNotFound, ErrValidation, ErrAlreadyExists, ErrInternal,
		ErrForbidden, ErrUnauthenticated, ErrRateLimited, ErrUnsupported,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("an unknown type matched %v — a caller branching on that "+
				"sentinel would take a path meant for a different failure", sentinel)
		}
	}
}

// Both 404 types must satisfy ErrNotFound: a caller asking "is it there?"
// should not have to know which kind of thing was missing.
func TestBothNotFoundTypesMatchTheSentinel(t *testing.T) {
	for _, typ := range []string{"index_not_found", "document_not_found"} {
		err := parseError(respFor(404,
			`{"error":{"type":"`+typ+`","reason":"gone"}}`, nil))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s does not satisfy errors.Is(err, ErrNotFound)", typ)
		}
	}
}

// The detail block is what a caller acts on for a rate limit.
func TestDetailSurvives(t *testing.T) {
	err := parseError(respFor(429,
		`{"error":{"type":"rate_limited","reason":"x","detail":{"retry_after_seconds":3}}}`, nil))
	var e *Error
	errors.As(err, &e)
	if e.Detail == nil {
		t.Fatal("Detail was dropped")
	}
	if _, ok := e.Detail["retry_after_seconds"]; !ok {
		t.Errorf("Detail = %v, missing retry_after_seconds", e.Detail)
	}
}

// REGRESSION: the status fallback used to apply only when Type was empty, so
// a 404 carrying any type the client did not know failed
// errors.Is(err, ErrNotFound). `route_not_found` was exactly that case until
// it was added to the map — and the next new type would have been too. A 404
// is not found whatever the body calls it.
func TestA404WithAnyTypeIsNotFound(t *testing.T) {
	for _, typ := range []string{"route_not_found", "some_type_added_next_year"} {
		err := parseError(respFor(404,
			`{"error":{"type":"`+typ+`","reason":"gone"}}`, nil))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("a 404 with type %q does not satisfy errors.Is(err, ErrNotFound)", typ)
		}
		var e *Error
		errors.As(err, &e)
		if e.Type != typ {
			t.Errorf("Type = %q — the status fallback must not rewrite the type", e.Type)
		}
	}
}

// Every status the client has a sentinel for matches it under an unknown
// type, and the type is never what decides it.
func TestStatusFallbackIgnoresTheType(t *testing.T) {
	for status, want := range map[int]error{
		401: ErrUnauthenticated,
		403: ErrForbidden,
		404: ErrNotFound,
		409: ErrConflict,
		429: ErrRateLimited,
		503: ErrUnavailable,
	} {
		err := parseError(respFor(status,
			`{"error":{"type":"a_future_type","reason":"x"}}`, nil))
		if !errors.Is(err, want) {
			t.Errorf("http %d with an unknown type does not satisfy %v", status, want)
		}
	}
}

// The description's ErrorType enum is a promise: these are the types a node
// may send. Every one must reach a sentinel, or a caller using errors.Is
// cannot branch on it without string-matching Type.
func TestEveryDescribedErrorTypeHasASentinel(t *testing.T) {
	for typ := range errorTypeEnum(t) {
		if _, ok := sentinelFor[typ]; !ok {
			t.Errorf("error type %q is in the description's enum but maps to no sentinel", typ)
		}
	}
	// And the reverse: a type this client maps but the server no longer
	// describes is dead weight that hides a rename.
	described := errorTypeEnum(t)
	for typ := range sentinelFor {
		if !described[typ] {
			t.Errorf("sentinelFor maps %q, which the description does not declare", typ)
		}
	}
}

// The BYOK wrong-key refusal is a 403 the node types `unauthorized`.
func TestWrongNamespaceKeyIsForbidden(t *testing.T) {
	err := parseError(respFor(403,
		`{"error":{"type":"unauthorized","reason":"invalid key for namespace"}}`, nil))
	if !errors.Is(err, ErrForbidden) {
		t.Error("unauthorized (403) does not satisfy ErrForbidden")
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Error("a wrong BYOK key is not a missing credential")
	}
}

func TestSnapshotConflictsAreConflicts(t *testing.T) {
	for _, typ := range []string{"job_in_progress", "namespace_not_snapshottable", "unverified_signer"} {
		err := parseError(respFor(409, `{"error":{"type":"`+typ+`","reason":"x"}}`, nil))
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s does not satisfy ErrConflict", typ)
		}
	}
}

func TestRetryAfterAcceptsAnHTTPDate(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	date := now.Add(90 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(date, now); got != 90*time.Second {
		t.Errorf("parseRetryAfter(%q) = %v, want 90s", date, got)
	}
	if got := parseRetryAfter(now.Add(-time.Hour).Format(http.TimeFormat), now); got != 0 {
		t.Errorf("a date in the past gave %v, want 0", got)
	}
	if got := parseRetryAfter("-3", now); got != 0 {
		t.Errorf("negative seconds gave %v, want 0", got)
	}
	if got := parseRetryAfter("soon", now); got != 0 {
		t.Errorf("garbage gave %v, want 0", got)
	}
}
