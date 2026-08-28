package main

// The scope strip: the row between the pictures and the waveforms, where a
// selection says whether it is OF the picture or OF a sound.
//
// A selection has meant one or the other since the lanes learnt the verbs: drag
// across the thumbnails and ⧉ Copy takes footage, drag across a waveform and it
// takes that recording's sound. But the choice was made entirely by where the
// hand happened to land, it was never written anywhere, and it could not be
// changed -- a selection dragged on the pictures that turned out to be about
// sound had to be thrown away and dragged again in a lane, at the exact same
// seconds, by eye.
//
// So the choice gets a row, on the seam it is a choice about. The strip is the
// border between the picture band and the sound band, and the selection wears a
// handle on it that points up into the pictures or down into the lanes. Press
// the half you mean. Press ▼ again and it walks to the next recording, which is
// the only place on the page that can say WHICH sound a selection drawn on the
// thumbnails is about, and after the last one it comes back up to both.
//
// Three positions, not two. It was two for a long time, on the argument that
// footage already carries its own sound -- a segment is picture and what was
// recorded with it -- so "video and audio" and "video" were the same selection
// wearing two names. That argument held exactly as long as nothing could put
// picture into the cut without its sound. ⧉ Insert can: a sting dropped over a
// selection scoped to the picture ALONE replaces the frames and leaves what is
// heard running underneath, and the same scope spliced into the cut goes in
// silent (cutSeg.Mute). So the two names now name two things, and the strip has
// a rung for each:
//
//	▲▲ picture alone -- the frames, and not the sound filmed with them
//	▲▼ picture + sound -- footage, in one piece: what a fresh selection is
//	 ▼ one recording's sound, on its own
//
// ▲ toggles between the middle rung and the top one, and brings a selection
// that had walked down into the lanes back to the middle: up is always towards
// the picture. ▼ walks down and round.
//
// Two of the three rungs are there in every session, however little was
// recorded. A silent screen capture has no lanes at all and still wants the top
// one, because what it says is not "which sound" -- it is "this insert brings
// no sound of its own", and a sting dropped into a silent capture is exactly
// where that gets asked. So the strip follows the footage, not the lanes, and
// ▼ with nothing to walk to simply comes back to the middle.
//
// It is a drawing area of its own rather than a few pixels borrowed from either
// neighbour, because the two neighbours are separate widgets: a handle drawn at
// the bottom of the picture band could not be pressed at the top of the lanes,
// and a strip that belongs to neither can sit between them and be pressed
// anywhere in its own height.

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// what a press on the strip lands on.
const (
	scopeNone = iota
	scopeUp   // ▲ towards the picture: footage, or the picture without its sound
	scopeDown // ▼ towards the lanes: one recording's sound, on its own
)

const (
	// the strip's height. Two rungs share it, so half of this is what the hand
	// actually gets -- at the sixteen it opened with, eight pixels, on a
	// control pressed as often as anything in the bar.
	scopeH = 24.0
	// the handle is never narrower than this, however few seconds are selected:
	// two stacked arrows need somewhere to be, and a selection of half a second
	// is exactly when saying what it is about matters most.
	scopeMinW = 44.0
)

// scopeBoxPx is the ▲▼ handle in timeline px, and whether there is one at all.
// A selection narrower than the handle keeps the handle and loses the exact
// alignment: it is still centred on the seconds it belongs to, which is what
// the eye needs, and being able to hit it is what the hand needs.
func (ed *cutEditor) scopeBoxPx() (float64, float64, bool) {
	if ed == nil || !ed.sel.active {
		return 0, 0, false
	}
	x0, x1 := ed.selSpanPx()
	if x1-x0 < scopeMinW {
		mid := (x0 + x1) / 2
		x0, x1 = mid-scopeMinW/2, mid+scopeMinW/2
	}
	return x0, x1, true
}

// scopePartAt is what a press at timeline-x px, y px down the strip takes.
func (ed *cutEditor) scopePartAt(px, y float64) int {
	x0, x1, ok := ed.scopeBoxPx()
	if !ok || px < x0 || px > x1 {
		return scopeNone
	}
	if y < scopeH/2 {
		return scopeUp
	}
	return scopeDown
}

// nextAud is where ▼ goes from here: the first recording when the selection is
// on the picture, the one after this when it is already on a recording, and
// back up to picture-and-sound after the last. Walking rather than choosing
// from a menu: there are two or three lanes, they are all on screen, and a
// second press is a cheaper way to say "not that one, the next one" than a
// popup listing what the eye can already see.
//
// The wrap goes to both rather than round to the first recording, because a
// walk that only ever cycles the lanes is a walk with no way out: ▼ pressed one
// time too many would strand the selection on a sound and leave ▲ as the only
// way back. Round through the middle rung, and ▼ alone can reach every scope.
func (ed *cutEditor) nextAud(base string) string {
	if len(ed.auds) == 0 {
		return ""
	}
	for i, au := range ed.auds {
		if au.base == base {
			if i+1 == len(ed.auds) {
				return "" // past the last: back to picture and sound together
			}
			return ed.auds[i+1].base
		}
	}
	return ed.auds[0].base
}

