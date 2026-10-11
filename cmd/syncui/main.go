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
	"time"

	"fyne.io/systray"

	"github.com/amitavroy/burrow/internal/store"
)

const pollEvery = 2 * time.Second

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
	sock := store.SocketPath()
	systray.SetTitle("Burrow")
	mStatus := systray.AddMenuItem("Starting…", "")
	mStatus.Disable()
	mToggle := systray.AddMenuItem("Pause uploads", "Stop starting new uploads; the queue keeps filling")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit tray", "Close this icon; syncd keeps running")

	shown := ""
	apply := func(v view) {
		if v.Kind != shown { // set the icon only when the state changes
			systray.SetIcon(trayIcon(v.Kind))
			shown = v.Kind
		}
		systray.SetTooltip(v.Tooltip)
		mStatus.SetTitle(v.StatusLine)
		mToggle.SetTitle(v.Toggle)
		if v.CanToggle {
			mToggle.Enable()
		} else {
			mToggle.Disable()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		cur := poll(ctx, sock, time.Now())
		apply(cur)
		tick := time.NewTicker(pollEvery)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				cur = poll(ctx, sock, time.Now())
			case <-mToggle.ClickedCh:
				cur = toggle(ctx, sock, cur, time.Now())
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
			apply(cur)
		}
	}()
}
