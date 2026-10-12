// Package control is the client side of the daemon's control API (the server
// is sync.ServeControl): what a running `syncd run` reports and how to ask it
// to pause or resume. It is separate from internal/sync so that the tray app
// can talk to the daemon without linking the sync engine and the Drive client.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

// SocketFile is the name of the control socket, next to the state database.
const SocketFile = "syncd.sock"

// ErrNoDaemon means no syncd run is listening on the control socket.
var ErrNoDaemon = errors.New("syncd is not running")

// Status is what GET /status returns.
type Status struct {
	State    string `json:"state"`               // "running" or "paused"
	Queue    int    `json:"queue"`               // jobs still to do (pending or running)
	LastSync string `json:"last_sync,omitempty"` // RFC 3339, UTC; empty until a job has finished
	Root     string `json:"root"`
	Debug    bool   `json:"debug"` // debug logging is on
	Version  string `json:"version"`
}

// ErrorLine is one current sync error, as GET /errors returns it.
type ErrorLine struct {
	Path     string `json:"path"`
	Message  string `json:"message"`
	Attempts int    `json:"attempts"`
}

// Request sends one request ("GET /status", "POST /pause", "POST /resume",
// "POST /debug") to the control socket at sock and returns the Status the
// daemon replies with. It returns ErrNoDaemon when nothing is listening.
func Request(ctx context.Context, sock, method, path string) (Status, error) {
	var st Status
	err := do(ctx, sock, method, path, &st)
	return st, err
}

// Errors returns the daemon's current sync errors, newest first.
func Errors(ctx context.Context, sock string) ([]ErrorLine, error) {
	var out []ErrorLine
	err := do(ctx, sock, "GET", "/errors", &out)
	return out, err
}

func do(ctx context.Context, sock, method, path string, out any) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, "http://syncd"+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return ErrNoDaemon
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon replied %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("daemon reply: %w", err)
	}
	return nil
}
