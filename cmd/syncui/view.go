package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amitavroy/burrow/internal/control"
)

// Icon kinds. The state is also spelled out in the tooltip and the menu, so no
// one has to tell the colors apart.
const (
	kindRunning = "running" // green
	kindPaused  = "paused"  // amber
	kindStopped = "stopped" // gray: syncd is not running, or not reachable
)

// view is everything the tray shows, worked out from one poll of the daemon.
type view struct {
	Kind       string
	Tooltip    string
	StatusLine string // the first, disabled menu line
	Toggle     string // label of the pause/resume item
	CanToggle  bool
	Debug      bool   // debug logging is on
	Root       string // sync root, for "Open sync folder"
	next       string // control path the toggle item sends: "/pause" or "/resume"
}

// viewOf turns the outcome of a status request into what the tray shows.
func viewOf(st control.Status, err error, now time.Time) view {
	switch {
	case errors.Is(err, control.ErrNoDaemon):
		return view{Kind: kindStopped, Tooltip: "Burrow: not running", StatusLine: "syncd is not running", Toggle: "Pause uploads"}
	case err != nil:
		return view{Kind: kindStopped, Tooltip: "Burrow: cannot reach syncd", StatusLine: "cannot reach syncd: " + err.Error(), Toggle: "Pause uploads"}
	}
	debug, root := st.Debug, st.Root
	queue := fmt.Sprintf("%d queued", st.Queue)
	last := "no sync yet"
	if t, perr := time.Parse(time.RFC3339, st.LastSync); perr == nil {
		last = "last sync " + ago(now.Sub(t))
	}
	if st.State == "paused" {
		return view{
			Kind: kindPaused, Tooltip: "Burrow: paused, " + queue,
			StatusLine: "Paused · " + queue + " · " + last,
			Toggle:     "Resume uploads", CanToggle: true, next: "/resume", Debug: debug, Root: root,
		}
	}
	tip := "Burrow: up to date"
	if st.Queue > 0 {
		tip = "Burrow: syncing, " + queue
	}
	return view{
		Kind: kindRunning, Tooltip: tip,
		StatusLine: "Running · " + queue + " · " + last,
		Toggle:     "Pause uploads", CanToggle: true, next: "/pause", Debug: debug, Root: root,
	}
}

// ago renders a duration as "12s ago", "5m ago" or "3h ago".
func ago(d time.Duration) string {
	switch {
	case d < 0:
		d = 0
		fallthrough
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// poll asks the daemon for its status and returns the view.
func poll(ctx context.Context, sock string, now time.Time) view {
	st, err := control.Request(ctx, sock, "GET", "/status")
	return viewOf(st, err, now)
}

// toggle sends the pause or resume that v's menu item stands for and returns
// the view after it. When the daemon cannot be reached the view says so.
func toggle(ctx context.Context, sock string, v view, now time.Time) view {
	if !v.CanToggle {
		return v
	}
	st, err := control.Request(ctx, sock, "POST", v.next)
	return viewOf(st, err, now)
}

// maxErrorItems is how many errors the Errors submenu lists; the title carries the full count.
const maxErrorItems = 5

// errorMenu renders the sync errors as the submenu title and one line each
// (at most maxErrorItems, newest first).
func errorMenu(errs []control.ErrorLine) (title string, lines []string) {
	if len(errs) == 0 {
		return "No errors", nil
	}
	title = fmt.Sprintf("Errors (%d)", len(errs))
	for _, e := range errs[:min(len(errs), maxErrorItems)] {
		lines = append(lines, e.Path+": "+e.Message)
	}
	return title, lines
}

// toggleDebug flips debug logging in the daemon and returns the view after it.
func toggleDebug(ctx context.Context, sock string, now time.Time) view {
	st, err := control.Request(ctx, sock, "POST", "/debug")
	return viewOf(st, err, now)
}
