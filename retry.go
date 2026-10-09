package gnarl

import (
	"math/rand/v2"
	"time"
)

// RetryPolicy decides whether and how long to wait before repeating a request
// the node turned away with 429 or 503.
//
// Only idempotent requests are retried: GET, PUT, DELETE, and the POSTs that
// only read (search, recall). A write sent by POST — a document without an id,
// a bulk batch — is never repeated by this client, because a 503 from a proxy
// does not prove the node did not apply it.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries, the first included. One or
	// less disables retrying.
	MaxAttempts int

	// BaseDelay is the first backoff when the node sent no Retry-After. It
	// doubles on each attempt, with jitter.
	BaseDelay time.Duration

	// MaxDelay caps any single wait. A Retry-After longer than this is NOT
	// shortened: the request fails with the node's error and its RetryAfter,
	// so the caller decides whether to wait that long. Retrying sooner than
	// the node asked is how a client turns a rate limit into a ban.
	MaxDelay time.Duration
}

// DefaultRetryPolicy is what a Client uses unless told otherwise: three tries,
// waiting at most ten seconds for any one of them.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts: 3,
	BaseDelay:   200 * time.Millisecond,
	MaxDelay:    10 * time.Second,
}

// WithRetry replaces the retry policy.
func WithRetry(p RetryPolicy) Option {
	return func(c *Client) error {
		c.retry = p
		return nil
	}
}

// WithoutRetry turns retrying off: every 429 and 503 is returned at once.
func WithoutRetry() Option {
	return WithRetry(RetryPolicy{MaxAttempts: 1})
}

// delay is how long to wait before attempt+1, or false to give up now.
func (p RetryPolicy) delay(err error, attempt int) (time.Duration, bool) {
	e, ok := retryable(err)
	if !ok {
		return 0, false
	}
	if e.RetryAfter > 0 {
		if p.MaxDelay > 0 && e.RetryAfter > p.MaxDelay {
			return 0, false
		}
		return e.RetryAfter, true
	}
	d := p.BaseDelay << (attempt - 1)
	if d <= 0 {
		d = p.BaseDelay
	}
	// Up to half again, so a fleet of clients turned away together does not
	// come back together.
	if d > 0 {
		d += time.Duration(rand.Int64N(int64(d)/2 + 1))
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d, true
}
