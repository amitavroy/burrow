package drive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// ErrSessionExpired means Google rejected the stored refresh token (revoked,
// expired or the account removed access). The user must run login again.
var ErrSessionExpired = errors.New("session expired")

// TokenFromRefresh builds a token that oauth2 will refresh on first use. A
// zero Expiry would mean "never expires", so the access token (which we don't
// have) would be sent as-is and never renewed.
func TokenFromRefresh(refreshToken string) *oauth2.Token {
	return &oauth2.Token{
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Minute),
	}
}

// Resume signs in from the stored refresh token, with no browser, and returns
// the account email. It returns ErrNotSignedIn when nothing is stored and
// ErrSessionExpired when Google rejects the token.
func Resume(ctx context.Context, client Client, store TokenStore, extra ...option.ClientOption) (string, error) {
	rt, err := store.Load()
	if err != nil {
		return "", err
	}
	addr, err := Email(ctx, client, TokenFromRefresh(rt), extra...)
	if err != nil {
		return "", sessionError(err)
	}
	return addr, nil
}

// sessionError maps Google rejecting the refresh token (invalid_grant) to
// ErrSessionExpired and passes any other error through unchanged.
func sessionError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
		return fmt.Errorf("%w: Google rejected the stored sign-in", ErrSessionExpired)
	}
	return err
}
