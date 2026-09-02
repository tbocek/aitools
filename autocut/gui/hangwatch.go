package main

// A watchdog for the GTK thread.
//
// Twice the window stopped answering half a second into a speed effect, and
// twice the only thing to read afterwards was the shell's offer to kill it.
// A hang on the main loop leaves no log line, no error and no stack -- the one
// thread that would write them is the one that is stuck. So a heartbeat runs
// on the main loop, and a goroutine of its own watches it: when the beat is
// hangStall late, every goroutine's stack goes to a file beside the settings,
// hang-<time>.txt, and to stderr. A goroutine blocked in GStreamer is a
// goroutine in a system call to the runtime, so the dump is taken without
// the main thread's help, and the Go frames above the C call say which seek,
// which draw, which state change it never came back from.
//
// One dump per hang: a loop that had stalled for a minute would otherwise
// write the same stacks sixty times.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

const (
	hangBeat  = 200 * time.Millisecond // how often the main loop says it is alive
	hangStall = 3 * time.Second        // how late a beat has to be to count as a hang
)

// startHangWatch installs the heartbeat and the watcher. Called once the main
// loop exists; harmless before the window does.
func (a *App) startHangWatch() {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	glib.TimeoutAdd(uint(hangBeat/time.Millisecond), func() bool {
		last.Store(time.Now().UnixNano())
		return true
	})
	go func() {
		dumped := false
		for range time.Tick(hangBeat) {
			late := time.Since(time.Unix(0, last.Load()))
			if late < hangStall {
				dumped = false
				continue
			}
			if dumped {
				continue
			}
			dumped = true
			a.dumpHang(late)
		}
	}()
}

// dumpHang writes every goroutine's stack. Not through the log: the log is a
// widget on the thread that is stuck.
func (a *App) dumpHang(late time.Duration) {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	head := fmt.Sprintf("autocut: the GTK thread has not answered for %s -- every goroutine's stack follows\n\n", late.Round(100*time.Millisecond))
	fmt.Fprint(os.Stderr, head)
	os.Stderr.Write(buf[:n])
	dir := configDir()
	if dir == "" {
		return
	}
	p := filepath.Join(dir, "hang-"+time.Now().Format("0102-150405")+".txt")
	os.WriteFile(p, append([]byte(head), buf[:n]...), 0o600)
	fmt.Fprintf(os.Stderr, "\nautocut: written to %s\n", p)
}
