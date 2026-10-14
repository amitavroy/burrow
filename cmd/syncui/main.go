// Command syncui is the tray icon for a running `syncd run`: it shows whether
// syncing is running, paused or stopped, the queue length and the last sync,
// and pauses and resumes uploads. It is a separate process from the daemon
// (ADR-001): it only talks to the control socket, so closing or crashing it
// never stops syncing, and the daemon stays free of the GUI toolkit.
//
// On Windows build it with -ldflags "-H=windowsgui" so no console window opens.
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/amitavroy/burrow/internal/autostart"
	"github.com/amitavroy/burrow/internal/control"
	"github.com/amitavroy/burrow/internal/store"
)

const pollEvery = 2 * time.Second

func main() {
	lock, ok := acquire(lockPath())
	if !ok {
		return // a tray is already running; one icon is enough
	}
	if lock != nil {
		defer lock.Unlock()
	}
	registerAutostart(autostart.DefaultEnv(), selfPath(), os.TempDir())
	systray.Run(onReady, func() {})
}

func onReady() {
	sock := store.SocketPath()
	systray.SetTitle("Burrow")
	mStatus := systray.AddMenuItem("Starting…", "")
	mStatus.Disable()
	mAccount := systray.AddMenuItem("", "")
	mAccount.Disable()
	mSignIn := systray.AddMenuItem("Sign in…", "Opens your browser to sign in to Google")
	mSignOut := systray.AddMenuItem("Sign out", "Forget this Google account on this computer")
	mStart := systray.AddMenuItem("Start syncing", "Install and start the sync service (signing in does this too)")
	mToggle := systray.AddMenuItem("Pause uploads", "Stop starting new uploads; the queue keeps filling")
	systray.AddSeparator()
	mErrors := systray.AddMenuItem("No errors", "Files that failed to sync")
	var mErrLines [maxErrorItems]*systray.MenuItem
	for i := range mErrLines {
		mErrLines[i] = mErrors.AddSubMenuItem("", "")
		mErrLines[i].Disable()
		mErrLines[i].Hide()
	}
	mRoot := systray.AddMenuItem("Open sync folder", "The folder syncd watches; change it with `syncd service install --root`")
	mLogs := systray.AddMenuItem("Open logs folder", "")
	mDebug := systray.AddMenuItemCheckbox("Debug logging", "Log every step to the log file", false)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit tray", "Close this icon; syncd keeps running")

	shown, note := "", "" // note: why the last sign-in or start failed
	apply := func(v view) {
		email := store.LoadEmail()
		mAccount.SetTitle(accountLine(email))
		if email == "" {
			mSignIn.Show()
			mSignOut.Hide()
		} else {
			mSignIn.Hide()
			mSignOut.Show()
		}
		if v.Kind == kindStopped && email != "" {
			mStart.Show()
		} else {
			mStart.Hide()
		}
		if note != "" && v.Kind == kindStopped {
			v.StatusLine = note
		}
		if v.Kind != shown { // set the icon only when the state changes
			systray.SetIcon(trayIcon(v.Kind))
			shown = v.Kind
		}
		systray.SetTooltip(v.Tooltip)
		mStatus.SetTitle(v.StatusLine)
		mToggle.SetTitle(v.Toggle)
		if v.Debug {
			mDebug.Check()
		} else {
			mDebug.Uncheck()
		}
		if v.CanToggle {
			mToggle.Enable()
		} else {
			mToggle.Disable()
		}
	}

	showErrors := func(ctx context.Context) {
		errs, err := control.Errors(ctx, sock)
		if err != nil {
			errs = nil // daemon gone: the status line already says so
		}
		title, lines := errorMenu(errs)
		mErrors.SetTitle(title)
		for i, m := range mErrLines {
			if i < len(lines) {
				m.SetTitle(lines[i])
				m.Show()
			} else {
				m.Hide()
			}
		}
	}

	// Sign in and start can take a while (the browser, the service manager), so
	// they run off the loop and report back through done.
	done := make(chan string, 1)
	busy := false
	background := func(args ...string) {
		if busy {
			return
		}
		busy = true
		go func() { done <- runSyncd(args...) }()
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		cur := poll(ctx, sock, time.Now())
		apply(cur)
		showErrors(ctx)
		tick := time.NewTicker(pollEvery)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				cur = poll(ctx, sock, time.Now())
				showErrors(ctx)
			case <-mSignIn.ClickedCh:
				background("login")
			case <-mSignOut.ClickedCh:
				note = runSyncd("logout")
			case <-mStart.ClickedCh:
				background("service", "install")
			case note = <-done:
				busy = false
				cur = poll(ctx, sock, time.Now())
			case <-mToggle.ClickedCh:
				cur = toggle(ctx, sock, cur, time.Now())
			case <-mDebug.ClickedCh:
				cur = toggleDebug(ctx, sock, time.Now())
			case <-mRoot.ClickedCh:
				if cur.Root != "" {
					openFolder(cur.Root)
				}
			case <-mLogs.ClickedCh:
				openFolder(filepath.Dir(store.LogPath()))
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
			apply(cur)
		}
	}()
}

// openFolder shows dir in the system file manager. Failures are ignored: the
// tray has nowhere useful to report them.
func openFolder(dir string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
