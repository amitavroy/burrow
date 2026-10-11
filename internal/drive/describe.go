package drive

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"google.golang.org/api/googleapi"
)

const maxDescribe = 200

// Describe turns an upload error into a short readable phrase for the user
// (see sync_errors). It never returns an empty string, never spans lines, and
// shows nothing a log line would not: no tokens, headers or request bodies.
func Describe(err error) string {
	var ge *googleapi.Error
	switch {
	case errors.Is(err, ErrNotSignedIn):
		return "not signed in"
	case errors.Is(err, ErrSessionExpired):
		return "sign-in expired"
	case errors.As(err, &ge):
		return describeDrive(ge)
	case isNetworkError(err):
		return "network problem"
	case errors.Is(err, fs.ErrPermission):
		return "cannot read file: permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "file is gone"
	}
	return oneLine(err.Error())
}

func describeDrive(ge *googleapi.Error) string {
	reasons := map[string]bool{}
	for _, e := range ge.Errors {
		reasons[e.Reason] = true
	}
	switch {
	case reasons["storageQuotaExceeded"]:
		return "Drive storage quota exceeded"
	case ge.Code == http.StatusTooManyRequests, rateLimited(reasons):
		return "Drive rate limit hit"
	case ge.Code == http.StatusForbidden:
		return "permission denied by Drive"
	case ge.Code == http.StatusNotFound:
		return "file or folder not found in Drive"
	case ge.Code >= 500:
		return fmt.Sprintf("Drive is unavailable (HTTP %d)", ge.Code)
	}
	return oneLine(ge.Error())
}

func rateLimited(reasons map[string]bool) bool {
	for r := range rateLimitReasons {
		if reasons[r] {
			return true
		}
	}
	return false
}

// oneLine keeps the first line of s, cut to maxDescribe characters.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxDescribe {
		s = string(r[:maxDescribe]) + "…"
	}
	if s == "" {
		return "unknown error"
	}
	return s
}
