package main

// Trimming a clip by its edge. Add and Remove work in regions, which is the
// wrong shape for the last thing you do to a scene -- half a second off the
// front -- so the right button picks a green border up and the frame buttons
// move it. What these pin is the arithmetic under that: how far an edge may go,
// that one hold is one Undo, and that the lower track paints the frame the
// boundary falls inside instead of leaving a grey bar there.

import (
	"os"
	"strings"
	"testing"
)

func edgeEd(t *testing.T) *cutEditor {
	t.Helper()
	ed := newTestEd(t)
	ed.vids = []tlVideo{{base: "v", path: "v.mkv", start: 0, dur: 600, interval: 5, fps: 30}}
	ed.relayout() // pps 4, so a session second is four timeline px
	ed.segs = []cutSeg{{S: 100, E: 130}, {S: 200, E: 260}}
	return ed
}

func TestAnEdgeIsPickedUpNearItsBorder(t *testing.T) {
	ed := edgeEd(t)

	if !ed.grabEdge(402) || !ed.edgeOn || ed.edgeSeg != 0 || ed.edgeEnd {
		t.Fatalf("a press 2 px from clip 1's start did not pick it up: on=%v seg=%d end=%v",
			ed.edgeOn, ed.edgeSeg, ed.edgeEnd)
	}
	if got := ed.edgeTime(); got != 100 {
		t.Errorf("the held edge reads as %g, want 100", got)
	}
	// the other three borders, each by its own px
	for _, c := range []struct {
		px   float64
		seg  int
		end  bool
		want float64
	}{{520, 0, true, 130}, {800, 1, false, 200}, {1040, 1, true, 260}} {
		if !ed.grabEdge(c.px) || ed.edgeSeg != c.seg || ed.edgeEnd != c.end || ed.edgeTime() != c.want {
			t.Errorf("px %g picked up seg %d end=%v at %g, want seg %d end=%v at %g",
				c.px, ed.edgeSeg, ed.edgeEnd, ed.edgeTime(), c.seg, c.end, c.want)
		}
	}
	// ...and a press out in the middle of a clip is not an edge at all
	if ed.grabEdge(460) {
		t.Error("a press 15 s from any border picked up an edge")
	}
}

// One hold is one Undo: a drag is a single act, and a snapshot per motion event
// would be the whole history.
func TestOneHoldIsOneUndo(t *testing.T) {
	ed := edgeEd(t)
	ed.grabEdge(400)
	for _, to := range []float64{104, 108, 112, 105} {
		ed.moveEdgeTo(to, true)
	}
	if len(ed.undo) != 1 {
		t.Fatalf("a four-move drag left %d undo entries, want 1", len(ed.undo))
	}
	if ed.segs[0].S != 105 {
		t.Fatalf("the edge ended at %g, want 105", ed.segs[0].S)
	}
	ed.undoLast()
	if ed.segs[0].S != 100 {
		t.Fatalf("undo left the edge at %g, want the 100 it started at", ed.segs[0].S)
	}
	if ed.edgeOn {
		t.Error("the edge is still held after an Undo replaced the cut it indexes into")
	}
	// picking an edge up and putting it down again is not an edit
	ed.grabEdge(400)
	ed.moveEdgeTo(100, true)
	if len(ed.undo) != 0 {
		t.Errorf("a hold that moved nothing pushed %d undo entry/entries", len(ed.undo))
	}
}

// An edge may not turn its clip inside out, walk onto the next one, or leave
// the recording it was cut from.
func TestAnEdgeStopsWhereItMust(t *testing.T) {
	segs := []cutSeg{{S: 100, E: 130}, {S: 200, E: 260}}
	for _, c := range []struct {
		name   string
		i      int
		end    bool
		to     float64
		lo, hi float64
		want   float64
	}{
		{"start dragged past its own end (a clip keeps minSegLn)", 0, false, 400, 0, 600, 129},
		{"end dragged past its own start", 0, true, 0, 0, 600, 101},
		{"end dragged onto the next clip (clips never overlap)", 0, true, 240, 0, 600, 200},
		{"start dragged onto the one before", 1, false, 50, 0, 600, 130},
		{"end dragged past the recording", 1, true, 900, 0, 600, 600},
		{"start dragged before the recording", 0, false, -50, 0, 600, 0},
		{"a move well inside every stop", 1, false, 210, 0, 600, 210},
	} {
		if got := clampEdge(segs, c.i, c.end, c.to, c.lo, c.hi); got != c.want {
			t.Errorf("%s: to %g gave %g, want %g", c.name, c.to, got, c.want)
		}
	}
}

