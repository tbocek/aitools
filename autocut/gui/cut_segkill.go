package main

// The ✕ on a kept scene.
//
// Removing footage used to be a button in the toolbar, and a toolbar button
// asks the hand to do the work twice: put the red line on the scene, cross the
// page, press － Remove, and hope the line was where you thought it was. The
// button also had to guess -- selection first, then held clip, then whatever
// the playhead happened to sit on -- and a verb that guesses is a verb that
// occasionally removes something nobody pointed at.
//
// So it moves onto the thing it acts on. Every stretch the cut keeps is tinted
// green over the thumbnails; each one now carries a ✕ in its top-right corner,
// and pressing it drops THAT stretch. There is no ambiguity left to resolve:
// the scene you pressed is the scene that goes.
//
// This does put a control on the tint, which drawSelBand's own rule says is
// the answer and not the control. The rule is worth breaking here for the one
// reason it exists: the band can only ever speak for the clip under the red
// line, so a ✕ there would have kept the whole "move the line first" dance
// that made the button tiring. The tint is per-scene, and this verb is too.
//
// Only footage. A violet insert is not green and gets none, and neither does
// the one insert that IS drawn green -- a sound laid over the picture, where a
// ✕ would have to mean the sound or the frames under it and could not say
// which. Both are removed by taking them in hand and pressing ⌦, which is the
// same key the button was on.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	segKillR   = 4.0  // the arms of the ✕, from its centre
	segKillPad = 3.0  // the plate's edge, beyond the arms
	segKillHit = 10.0 // and the target, which is bigger than either
	segKillIn  = 11.0 // its centre, in from the scene's right border
	segKillTop = 11.0 // and down from the top of the picture band
	// under this a scene has no corner to spare. The number is not a taste: the
	// target reaches segKillIn+segKillHit in from the right border, and a scene
	// narrower than twice that has its MIDDLE inside the ✕ -- so a click meant
	// to put the red line on the clip would remove the clip. Twice the reach,
	// and a little over, so the left half of every scene wearing a ✕ is plain
	// timeline. Narrower ones are removed with ⌦, like anything else too small
	// to aim at.
	segKillMin = 2*(segKillIn+segKillHit) + 6
)

// segKillCentre is where scene i's ✕ sits -- timeline x, area y -- and whether
// it has one at all.
func (ed *cutEditor) segKillCentre(i int) (float64, float64, bool) {
	if i < 0 || i >= len(ed.segs) {
		return 0, 0, false
	}
	s := ed.segs[i]
	if s.isInsert() {
		return 0, 0, false
	}
	x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
	if x1-x0 < segKillMin {
		return 0, 0, false
	}
	return x1 - segKillIn, ed.segTop(s) + segKillTop, true
}

// segKillAt is the kept scene whose ✕ is under a press at timeline-x px and
// area-y y, or -1. Square rather than round: the plate is a disc but the hand
// is aiming at a corner, and the corners of the target are the part of it the
// pointer arrives at first.
func (ed *cutEditor) segKillAt(px, y float64) int {
	for i := range ed.segs {
		cx, cy, ok := ed.segKillCentre(i)
		if ok && math.Abs(px-cx) <= segKillHit && math.Abs(y-cy) <= segKillHit {
			return i
		}
	}
	return -1
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
	ed.killHovOn = false
	ed.persist()
	ed.a.setStatus(fmt.Sprintf("removed the scene at %s (%.0f s) — ↶ Undo takes it back",
		mmss(s.S), s.E-s.S))
	ed.redrawTracks()
}

// hoverSegKill lights the ✕ under the pointer. x below zero means the pointer
// has left the band.
func (ed *cutEditor) hoverSegKill(x, y float64) {
	i := -1
	if x >= 0 && ed.hitPics(y) {
		i = ed.segKillAt(x+ed.viewX, y)
	}
	if on := i >= 0; on != ed.killHovOn || (on && i != ed.killHov) {
		ed.killHovOn, ed.killHov = on, i
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
		}
	}
}

// drawSegKill paints the badges. Called from drawTrack inside its translation,
// so x here is timeline px like everything drawn around it.
//
// A plate under the mark, always: two white strokes laid straight onto a
// thumbnail are legible over a dark frame and invisible over a bright one, and
// a control that disappears on half the footage is not a control. Red only
// under the pointer -- a row of red buttons standing open over the cut reads
// as damage already done.
func (ed *cutEditor) drawSegKill(cr *cairo.Context, vx0, vx1 float64) {
	for i := range ed.segs {
		cx, cy, ok := ed.segKillCentre(i)
		if !ok || cx < vx0-segKillHit || cx > vx1+segKillHit {
			continue
		}
		hot := ed.killHovOn && ed.killHov == i
		if hot {
			cr.SetSourceRGBA(0.85, 0.24, 0.28, 0.95)
		} else {
			cr.SetSourceRGBA(0.06, 0.06, 0.07, 0.55)
		}
		cr.Arc(cx, cy, segKillR+segKillPad, 0, 2*math.Pi)
		cr.Fill()
		// the mark itself is a path, like every other mark on this page
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.SetLineWidth(1.6)
		for _, d := range [][2]float64{{-1, -1}, {-1, 1}} {
			cr.MoveTo(cx+segKillR*d[0], cy+segKillR*d[1])
			cr.LineTo(cx-segKillR*d[0], cy-segKillR*d[1])
		}
		cr.Stroke()
	}
}
