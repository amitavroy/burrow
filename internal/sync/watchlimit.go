package sync

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// ErrWatchLimit means the operating system refused another file watch: on
// Linux the per-user inotify limits (fs.inotify.max_user_watches or
// fs.inotify.max_user_instances).
var ErrWatchLimit = errors.New("inotify watch limit reached")

const (
	watchLimitFile    = "/proc/sys/fs/inotify/max_user_watches"
	instanceLimitFile = "/proc/sys/fs/inotify/max_user_instances"
)

// watchLimitError turns the error from adding a watch on dir into ErrWatchLimit
// with a message that says what to do, when it is the Linux inotify limit.
// watched is how many directories were being watched before. Anything else is
// returned unchanged.
func watchLimitError(err error, dir string, watched int) error {
	return watchLimitErrorFor(runtime.GOOS, readLimit, err, dir, watched)
}

func readLimit(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func watchLimitErrorFor(goos string, read func(file string) string, err error, dir string, watched int) error {
	if goos != "linux" || err == nil {
		return err
	}
	switch {
	case errors.Is(err, syscall.ENOSPC): // out of watches (on other systems it would mean a full disk)
		limit := ""
		if v := read(watchLimitFile); v != "" {
			limit = " (fs.inotify.max_user_watches=" + v + ")"
		}
		return fmt.Errorf("%w: cannot watch %s after %d directories%s\n"+
			"  raise it with:  sudo sysctl fs.inotify.max_user_watches=524288\n"+
			"  and keep it after a reboot with:  echo 'fs.inotify.max_user_watches=524288' | sudo tee /etc/sysctl.d/60-burrow.conf",
			ErrWatchLimit, dir, watched, limit)
	case errors.Is(err, syscall.EMFILE): // out of inotify instances
		limit := ""
		if v := read(instanceLimitFile); v != "" {
			limit = " (fs.inotify.max_user_instances=" + v + ")"
		}
		return fmt.Errorf("%w: too many inotify instances open%s while watching %s\n"+
			"  raise it with:  sudo sysctl fs.inotify.max_user_instances=512",
			ErrWatchLimit, limit, dir)
	}
	return err
}