// selPic is the selection scoped to the picture ALONE, selSnd to one
// recording's sound, and neither is footage in one piece. Written out because
// "aud is empty" stopped meaning "the footage" the moment there were three
// answers, and every reader of the scope should have to say which of the two
// empty-aud cases it means.
func (ed *cutEditor) selPic() bool { return ed.sel.aud == "" && ed.sel.pic }
func (ed *cutEditor) selSnd() bool { return ed.sel.aud != "" }

// scopeName is what the strip writes beside the handle. The two picture rungs
// are spelled apart rather than shortened, since telling them apart is the
// whole reason the third rung exists.
func (ed *cutEditor) scopeName() string {
	switch {
	case ed.selSnd():
		return ed.sel.aud
	case ed.sel.pic:
		return "picture alone"
	}
	return "picture + sound"
}

// setSelScope puts the selection on one of the three rungs -- one recording's
// sound (base), the picture alone (pic), or footage in one piece -- and says
// what the verbs now mean. Nothing is edited: this is the same seconds, read
// differently.
func (ed *cutEditor) setSelScope(base string, pic bool) {
	if !ed.sel.active {
		ed.a.setStatus("drag a region on a track first — the ▲▼ handle is the selection's")
		return
	}
	// naming a recording and meaning the picture alone are answers to the same
	// question, so one arriving puts the other down
	ed.sel.aud, ed.sel.pic = base, pic && base == ""
	ed.syncSelBtns()
	a, b := ed.selSpan()
	switch {
	case base != "":
		ed.a.setStatus(fmt.Sprintf("▼ %s – %s is %s's sound — ⧉ Copy takes those seconds of it "+
			"and ⧉ Paste lays them over the footage without costing the video a frame; "+
			"＋ Add is the picture's verb and waits for ▲",
			mmss(a), mmss(b), base))
	case ed.sel.pic:
		ed.a.setStatus(fmt.Sprintf("▲▲ %s – %s is the picture alone — ⧉ Insert lays a clip over "+
			"these frames and leaves what is heard running underneath, and one spliced in "+
			"goes silent. ▲ again for the sound filmed with them",
			mmss(a), mmss(b)))
	default:
		ed.a.setStatus(fmt.Sprintf("▲▼ %s – %s is footage — ＋ Add keeps it, the ✕ in a green "+
			"scene's corner drops one, ⧉ Copy takes the picture with the sound filmed with it",
			mmss(a), mmss(b)))
	}
	ed.redrawTracks()
}

// scopeClicked answers a press on the strip. ▲ is always towards the picture:
// from a lane it comes back to footage, and from footage it goes on up to the
// frames on their own. ▼ walks down and round (nextAud).
func (ed *cutEditor) scopeClicked(px, y float64) {
	switch ed.scopePartAt(px, y) {
	case scopeUp:
		ed.setSelScope("", ed.sel.aud == "" && !ed.sel.pic)
	case scopeDown:
		ed.setSelScope(ed.nextAud(ed.sel.aud), false)
	default:
		if !ed.sel.active {
			ed.a.setStatus("drag a region on the pictures or in a lane, then ▲▼ here says " +
				"which of the three it is about")
		}
	}
}

// hoverScope lights the half under the pointer, and takes the light away when
// it leaves (x below zero). The strip is small and its two halves are smaller,
// so which one a press would take has to be visible before the press.
func (ed *cutEditor) hoverScope(x, y float64) {
	part := scopeNone
	if x >= 0 {
		part = ed.scopePartAt(x+ed.viewX, y)
	}
	if part != ed.scopeHov {
		ed.scopeHov = part
		if ed.scopeArea != nil {
			ed.scopeArea.QueueDraw()
		}
	}
	name := "default"
	if part != scopeNone {
		name = "pointer"
	}
	ed.setCursor(ed.scopeArea, name)
}

