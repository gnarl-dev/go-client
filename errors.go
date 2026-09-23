package gnarl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Error is the one error type a caller has to handle.
//
// The node emits a single error envelope on every route:
//
//	{"error": {"type": "...", "reason": "...", "detail": {...}}}
//
// That is worth stating because it was not always true. The API used to
// produce four different bodies — including one where `error` was a plain
// string rather than an object — which forced a second error type into any
// client generated from the description. Because there is now one shape, this
// is one struct.
type Error struct {
	// Type is the stable machine-readable error type, e.g. "index_not_found".
	// New types may be added; existing ones are not removed or renamed, so it
	// is safe to switch on. Prefer errors.Is with the sentinels below.
	Type string

	// Reason is the human-readable description. Not stable; do not match on it.
	Reason string

	// Detail carries structured, type-specific context. Present on capability,
	// engine and rate-limit errors; nil otherwise.
	Detail map[string]any

	// Status is the HTTP status code that carried the error.
	Status int

	// RetryAfter is set from the Retry-After header on a 429. Zero otherwise.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("gnarl: %s (http %d)", e.Type, e.Status)
	}
	return fmt.Sprintf("gnarl: %s: %s", e.Type, e.Reason)
}

// Sentinels for errors.Is. Matching on these rather than on Type means a
// caller's control flow does not break if we ever split one type into two.
var (
	ErrNotFound        = errors.New("gnarl: not found")
	ErrAlreadyExists   = errors.New("gnarl: already exists")
	ErrValidation      = errors.New("gnarl: validation error")
	ErrUnauthenticated = errors.New("gnarl: unauthenticated")
	ErrForbidden       = errors.New("gnarl: forbidden")
	ErrRateLimited     = errors.New("gnarl: rate limited")
	ErrUnsupported     = errors.New("gnarl: unsupported")
	ErrInternal        = errors.New("gnarl: internal error")
)

// sentinelFor maps the wire type onto a sentinel. Types absent from this map
// still produce a *Error with Type set — an unrecognised type must never be
// swallowed or remapped to something more familiar.
var sentinelFor = map[string]error{
	"index_not_found":             ErrNotFound,
	"document_not_found":          ErrNotFound,
	"field_not_found":             ErrNotFound,
	"repository_not_found":        ErrNotFound,
	"snapshot_not_found":          ErrNotFound,
	"index_already_exists":        ErrAlreadyExists,
	"validation_error":            ErrValidation,
	"schema_error":                ErrValidation,
	"unauthenticated":             ErrUnauthenticated,
	"unauthorized":                ErrUnauthenticated,
	"forbidden":                   ErrForbidden,
	"rate_limited":                ErrRateLimited,
	"unsupported_capability":      ErrUnsupported,
	"unsupported_engine":          ErrUnsupported,
	"namespace_not_snapshottable": ErrUnsupported,
	"internal_error":              ErrInternal,
	"repository_error":            ErrInternal,
}

func (e *Error) Is(target error) bool {
	if s, ok := sentinelFor[e.Type]; ok && s == target {
		return true
	}
	// Fall back to status for a body we could not type — a proxy's 404 or a
	// load balancer's 503 never carries our envelope.
	switch target {
	case ErrNotFound:
		return e.Type == "" && e.Status == http.StatusNotFound
	case ErrUnauthenticated:
		return e.Type == "" && e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Type == "" && e.Status == http.StatusForbidden
	case ErrRateLimited:
		return e.Type == "" && e.Status == http.StatusTooManyRequests
	}
	return false
}

// RetryAfterOrDefault is the server's backoff hint, or d when it gave none.
func (e *Error) RetryAfterOrDefault(d time.Duration) time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	return d
}

// errorEnvelope mirrors the wire shape. `message` is a deprecated alias for
// `reason` that the server still emits; it is read only as a fallback so this
// client keeps working against a node older than the envelope unification.
type errorEnvelope struct {
	Error struct {
		Type    string         `json:"type"`
		Reason  string         `json:"reason"`
		Message string         `json:"message"`
		Detail  map[string]any `json:"detail"`
	} `json:"error"`
}

// parseError turns a non-2xx response into an *Error.
//
// It must never return nil for a failed response, and it must never lose the
// status. A body that is not our envelope still produces a usable error: the
// framework's own 422 for a malformed request body is text/plain and arrives
// before any handler runs, so it has no envelope at all.
func parseError(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		e.Type = "transport_error"
		e.Reason = "could not read error body: " + readErr.Error()
		return e
	}

	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil || env.Error.Type == "" {
		// Not our envelope. Surface the status and as much of the body as is
		// useful, rather than inventing a type we did not receive.
		e.Reason = truncate(string(body), 512)
		if e.Reason == "" {
			e.Reason = http.StatusText(resp.StatusCode)
		}
		return e
	}

	e.Type = env.Error.Type
	e.Reason = env.Error.Reason
	if e.Reason == "" {
		e.Reason = env.Error.Message // pre-unification node
	}
	e.Detail = env.Error.Detail
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
