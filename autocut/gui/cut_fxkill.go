package main

// The ✕ on an effect's band.
//
// An effect could only be removed by taking it in hand and pressing ⌦, and
// nothing on the lane said so: the band offered no control at all, so the
// verb might as well not have existed. The kept scenes and the cut lanes
// already answered this with a ✕ on the thing itself, and the effects lane
// now speaks the same language: every band wide enough carries the mark at
// its right end, and pressing it drops THAT effect.
//
// The same corner buys the same trade the scene's ✕ made: the press is asked
// about the ✕ before the band, so the stretch of the band under the badge no
// longer picks the effect up or takes its end. The left end still resizes,
// the rest of the band still slides, and a band too narrow to spare the
// corner keeps its old removal -- in hand, then ⌦ -- like a scene too narrow
// for its own ✕.

import (
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// fxKillCentre is where effect i's ✕ sits -- timeline x, area y -- and
// whether it has one at all. The width floor is the scene's (segKillMin), for
// the scene's reason: below it the band's middle is inside the target, and a
// press meant to slide the effect would remove it.
func (ed *cutEditor) fxKillCentre(i int) (float64, float64, bool) {
	if i < 0 || i >= len(ed.fx) {
		return 0, 0, false
	}
	x0, x1 := ed.fxSpanPx(ed.fx[i])
	if x1-x0 < segKillMin {
		return 0, 0, false
	}
	rows, _ := fxRows(ed.fx)
	return x1 - segKillIn, ed.fxLaneTop() + (float64(rows[i])+0.5)*fxLaneH, true
}

// fxKillAt is the effect whose ✕ is under a press at timeline-x px and
// area-y y, or -1.
func (ed *cutEditor) fxKillAt(px, y float64) int {
	for i := range ed.fx {
		cx, cy, ok := ed.fxKillCentre(i)
		if ok && math.Abs(px-cx) <= segKillHit && math.Abs(y-cy) <= segKillHit {
			return i
		}
	}
	return -1
}

// killFx drops effect i. It is what ⌦ has always done, with the hold taken
// out: the press names the effect, so nothing has to be picked up first --
// and removeHeldFx now comes through here too, so there is one copy of the
// index surgery for both doors.
func (ed *cutEditor) killFx(i int) {
	if i < 0 || i >= len(ed.fx) {
		return
	}
	was := ed.fx[i].fxLabel()
	ed.pushUndo()
	ed.fx = append(ed.fx[:i], ed.fx[i+1:]...)
	// the hold and the hover were indices, and the indices have moved
	switch {
	case ed.fxOn && ed.fxSel == i:
		ed.dropFx()
	case ed.fxOn && ed.fxSel > i:
		ed.fxSel--
	}
	ed.fxHovOn, ed.fxKillHov = false, -1
	ed.persist()
	ed.a.setStatus("removed " + was + " — ↶ Undo takes it back")
	ed.redrawTracks()
}

// hoverFxKill lights the ✕ under the pointer. x below zero means the pointer
// has left the band.
func (ed *cutEditor) hoverFxKill(x, y float64) {
	i := -1
	if x >= 0 && ed.fxHitLane(y) {
		i = ed.fxKillAt(x+ed.viewX, y)
	}
	if i != ed.fxKillHov {
		ed.fxKillHov = i
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
		}
	}
}

// drawFxKill paints the badges, the same plate-and-arms the scenes wear, on
// top of the bands drawFxLane has already drawn. Called from drawTrack inside
// its translation, so x here is timeline px like everything drawn around it.
func (ed *cutEditor) drawFxKill(cr *cairo.Context, vx0, vx1 float64) {
	for i := range ed.fx {
		cx, cy, ok := ed.fxKillCentre(i)
		if !ok || cx < vx0-segKillHit || cx > vx1+segKillHit {
			continue
		}
		if ed.fxKillHov == i {
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
}
