package main

// Which lanes a scene is heard on.
//
// This used to be one answer for the whole cut -- ed.snd, walked with ↑ and ↓ --
// and one answer is the wrong shape for the question. A session is a game
// capture with its own sound, a microphone on the desk, and whatever else was
// running; the scene where somebody is talking wants the microphone, the scene
// where nobody is wants the game, and the scene where both matter wants both.
// A single choice made every one of those the same, and the arrows that walked
// it were invisible: there was nothing on the page saying they existed, and
// nothing but a word on one plate saying what they had done.
//
// So the choice moved onto the lanes, per scene. Take a scene in hand and every
// audio lane says whether that scene hears it -- green for yes, grey for no --
// and carries a badge that turns it off and on. Two lanes on is a legal answer
// and a common one: they are summed at the level each was recorded at, the way
// a hand on a desk would do it, with a limiter on the finished clip so the sum
// cannot reach the encoder clipping (clipCeil, produce.go).
//
// The badge sits at the LEFT of the held scene, where the ✕ that drops the
// scene sits at the right: they are the two things you do to a scene you are
// pointing at, and putting them at opposite ends means neither can be pressed
// by mistake for the other. Both kinds of lane wear one -- the camera's own
// sound, drawn under its pictures, and every separately recorded lane in the
// band below -- because "which of these do I hear" is one question and the page
// draws the answers in two places only by accident of layout.

import (
	"fmt"
	"math"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	hearR   = 4.5  // the speaker cone, from its centre
	hearPad = 3.5  // the plate's edge, beyond it
	hearHit = 10.0 // and the target, bigger than either
	hearIn  = 12.0 // its centre, in from the held scene's LEFT border
	// under this a scene has no room to wear one, for the same reason as
	// segKillMin: the target reaches hearIn+hearHit in from the left border,
	// the ✕ reaches as far in from the right, and a scene narrower than the
	// two together has no plain timeline left in the middle to press.
	hearMin = (hearIn + hearHit) + (segKillIn + segKillHit) + 6
)

// hearBadge is one lane's answer for the held scene, and where it is drawn.
//
// h is the whole RECORDING's depth and not one channel's: a stereo microphone
// is two waveforms and one answer, and a wash that covered only the first would
// say the second is a lane of its own that nobody has decided about yet.
type hearBadge struct {
	base   string
	cx, cy float64 // timeline x, and the middle of the lane in area y
	h      float64 // how deep the lane is, all its channels together
	on     bool    // this scene hears this lane
}

// hearX is where the badges for the held scene go, and whether it is wide
// enough to wear any. Timeline px, like everything else drawn in a translated
// area.
func (ed *cutEditor) hearX() (float64, bool) {
	s := ed.heldSeg()
	if s == nil || s.isInsert() {
		// an insert brings its own sound or replaces the lane it was laid in
		// (dropLane), and neither is a question about which lanes are heard
		return 0, false
	}
	x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
	if x1-x0 < hearMin {
		return 0, false
	}
	return x0 + hearIn, true
}

// hearBadgesAud is the badges in the recorders' band, walking exactly the
// layout drawAudio draws so the hand lands where the eye is pointing. One per
// RECORDING and not per channel: a stereo lane is one microphone.
func (ed *cutEditor) hearBadgesAud() []hearBadge {
	cx, ok := ed.hearX()
	if !ok {
		return nil
	}
	s := ed.heldSeg()
	var out []hearBadge
	y := wavePad
	for _, au := range ed.sepAuds() {
		h := float64(ed.lanes(au)) * waveLaneH
		out = append(out, hearBadge{au.base, cx, y + h/2, h, s.hears(au.base)})
		y += h + waveGap
	}
	return out
}

// hearBadgesSrc is the badges on the paired strips, in the picture band. Only
// the camera the scene is SHOWN from wears one: the render takes a clip's own
// sound off the recording it is cut from (pickVideoOn), so silencing another
// row's camera would be a control that does nothing to this scene.
func (ed *cutEditor) hearBadgesSrc() []hearBadge {
	cx, ok := ed.hearX()
	if !ok {
		return nil
	}
	s := ed.heldSeg()
	v := pickVideoOn(ed.vids, s.Cam, s.S)
	au := ed.pairAud(v.base)
	if v == nil || au == nil {
		return nil // no footage here, or a camera that filmed no sound
	}
	h := float64(ed.lanes(*au)) * waveLaneH
	return []hearBadge{{v.base, cx, ed.laneTop(v.lane) + ed.laneH() + h/2, h, s.hears(v.base)}}
}

