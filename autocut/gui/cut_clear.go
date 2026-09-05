package main

// Clear: the timeline emptied of everything the cut put on it.
//
// Undo takes one edit back and Revert goes to the last suggestion, and neither
// is "start again". A session cut three ways over an evening ends up with a
// dozen kept stretches, their speeds and their captions, none of which belongs
// to the fourth idea -- and the way to be rid of them was to press ✕ on each
// green bar in turn, or to run a suggestion whose only job was to be thrown
// away too.
//
// It leaves the RECORDINGS alone. What goes is the cut: the kept stretches and
// the effects over them, including the ones the model proposed. What stays is
// everything about the session itself -- the sources, the rows they sit on, the
// corrections to their clocks, the lanes cut from them -- because none of that
// is a decision about the video, and losing a hand-corrected clock to a button
// labelled Clear would be losing the wrong thing.
//
// One undo step for the lot: it is one press about one page, and putting it
// back a scene at a time is not an edit history.

import "fmt"

// clearCut empties the cut. Nothing at all to clear is not an edit and says so
// rather than leaving an undo step that undoes nothing.
func (ed *cutEditor) clearCut() {
	if len(ed.segs) == 0 && len(ed.fx) == 0 {
		ed.a.setStatus("nothing to clear — the timeline holds no cut yet")
		return
	}
	segs, fx := len(ed.segs), len(ed.fx)
	ed.pushUndo()
	ed.segs, ed.fx = nil, nil
	// every hold is an index into a list that is now empty, and the selection
	// is a span over footage the cut no longer keeps
	ed.dropSeg()
	ed.dropEdge()
	ed.dropFx()
	ed.dropSel()
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	ed.syncSelBtns()
	ed.redrawTracks()
	ed.a.setStatus(fmt.Sprintf("cleared %d scene(s) and %d effect(s) — the recordings are "+
		"untouched, and ↶ Undo brings the cut back", segs, fx))
}
