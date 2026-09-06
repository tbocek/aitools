package main

// The black head of the timeline, and the switches that live in it.
//
// Every band on this page has a few permanent controls at its left: the
// whole-lane sound switch on a recorder's band, the same switch for the sound
// filmed with a camera row, the ✕ on an emptied row. They used to ride the
// VIEW's left edge -- pinned to the widget, with the tape scrolling under them
// -- which is the usual answer and is wrong here for one reason: at every
// scroll position they are drawn ON the footage. A speaker plate sat in the
// middle of a waveform, and the reading it covered is the reading you are
// there to look at.
//
// So the timeline starts a little way in, and what is in front of it is black:
// scroll home and you scroll PAST the start of the session into a strip that
// belongs to the controls alone. Nothing is drawn over anything, the switches
// keep exactly the jobs they had, and the price is that they scroll away with
// the tape -- which is the trade this page asked for, and the fold-all control
// added here would have had nowhere to stand otherwise.
//
// It is content, not a pinned column: the strip is the first gutterPx of the
// timeline's own coordinate space (layoutPx), so every control in it is placed
// and pressed in timeline px like everything else, and xOf/tAt need to know
// nothing about it.

import (
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	// gutterPx is how much black stands in front of second zero, and
	// gutterMid is where a control in it is centred. Wide enough for the
	// plates the switches wear (hearPlate, foldPlate) with a px or two either
	// side, and no wider: it is space taken off the width the footage has.
	gutterPx  = 30.0
	gutterMid = gutterPx / 2
)

// drawGutter fills the strip. Black rather than the band's own dark grey: the
// bands say "footage" and this is the one part of the page that is not, and at
// the zoom floor -- where the whole session is on screen and the strip is the
// only thing to the left of it -- a shade of the same grey would read as more
// timeline with nothing on it.
func (ed *cutEditor) drawGutter(cr *cairo.Context, top, h float64) {
	cr.SetSourceRGB(0, 0, 0)
	cr.Rectangle(0, top, gutterPx, h)
	cr.Fill()
}

// ---- fold the lot -----------------------------------------------------------

// foldAllY is where the fold-all control sits: the selection band's own line,
// under the clock, because folding is about the gaps between the green bars
// that band draws.
func (ed *cutEditor) foldAllY() float64 { return ed.selBandTop() + selBandH/2 }

// foldAllOn is what pressing it would do: with anything folded it opens
// everything, and only with nothing folded does it fold. One press to get the
// page back is worth more than one press to fold the last gap -- and a control
// that reads "+" while half the page is folded and half is not would be lying
// about the half it is not.
func (ed *cutEditor) foldAllOn() bool { return len(ed.folds) > 0 }

// foldAllAt is whether a press in the gutter is on it.
func (ed *cutEditor) foldAllAt(px, y float64) bool {
	return len(ed.foldGaps()) > 0 &&
		math.Abs(px-gutterMid) <= segKillHit && math.Abs(y-ed.foldAllY()) <= segKillHit
}

// toggleFoldAll folds every dropped stretch away, or brings them all back.
//
// The view is anchored the way one gap's own badge anchors it (toggleFold):
// the second at the left of the page stays at the left of the page, so folding
// the lot pulls the timeline in around what you were looking at rather than
// throwing it somewhere else entirely.
func (ed *cutEditor) toggleFoldAll() {
	gaps := ed.foldGaps()
	if len(gaps) == 0 {
		return
	}
	t := ed.tAt(ed.viewX + gutterPx)
	on := ed.foldAllOn()
	n := 0
	if on {
		ed.folds = nil
		n = len(gaps)
	} else {
		for _, g := range gaps {
			ed.folds = append(ed.folds, [2]float64{g.t0, g.t1})
			n++
		}
		ed.syncFolds() // a gap that is a whole recording is not one to fold (wholeRun)
		n = len(ed.folds)
	}
	ed.foldLayout(t, ed.xOf(t))
	ed.persist()
	if on {
		ed.a.setStatus("unfolded " + plural(n, "seam"))
		return
	}
	ed.a.setStatus("folded " + plural(n, "seam"))
}

// drawFoldAll paints it: the same − and + a single gap's badge wears, because
// it is the same verb over all of them.
func (ed *cutEditor) drawFoldAll(cr *cairo.Context) {
	if len(ed.foldGaps()) == 0 {
		return
	}
	mark := "−"
	if ed.foldAllOn() {
		mark = "+"
	}
	foldPlate(cr, gutterMid, ed.foldAllY(), mark, ed.foldAllHov)
}

// gutterCtl is whether a press landed on a control standing in the strip: the
// fold-all badge, a lane's sound switch, the sound switch on a row's own
// strip, or an emptied row's ✕.
//
// A click on the timeline cues the red line to the second under it, and every
// second in the gutter is second zero -- so pressing any of these threw the
// line back to the start of the session as a side effect of switching a lane
// off. The strip is not the tape: a press on something in it is a press on
// that thing, and only the black between them is a place to put the line.
func (ed *cutEditor) gutterCtl(px, y float64) bool {
	if px > gutterPx {
		return false
	}
	return ed.foldAllAt(px, y) || ed.laneSwitchAt(px, y) != "" ||
		ed.pairSwitchAt(px, y) != nil || ed.rowKillAt(px, y) >= 0
}

// hoverFoldAll lights it under the pointer, like every other badge here.
func (ed *cutEditor) hoverFoldAll(x, y float64) {
	on := x >= 0 && ed.foldAllAt(x+ed.viewX, y)
	if on != ed.foldAllHov {
		ed.foldAllHov = on
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
		}
	}
}
