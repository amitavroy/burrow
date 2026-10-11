package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

func gerr(code int, reason string) error {
	e := &googleapi.Error{Code: code}
	if reason != "" {
		e.Errors = []googleapi.ErrorItem{{Reason: reason}}
	}
	return e
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		ok   bool
	}{
		{"429", gerr(429, ""), true},
		{"500", gerr(500, ""), true},
		{"503", gerr(503, ""), true},
		{"403 rate", gerr(403, "rateLimitExceeded"), true},
		{"403 user rate", gerr(403, "userRateLimitExceeded"), true},
		{"403 sharing rate", gerr(403, "sharingRateLimitExceeded"), true},
		{"403 permission", gerr(403, "forbidden"), false},
		{"403 quota", gerr(403, "storageQuotaExceeded"), false},
		{"404", gerr(404, "notFound"), false},
		{"401", gerr(401, ""), false},
		{"invalid_grant", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"conn reset", fmt.Errorf("x: %w", syscall.ECONNRESET), true},
		{"conn refused", syscall.ECONNREFUSED, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"plain", errors.New("boom"), false},
	}
	for _, c := range cases {
		if _, ok := retryable(c.err); ok != c.ok {
			t.Errorf("%s: retryable = %v, want %v", c.name, ok, c.ok)
		}
	}
}

func TestRetryableRetryAfter(t *testing.T) {
	e := &googleapi.Error{Code: 429, Header: http.Header{"Retry-After": {"7"}}}
	if d, ok := retryable(e); !ok || d != 7*time.Second {
		t.Fatalf("got %v %v", d, ok)
	}
}

func TestBackoff(t *testing.T) {
	old := retryJitter
	defer func() { retryJitter = old }()
	retryJitter = func(max time.Duration) time.Duration { return max } // worst case: full ceiling
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got := backoff(i+1, 0); got != w*time.Second {
			t.Errorf("attempt %d: %v, want %vs", i+1, got, w)
		}
	}
	if got := backoff(1, 10*time.Second); got != 10*time.Second {
		t.Errorf("Retry-After ignored: %v", got)
	}
	retryJitter = old
	for range 100 {
		if d := backoff(3, 0); d < 0 || d > 4*time.Second {
			t.Fatalf("jitter out of bounds: %v", d)
		}
	}
}

func stubSleep(t *testing.T) *int {
	t.Helper()
	old := retrySleep
	t.Cleanup(func() { retrySleep = old })
	n := 0
	retrySleep = func(ctx context.Context, _ time.Duration) error { n++; return ctx.Err() }
	return &n
}

func TestWithRetry(t *testing.T) {
	sleeps := stubSleep(t)
	calls := 0
	err := withRetry(context.Background(), func() error {
		if calls++; calls < 3 {
			return gerr(429, "")
		}
		return nil
	})
	if err != nil || calls != 3 || *sleeps != 2 {
		t.Fatalf("err=%v calls=%d sleeps=%d", err, calls, *sleeps)
	}
}

func TestWithRetryExhausted(t *testing.T) {
	stubSleep(t)
	calls := 0
	err := withRetry(context.Background(), func() error { calls++; return gerr(500, "") })
	var ge *googleapi.Error
	if calls != retryAttempts || !errors.As(err, &ge) || ge.Code != 500 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWithRetryPermanent(t *testing.T) {
	stubSleep(t)
	calls := 0
	_ = withRetry(context.Background(), func() error { calls++; return gerr(403, "forbidden") })
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestWithRetryCancelDuringWait(t *testing.T) {
	stubSleep(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := withRetry(ctx, func() error { return gerr(503, "") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestRetrySleepStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := retrySleep(ctx, time.Minute); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v", err)
	}
}