// drawScope paints the strip: the seam, and the selection's handle on it.
func (ed *cutEditor) drawScope(cr *cairo.Context, w, h int) {
	cr.SetSourceRGB(0.13, 0.13, 0.13)
	cr.Rectangle(0, 0, float64(w), float64(h))
	cr.Fill()
	if len(ed.vids) == 0 {
		return
	}
	vx0, vx1 := ed.viewX, ed.viewX+float64(w)
	cr.Save()
	cr.Translate(-ed.viewX, 0)
	// the seam itself, drawn only under the recordings, so an empty strip reads
	// as a row that could hold something rather than a gap between two widgets
	cr.SetSourceRGB(0.155, 0.155, 0.165)
	for _, v := range ed.vids {
		cr.Rectangle(v.pxOrigin, 0, v.dur*ed.pps, scopeH)
	}
	cr.Fill()

	x0, x1, ok := ed.scopeBoxPx()
	if !ok || x1 < vx0 || x0 > vx1 {
		cr.Restore()
		return
	}
	half := scopeH / 2
	// Both halves are always drawn, and the live ones wear the selection's own
	// blue: the strip has to say what the selection IS as well as offer the
	// other answers, and a handle that showed only the choice not taken would
	// read as a button rather than as a switch.
	//
	// So an arrow is lit when its half is IN the selection, and on the middle
	// rung both are. That is not "nothing chosen" -- it is the answer, drawn as
	// what it means, and it is the one picture the two narrow rungs cannot be
	// mistaken for since each of those has an arrow dark.
	for i, up := range []bool{true, false} {
		y := float64(i) * half
		lit := (up && !ed.selSnd()) || (!up && !ed.selPic())
		switch {
		case lit:
			cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.85)
		case ed.scopeHov == scopeUp && up, ed.scopeHov == scopeDown && !up:
			cr.SetSourceRGB(0.3, 0.3, 0.34)
		default:
			cr.SetSourceRGB(0.22, 0.22, 0.25)
		}
		cr.Rectangle(x0, y, x1-x0, half)
		cr.Fill()
		// the arrow, as a path: at this size a glyph from whatever font the
		// theme lands on is a smudge, and these two have to be told apart at a
		// glance
		if lit {
			cr.SetSourceRGB(1, 1, 1)
		} else {
			cr.SetSourceRGBA(1, 1, 1, 0.45)
		}
		cx, cy, tip := x0+13, y+half/2, 3.5
		if !up {
			tip = -tip
		}
		cr.MoveTo(cx, cy-tip)
		cr.LineTo(cx+6, cy+tip)
		cr.LineTo(cx-6, cy+tip)
		cr.ClosePath()
		cr.Fill()
	}
	// the hairline between the halves, so the handle reads as two targets and
	// not as one block with two pictures on it
	cr.SetSourceRGBA(0, 0, 0, 0.5)
	cr.Rectangle(x0, half-0.5, x1-x0, 1)
	cr.Fill()
	// what it is pointing at, outside the handle rather than in it: a word in
	// half a strip would be a smudge at any height the seam can afford, and the
	// name answers "which lane", which is a question about the lanes below
	cr.SetFontSize(9)
	cr.SetSourceRGBA(1, 1, 1, 0.7)
	cr.MoveTo(x1+6, scopeH/2+4)
	cr.ShowText(ed.scopeName())
	cr.Restore()
}

// fitScope shows the strip whenever there is footage under it to be a scope of.
//
// It waited on the lanes for a while, on the argument that the handle's job was
// to name WHICH sound and a session with one had nothing to name. The third
// rung ended that argument: the picture alone is a scope every session has, and
// a capture with no sound anywhere -- no lanes, no strip -- was the one session
// that could not reach it and the one most likely to want it, since an inserted
// sting has nothing to be heard over.
//
// It is also where a selection that named a recording the session no longer has
// comes back to the footage. The lanes are rebuilt from what is on disk, a
// recording can go away between one reload and the next, and a selection
// pointing at one nobody has is a selection whose every verb would miss.
func (ed *cutEditor) fitScope() {
	if ed.sel.aud != "" && ed.audByBase(ed.sel.aud) == nil {
		ed.sel.aud, ed.sel.pic = "", false
		ed.syncSelBtns()
	}
	if ed.scopeArea == nil {
		return
	}
	ed.scopeArea.SetVisible(len(ed.vids) > 0)
}

// newScopeArea builds the strip and wires the one gesture it answers.
func (ed *cutEditor) newScopeArea() *gtk.DrawingArea {
	area := gtk.NewDrawingArea()
	area.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawScope(cr, w, h)
	})
	area.SetSizeRequest(-1, int(scopeH))
	area.SetHExpand(true)
	area.SetVisible(false) // until reload finds footage to put it under
	click := gtk.NewGestureClick()
	click.ConnectPressed(func(_ int, x, y float64) {
		ed.scopeClicked(x+ed.viewX, y)
	})
	area.AddController(click)
	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(func(x, y float64) { ed.hoverScope(x, y) })
	motion.ConnectLeave(func() { ed.hoverScope(-1, -1) })
	area.AddController(motion)
	return area
}
