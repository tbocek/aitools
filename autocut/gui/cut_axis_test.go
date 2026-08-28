package main

// What the x-axis MEANS.
//
// It used to mean "the files, one after another": each recording laid down
// after the last with a fixed hole between them, and every x measured from its
// own file's origin. That is indistinguishable from a clock right up until two
// cameras roll at the same time, and then it is nonsense -- the same minute
// filmed twice is drawn as two minutes, and session-second 3:00 is at two
// places at once. Everything that has to line up with something else (the
// sound lanes, the effects lane, the green) lines up with a different story.
//
// So the axis is session time, and what is drawn is the union of what the
// cameras covered: the filmed runs. Time nobody filmed is collapsed to the one
// hole width, because an hour of nothing between two clips is an hour of dead
// pixels to scroll past.
//
// The load-bearing claim, and the first thing pinned here: for one recording,
// or several that do not overlap, this is the OLD layout to the pixel.

import (
	"math"
	"testing"
)

// axisEd is an editor at 4 px per session second, laid out over vids.
func axisEd(t *testing.T, vids ...tlVideo) *cutEditor {
	t.Helper()
	ed := newTestEd(t)
	ed.vids = vids
	ed.relayout()
	return ed
}

// ---- the runs ----------------------------------------------------------------

func TestTheFilmedRunsAreTheUnionOfTheRecordings(t *testing.T) {
	for _, c := range []struct {
		what string
		vids []tlVideo
		want []tlSpan
	}{
		{"nothing loaded", nil, nil},
		{"one recording", []tlVideo{{start: 10, dur: 50}}, []tlSpan{{t0: 10, t1: 60}}},
		{"a hole between two", []tlVideo{{start: 0, dur: 30}, {start: 90, dur: 10}},
			[]tlSpan{{t0: 0, t1: 30}, {t0: 90, t1: 100}}},
		// abutting is not a hole: the second starts where the first stopped,
		// and there is nothing between them to hatch
		{"one stopping as the next starts", []tlVideo{{start: 0, dur: 30}, {start: 30, dur: 10}},
			[]tlSpan{{t0: 0, t1: 40}}},
		{"two cameras overlapping", []tlVideo{{start: 0, dur: 60}, {start: 40, dur: 50}},
			[]tlSpan{{t0: 0, t1: 90}}},
		// one long camera and a short one inside it: the short one adds no
		// timeline at all, which the naive "extend to the newest end" merge
		// gets wrong by cutting the run short at 50
		{"one swallowed by another", []tlVideo{{start: 0, dur: 100}, {start: 20, dur: 30}},
			[]tlSpan{{t0: 0, t1: 100}}},
		// the list is sorted by start on load, but a lane the user has shifted
		// in time is not, and an unsorted merge would leave two runs here
		{"out of order", []tlVideo{{start: 50, dur: 20}, {start: 0, dur: 60}},
			[]tlSpan{{t0: 0, t1: 70}}},
		// a recording that probed as zero-length is not a run
		{"an empty file", []tlVideo{{start: 0, dur: 0}, {start: 10, dur: 5}},
			[]tlSpan{{t0: 10, t1: 15}}},
	} {
		got := timeSpans(c.vids)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d runs %v, want %d %v", c.what, len(got), got, len(c.want), c.want)
			continue
		}
		for i := range got {
			if got[i].t0 != c.want[i].t0 || got[i].t1 != c.want[i].t1 {
				t.Errorf("%s: run %d is %.0f–%.0f, want %.0f–%.0f",
					c.what, i, got[i].t0, got[i].t1, c.want[i].t0, c.want[i].t1)
			}
		}
	}
}

// ---- the old layout, unchanged -----------------------------------------------

