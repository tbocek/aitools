package main

// － Remove, beside ＋ Add.
//
// A remove used to live on this bar and was taken off it (cut_segkill.go): it
// guessed between the selection, the held clip and the playhead, and a verb
// that guesses is a verb that occasionally removes something nobody pointed at.
// The ✕ on the green bar replaced it, and for "drop THAT scene" it is the
// better control -- the thing you press is the thing that goes.
//
// It cannot do this, though. A ✕ drops a whole scene, and the ask here is to
// cut a HOLE in one: mark ten seconds in the middle of a kept minute, take them
// out, and be left with two kept stretches either side. There is no scene to
// press for that, because the thing being removed is not a scene -- it is a
// span the hand drew, and the two scenes it makes did not exist until it was
// drawn.
//
// So this one comes back with the ambiguity removed rather than resolved: it is
// the SELECTION's verb and nothing else's. No selection and it says so; a
// selection scoped to a sound and it says that instead of quietly dropping
// pictures; a selection lying where the cut keeps nothing and it says that too,
// rather than reporting a removal and leaving an undo step that undoes nothing.
// It sits next to ＋ Add because they are one pair -- the same span, kept or
// dropped -- and it is greyed under exactly the same rule.

import (
	"fmt"
	"math"
)

// spanTouches reports whether removing t0..t1 would change the cut at all.
//
// It is the same test removeSpan makes on each segment, asked ahead of time:
// anything with a positive overlap is trimmed, split or (an insert) dropped
// whole, and anything else is copied across untouched. So a false here means
// the press would be a no-op, which is the one thing the status line must not
// call a removal.
func (ed *cutEditor) spanTouches(t0, t1 float64) bool {
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	for _, s := range ed.segs {
		if s.E > t0 && s.S < t1 {
			return true
		}
	}
	return false
}

// removeSelRange is － Remove: the selected stretch comes out of the cut.
func (a *App) removeSelRange() {
	ed := a.ed
	// the band, not its numbers: sel.t0/t1 keep whatever the last drag left in
	// them after the band comes down, so a press with nothing selected would
	// otherwise cut a hole where a selection USED to be. (＋ Add asks for
	// footage as well as a band; this one does not need to -- it edits the cut,
	// and a cut with scenes in it is one there is something to remove from.)
	if !ed.sel.active {
		a.setStatus("drag a region on a track first")
		return
	}
	// the mirror of ＋ Add's guard, in the same words: both buttons act on
	// footage, and a selection pointed at a waveform is not about footage. The
	// button is greyed for this; the guard is for every other way in.
	if ed.sel.aud != "" {
		a.setStatus(fmt.Sprintf("－ Remove drops footage, and the selection is %s's sound — "+
			"press ▲ on the strip above the lanes to point it at the picture", ed.sel.aud))
		return
	}
	if !ed.spanTouches(ed.sel.t0, ed.sel.t1) {
		a.setStatus(fmt.Sprintf("nothing to remove: the cut keeps nothing between %s and %s",
			mmss(math.Min(ed.sel.t0, ed.sel.t1)), mmss(math.Max(ed.sel.t0, ed.sel.t1))))
		return
	}
	// measured on the finished video's clock, not on the session's: a stretch
	// under a ×2 is half as many seconds of video as it is of footage, and
	// "removed 20 s" over a video that got 10 s shorter is the number nobody
	// can check against the total under the tracks (cutLen).
	before, was := len(ed.segs), ed.cutLen()
	ed.pushUndo()
	ed.removeRange(ed.sel.t0, ed.sel.t1)
	ed.sel.active = false
	ed.clearMarks()
	a.setStatus(removedMsg(was-ed.cutLen(), before, len(ed.segs)))
}

// removedMsg is what the line says afterwards.
//
// The scene count going UP is the whole point of this button and reads as a
// mistake unless it is named -- "2 scenes, was 1" over a press that REMOVED
// something is a sentence that has to be worked out. So the split says what
// happened in words, and everything else says the count.
func removedMsg(gone float64, before, after int) string {
	if after > before {
		return fmt.Sprintf("removed %.1f s — the scene it went through is two now "+
			"(↶ Undo takes it back)", gone)
	}
	return fmt.Sprintf("removed %.1f s — %d scene(s), was %d (↶ Undo takes it back)",
		gone, after, before)
}