// hearAt is the lane whose badge is under a press, or "". src says which area
// the press was in, because the two draw their lanes in different y.
func (ed *cutEditor) hearAt(px, y float64, src bool) string {
	badges := ed.hearBadgesAud()
	if src {
		badges = ed.hearBadgesSrc()
	}
	for _, b := range badges {
		if math.Abs(px-b.cx) <= hearHit && math.Abs(y-b.cy) <= hearHit {
			return b.base
		}
	}
	return ""
}

// toggleHear turns one lane off or on for the held scene.
//
// A FRESH list every time, never an append into the one that is there: the undo
// snapshot copies the segment slice but not the strings inside it, so growing
// the held scene's list in place would silently rewrite every snapshot holding
// the same one -- and Undo would put back the state it was already in.
func (ed *cutEditor) toggleHear(base string) {
	s := ed.heldSeg()
	if s == nil || base == "" {
		return
	}
	ed.pushUndo()
	var next []string
	for _, q := range s.Quiet {
		if q != base {
			next = append(next, q)
		}
	}
	on := len(next) != len(s.Quiet) // it was silent, and dropping it turns it on
	if !on {
		next = append(next, base)
	}
	s.Quiet = next
	ed.persist()
	ed.redrawTracks()
	word := "silent in"
	if on {
		word = "heard in"
	}
	ed.a.setStatus(fmt.Sprintf("%s is %s the scene at %s — every lane still green is "+
		"mixed at the level it was recorded at, and the finished clip is kept off the "+
		"ceiling so two at once cannot clip", base, word, mmss(s.S)))
}

// drawHearBadges paints them, and the held scene's own stretch of each lane
// behind them: green where it is heard, grey where it is not. The wash is the
// part that works at any zoom -- a scene too narrow for a badge still says what
// it does -- and the badge is the part you can press.
//
// Called from inside each area's translation, so x is timeline px.
func (ed *cutEditor) drawHearBadges(cr *cairo.Context, badges []hearBadge, vx0, vx1 float64) {
	s := ed.heldSeg()
	if s == nil {
		return
	}
	x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
	for _, b := range badges {
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		if b.on {
			cr.SetSourceRGBA(0.2, 0.85, 0.35, 0.22)
		} else {
			cr.SetSourceRGBA(0.55, 0.55, 0.6, 0.3)
		}
		cr.Rectangle(x0, b.cy-b.h/2, x1-x0, b.h)
		cr.Fill()
	}
	for _, b := range badges {
		if b.cx < vx0-hearHit || b.cx > vx1+hearHit {
			continue
		}
		if b.on {
			cr.SetSourceRGBA(0.15, 0.65, 0.3, 0.95)
		} else {
			cr.SetSourceRGBA(0.06, 0.06, 0.07, 0.62)
		}
		cr.Arc(b.cx, b.cy, hearR+hearPad, 0, 2*math.Pi)
		cr.Fill()
		drawSpeaker(cr, b.cx, b.cy, b.on)
	}
}

// drawSpeaker is the mark on the plate: a cone, with two arcs coming off it
// when the lane is heard and a stroke through it when it is not. A path, like
// every other mark on this page -- a glyph would be the font's idea of a
// speaker at 9 px, which on some machines is a box.
func drawSpeaker(cr *cairo.Context, cx, cy float64, on bool) {
	cr.SetSourceRGBA(1, 1, 1, 0.92)
	cr.SetLineWidth(1.4)
	// the cone: a small square with a horn opening to the right
	cr.MoveTo(cx-hearR, cy-hearR/2.5)
	cr.LineTo(cx-hearR/2.5, cy-hearR/2.5)
	cr.LineTo(cx+hearR/4, cy-hearR)
	cr.LineTo(cx+hearR/4, cy+hearR)
	cr.LineTo(cx-hearR/2.5, cy+hearR/2.5)
	cr.LineTo(cx-hearR, cy+hearR/2.5)
	cr.ClosePath()
	cr.Fill()
	if on {
		for _, r := range []float64{hearR * 0.65, hearR * 1.05} {
			cr.Arc(cx+hearR/4, cy, r, -0.9, 0.9)
			cr.Stroke()
		}
		return
	}
	cr.MoveTo(cx+hearR*0.55, cy-hearR*0.55)
	cr.LineTo(cx+hearR*1.25, cy+hearR*0.55)
	cr.MoveTo(cx+hearR*1.25, cy-hearR*0.55)
	cr.LineTo(cx+hearR*0.55, cy+hearR*0.55)
	cr.Stroke()
}

