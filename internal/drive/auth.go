package drive

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// DefaultLoginTimeout bounds how long Login waits for the browser redirect.
const DefaultLoginTimeout = 2 * time.Minute

// LoginOptions tunes Login. The zero value is production behaviour.
type LoginOptions struct {
	// OpenBrowser opens the auth URL. Defaults to the OS opener.
	OpenBrowser func(url string) error
	// Out receives the auth URL and hints. Defaults to io.Discard.
	Out io.Writer
	// Timeout overrides DefaultLoginTimeout.
	Timeout time.Duration
	// Endpoint overrides Google's OAuth endpoint (tests).
	Endpoint *oauth2.Endpoint
}

// OAuthConfig builds the oauth2 config for the shipped client.
func (c Client) OAuthConfig(redirectURL string) *oauth2.Config {
	endpoint := google.Endpoint
	if c.Endpoint != nil {
		endpoint = *c.Endpoint
	}
	return &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		Endpoint:     endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{ScopeDriveFile},
	}
}

type callbackResult struct {
	code string
	err  error
}

// Login runs the loopback + PKCE flow and returns the resulting token.
// The token is returned to the caller and never logged.
func Login(ctx context.Context, client Client, opts LoginOptions) (*oauth2.Token, error) {
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultLoginTimeout
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start loopback listener: %w", err)
	}
	defer ln.Close()

	cfg := client.OAuthConfig(fmt.Sprintf("http://%s/callback", ln.Addr().String()))
	if opts.Endpoint != nil {
		cfg.Endpoint = *opts.Endpoint
	}

	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res callbackResult
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			res.err = errors.New("state mismatch in OAuth callback")
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization failed: %s", q.Get("error"))
		case q.Get("code") == "":
			res.err = errors.New("OAuth callback had no code")
		default:
			res.code = q.Get("code")
		}
		if res.err != nil {
			http.Error(w, "Sign-in failed. You can close this tab.", http.StatusBadRequest)
		} else {
			fmt.Fprintln(w, "Signed in. You can close this tab and return to the terminal.")
		}
		select {
		case results <- res:
		default: // only the first callback counts
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	)
	fmt.Fprintf(opts.Out, "Opening your browser to sign in. If it does not open, visit:\n%s\n", authURL)
	if err := opts.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(opts.Out, "Could not open the browser (%v); open the URL above manually.\n", err)
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("exchange code: %w", err)
		}
		return tok, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("sign-in not completed: %w", ctx.Err())
	}
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
