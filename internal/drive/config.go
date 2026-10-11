package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	drv "google.golang.org/api/drive/v3"
)

const (
	// ConfigVersion is the settings version this build reads and writes.
	ConfigVersion = 1

	configName    = "config.json"
	tagRole       = "burrow_role" // marks the settings file; not rel_path, so a user's own config.json at the root is never mistaken for it
	roleConfig    = "config"
	maxConfigSize = 1 << 20
)

// ErrConfigTooNew means config.json in Drive was written by a newer syncd.
var ErrConfigTooNew = errors.New("config.json was written by a newer syncd")

// Config is the small settings file shared through Drive: no absolute paths,
// nothing machine specific.
type Config struct {
	Version int `json:"version"`
	// Ignore are gitignore-style rules applied on every machine, after the
	// defaults and before the local .syncignore.
	Ignore []string `json:"ignore"`
}

// ReadConfig returns the settings stored in config.json directly under MySync.
// With no such file it returns the empty Config{Version: ConfigVersion} and
// creates nothing. A file from a newer version is ErrConfigTooNew. Sign-in
// errors are as for Upload.
func (u *Uploader) ReadConfig(ctx context.Context) (Config, error) {
	var cfg Config
	err := withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		f, err := u.findConfig(ctx)
		if err != nil {
			return sessionError(err)
		}
		if f == nil {
			cfg = Config{Version: ConfigVersion}
			return nil
		}
		resp, err := u.svc.Files.Get(f.Id).Context(ctx).Download()
		if err != nil {
			return sessionError(fmt.Errorf("read %s: %w", configName, err))
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigSize+1))
		if err != nil {
			return fmt.Errorf("read %s: %w", configName, err)
		}
		if len(b) > maxConfigSize {
			return fmt.Errorf("%s is larger than %d bytes", configName, maxConfigSize)
		}
		cfg = Config{}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("parse %s: %w", configName, err)
		}
		if cfg.Version > ConfigVersion {
			return fmt.Errorf("%w (version %d, this one reads %d)", ErrConfigTooNew, cfg.Version, ConfigVersion)
		}
		cfg.Version = ConfigVersion
		return nil
	})
	return cfg, err
}

// WriteConfig stores cfg as config.json directly under MySync, replacing the
// content when the file exists (same Drive ID) and creating it otherwise.
// ponytail: read-modify-write without a conditional update; two machines
// editing at the same moment can lose one edit. Use headRevisionId checks if
// that ever matters.
func (u *Uploader) WriteConfig(ctx context.Context, cfg Config) error {
	cfg.Version = ConfigVersion
	if cfg.Ignore == nil {
		cfg.Ignore = []string{}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		f, err := u.findConfig(ctx)
		if err != nil {
			return sessionError(err)
		}
		media := bytes.NewReader(b)
		if f != nil {
			_, err = u.svc.Files.Update(f.Id, &drv.File{}).Media(media).Fields("id").Context(ctx).Do()
		} else {
			_, err = u.svc.Files.Create(&drv.File{
				Name:          configName,
				Parents:       []string{u.rootID},
				MimeType:      "application/json",
				AppProperties: map[string]string{tagRole: roleConfig},
			}).Media(media).Fields("id").Context(ctx).Do()
		}
		if err != nil {
			return sessionError(fmt.Errorf("write %s: %w", configName, err))
		}
		return nil
	})
}

// findConfig returns the oldest live settings file directly under MySync, or nil.
func (u *Uploader) findConfig(ctx context.Context) (*drv.File, error) {
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and '%s' in parents and trashed = false",
		tagRole, roleConfig, u.rootID)
	f, err := oldestMatch(ctx, u.svc, q, "id")
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", configName, err)
	}
	return f, nil
}
