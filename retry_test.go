package gnarl

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func flaky(t *testing.T, failures, status int, retryAfter string) (*Client, *fakeNode) {
	t.Helper()
	return newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n <= failures {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limited","reason":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"node_id":"n","mode":"single-node"}`))
	})
}

func TestAnIdempotentRequestIsRetriedOn429And503(t *testing.T) {
	for _, status := range []int{429, 503} {
		c, f := flaky(t, 2, status, "")
		if _, err := c.Status(context.Background()); err != nil {
			t.Fatalf("http %d: %v", status, err)
		}
		if f.count() != 3 {
			t.Errorf("http %d: node saw %d requests, want 3", status, f.count())
		}
	}
}

// Every attempt must carry the whole body: a reader drained by the first
// would send the second empty.
func TestARetriedRequestResendsItsBody(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"hits":{"hits":[],"total":{"value":0,"relation":"eq"}},
			"coverage":{"expected_claims":0,"served_claims":0,"skipped_claims":[]},"partial":false}`))
	})
	if _, err := c.Search(context.Background(), "i", SearchRequest{Query: MatchAll(), Size: 3}); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatalf("node saw %d requests", f.count())
	}
	if got := f.last(t).field(t, "size"); got != float64(3) {
		t.Errorf("retry sent size %v: %s", got, f.last(t).Raw)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	c, f := flaky(t, 100, 503, "")
	_, err := c.Status(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	if f.count() != 3 {
		t.Errorf("node saw %d requests, want MaxAttempts = 3", f.count())
	}
}

// A POST that writes is never repeated: a 503 does not prove it was not
// applied.
func TestANonIdempotentWriteIsNotRetried(t *testing.T) {
	c, f := flaky(t, 100, 503, "")
	if _, err := c.IndexDocument(context.Background(), "i", "", map[string]int{"a": 1}); err == nil {
		t.Fatal("a 503 was not an error")
	}
	if f.count() != 1 {
		t.Errorf("node saw %d writes, want 1", f.count())
	}
}

// Other failures are not the node asking for a retry.
func TestOtherStatusesAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 404, 500} {
		c, f := flaky(t, 100, status, "")
		_, _ = c.Status(context.Background())
		if f.count() != 1 {
			t.Errorf("http %d was retried %d times", status, f.count()-1)
		}
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	c, f := flaky(t, 1, 429, "1")
	start := time.Now()
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Errorf("retried after %v; the node asked for 1s", waited)
	}
	if f.count() != 2 {
		t.Errorf("node saw %d requests", f.count())
	}
}

// Waiting less than the node asked is how a rate limit becomes a ban. A
// Retry-After beyond MaxDelay is returned to the caller, not shortened.
func TestARetryAfterBeyondTheCapIsNotWaitedOrShortened(t *testing.T) {
	c, f := flaky(t, 100, 429, "3600")
	start := time.Now()
	_, err := c.Status(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.RetryAfter != time.Hour {
		t.Fatalf("got %v, want the node's error with RetryAfter = 1h", err)
	}
	if f.count() != 1 || time.Since(start) > time.Second {
		t.Errorf("node saw %d requests in %v", f.count(), time.Since(start))
	}
}

func TestWithoutRetryReturnsAtOnce(t *testing.T) {
	c, f := flaky(t, 100, 503, "")
	if err := WithoutRetry()(c); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Status(context.Background())
	if f.count() != 1 {
		t.Errorf("node saw %d requests with retry disabled", f.count())
	}
}

func TestTheContextEndsARetryWait(t *testing.T) {
	c, _ := flaky(t, 100, 429, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Status(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v, want both the node's error and the deadline", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited %v past a 50ms deadline", time.Since(start))
	}
}

func TestBackoffIsCappedAndGrows(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: 300 * time.Millisecond}
	busy := &Error{Status: 503}
	d1, ok1 := p.delay(busy, 1)
	d3, ok3 := p.delay(busy, 3)
	if !ok1 || !ok3 {
		t.Fatal("a 503 was not retryable")
	}
	if d1 < 100*time.Millisecond || d1 > 150*time.Millisecond {
		t.Errorf("first backoff %v, want 100-150ms", d1)
	}
	if d3 != 300*time.Millisecond {
		t.Errorf("third backoff %v, want the 300ms cap", d3)
	}
}

// Recall only reads, so a 503 is retried even though it is a POST.
func TestRecallIsRetriedLikeARead(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"namespace":"n","count":0,"embedder":"e","memories":[]}`))
	})
	if _, err := c.Recall(context.Background(), RecallRequest{Query: "q"}); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Errorf("node saw %d requests, want 2", f.count())
	}
}

// Remember writes. A 503 does not prove the memory was not stored, so it is
// never repeated.
func TestRememberIsNeverRetried(t *testing.T) {
	c, f := replying(t, 503, `{"error":{"type":"internal_error","reason":"busy"}}`)
	if _, err := c.Remember(context.Background(), RememberRequest{Content: "x"}); err == nil {
		t.Fatal("a 503 was not an error")
	}
	if f.count() != 1 {
		t.Errorf("node saw %d requests, want exactly 1", f.count())
	}
}

// A restore writes over live data, and is a POST: never repeated by the client.
func TestRestoreIsNeverRetried(t *testing.T) {
	c, f := replying(t, 503, ``)
	if _, err := c.RestoreSnapshot(context.Background(), "r", "s", RestoreRequest{}); err == nil {
		t.Fatal("a 503 was not an error")
	}
	if f.count() != 1 {
		t.Errorf("node saw %d restores, want 1", f.count())
	}
}

// A merge is expensive. Being told to back off is not a reason to queue a
// second one.
func TestForceMergeIsNotRetried(t *testing.T) {
	c, f := replying(t, 503, ``)
	if _, err := c.ForceMerge(context.Background(), "docs", 1); err == nil {
		t.Fatal("a 503 was not an error")
	}
	if f.count() != 1 {
		t.Errorf("node saw %d merges, want 1", f.count())
	}
}
