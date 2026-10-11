package drive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

const (
	retryAttempts = 5
	retryBase     = time.Second
	retryCap      = 30 * time.Second
)

// Package vars so tests run instantly and deterministically.
var (
	retrySleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	retryJitter = func(max time.Duration) time.Duration {
		return rand.N(max + 1)
	}
)

var rateLimitReasons = map[string]bool{
	"rateLimitExceeded":        true,
	"userRateLimitExceeded":    true,
	"sharingRateLimitExceeded": true,
}

// retryable reports whether err is worth retrying and, for a Drive reply, how
// long Retry-After asked us to wait (0 if absent).
func retryable(err error) (retryAfter time.Duration, ok bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return 0, false // sign-in problem, waiting will not fix it
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		switch {
		case ge.Code == 429 || ge.Code >= 500:
		case ge.Code == 403:
			found := false
			for _, e := range ge.Errors {
				found = found || rateLimitReasons[e.Reason]
			}
			if !found {
				return 0, false
			}
		default:
			return 0, false
		}
		if s, perr := strconv.Atoi(ge.Header.Get("Retry-After")); perr == nil && s > 0 {
			retryAfter = time.Duration(s) * time.Second
		}
		return retryAfter, true
	}
	return 0, isNetworkError(err)
}

// isNetworkError reports a transient network failure: a timeout, a reset or
// refused connection, or a stream cut short.
func isNetworkError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// backoff returns the wait before retry number attempt (1-based): full jitter
// under an exponential ceiling, raised to retryAfter when Drive asked for more.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	ceiling := retryCap
	if attempt-1 < 6 { // 1s<<5 = 32s already exceeds the cap
		ceiling = min(retryBase<<(attempt-1), retryCap)
	}
	return max(retryJitter(ceiling), retryAfter)
}

// withRetry runs op up to retryAttempts times, waiting between retryable
// failures. It returns the last error once attempts run out.
func withRetry(ctx context.Context, op func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		ra, ok := retryable(err)
		if !ok || attempt == retryAttempts {
			return err
		}
		d := backoff(attempt, ra)
		slog.Warn("drive retry", "attempt", attempt, "delay", d, "err", err)
		if serr := retrySleep(ctx, d); serr != nil {
			return serr
		}
	}
}
