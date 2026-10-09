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

	// RetryAfter is set from the Retry-After header, which the node sends on
	// a 429 and a proxy may send on a 503. Zero otherwise.
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
	ErrConflict        = errors.New("gnarl: conflict")
	ErrRateLimited     = errors.New("gnarl: rate limited")
	ErrUnavailable     = errors.New("gnarl: unavailable")
	ErrUnsupported     = errors.New("gnarl: unsupported")
	ErrInternal        = errors.New("gnarl: internal error")
)

// sentinelFor maps the wire type onto a sentinel. It covers every type in the
// description's ErrorType enum — a test holds it to that. Types absent from
// this map still produce a *Error with Type set — an unrecognised type must
// never be swallowed or remapped to something more familiar.
var sentinelFor = map[string]error{
	"index_not_found":    ErrNotFound,
	"document_not_found": ErrNotFound,
	// No route at that path. Before this type existed an unmatched /v1 path
	// answered 200 with the Console's HTML, so a typo looked like success.
	"route_not_found":      ErrNotFound,
	"field_not_found":      ErrNotFound,
	"repository_not_found": ErrNotFound,
	"snapshot_not_found":   ErrNotFound,
	"index_already_exists": ErrAlreadyExists,
	"validation_error":     ErrValidation,
	"schema_error":         ErrValidation,
	// Snapshotting a `__pool_N` index by name without allow_shared_pool: a
	// request the caller can fix, so it is a validation failure.
	"shared_pool":     ErrValidation,
	"unauthenticated": ErrUnauthenticated,
	// The node sends `unauthorized` for exactly one thing: a BYOK key that
	// does not unwrap the namespace's DEK, carried on a 403. It is a refusal
	// of what was presented, not a missing credential, so it is Forbidden.
	"unauthorized":    ErrForbidden,
	"forbidden":       ErrForbidden,
	"rate_limited":    ErrRateLimited,
	"job_in_progress": ErrConflict,
	// A namespace mid-promotion: writes go to two places, so a snapshot now
	// could miss documents. Wait and retry; it is not a capability gap.
	"namespace_not_snapshottable": ErrConflict,
	// A snapshot signed by a node this one cannot attribute. Supply the
	// signer's key or opt in deliberately.
	"unverified_signer":      ErrConflict,
	"unsupported_capability": ErrUnsupported,
	"unsupported_engine":     ErrUnsupported,
	"internal_error":         ErrInternal,
	"repository_error":       ErrInternal,
}

// sentinelForStatus is what the HTTP status alone says.
var sentinelForStatus = map[int]error{
	http.StatusNotFound:           ErrNotFound,
	http.StatusUnauthorized:       ErrUnauthenticated,
	http.StatusForbidden:          ErrForbidden,
	http.StatusConflict:           ErrConflict,
	http.StatusTooManyRequests:    ErrRateLimited,
	http.StatusServiceUnavailable: ErrUnavailable,
}

func (e *Error) Is(target error) bool {
	if s, ok := sentinelFor[e.Type]; ok && s == target {
		return true
	}
	// Fall back to the status, whatever the type. A 404 means not found
	// whether the body said index_not_found, a type added after this client
	// was built, or nothing at all — a proxy's 404 or a load balancer's 503
	// never carries our envelope. This used to apply only when Type was
	// empty, so a 404 carrying any type this map did not know — including
	// route_not_found — failed errors.Is(err, ErrNotFound) and read to the
	// caller as something other than absence.
	//
	// This is not remapping the type: Type is still exactly what the node
	// sent. It is reading the one fact HTTP guarantees.
	if s, ok := sentinelForStatus[e.Status]; ok && s == target {
		return true
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

	e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())

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

// parseRetryAfter reads both forms RFC 9110 allows: delay-seconds, which the
// node sends, and an HTTP-date, which a proxy in front of it may. A date in
// the past is zero, not negative.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
