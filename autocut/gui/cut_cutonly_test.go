package main

// The ✂ Cut only preview: the button that makes ▶ play the FINISHED video
// rather than the recording. Two halves have to hold. The transport half --
// the tick jumps every stretch the cut throws away, and the clock switches to
// the cut's own time, because "3:20 into the session" answers nothing once the
// removed stretches stop playing. And the drawing half -- the track dims what
// is about to be skipped, so the jumps are visible before they happen rather
// than felt as stutters.

import (
	"math"
	"os"
	"strings"
	"testing"
)

// ---- the cut-only preview ---------------------------------------------------

// What the ✂ Cut only scrim covers: everything the finished video will never
// contain. The holes between kept clips, and whatever hangs off either end of
// a recording -- and nothing that is kept, which is the half that matters,
// because dimming a clip that plays would say the cut drops it.
func TestTheDroppedStretchesAreTheOnesNotKept(t *testing.T) {
	ed := &cutEditor{
		vids: []tlVideo{{start: 0, dur: 60}, {start: 60, dur: 40}},
		segs: []cutSeg{
			{S: 5, E: 20},
			{S: 30, E: 55},
			{S: 70, E: 90},
		},
	}
	want := [][2]float64{
		{0, 5},   // the run-up to the first clip
		{20, 30}, // a hole
		// one stretch and not two, though the seam between the recordings is
		// in the middle of it: the axis is session time, the second camera
		// starts where the first stopped, and there is no hole at 60 to break
		// the scrim over. It used to be split there, back when the timeline
		// was files laid end to end rather than a clock
		{55, 70},
		{90, 100}, // and the tail of the last recording
	}
	got := ed.droppedSpans()
	if len(got) != len(want) {
		t.Fatalf("dropped %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dropped span %d is %v, want %v", i, got[i], want[i])
		}
	}
	// nothing that is kept is in there, said as the property rather than as
	// the list: a clip covered by a dropped span is a clip the scrim hides
	for _, s := range ed.segs {
		mid := (s.S + s.E) / 2
		for _, g := range got {
			if mid >= g[0] && mid < g[1] {
				t.Errorf("the middle of the kept clip %v–%v falls inside dropped span %v", s.S, s.E, g)
			}
		}
	}
	// a cut that keeps everything drops nothing
	whole := &cutEditor{vids: []tlVideo{{start: 0, dur: 60}}, segs: []cutSeg{{S: 0, E: 60}}}
	if g := whole.droppedSpans(); len(g) != 0 {
		t.Errorf("a cut that keeps the whole recording dropped %v", g)
	}
	// two cameras on the same minute are ONE minute of dropped time. Counted
	// per recording it would be dropped twice, and the scrim would be painted
	// twice over the same pixels -- harmless there, but the same count is what
	// the zoom-to-fit is measured against, and that would fit the tracks into
	// half the window
	both := &cutEditor{vids: []tlVideo{{start: 0, dur: 60}, {start: 10, dur: 40}}}
	if g := both.droppedSpans(); len(g) != 1 || g[0] != [2]float64{0, 60} {
		t.Errorf("two overlapping recordings with nothing kept drop %v, want one span 0-60", g)
	}
}

// The clock reads the cut's own time under ✂ Cut only: how far into the
// FINISHED video the line is, which is the question that mode is asked. Both
// readings are the same width, or switching modes would shove the whole bar
// sideways -- the reason playheadClock pads its dashes in the first place.
func TestTheCutClockIsTheFinishedVideosOwn(t *testing.T) {
	segs := []cutSeg{{S: 5, E: 20}, {S: 30, E: 55}}
	// 12s into the session is 7s into the cut; 40s in is 15+10 = 25s in
	for _, c := range []struct{ sess, cut float64 }{
		{5, 0}, {12, 7}, {20, 15}, {30, 15}, {40, 25}, {55, 40}, {600, 40},
	} {
		if got := cutPos(segs, c.sess); math.Abs(got-c.cut) > 1e-9 {
			t.Errorf("%.0fs of session reads as %.2fs of cut, want %.0f", c.sess, got, c.cut)
		}
	}
	if a, b := len(playheadClock(12, true)), len(playheadClock(cutPos(segs, 12), true)); a != b {
		t.Errorf("the two clocks are %d and %d characters wide — switching modes will shove the bar", a, b)
	}
}

// The mode is wired: the tick skips, ▶ snaps before the picture starts, the
// button drives the flag, and the track dims what is about to be jumped.
// Pinned as source, like every other gesture on this page.
func TestTheCutOnlyPreviewIsWired(t *testing.T) {
	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// the tick jumps the gap, and stops dead past the last clip
		"if ed.skipGap() {",
		"cur, next := gapAt(ed.segs, ed.playhead)",
		"case next != ed.jumped:",
		"ed.setPlayhead(ed.segs[next].S)",
		// ▶ never opens on a frame the cut throws away
		"ed.cutOnlySnap()",
		// the button, and the flag it drives
		"ed.cutBtn = gtk.NewToggleButton()",
		"ed.cutOnly = ed.cutBtn.Active()",
		// the clock changes meaning with it
		"t = cutPos(ed.segs, ed.playhead)",
		// and the track says which seconds are about to be skipped
		"for _, g := range ed.droppedSpans() {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
	// the re-entry guard is reset wherever the cut it counted against is
	// replaced, or the first gap of a freshly loaded project is not jumped
	if n := strings.Count(src, "ed.jumped = -1"); n < 4 {
		t.Errorf("the gap-skip guard is reset in %d places, want the reload, the toggle and both arms of the skip", n)
	}
}

// An empty cut under ✂ Cut only is an empty video, so ▶ has nothing it could
// honestly play -- with no clips there are no gaps to skip, and the preview
// would run the whole recording against the mode's one promise. The button
// greys out, and every other way into playing refuses for the same reason.
// The sensitivity runs on live widgets, so the wiring is pinned to the source;
// the refusal itself is exercised.
func TestAnEmptyCutHasNothingToPlay(t *testing.T) {
	// the refusal comes before the player is even asked: a click on the picture
	// and the run bar land in toggle too, and none of them may start playback
	ed := &cutEditor{a: &App{}, cutOnly: true}
	ed.toggle() // must return without touching the (absent) player
	if ed.started {
		t.Error("toggling an empty cut-only preview counted as started")
	}

	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// the one rule, in the one place that draws it
		"ed.playBtn.SetSensitive(!ed.cutOnly || len(ed.segs) > 0)",
		// ...and toggle refuses on the same rule, saying why
		"the cut is empty — add a clip to play it",
		// the toolbar clock stays on the SESSION's time too: an empty cut's own
		// clock reads 0:00.0 wherever the red line stands, and "where is the
		// line" is the one thing the clock is for
		"if ed.cutOnly && len(ed.segs) > 0 {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
	// synced from the mode's toggle AND from syncButtons, which every edit
	// passes through -- or the button would stay grey after the first clip is
	// added, or stay live after the last is removed
	if n := strings.Count(src, "ed.syncPlayBtn()"); n < 2 {
		t.Errorf("syncPlayBtn is called from %d places, want the ✂ toggle and syncButtons both", n)
	}
}