// migrateSound reads a cut written when the sound was one choice for the whole
// project -- cutFile.Sound, a lane name, meaning "every scene is heard on this
// one" -- and says the same thing in the only way a cut now can: every scene
// silences every lane but that one.
//
// It runs once by construction. Nothing writes Sound any more, so the next save
// drops the field, and a project migrated cannot be migrated twice. A scene that
// already names silenced lanes is left exactly as it is: the only way one can
// exist is a file written after the move, and there the scenes are the truth
// and the old field is a leftover.
//
// One thing the old choice could do that no arrangement of silences can: put
// camera A's sound under camera B's picture. Only B's own track reaches a clip
// cut from B, so there is no lane there to leave audible. Those scenes keep
// their OWN camera rather than being migrated into silence -- a scene that
// sounds like the wrong camera is something anyone can hear and correct, and a
// scene that plays nothing reads as a broken render -- and the note says how
// many, because it is the one part of this that is not what the file asked for.
func migrateSound(segs []cutSeg, snd string, vids []tlVideo, auds []tlAudio) ([]cutSeg, string) {
	if strings.TrimSpace(snd) == "" || len(segs) == 0 {
		return segs, ""
	}
	// the chosen name read as a camera ROW and not as one file: a camera
	// stopped and started again is several recordings in a line, and the old
	// choice carried across them exactly as the picture does. Below zero means
	// it named a separately recorded lane instead, which is one file and needs
	// none of this.
	row := -1
	for _, v := range vids {
		if v.base == snd {
			row = v.lane
		}
	}
	out := append([]cutSeg(nil), segs...)
	crossed := 0
	for i := range out {
		if len(out[i].Quiet) > 0 {
			continue
		}
		keep := snd
		if row >= 0 {
			switch v := pickVideoOn(vids, out[i].Cam, out[i].S); {
			case v == nil:
				keep = ""
			case v.lane != row:
				keep, crossed = v.base, crossed+1
			default:
				keep = v.base
			}
		}
		var quiet []string
		for _, au := range auds {
			if au.base != keep {
				quiet = append(quiet, au.base)
			}
		}
		out[i].Quiet = quiet
	}
	note := fmt.Sprintf("this cut was heard on %s from end to end; that is now said scene "+
		"by scene, and every scene has been set to silence the other lanes", snd)
	if crossed > 0 {
		were := "scenes are"
		if crossed == 1 {
			were = "scene is"
		}
		note += fmt.Sprintf(" — except %d, which %s shown from another camera and left "+
			"hearing their own sound: one camera's sound under another's picture is no "+
			"longer something the render can do", crossed, were)
	}
	return out, note
}

// syncHush tells the preview what the scene under the playhead hears. It runs
// from showInsert, which is every path that moves the line: a click, a frame
// step, playback following its own clock, and the badge that changed the answer
// in the first place -- so the press is audible at once, and playback picks the
// new answer up at the boundary rather than carrying the old scene's lanes into
// the next one.
//
// Off the ends of the cut, and in a gap between kept scenes, everything is
// heard: what is playing there is not a scene and has no answer of its own,
// and the scrub through a cut-out stretch is worth hearing.
func (ed *cutEditor) syncHush() {
	if ed.player == nil {
		return
	}
	var s *cutSeg
	if i := ed.segAt(ed.playhead); i >= 0 {
		s = &ed.segs[i]
	}
	var base string
	if ed.playVideo != nil {
		base = ed.playVideo.base
	}
	ed.player.Hush(hushOf(s, base))
}

// hushOf is what a scene does not hear, in the two pieces the preview keeps its
// sound in: whether the recording it is playing the picture from is silenced --
// shown from the camera, heard from the microphone is a legal scene -- and the
// scene's own list, which names the recordings mixed under it.
//
// No scene, no answer: off the ends of the cut and in the gaps between kept
// scenes there is nothing holding an opinion, and a scrub through a cut-out
// stretch is worth hearing.
func hushOf(s *cutSeg, base string) (bool, []string) {
	if s == nil {
		return false, nil
	}
	return base != "" && laneQuiet(s.Quiet, base), s.Quiet
}
