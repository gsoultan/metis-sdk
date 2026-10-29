package metis

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy says how often the client sends a read again after it got no
// answer, or an answer saying the failure may pass: 429, 500, 502, 503 or 504.
//
// Only reads (GET) are retried. A start, a completion or any other write is
// sent once, whatever the policy says: the server has no idempotency key for
// them, so a write whose answer was lost may well have happened, and sending
// it again could do it twice. Look before acting again instead: find the
// instance by its business key, or read the task to see if it is still open.
//
// The zero value retries nothing, which is the client's default.
type RetryPolicy struct {
	// Attempts is how many times a read is sent in all, the first included.
	// Below 2 means it is sent once.
	Attempts int
	// Base is the wait before the first retry. Each retry after it waits
	// twice as long as the one before, less a random part of up to half, so
	// that many clients failing together do not come back together. Zero
	// means 200 milliseconds.
	Base time.Duration
	// Max caps one wait, a Retry-After the server asked for included. Zero
	// means 30 seconds.
	Max time.Duration
}

// WithRetry sends reads again as the policy says. See [RetryPolicy] for what
// is retried and what never is.
//
//	client := metis.NewClient(url, metis.WithRetry(metis.RetryPolicy{Attempts: 4}))
func WithRetry(p RetryPolicy) Option {
	return func(c *Client) { c.retry = p }
}

// transportError is a request that got no answer from the server: the
// connection failed, or the answer was cut off. Its message is the error it
// wraps, and errors.Is and errors.As see through it.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// retryable reports whether err may not be there a second time. A failure
// that came from the caller's own context is final.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if _, ok := errors.AsType[*transportError](err); ok {
		return true
	}
	return statusIs(err,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	)
}

// delay is how long to wait before retry number attempt, the first being 1.
// A Retry-After the server sent wins over the backoff, up to Max.
func (p RetryPolicy) delay(attempt int, err error) time.Duration {
	base := p.Base
	if base <= 0 {
		base = 200 * time.Millisecond
	}
	ceiling := p.Max
	if ceiling <= 0 {
		ceiling = 30 * time.Second
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.RetryAfter > 0 {
		return min(apiErr.RetryAfter, ceiling)
	}
	// Shifting past the width of a Duration wraps to nonsense, so stop
	// doubling once the ceiling is reached.
	wait := base
	for range attempt - 1 {
		if wait >= ceiling {
			break
		}
		wait *= 2
	}
	wait = min(wait, ceiling)
	return wait - rand.N(wait/2+1)
}

// retryAfter reads a Retry-After header, which is either a number of seconds
// or an HTTP date. Anything else, or a time already past, is zero.
func retryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		// A day is far past any Max; the cap keeps the multiplication from
		// overflowing on a header of absurd size.
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	at, err := http.ParseTime(header)
	if err != nil || !at.After(now) {
		return 0
	}
	return at.Sub(now)
}
