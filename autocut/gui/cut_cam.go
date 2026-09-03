package main

// Which camera a scene is shown from, said on the rows themselves.
//
// The cut has always known: cutSeg.Cam is the row a scene takes its picture
// from. Nothing could ever change it. It was decided once, at ＋ Add, by which
// row the hand happened to be dragging on (addRange writes Cam: ed.sel.lane),
// and a scene added from the wrong camera had to be removed and added again on
// the other row, at the same seconds, by eye. A field the file stores, the
// render reads and the page draws, with no control anywhere on the page.
//
// So it gets the control the sound already had. A held scene wears a badge on
// every row it could be shown from -- lit on the row it IS shown from -- and
// pressing one moves the picture there. It is the same column of marks the
// lanes carry (cut_hear.go), at the same x, asking the same question of every
// row of the page:
//
//	is this row in this scene?
//
// The two halves answer it differently, and the marks say which is which. A
// scene hears any number of lanes, so a speaker is a checkbox and the badges
// are independent. A scene has ONE picture -- Cam is a single row, and "no
// picture at all" is not a state footage can be in -- so a lens is a radio:
// pressing one lights it and puts the other out, and none of them can be
// pressed off.
//
// Only rows that were actually rolling wear one. Offering a camera that filmed
// nothing at those seconds would be offering a hole: the render falls back to
// another row rather than show one (pickVideoOn), so the press would appear to
// do nothing, or worse, appear to work.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// camBadge is one row's answer for the scene the badges are about: where it is
// drawn, in timeline x and area y, and whether the scene is shown from there.
type camBadge struct {
	row    int
	cx, cy float64
	on     bool
}

// camBadges is one per row that could show this scene: the rows that have
// footage under it. Empty when there is no scene in hand, when the scene is too
// narrow to wear a mark, or when one row is all there is -- a badge that is the
// only badge answers a question nobody can ask.
//
// The x is hearX's, so the picture's marks and the sound's stand in one column
// at the scene's left edge and read as one question asked of every row.
func (ed *cutEditor) camBadges() []camBadge {
	cx, ok := ed.hearX()
	if !ok || ed.laneN < 2 {
		return nil
	}
	s := ed.hearScene()
	var out []camBadge
	for r := 0; r < ed.laneN; r++ {
		if videoOn(ed.vids, r, s.S) == nil {
			continue // that camera was not rolling here
		}
		out = append(out, camBadge{r, cx, ed.laneTop(r) + ed.laneH()/2, r == s.Cam})
	}
	if len(out) < 2 {
		return nil // one answer is not a choice
	}
	return out
}

// camBadgeAt is the row whose badge is under a press, or -1.
func (ed *cutEditor) camBadgeAt(px, y float64) int {
	for _, b := range ed.camBadges() {
		if math.Abs(px-b.cx) <= hearHit && math.Abs(y-b.cy) <= hearHit {
			return b.row
		}
	}
	return -1
}

// setSegCam shows the scene the badges are about from row r.
//
// The scene keeps everything else it is: the same seconds, the same silenced
// lanes, the same effects over it. What changes is which recording the frames
// are read from, which is the one thing the cut could say and nobody could
// alter.
//
// It does not coalesce. Two touching scenes of one camera are one scene, and
// this press can make two neighbours agree -- but rearranging the list under
// the hand that is still pointing at a badge is how a press turns into "what
// happened to my clip". The next edit that rebuilds the list will join them,
// which is where every other join happens.
func (ed *cutEditor) setSegCam(r int) {
	s := ed.hearScene()
	if s == nil || r < 0 || r == s.Cam {
		return
	}
	ed.pushUndo()
	s.Cam = r
	ed.persist()
	ed.showInsert() // the preview is standing on a frame that came from the old row
	ed.redrawTracks()
	ed.a.setStatus(fmt.Sprintf("the scene at %s is shown from %s now — "+
		"the seconds and the sound are untouched (↶ Undo takes it back)",
		mmss(s.S), ed.camName(r)))
}

// drawCamBadges paints them over the pictures, from inside drawTrack's
// translation like everything else in the band.
//
// The wash under a badge is the sound's, for the same reason: a scene too
// narrow to carry a mark still says which row it is shown from, and at any
// zoom the tint is the part that reads. Only the row that is NOT showing it
// gets a wash of its own -- the row that is has the cut's own green over it
// already, and a second tint on top would be the page saying the same thing
// twice in two colours.
func (ed *cutEditor) drawCamBadges(cr *cairo.Context, vx0, vx1 float64) {
	badges := ed.camBadges()
	if len(badges) == 0 {
		return
	}
	s := ed.hearScene()
	x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
	for _, b := range badges {
		if b.on || x1 < vx0 || x0 > vx1 {
			continue
		}
		cr.SetSourceRGBA(0.55, 0.55, 0.6, 0.22)
		cr.Rectangle(x0, ed.laneTop(b.row), x1-x0, ed.laneH())
		cr.Fill()
	}
	for _, b := range badges {
		if b.cx < vx0-hearHit || b.cx > vx1+hearHit {
			continue
		}
		camPlate(cr, b.cx, b.cy, b.on)
	}
}

// camPlate is the round plate a lens is drawn on: lit on the row the scene is
// shown from, dark on the rows it could be shown from instead. The plate is the
// speaker's, because both marks answer "is this row in this scene" and the page
// should say that once -- and what is ON the plate is what says which half of
// the question this is, and that a press here is a choice rather than a toggle.
func camPlate(cr *cairo.Context, cx, cy float64, on bool) {
	if on {
		cr.SetSourceRGBA(0.15, 0.65, 0.3, 0.95)
	} else {
		cr.SetSourceRGBA(0.06, 0.06, 0.07, 0.62)
	}
	cr.Arc(cx, cy, hearR+hearPad, 0, 2*math.Pi)
	cr.Fill()
	drawLens(cr, cx, cy, on)
}

// drawLens is the mark: a ring, filled on the row in use and hollow on the
// rest -- a radio button wearing a camera's face. A path rather than a glyph,
// like every other mark on this page.
func drawLens(cr *cairo.Context, cx, cy float64, on bool) {
	cr.SetSourceRGBA(1, 1, 1, 0.92)
	cr.SetLineWidth(1.4)
	cr.Arc(cx, cy, hearR*0.82, 0, 2*math.Pi)
	cr.Stroke()
	if !on {
		return
	}
	cr.Arc(cx, cy, hearR*0.4, 0, 2*math.Pi)
	cr.Fill()
}