// The compatibility claim. One recording is drawn from x=0 at the zoom, and a
// second recording after a hole starts exactly one gap past the first's end --
// which is what relayout did when it laid the files out itself.
func TestNonOverlappingSourcesLayOutExactlyAsBefore(t *testing.T) {
	ed := axisEd(t, tlVideo{start: 0, dur: 60}, tlVideo{start: 300, dur: 40})
	firstW := 60 * ed.pps
	if ed.xOf(0) != 0 || ed.xOf(60) != firstW {
		t.Errorf("the first recording runs %.0f–%.0f px, want 0–%.0f", ed.xOf(0), ed.xOf(60), firstW)
	}
	if got, want := ed.xOf(300), firstW+gapPx; got != want {
		t.Errorf("the second recording starts at %.0f px, want %.0f", got, want)
	}
	if got, want := ed.totalW, firstW+gapPx+40*ed.pps; got != want {
		t.Errorf("the timeline is %.0f px wide, want %.0f", got, want)
	}
	// the per-file origins the thumbnails are walked from still agree with the
	// map, which is the whole reason they are kept
	for _, v := range ed.vids {
		if v.pxOrigin != ed.xOf(v.start) {
			t.Errorf("a recording's origin is %.0f px but its start reads as %.0f", v.pxOrigin, ed.xOf(v.start))
		}
	}
	// and the four and a half minutes nobody filmed cost one hole, not
	// 240 seconds of blank track
	if ed.totalW > (60+40)*ed.pps+gapPx+0.5 {
		t.Errorf("the unfilmed stretch is being drawn: %.0f px for 100 s of footage", ed.totalW)
	}
}

// ---- one second, one place ---------------------------------------------------

func TestOverlappingSourcesShareOneAxis(t *testing.T) {
	ed := axisEd(t, tlVideo{start: 0, dur: 60}, tlVideo{start: 40, dur: 50})
	// the whole session is one run, so x is the clock times the zoom all the
	// way across -- including the twenty seconds both cameras saw
	for _, tt := range []float64{0, 20, 40, 50, 60, 90} {
		if got, want := ed.xOf(tt), tt*ed.pps; math.Abs(got-want) > 1e-9 {
			t.Errorf("session second %.0f is at %.1f px, want %.1f", tt, got, want)
		}
	}
	if got, want := ed.totalW, 90*ed.pps; math.Abs(got-want) > 1e-9 {
		t.Errorf("two overlapping recordings measure %.0f px, want %.0f", got, want)
	}
	// the second camera is drawn where its footage actually is, not after the
	// first one -- the bug this replaced
	if ed.vids[1].pxOrigin != 40*ed.pps {
		t.Errorf("the second camera starts at %.0f px, want %.0f", ed.vids[1].pxOrigin, 40*ed.pps)
	}
}

// ---- the round trip ----------------------------------------------------------

func TestAPixelAndASecondAgree(t *testing.T) {
	ed := axisEd(t, tlVideo{start: 0, dur: 60}, tlVideo{start: 300, dur: 40})
	for _, tt := range []float64{0, 1, 30, 60, 300, 320, 340} {
		if got := ed.tAt(ed.xOf(tt)); math.Abs(got-tt) > 1e-9 {
			t.Errorf("%.0f s reads back as %.3f s", tt, got)
		}
	}
	// inside the hatched hole there is no second to be at, so the x clamps
	// forward to the next run rather than reporting a time nobody filmed
	if got := ed.tAt(60*ed.pps + gapPx/2); got != 300 {
		t.Errorf("the middle of the hole reads as %.0f s, want 300", got)
	}
	// off the right-hand end is the end of the session
	if got := ed.tAt(ed.totalW + 500); got != 340 {
		t.Errorf("past the end reads as %.0f s, want 340", got)
	}
	// an empty timeline has no time on it at all
	if got := newTestEd(t).tAt(123); got != 0 {
		t.Errorf("an x on an empty track reads as %.0f s, want 0", got)
	}
}

// ---- what the session measures -----------------------------------------------

func TestTheSessionsEndAndLengthCountTimeNotFiles(t *testing.T) {
	// sorted by start, so the LAST entry is the last to begin -- and here it is
	// the first to finish. Reading the end off it would cut fourteen minutes
	// off the session, and every effect would be clamped into the first camera
	ed := axisEd(t, tlVideo{start: 0, dur: 900}, tlVideo{start: 600, dur: 60})
	if got := ed.sessEnd(); got != 900 {
		t.Errorf("the session ends at %.0f s, want 900", got)
	}
	// twenty-five minutes of footage, fifteen minutes of session: the zoom is
	// fitted to the timeline, not to the sum of the files
	if got := ed.filmedDur(); got != 900 {
		t.Errorf("the filmed stretch measures %.0f s, want 900", got)
	}
	ed.viewW = 1800
	if got, want := ed.minPps(), fitPps(1800, 900, 1); got != want {
		t.Errorf("zoom-to-fit is %.4f px/s, want %.4f", got, want)
	}
	if newTestEd(t).sessEnd() != 0 {
		t.Error("an empty session does not end at zero")
	}
}
