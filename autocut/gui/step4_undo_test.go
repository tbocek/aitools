package main

// Undo has one way to go wrong that a mouse would not catch quickly: a snapshot
// that shares its backing array with the live cut, so editing after an Add
// silently rewrites history. This exercises exactly that.

import (
	"path/filepath"
	"testing"
)

func newTestEd(t *testing.T) *cutEditor {
	t.Helper()
	return &cutEditor{a: &App{outDir: t.TempDir()}, pps: 4, thumbHt: 64}
}

func TestCutUndoRestores(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{10, 20}, {30, 40}, {50, 60}}

	ed.pushUndo()
	ed.removeRange(28, 42) // drops the middle scene
	if len(ed.segs) != 2 {
		t.Fatalf("remove left %d segments, want 2: %v", len(ed.segs), ed.segs)
	}

	// a further edit must not reach back into the snapshot
	ed.pushUndo()
	ed.segs = append(ed.segs[:0], ed.segs[1:]...)
	ed.persist()

	ed.segs = ed.undo[len(ed.undo)-1]
	ed.undo = ed.undo[:len(ed.undo)-1]
	if len(ed.segs) != 2 || ed.segs[0] != (cutSeg{10, 20}) || ed.segs[1] != (cutSeg{50, 60}) {
		t.Fatalf("first undo gave %v, want [{10 20} {50 60}]", ed.segs)
	}
	ed.segs = ed.undo[len(ed.undo)-1]
	if len(ed.segs) != 3 || ed.segs[1] != (cutSeg{30, 40}) {
		t.Fatalf("second undo gave %v, want the original three", ed.segs)
	}
}

// Revert must take back the hand-made delta and leave the checkpoint standing.
// The failure that matters is the opposite of undo aliasing: a baseline that
// tracks the live cut, so Revert becomes a no-op or wipes the suggestion too.
func TestCutRevertKeepsBaseline(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{100, 120}, {200, 220}} // as if suggested
	ed.setBase()
	if !sameCut(ed.segs, ed.base) {
		t.Fatal("baseline does not match the cut it was taken from")
	}

	ed.segs = append(ed.segs, cutSeg{300, 310}) // ten Adds, abridged
	ed.segs = append(ed.segs, cutSeg{400, 410})
	ed.coalesce()
	ed.persist()
	ed.removeRange(195, 225) // and a hand Remove of a suggested scene
	if sameCut(ed.segs, ed.base) {
		t.Fatal("baseline followed the edits")
	}

	ed.pushUndo()
	ed.segs = append([]cutSeg(nil), ed.base...)
	ed.persist()
	if !sameCut(ed.segs, []cutSeg{{100, 120}, {200, 220}}) {
		t.Fatalf("revert gave %v, want the suggestion back", ed.segs)
	}

	// and the revert itself is undoable
	ed.segs = ed.undo[len(ed.undo)-1]
	if len(ed.segs) != 3 {
		t.Fatalf("undo after revert gave %v, want the 3 edited segments", ed.segs)
	}
}

func TestCutSegAt(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{10, 20}, {30, 40}}
	for _, c := range []struct {
		t    float64
		want int
	}{{10, 0}, {19.9, 0}, {20, -1}, {25, -1}, {35, 1}, {99, -1}} {
		if got := ed.segAt(c.t); got != c.want {
			t.Errorf("segAt(%g) = %d, want %d", c.t, got, c.want)
		}
	}
}

func TestCutPersistRoundTrip(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{1, 5}, {9, 12}}
	ed.persist()
	if !exists(filepath.Join(ed.a.outDir, "step4", "cut.json")) {
		t.Fatal("persist wrote no cut.json")
	}
}
