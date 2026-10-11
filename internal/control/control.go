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
	Version  string `json:"version"`
}

// Request sends one request ("GET /status", "POST /pause", "POST /resume") to
// the control socket at sock and returns the Status the daemon replies with.
// It returns ErrNoDaemon when nothing is listening.
func Request(ctx context.Context, sock, method, path string) (Status, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, "http://syncd"+path, nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return Status{}, ErrNoDaemon
		}
		return Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("daemon replied %s", resp.Status)
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return Status{}, fmt.Errorf("daemon reply: %w", err)
	}
	return st, nil
}
