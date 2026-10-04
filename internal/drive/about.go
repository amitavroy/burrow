package drive

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/oauth2"
	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Email returns the signed-in account's email. It uses Drive's about.get
// because the userinfo endpoint would need an extra scope beyond drive.file.
// extra options (e.g. option.WithEndpoint) are for tests.
func Email(ctx context.Context, client Client, tok *oauth2.Token, extra ...option.ClientOption) (string, error) {
	return email(ctx, client.OAuthConfig(""), tok, extra...)
}

// newService builds a Drive service that signs requests with tok and refreshes
// it through cfg. extra options (e.g. option.WithEndpoint) are for tests.
func newService(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token, extra ...option.ClientOption) (*drv.Service, error) {
	opts := append([]option.ClientOption{option.WithHTTPClient(cfg.Client(ctx, tok))}, extra...)
	svc, err := drv.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return svc, nil
}

// email is Email with an explicit oauth2 config, so tests can point token
// refreshes at a fake endpoint.
func email(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token, extra ...option.ClientOption) (string, error) {
	svc, err := newService(ctx, cfg, tok, extra...)
	if err != nil {
		return "", err
	}
	about, err := svc.About.Get().Fields("user(emailAddress)").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("fetch account: %w", err)
	}
	if about.User == nil || about.User.EmailAddress == "" {
		return "", errors.New("Drive did not return an email address")
	}
	return about.User.EmailAddress, nil
}
