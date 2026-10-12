package main

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/control"
	burrowsync "github.com/amitavroy/burrow/internal/sync"
)

var now = time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC)

func TestViewOf(t *testing.T) {
	tests := []struct {
		name       string
		st         control.Status
		err        error
		kind       string
		tooltip    string
		statusLine string
		toggle     string
		canToggle  bool
		next       string
	}{
		{name: "idle", st: control.Status{State: "running", LastSync: "2026-10-11T12:00:00Z"}, kind: kindRunning,
			tooltip: "Burrow: up to date", statusLine: "Running · 0 queued · last sync 30s ago", toggle: "Pause uploads", canToggle: true, next: "/pause"},
		{name: "busy", st: control.Status{State: "running", Queue: 3}, kind: kindRunning,
			tooltip: "Burrow: syncing, 3 queued", statusLine: "Running · 3 queued · no sync yet", toggle: "Pause uploads", canToggle: true, next: "/pause"},
		{name: "paused", st: control.Status{State: "paused", Queue: 5, LastSync: "2026-10-11T11:55:00Z"}, kind: kindPaused,
			tooltip: "Burrow: paused, 5 queued", statusLine: "Paused · 5 queued · last sync 5m ago", toggle: "Resume uploads", canToggle: true, next: "/resume"},
		{name: "not running", err: control.ErrNoDaemon, kind: kindStopped,
			tooltip: "Burrow: not running", statusLine: "syncd is not running", toggle: "Pause uploads"},
		{name: "unreachable", err: errors.New("boom"), kind: kindStopped,
			tooltip: "Burrow: cannot reach syncd", statusLine: "cannot reach syncd: boom", toggle: "Pause uploads"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viewOf(tt.st, tt.err, now)
			if v.Kind != tt.kind || v.Tooltip != tt.tooltip || v.StatusLine != tt.statusLine || v.Toggle != tt.toggle || v.CanToggle != tt.canToggle || v.next != tt.next {
				t.Errorf("view = %+v", v)
			}
		})
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{-time.Second: "0s ago", 12 * time.Second: "12s ago", 90 * time.Second: "1m ago", 3 * time.Hour: "3h ago"} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

// The ticket's checkpoint, without a display: the tray's view follows a real
// daemon control socket, and the toggle really pauses and resumes it.
func TestPollAndToggleAgainstTheControlSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "bs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, control.SocketFile)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if v := poll(ctx, sock, now); v.Kind != kindStopped || v.CanToggle {
		t.Fatalf("no daemon: view = %+v", v)
	}

	c := &burrowsync.Control{}
	info := func(context.Context) (control.Status, error) { return control.Status{Queue: 2, Root: "/r"}, nil }
	if err := burrowsync.ServeControl(ctx, sock, c, info); err != nil {
		t.Fatal(err)
	}

	v := poll(ctx, sock, now)
	if v.Kind != kindRunning || !v.CanToggle || v.Toggle != "Pause uploads" {
		t.Fatalf("running: view = %+v", v)
	}
	v = toggle(ctx, sock, v, now)
	if v.Kind != kindPaused || v.Toggle != "Resume uploads" || !c.Paused() {
		t.Fatalf("after pause: view = %+v, paused = %v", v, c.Paused())
	}
	v = toggle(ctx, sock, v, now)
	if v.Kind != kindRunning || c.Paused() {
		t.Fatalf("after resume: view = %+v, paused = %v", v, c.Paused())
	}
	// A disabled item does nothing.
	stopped := viewOf(control.Status{}, control.ErrNoDaemon, now)
	if got := toggle(ctx, sock, stopped, now); got != stopped || c.Paused() {
		t.Errorf("toggle on a stopped view changed something: %+v", got)
	}
}

func TestIcons(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range []string{kindRunning, kindPaused, kindStopped, "bogus"} {
		b := iconPNG(kind)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != iconSize || img.Bounds().Dy() != iconSize {
			t.Fatalf("%s: not a %dpx PNG: %v", kind, iconSize, err)
		}
		if _, _, _, a := img.At(iconSize/2, iconSize/2).RGBA(); a == 0 {
			t.Errorf("%s: centre is transparent", kind)
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%s: corner should be transparent", kind)
		}
		seen[string(b)] = true
	}
	if len(seen) != 3 { // three distinct states; an unknown kind looks like stopped
		t.Errorf("%d distinct icons, want 3", len(seen))
	}

	ico := iconICO(iconPNG(kindRunning))
	if !bytes.HasPrefix(ico, []byte{0, 0, 1, 0, 1, 0}) || !bytes.Contains(ico, []byte("\x89PNG")) {
		t.Errorf("not an ico with one PNG image: % x...", ico[:22])
	}
	if !strings.HasPrefix(string(trayIcon(kindPaused)), "\x89PNG") && !strings.HasPrefix(string(trayIcon(kindPaused)), "\x00\x00\x01\x00") {
		t.Error("trayIcon is neither PNG nor ICO")
	}
}

func TestErrorMenu(t *testing.T) {
	if title, lines := errorMenu(nil); title != "No errors" || len(lines) != 0 {
		t.Errorf("none: %q, %v", title, lines)
	}
	var errs []control.ErrorLine
	for i := 0; i < 7; i++ {
		errs = append(errs, control.ErrorLine{Path: "f" + string(rune('0'+i)), Message: "quota exceeded"})
	}
	title, lines := errorMenu(errs)
	if title != "Errors (7)" || len(lines) != maxErrorItems || lines[0] != "f0: quota exceeded" {
		t.Errorf("many: %q, %v", title, lines)
	}
}