// The frame buttons are the edge's when one is held -- ‹f on a boundary you
// have just picked up cannot mean "move the playhead somewhere else" -- and
// they move it by whole frames of the recording it belongs to.
func TestTheFrameButtonsMoveTheHeldEdge(t *testing.T) {
	ed := edgeEd(t)
	ed.grabEdge(400)
	ed.frameStep(-30) // 30 fps: one second
	if got := ed.segs[0].S; got != 99 {
		t.Fatalf("‹‹f moved the edge to %g, want 99", got)
	}
	ed.frameStep(+15)
	if got := ed.segs[0].S; got != 99.5 {
		t.Fatalf("f› moved the edge to %g, want 99.5", got)
	}
	// the whole trim is still one entry
	if len(ed.undo) != 1 {
		t.Errorf("two nudges of one held edge left %d undo entries, want 1", len(ed.undo))
	}
	// with nothing held they are the playhead's again, and with no preview
	// loaded that is a no-op rather than a trim
	ed.dropEdge()
	ed.frameStep(+30)
	if got := ed.segs[0].S; got != 99.5 {
		t.Errorf("a frame step with no edge held moved the cut to %g", got)
	}
}

// The cut track used to draw a frame only if the frame's own sample time fell
// inside the cut. Frames are sampled seconds apart, so a clip that starts
// between two of them lost the frame covering its first seconds and showed a
// grey bar at the head of nearly every clip.
func TestTheBoundaryFrameIsPainted(t *testing.T) {
	segs := []cutSeg{{S: 103, E: 137}}
	// the thumbnail sampled at 100 covers 100-105, of which the cut keeps 103-105
	got := keptSpans(segs, 100, 105)
	if len(got) != 1 || got[0] != [2]float64{103, 105} {
		t.Fatalf("the frame at the clip's head paints %v, want [{103 105}]", got)
	}
	// one wholly inside is painted whole, one wholly outside not at all
	if got := keptSpans(segs, 110, 115); len(got) != 1 || got[0] != [2]float64{110, 115} {
		t.Errorf("a frame inside the clip paints %v, want all of it", got)
	}
	if got := keptSpans(segs, 90, 95); got != nil {
		t.Errorf("a frame the cut dropped paints %v", got)
	}
	// the tail is the same case as the head, and a frame spanning two clips
	// paints once per clip
	if got := keptSpans(segs, 135, 140); len(got) != 1 || got[0] != [2]float64{135, 137} {
		t.Errorf("the frame at the clip's tail paints %v, want [{135 137}]", got)
	}
	two := []cutSeg{{S: 101, E: 102}, {S: 103, E: 104}}
	if got := keptSpans(two, 100, 105); len(got) != 2 {
		t.Errorf("a frame spanning two clips paints %v, want a piece for each", got)
	}
}

// ...and the wiring that hands all of that to the right mouse button.
func TestTheEdgeToolIsWired(t *testing.T) {
	b, err := os.ReadFile("step3.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"edge.SetButton(gdk.BUTTON_SECONDARY)",           // the right button, and only it
		"if !ed.grabEdge(x + ed.viewX) {",                // press: pick up the border under the cursor
		"ed.moveEdgeTo(ed.tAtView(edgeStartX+ox), true)", // drag: move it, without writing the file per motion
		"ed.persist() // the drag is over",               // release: this is the cut that goes on disk
		"ed.dropEdge() // any left click puts a held edge down",
		"case ed.edgeOn && (keyval == gdk.KEY_Left || keyval == gdk.KEY_Right):", // arrows nudge, but only while held
		"case ed.edgeOn && keyval == gdk.KEY_Escape:",
		"if ed.edgeOn {\n\t\ted.nudgeEdge(n)",                                // ‹f and f› are the edge's while one is held
		"if ed.edgeOn && !ed.playing() {\n\t\ted.setPlayhead(ed.edgeTime())", // ▶ plays from the held edge
		"if ed.edgeOn && ed.edgeSeg < len(ed.segs) {",                        // ...and the held edge is drawn
		"for _, k := range keptSpans(ed.segs, t, t+float64(step)*v.interval) {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
	if strings.Contains(src, "if isCut && !ed.inCut(t) {") {
		t.Error("the cut track is back to drawing whole frames by their sample time — the grey bar at every clip's head")
	}
}
