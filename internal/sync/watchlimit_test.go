package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeLimits(m map[string]string) func(string) string {
	return func(file string) string { return m[file] }
}

func TestWatchLimitErrorMessages(t *testing.T) {
	read := fakeLimits(map[string]string{watchLimitFile: "8192", instanceLimitFile: "128"})

	err := watchLimitErrorFor("linux", read, fmt.Errorf("add: %w", syscall.ENOSPC), "/data/vendor/x", 8192)
	if !errors.Is(err, ErrWatchLimit) {
		t.Fatalf("err = %v, want ErrWatchLimit", err)
	}
	for _, want := range []string{"/data/vendor/x", "after 8192 directories", "fs.inotify.max_user_watches=8192", "sudo sysctl fs.inotify.max_user_watches=524288", "/etc/sysctl.d/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q:\n%v", want, err)
		}
	}

	err = watchLimitErrorFor("linux", read, syscall.EMFILE, "/data/y", 3)
	if !errors.Is(err, ErrWatchLimit) || !strings.Contains(err.Error(), "fs.inotify.max_user_instances=128") || !strings.Contains(err.Error(), "/data/y") {
		t.Errorf("EMFILE message = %v", err)
	}
}

func TestWatchLimitErrorWithoutProcStillReadsWell(t *testing.T) {
	err := watchLimitErrorFor("linux", fakeLimits(nil), syscall.ENOSPC, "/d", 5)
	if !errors.Is(err, ErrWatchLimit) || strings.Contains(err.Error(), "(fs.inotify") || !strings.Contains(err.Error(), "after 5 directories") {
		t.Errorf("message = %v", err)
	}
}

func TestWatchLimitErrorLeavesOtherErrorsAlone(t *testing.T) {
	other := errors.New("permission denied")
	if got := watchLimitErrorFor("linux", fakeLimits(nil), other, "/d", 1); got != other {
		t.Errorf("got %v, want the same error back", got)
	}
	enospc := fmt.Errorf("x: %w", syscall.ENOSPC)
	if got := watchLimitErrorFor("darwin", fakeLimits(nil), enospc, "/d", 1); got != enospc {
		t.Errorf("on another OS ENOSPC means a full disk and must pass through, got %v", got)
	}
	if got := watchLimitErrorFor("linux", fakeLimits(nil), nil, "/d", 1); got != nil {
		t.Errorf("nil in, %v out", got)
	}
}

func TestNewWatcherStopsAtTheWatchLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the inotify limit message is Linux only")
	}
	root := t.TempDir()
	mkdirs(t, root, "a", "b", "c", "d")
	count := 0
	_, err := newWatcherWithHook(root, func(p string, add func(string) error) error {
		if count++; count == 3 {
			return syscall.ENOSPC
		}
		return add(p)
	})

	if !errors.Is(err, ErrWatchLimit) || !strings.Contains(err.Error(), "after 2 directories") {
		t.Fatalf("err = %v, want the watch limit error after 2 directories", err)
	}
}

// While running, folders that cannot be watched are reported once and the rest keeps working.
func TestWatcherReportsTheLimitOnceWhileRunning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the inotify limit message is Linux only")
	}
	root := t.TempDir()
	mkdirs(t, root, "old")
	w, err := newWatcherWithHook(root, func(p string, add func(string) error) error {
		if strings.Contains(filepath.Base(p), "new") {
			return syscall.ENOSPC
		}
		return add(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	s := &seen{t: t, w: w}

	for _, d := range []string{"new1", "new2", "new3"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "old", "f.txt"), "x") // the watched folder still works
	s.expect(ev(OpCreate, "old/f.txt"))

	var reported int
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case err := <-w.Errors():
			if errors.Is(err, ErrWatchLimit) {
				reported++
			}
		case <-s.w.Events():
		case <-deadline:
			break loop
		}
	}
	if reported != 1 {
		t.Errorf("watch limit reported %d times, want once", reported)
	}
}
