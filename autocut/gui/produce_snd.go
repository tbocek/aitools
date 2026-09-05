package main

// The render's half of what the sound does over a speed effect.
//
// Three of the five answers are a filter and nothing else: the sound goes with
// the picture by time-stretch (atempoChain, which is what it has always done),
// by tape speed (asetrateChain), or not at all (hushCues). The other two are a
// plan -- the sound comes away from the picture and has to be told, clip by
// clip, which second of which recording it is reading and for how long.
//
// That plan is possible at all because of where the boundaries fall. A speed
// effect at any rate but ×1 cuts the segment list at its own edges (applyFx),
// a scene is never straddled by a clip, and a clip is one encode: so "the
// sound is off the picture's clock here" is always a statement about whole
// clips, and a clip's sound is just another input read from another second.
//
// What it cannot be is a crossfade at the seam. Clips are encoded one at a
// time and joined with a stream copy, so nothing crosses the join -- the dip
// is half of one at the end of one clip and half at the head of the next
// (sndDip, cut_fxsound.go).

import (
	"fmt"
	"math"
)

// hushCues is the seconds of one clip a speed effect asks to be silent, in the
// clip's own output seconds. Mapped exactly as a volume effect's band is
// (gainCues), so a stop and a title placed at the same second come and go
// together.
//
// A window rather than a property of the clip, because the two rates that do
// not cut the segment list -- ×1, and a stop, whose footage runs at ×1
// underneath -- have bands that lie INSIDE a clip. Silencing the clip would
// silence seconds the effect never covered.
func hushCues(fx []cutFx, sessS, span, rate, length float64) []textCue {
	if length <= 0 || span <= 0 {
		return nil
	}
	var out []textCue
	for i, f := range fx {
		if f.Kind != "speed" || f.sound() != sndMute {
			continue
		}
		s, e, _, _, ok := cueClip(f.T, f.Dur, 0, 0, sessS, rate, length)
		if !ok {
			continue
		}
		out = append(out, textCue{fx: f, s: s, e: e, idx: i})
	}
	return out
}

// hushExpr is every silent window of a clip as one enable expression, in the
// clip's own seconds -- the shape stillMute already builds for a stop, which
// this is the general case of. Empty when nothing is silenced.
func hushExpr(hushes []textCue, stills []stillCue) string {
	expr := stillMute(stills)
	for _, h := range hushes {
		if h.e <= h.s {
			continue
		}
		if expr != "" {
			expr += "+"
		}
		expr += fmt.Sprintf("between(t,%.3f,%.3f)", h.s, h.e)
	}
	return expr
}

// asetrateChain is the sound going with the picture by tape speed: the samples
// are simply declared to arrive faster, so the pitch rides the rate -- the
// fast-forward everybody knows the sound of. aresample puts the rate back
// afterwards, because everything downstream of here is 48 kHz.
//
// atempoChain is the other half of the same question and needs a chain of
// filters to get past its own limits; this one has no limits to get past.
func asetrateChain(rate float64) string {
	if rate <= 0 || rate == 1 {
		return ""
	}
	return fmt.Sprintf(",asetrate=48000*%g,aresample=48000", rate)
}

// audioPlan walks the clips and marks the ones whose sound has come away from
// the picture: where it is read from, how far the run reaches, and where the
// splice that closes it falls.
//
// The walk carries one running read-head. It opens where an effect asking for
// 1× sound starts -- in sync, at that clip's own first second -- and advances
// by each clip's SCREEN length, because that is all the time the sound has.
// The picture meanwhile advances by the footage each clip covers, which under
// a ×4 is four times as much: that difference is the gap, and where it closes
// is the answer the effect carries.
//
//	sndFx     the run is that effect's clips and no more
//	sndScene  it runs to the end of the scene the effect is in
//
// A card, a held frame or a clip on no recording closes a run wherever it
// falls: a card brings its own sound, and a run that carried on across one
// would be reading a file nothing on screen came from.
func audioPlan(clips []prodClip, fx []cutFx) {
	run, open := 0, false
	var mode, path string
	var fxT, at, sess float64
	var scene int
	for i := range clips {
		c := &clips[i]
		playable := c.ins == "" && !c.freeze && c.video != nil
		// what the effect covering this clip says. The clip's own middle, so
		// a band that starts exactly where the clip does and one that ends
		// there are both read as this clip's rather than the neighbour's.
		mid := c.sessS + c.length*c.speed()/2
		kind, f, has := sndAt(fx, mid)
		if open {
			switch {
			case !playable:
				open = false
			case mode == sndFx:
				open = has && math.Abs(f.T-fxT) < 1e-9
			default:
				open = c.scene == scene
			}
		}
		if !open && playable && sndShifts(kind) && math.Abs(sndDebt(f)) >= 0.05 {
			// a ×1 with one of these answers on it has nothing to come apart,
			// and a run that opens with no gap is a run that only complicates
			// the graph
			open, mode, fxT, scene = true, kind, f.T, c.scene
			path, at, sess = c.video.path, c.video.at(c.sessS), c.sessS
			run++
		}
		if !open {
			continue
		}
		c.audOwn, c.audRun, c.audPath, c.audAt, c.audSess = true, run, path, at, sess
		c.audPitch = false // it is at 1×: there is no rate for a pitch to ride
		at += c.length     // the sound reads on at its own speed
		sess += c.length
	}
	// the pitch answer, which is a filter and not a plan -- and the splices,
	// which are where a run stops being true
	for i := range clips {
		c := &clips[i]
		if !c.audOwn {
			if k, _, has := sndAt(fx, c.sessS+c.length*c.speed()/2); has && k == sndPitch {
				c.audPitch = true
			}
			continue
		}
		if i+1 < len(clips) && clips[i+1].audRun == c.audRun {
			continue // the run carries on: no seam here
		}
		// the sound jumps at this clip's end -- forward over what it never
		// reached, or back over what it has already played -- so it fades out
		// here and the clip that follows fades in
		c.audOut = true
		if i+1 < len(clips) {
			clips[i+1].audIn = true
		}
	}
}

// audDipChain is the two half-fades, in the clip's own seconds. in is the
// label the bed arrives on and the label it leaves on comes back, so a clip
// with no seam at either end costs nothing at all.
func audDipChain(c prodClip, in string) (string, string) {
	var parts []string
	if c.audIn {
		parts = append(parts, fmt.Sprintf("afade=t=in:st=0:d=%.3f", sndDip))
	}
	if c.audOut && c.length > sndDip {
		parts = append(parts, fmt.Sprintf("afade=t=out:st=%.3f:d=%.3f", c.length-sndDip, sndDip))
	}
	if len(parts) == 0 {
		return "", in
	}
	out := "dip"
	fc := "[" + in + "]" + parts[0]
	for _, p := range parts[1:] {
		fc += "," + p
	}
	return fc + "[" + out + "];", out
}
