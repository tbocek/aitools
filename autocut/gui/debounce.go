package main

import "github.com/diamondburned/gotk4/pkg/glib/v2"

// editWait is how long a burst of typing has to stop before the work behind it
// runs. Long enough that a sentence is one run rather than forty, short enough
// that it has happened by the time you have looked up from the keyboard.
const editWait = 400

// debounce collects a burst of calls that all mean the same thing into one run
// of the work. Typing is exactly such a burst: every keystroke in a sentence
// asks for the same file to be written and the same folder to be counted, and
// only the last one is worth doing.
//
// The wait is restarted by counting rather than by cancelling the armed timer.
// A glib source has to be removed on the thread that added it, and a removal
// that is missed -- or one that lands after the handle has been handed out
// again -- fires at something that is gone. A stale timer that wakes up, finds
// a newer generation and returns costs one comparison and cannot go wrong.
type debounce struct {
	ms  uint // 0 is editWait, so a plain field needs no construction
	gen int
	// what is waiting to run, kept so it can be run early instead of waited
	// out (flush). nil when nothing is owed.
	owed func()
	// glib's timer, replaced in tests by a clock the test winds itself
	arm func(uint, func() bool)
}

// call runs f once, a beat after the LAST call of the burst it belongs to.
func (d *debounce) call(f func()) {
	d.gen++
	d.owed = f
	want := d.gen
	ms, arm := d.ms, d.arm
	if ms == 0 {
		ms = editWait
	}
	if arm == nil {
		arm = func(ms uint, fn func() bool) { glib.TimeoutAdd(ms, fn) }
	}
	arm(ms, func() bool {
		if d.gen == want { // no newer keystroke came in behind this one
			d.owed = nil
			f()
		}
		return false // one shot; the next call arms the next one
	})
}

// flush runs what is owed right now and leaves nothing armed behind it, for the
// moments where waiting is not allowed: leaving the page, closing the window,
// or starting the run that reads the file being written. Nothing owed is a
// no-op, so it is safe to put it wherever it belongs rather than only where
// something is known to be pending.
func (d *debounce) flush() {
	f := d.owed
	if f == nil {
		return
	}
	d.gen++ // every armed timer is stale from here
	d.owed = nil
	f()
}

// pending is whether work is owed. For the callers that have to know whether
// what is on screen has reached the disk yet.
func (d *debounce) pending() bool { return d.owed != nil }
