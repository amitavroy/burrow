package sync

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/control"
)

// shortSock returns a socket path short enough for the sun_path limit.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

func serve(t *testing.T, sock string, c *Control, queue int) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	info := func(context.Context) (control.Status, error) {
		return control.Status{Queue: queue, Root: "/r", Version: "v"}, nil
	}
	if err := ServeControl(ctx, sock, c, info); err != nil {
		t.Fatal(err)
	}
	return cancel
}

func TestControlAPIStatusPauseResume(t *testing.T) {
	sock, c := shortSock(t), &Control{}
	serve(t, sock, c, 3)
	ctx := context.Background()

	st, err := control.Request(ctx, sock, "GET", "/status")
	if err != nil || st.State != "running" || st.Queue != 3 || st.Root != "/r" || st.LastSync != "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if st, err = control.Request(ctx, sock, "POST", "/pause"); err != nil || st.State != "paused" || !c.Paused() {
		t.Errorf("pause = %+v, %v, paused %v", st, err, c.Paused())
	}
	c.touch()
	if st, _ = control.Request(ctx, sock, "GET", "/status"); st.State != "paused" || st.LastSync == "" {
		t.Errorf("status while paused = %+v", st)
	}
	if st, err = control.Request(ctx, sock, "POST", "/resume"); err != nil || st.State != "running" || c.Paused() {
		t.Errorf("resume = %+v, %v", st, err)
	}
	// Wrong method or path is refused, and changes nothing.
	if _, err := control.Request(ctx, sock, "GET", "/pause"); err == nil || c.Paused() {
		t.Errorf("GET /pause: err = %v, paused = %v", err, c.Paused())
	}
	if _, err := control.Request(ctx, sock, "GET", "/nope"); err == nil {
		t.Error("unknown path should fail")
	}
}

func TestControlSocketIsPrivateAndCleanedUp(t *testing.T) {
	sock := shortSock(t)
	cancel := serve(t, sock, &Control{}, 0)

	if st, err := os.Stat(sock); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("socket: %v, %v; want mode 0600", st, err)
	}
	cancel()
	eventually(t, "socket removed", func() bool { _, err := os.Stat(sock); return err != nil })
}

func TestControlReplacesAStaleSocketAndReportsNoDaemon(t *testing.T) {
	sock := shortSock(t)
	// A crashed daemon leaves its socket file behind: connecting is refused.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := control.Request(context.Background(), sock, "GET", "/status"); !errors.Is(err, control.ErrNoDaemon) {
		t.Errorf("stale socket: err = %v, want control.ErrNoDaemon", err)
	}
	if _, err := control.Request(context.Background(), filepath.Join(filepath.Dir(sock), "none"), "GET", "/status"); !errors.Is(err, control.ErrNoDaemon) {
		t.Errorf("no socket: err = %v, want control.ErrNoDaemon", err)
	}

	serve(t, sock, &Control{}, 1) // replaces the stale file
	if st, err := control.Request(context.Background(), sock, "GET", "/status"); err != nil || st.Queue != 1 {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// The ticket's pause: no new upload starts while paused, the file waits in the
// queue, and resuming uploads it.
func TestRunPauseStopsUploadsAndResumeContinues(t *testing.T) {
	root, repo, fd, c := t.TempDir(), openRepo(t), &fakeDrive{}, &Control{}
	startRun(t, root, repo, fd, RunOptions{Control: c})
	c.Pause()

	write(t, filepath.Join(root, "a.txt"), "alpha")

	eventually(t, "queued", func() bool { n, _ := repo.CountJobs(context.Background()); return n == 1 })
	stays(t, "no upload while paused", 500*time.Millisecond, func() bool { return fd.total() == 0 })
	if !c.LastSync().IsZero() {
		t.Error("LastSync set before any job finished")
	}

	c.Resume()

	eventually(t, "upload after resume", func() bool { return fd.count("a.txt") == 1 })
	eventually(t, "last sync", func() bool { return !c.LastSync().IsZero() })
	eventually(t, "queue drained", func() bool { n, _ := repo.CountJobs(context.Background()); return n == 0 })
}
