package main

// The run queue: what one press of ▶ turned into, in the order it will happen.
//
// Every long job on these pages is a list of small ones — chunks of frames to
// describe, blocks of transcript to fix, lines to speak, clips to encode — and
// each of them used to talk to the run bar in its own full sentence, filename
// and all: "[2026-08-08_19-59-00] describing 4/12 (t=180s)". Two of those
// joined with a dot is not a thing anyone reads at a glance, and it said the
// same as the log line under it.
//
// So the work is a queue. A job fills it, the runner picks tasks off the front,
// and the bar says one short line: which job, which of the run's jobs that is,
// what the task at the head is doing, and how far down the queue it has got.
// The filename is gone from it on purpose — the log is where that belongs.
//
// The queue is filled as work is found rather than all at once, because most of
// it cannot be counted before it is opened: how many windows a recording
// diarizes into depends on its length, and how many moments a session has is
// the model's decision. That is exactly why the bar's FRACTION is still not the
// queue's length. The two halves are weighted by how long the work TAKES (see
// prog and the plan comments at each step), and a total that grows under a
// fraction built from it would drag the bar backwards — which is the one thing
// a progress bar must never do.

import (
	"fmt"
	"math"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// The two tracks are the two halves of the bar. They are concurrent in Prepare
// (speech recognition on the GPU, frame extraction on the CPU) and sequential
// on Describe + Transcript, but the arithmetic is the same either way: each
// track reports its own absolute contribution and the bar shows the sum.
// Letting either write a raw fraction would make the bar bounce between them.
const (
	trackSTT    = 0
	trackFrames = 1
	// the same two tracks under the names the Describe + Transcript step uses
	// them for: its two jobs run one after the other, but each still owns half
	// the bar and its own line
	trackDescribe = trackSTT
	trackFix      = trackFrames
)

// qTrack is one half of the bar: the job on it, the work it has left, and what
// the task at the head of the queue is doing this second.
type qTrack struct {
	job        string // "describe", "speech", "narrate" — what this half is
	phase, of  int    // ...and which of the run's jobs it is, when there are two
	queued     int    // tasks known about, including the ones already taken
	taken      int    // how many have been picked up: the head's position
	what       string // what the head is doing, in two or three words
	kind       string // what a task of this queue is, when nothing else is said
	doneJob    bool   // the job finished; the other track may still be going
	everFilled bool   // a queue that was filled and emptied is not an unfilled one
}

// line is a track's whole contribution to the bar:
//
//	describe 1/2: chunk 4/12
//	transcript 2/2: fixing block 3/7
//	speech: recognising 2/3
//
// Every piece is dropped when it has nothing to say, so a job with one task and
// no phases is simply "thumbnail: drawing".
func (t qTrack) line() string {
	if t.doneJob {
		if t.job == "" {
			return ""
		}
		return t.job + " done"
	}
	head := t.job
	if t.of > 1 {
		head += fmt.Sprintf(" %d/%d", t.phase, t.of)
	}
	what := t.what
	if what == "" {
		what = t.kind
	}
	if t.everFilled && t.taken > 0 {
		pos := fmt.Sprintf("%d/%d", t.taken, max(t.queued, t.taken))
		if what == "" {
			what = pos
		} else {
			what += " " + pos
		}
	}
	switch {
	case head == "":
		return what
	case what == "":
		return head
	}
	return head + ": " + what
}

// ---- what a run tells the queue ---------------------------------------------

// qReset empties both halves. A run starts here: the tracks are summed, so last
// run's leftovers would be added to every reading this one takes. It also gives
// the bar back to whatever runs next, whole (see qPhase).
func (a *App) qReset() {
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progQ = [2]qTrack{}
	a.progBase, a.progShare = 0, 0
	a.progMu.Unlock()
	a.showProg()
}

// qPhase says where in the bar the jobs that come next are drawn, and clears
// the queue for them.
//
// A press that is one step's work never calls it: the two tracks ARE the bar
// and they add up to the whole of it. Prepare's ▶ is two steps back to
// back, and neither half knows the other exists -- each still says "half the
// bar each" and reports its own absolute contribution. So the run says where
// each half goes and the scaling happens here, rather than in the twenty places
// that move the needle.
//
// base is where the phase starts, share is how much of the bar it owns, and the
// two tracks split base between them: a phase that has reported nothing yet
// stands exactly where the one before it finished. A bar that drops back to
// zero halfway through one press is the thing a progress bar must never do.
func (a *App) qPhase(base, share float64) {
	a.progMu.Lock()
	a.progBase, a.progShare = base, share
	a.progQ = [2]qTrack{}
	a.progParts = [2]float64{base / 2, base / 2}
	a.progMu.Unlock()
	a.showProg()
}

// scaled maps a track's own fraction into the phase's slice of the bar. Share 0
// means no phase was set, which is the whole bar -- that is every step but
// Prepare, and it is also the headless App the tests build. Called with
// progMu held.
func (a *App) scaled(f float64) float64 {
	if a.progShare == 0 {
		return f
	}
	return a.progBase/2 + f*a.progShare
}

// qJob says which job now owns a track, and which of the run's jobs it is --
// "describe 1/2", then "transcript 2/2". Pass of = 0 for a run that is one job,
// or whose jobs are concurrent and named rather than numbered.
func (a *App) qJob(track int, job string, phase, of int) {
	a.progMu.Lock()
	a.progQ[track] = qTrack{job: job, phase: phase, of: of}
	a.progMu.Unlock()
	a.showProg()
}

// qPush queues n more tasks of one kind. Called again whenever more work is
// found: a run that could count all of its work in advance would not need a
// queue.
func (a *App) qPush(track, n int, kind string) {
	if n <= 0 {
		return
	}
	a.progMu.Lock()
	t := &a.progQ[track]
	t.queued += n
	t.everFilled = true
	if kind != "" {
		t.kind = kind
	}
	a.progMu.Unlock()
	a.showProg()
}

// qTake picks the next task up. It is called once per task whatever becomes of
// it -- work already on disk from an earlier run is a task this run is done
// with, and a queue that skipped those would stall at the position where the
// resume started.
func (a *App) qTake(track int) {
	a.progMu.Lock()
	t := &a.progQ[track]
	t.taken++
	t.what = ""
	a.progMu.Unlock()
	a.showProg()
}

// qDone ends a track's job: it keeps the half of the bar it earned and stops
// saying anything but its name, so the line belongs to whatever is still
// running.
func (a *App) qDone(track int, f float64) {
	a.progMu.Lock()
	a.progParts[track] = a.scaled(f)
	a.progQ[track].doneJob = true
	a.progMu.Unlock()
	a.showProg()
}

// prog is how far this track has got and what its current task is doing. The
// fraction is the track's absolute contribution, as it has always been; the
// text is two or three words with no filename and no count in it -- the queue
// supplies the count, and the log has the name.
//
// An empty format says nothing of its own, and the task falls back to the kind
// of thing it is ("chunk 4/12") -- which is all the many calls that do nothing
// but move the needle ever had to say.
func (a *App) prog(track int, f float64, format string, args ...any) {
	txt := fmt.Sprintf(format, args...)
	a.progMu.Lock()
	a.progParts[track] = a.scaled(f)
	a.progQ[track].what = txt
	a.progMu.Unlock()
	a.showProg()
}

// pulseUntilCounted keeps the bar moving while a model thinks. The LLM calls
// have nothing countable in them, so the bar pulses until something with real
// news -- the thumbnail's first drawing fraction -- takes the needle. What
// stops it is that fraction rather than a flag set from the goroutine: Pulse
// and SetFraction drive the same needle, so the one that lasts has to be the
// one with news -- and reading progParts under its mutex is also the only way
// to ask this question from the GUI thread without racing the runner.
func (a *App) pulseUntilCounted() {
	glib.TimeoutAdd(150, func() bool {
		if !a.running {
			return false
		}
		a.progMu.Lock()
		counted := a.progParts[trackSTT] > 0
		a.progMu.Unlock()
		if counted {
			return false
		}
		a.progress.Pulse()
		return true
	})
}

// showProg puts the two halves on the bar: the fractions summed, the lines
// joined. Callers are worker goroutines, so the widget is touched on the GUI
// thread and nowhere else.
func (a *App) showProg() {
	a.progMu.Lock()
	total := math.Max(0, math.Min(1, a.progParts[0]+a.progParts[1]))
	text, tip := progLine(a.progQ)
	a.progMu.Unlock()
	if a.progress == nil {
		return // a headless App under the tests
	}
	glib.IdleAdd(func() {
		a.progress.SetFraction(total)
		a.progress.SetText(text)
		if tip != "" {
			// a run with nothing queued yet leaves the standing tooltip -- the
			// one that says how to read the bar -- rather than blanking it
			a.progress.SetTooltipText(tip)
		}
	})
}

// progLine is the bar's text and its tooltip. The text is the short one: at
// most two jobs, joined, each of them a few words. The tooltip is where the
// counting is spelled out, for the one moment anyone wants it.
func progLine(q [2]qTrack) (text, tip string) {
	var lines, tips []string
	for _, t := range q {
		if s := t.line(); s != "" {
			lines = append(lines, s)
		}
		if t.job == "" || !t.everFilled {
			continue
		}
		switch left := max(t.queued, t.taken) - t.taken; {
		case t.doneJob:
			tips = append(tips, fmt.Sprintf("%s: %d task(s), all done", t.job, t.taken))
		case left == 0:
			tips = append(tips, fmt.Sprintf("%s: task %d of %d, none waiting",
				t.job, t.taken, max(t.queued, t.taken)))
		default:
			tips = append(tips, fmt.Sprintf("%s: task %d of %d, %d waiting",
				t.job, t.taken, t.queued, left))
		}
	}
	return strings.Join(lines, "  ·  "), strings.Join(tips, "\n")
}
