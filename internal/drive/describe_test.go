package drive

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"google.golang.org/api/googleapi"
)

func TestDescribe(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"quota", gerr(403, "storageQuotaExceeded"), "Drive storage quota exceeded"},
		{"rate 403", gerr(403, "rateLimitExceeded"), "Drive rate limit hit"},
		{"user rate", gerr(403, "userRateLimitExceeded"), "Drive rate limit hit"},
		{"429", gerr(429, ""), "Drive rate limit hit"},
		{"permission", gerr(403, "forbidden"), "permission denied by Drive"},
		{"404", gerr(404, "notFound"), "file or folder not found in Drive"},
		{"500", gerr(500, ""), "Drive is unavailable (HTTP 500)"},
		{"503 wrapped", fmt.Errorf("upload a.txt: %w", gerr(503, "")), "Drive is unavailable (HTTP 503)"},
		{"reset", fmt.Errorf("x: %w", syscall.ECONNRESET), "network problem"},
		{"not signed in", ErrNotSignedIn, "not signed in"},
		{"expired", fmt.Errorf("%w: rejected", ErrSessionExpired), "sign-in expired"},
		{"permission local", &fs.PathError{Op: "open", Path: "/r/a", Err: fs.ErrPermission}, "cannot read file: permission denied"},
		{"gone", &fs.PathError{Op: "open", Path: "/r/a", Err: fs.ErrNotExist}, "file is gone"},
		{"other", errors.New("boom"), "boom"},
		{"multi-line", errors.New("first\nsecond"), "first"},
		{"empty", errors.New(""), "unknown error"},
		{"other status", &googleapi.Error{Code: 400, Message: "bad request"}, (&googleapi.Error{Code: 400, Message: "bad request"}).Error()},
	}
	for _, c := range cases {
		if got := Describe(c.err); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDescribeCutsLongText(t *testing.T) {
	got := Describe(errors.New(strings.Repeat("x", 500)))
	if n := len([]rune(got)); n != maxDescribe+1 {
		t.Errorf("length = %d, want %d", n, maxDescribe+1)
	}
}
