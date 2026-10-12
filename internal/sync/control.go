package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/amitavroy/burrow/internal/control"
)

// Control is the part of a running daemon that outside callers (the control
// API, later the tray app) can steer and read: pause and resume, and when the
// last job finished. The zero value is a running daemon that has not synced
// anything yet. It is safe for concurrent use.
type Control struct {
	paused   atomic.Bool
	lastSync atomic.Int64 // Unix nanoseconds of the last job that finished without an error; 0 = none yet

	// Optional, set before ServeControl. Level is the daemon's log level, which
	// POST /debug flips between debug and info; Errors feeds GET /errors.
	Level  *slog.LevelVar
	Errors func(context.Context) ([]control.ErrorLine, error)
}

// Pause stops workers from starting new jobs. Jobs in flight finish; the
// watcher keeps queueing, so nothing is lost. It is not remembered across restarts.
func (c *Control) Pause() { c.paused.Store(true) }

// Resume lets workers take jobs again (within one poll period, 1 s by default).
func (c *Control) Resume() { c.paused.Store(false) }

// Paused reports whether the daemon is paused. A nil Control never is.
func (c *Control) Paused() bool { return c != nil && c.paused.Load() }

// LastSync is when a job last finished without an error, or the zero time.
func (c *Control) LastSync() time.Time {
	if c == nil || c.lastSync.Load() == 0 {
		return time.Time{}
	}
	return time.Unix(0, c.lastSync.Load())
}

func (c *Control) touch() {
	if c != nil {
		c.lastSync.Store(time.Now().UnixNano())
	}
}

// ServeControl serves the control API on a Unix domain socket at sock until
// ctx is cancelled, then removes the socket. It returns once the socket is
// listening. The socket is readable and writable by its owner only, which is
// the access control: nothing else on the machine, and no web page, can reach it.
//
//	GET  /status  -> control.Status as JSON; info fills in what Control does not know
//	POST /pause   -> pause, replies with the new Status
//	POST /resume  -> resume, replies with the new Status
//	POST /debug   -> flip debug logging, replies with the new Status
//	GET  /errors  -> the current sync errors as JSON
//
// A socket file left by a dead process is replaced; callers hold the single
// instance lock, so no live daemon can own it.
func ServeControl(ctx context.Context, sock string, c *Control, info func(context.Context) (control.Status, error)) error {
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control socket: %w", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("control socket: %w", err)
	}

	status := func(w http.ResponseWriter, r *http.Request) {
		st, err := info(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		st.State = "running"
		if c.Paused() {
			st.State = "paused"
		}
		st.Debug = c != nil && c.Level != nil && c.Level.Level() <= slog.LevelDebug
		if t := c.LastSync(); !t.IsZero() {
			st.LastSync = t.UTC().Format(time.RFC3339)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", status)
	mux.HandleFunc("POST /pause", func(w http.ResponseWriter, r *http.Request) { c.Pause(); status(w, r) })
	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, r *http.Request) { c.Resume(); status(w, r) })

	mux.HandleFunc("POST /debug", func(w http.ResponseWriter, r *http.Request) {
		if c != nil && c.Level != nil {
			if c.Level.Level() <= slog.LevelDebug {
				c.Level.Set(slog.LevelInfo)
			} else {
				c.Level.Set(slog.LevelDebug)
			}
		}
		status(w, r)
	})
	mux.HandleFunc("GET /errors", func(w http.ResponseWriter, r *http.Request) {
		var errs []control.ErrorLine
		if c != nil && c.Errors != nil {
			var err error
			if errs, err = c.Errors(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(errs)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		safely("control.serve", func() { _ = srv.Serve(ln) })
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		_ = os.Remove(sock)
	}()
	return nil
}
