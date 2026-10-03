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
	httpClient := client.OAuthConfig("").Client(ctx, tok)
	opts := append([]option.ClientOption{option.WithHTTPClient(httpClient)}, extra...)
	svc, err := drv.NewService(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("create drive service: %w", err)
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
