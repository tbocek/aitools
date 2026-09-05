package main

// Dropping a scene, and the ✕ badge every remove on this page is drawn as.
//
// Removing footage used to be a button in the toolbar, and a toolbar button
// asks the hand to do the work twice: put the red line on the scene, cross the
// page, press － Remove, and hope the line was where you thought it was. The
// button also had to guess -- selection first, then held clip, then whatever
// the playhead happened to sit on -- and a verb that guesses is a verb that
// occasionally removes something nobody pointed at.
//
// So it moved onto the thing it acts on. (A － Remove is back on the bar since,
// for the one job an ✕ cannot do -- cutting a hole in the middle of a scene,
// which leaves two scenes that did not exist to be pressed. It guesses nothing:
// it is the selection's verb alone. See cut_selrm.go.)
//
// Where it moved to has changed once. It was a badge in the top-right corner of
// every green stretch, drawn on the thumbnails; it is now the ✕ on the green
// bar in the selection row (cut_selband.go), which stands for the clip in hand
// or the clip under the red line. Two marks for one verb was the state of the
// page for a while -- the bar had grown the blue band's whole vocabulary and
// the ✕ came with it -- and one of them had to go. The bar's is the one that
// stayed: it has a flat row to itself, so it is legible over dark footage and
// bright without fighting the picture, and the row is wide enough to give the
// mark a plate. The cost is honest and worth saying: the bar speaks for one
// clip, so dropping a scene means having the line in it, where a badge per
// scene did not. ⌦ still removes whatever is in hand, which is how the cards
// and the sounds go.
//
// What the badge looks like is here because four different things wear it: the
// bar's ✕, a cut lane's, an emptied row's (cut_lane.go) and an effect's
// (cut_fxkill.go). One recipe, so a remove looks like a remove wherever it is
// drawn -- and one place to change if it ever should.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	segKillR   = 4.0  // the arms of the ✕, from its centre
	segKillPad = 3.0  // the plate's edge, beyond the arms
	segKillHit = 10.0 // and the target, which is bigger than either
	segKillTop = 11.0 // its centre, down from the top of the picture band
	// ...and in from the edge it is drawn against. ONE number, for every ✕ on
	// the page: the bar's, an effect's, a cut lane's, an emptied row's.
	//
	// It is not a taste either. Every edge one of these sits against can be
	// GRABBED -- a clip border, a bar's end, an effect's end -- and the grab
	// reaches edgeGrab px either side of it. Set closer in than that, the
	// badge's target overlaps the border's: the press asking for "a bit
	// shorter" finds "gone", and the press asking for the badge's outer edge
	// finds the handle. So the target begins exactly where the grab stops.
	//
	// The badges were set from two different numbers for a while -- 16 on the
	// green bar, which had worked this out, and 11 everywhere else, which had
	// not -- so an effect's ✕ swallowed the whole of its own right-hand grip,
	// and the two marks sat at visibly different distances from their edges.
	// The speaker badges already keep this rule (hearIn); this is the same
	// sentence for the ✕.
	killIn = edgeGrab + segKillHit
	// under this a thing has no corner to spare: the target reaches
	// killIn+segKillHit in from the edge, and anything narrower than twice
	// that has its MIDDLE inside the ✕ -- so a click meant for the thing
	// itself would remove the thing. Twice the reach, and a little over, so
	// the half away from the badge is plain timeline.
	killMin = 2*(killIn+segKillHit) + 6
)

// drawKillBadge paints one ✕ centred on cx,cy: a plate, then the arms.
//
// A plate under the mark, always: two white strokes laid straight onto a
// thumbnail are legible over a dark frame and invisible over a bright one, and
// a control that disappears on half the footage is not a control. Red only
// under the pointer -- a row of red buttons standing open over the cut reads
// as damage already done.
func drawKillBadge(cr *cairo.Context, cx, cy float64, hot bool) {
	if hot {
		cr.SetSourceRGBA(0.85, 0.24, 0.28, 0.95)
	} else {
		cr.SetSourceRGBA(0.06, 0.06, 0.07, 0.55)
	}
	cr.Arc(cx, cy, segKillR+segKillPad, 0, 2*math.Pi)
	cr.Fill()
	cr.SetSourceRGBA(1, 1, 1, 0.9)
	cr.SetLineWidth(1.6)
	for _, d := range [][2]float64{{-1, -1}, {-1, 1}} {
		cr.MoveTo(cx+segKillR*d[0], cy+segKillR*d[1])
		cr.LineTo(cx-segKillR*d[0], cy-segKillR*d[1])
	}
	cr.Stroke()
}

// killSeg drops scene i. It is removeSelClicked's own arithmetic with the
// guessing taken out: the scene is named by the press, so there is no held
// clip and no playhead to consult.
func (ed *cutEditor) killSeg(i int) {
	if i < 0 || i >= len(ed.segs) {
		return
	}
	s := ed.segs[i]
	ed.pushUndo()
	ed.segs = append(ed.segs[:i], ed.segs[i+1:]...)
	// whatever was in hand was holding an index, and the indices have moved
	ed.dropSeg()
	ed.dropEdge()
	ed.bandKillHov = -1 // the indices moved; whatever was lit is not that clip now
	ed.persist()
	ed.a.setStatus(fmt.Sprintf("removed the scene at %s (%s) — ↶ Undo takes it back",
		mmss(s.S), ed.spanSecs(s.S, s.E)))
	ed.redrawTracks()
}
