package sync

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

// testPanic is a test seam: when set, safely calls it with its name before
// running fn, so a test can force a panic at a named place. Nil in production.
var testPanic atomic.Pointer[func(where string)]

// safely runs fn and recovers a panic in it. The panic is logged at error
// level with the goroutine name, the panic value, the stack and attrs, and
// safely reports that it happened. Long-lived loops wrap one iteration (one
// event, one job) in it, so a bug costs that item and not the daemon.
func safely(name string, fn func(), attrs ...any) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			slog.Error("panic recovered", append([]any{
				"goroutine", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()),
			}, attrs...)...)
		}
	}()
	if f := testPanic.Load(); f != nil {
		(*f)(name)
	}
	fn()
	return false
}
