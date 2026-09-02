package main

// Which seconds of a recording a voice reference is cut from.
//
// IndexTTS2 takes its whole idea of a speaker from this one file, so what goes
// into it decides what every narrated line sounds like. The choice used to be
// made from the diarization alone: the dominant speaker's longest turns, clear
// of everyone else. That finds where somebody HELD the floor, which is not the
// same as where somebody SPOKE -- a twelve-second turn can be a laugh, a
// breath, "…yeah", and a long think, and the model given those twelve seconds
// clones a person who hardly talks.
//
// The transcript is the part that knows. Its rows are the words with the
// seconds they were said in, so a stretch can be asked how much was actually
// said in it, and the takes with the most speech in them win. It trims too: a
// diarization turn opens and closes on the edges of a voice activity detector,
// and the words inside it start later and stop earlier than that, so the take
// is narrowed to the first word and the last rather than to the silence around
// them.
//
// The transcript is not required. Every scoring rule below reads zero words
// when there is none, the density test then admits nothing, and the fallback
// is the ranking this had before -- longest first, which is what a project
// with no inputs/ transcript still deserves.

import (
	"sort"
	"strings"
)

const (
	// refMinLen is the shortest take worth having. Under about five seconds
	// there is not enough of a voice in it to be one.
	refMinLen = 5.0
	// refPad is how far another speaker must stay from a take on either side.
	// Diarization edges are approximate, and a syllable of the wrong person
	// bleeding into the reference is a syllable the clone learns.
	refPad = 2.0
	// refWant is how much reference the model is given, and refTakeMax how
	// many pieces it may be assembled from. More than this stops helping;
	// fewer, longer pieces are steadier than many short ones.
	refWant    = 14.0
	refTakeMax = 3
	// refMinRate is how much speech a take has to hold to count as speech:
	// words per second. Ordinary talk runs near 2.5, so this admits somebody
	// speaking slowly or leaving pauses and turns away the stretch that is
	// mostly not talking -- including the credits ASR invents over silence,
	// which are a handful of words spread over a long quiet.
	refMinRate = 1.5
)

// refTake is one stretch worth cloning: the seconds it covers, and how much
// was said in them.
type refTake struct {
	s, e  float64
	words int
}

func (t refTake) dur() float64 { return t.e - t.s }

// domSpeaker is whoever did most of the talking, by total time. A voice
// recording is one person plus whatever the room leaked into it, so this is
// the person, and everyone else is the leak.
func domSpeaker(turns []span) string {
	by := map[string]float64{}
	for _, t := range turns {
		by[t.slot] += t.e - t.s
	}
	dom, best := "", 0.0
	for s, d := range by {
		if d > best {
			dom, best = s, d
		}
	}
	return dom
}

// refCuts ranks the stretches of a recording a reference should be cut from,
// best first. turns is the diarization, rows the transcript of the same
// recording on the same clock; rows may be empty.
func refCuts(turns []span, rows []seg4) []refTake {
	dom := domSpeaker(turns)
	if dom == "" {
		return nil
	}
	var cand []refTake
	for _, t := range turns {
		if t.slot != dom || !soloTurn(turns, t, dom) {
			continue
		}
		// no length test on the turn itself: spokenIn only ever narrows it,
		// so the take being long enough says the turn was
		if c := spokenIn(t, rows); c.dur() >= refMinLen {
			cand = append(cand, c)
		}
	}
	// The takes somebody is speaking through. With no transcript none of them
	// qualifies and the whole set stays, ranked below by length alone -- and
	// the same happens for a speaker so slow that nothing clears the bar,
	// which is a thin reference but a better one than none.
	var spoke []refTake
	for _, c := range cand {
		if float64(c.words) >= refMinRate*c.dur() {
			spoke = append(spoke, c)
		}
	}
	if len(spoke) > 0 {
		cand = spoke
	}
	// most said first, and length breaks the tie the no-transcript case makes
	// of every comparison
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].words != cand[j].words {
			return cand[i].words > cand[j].words
		}
		return cand[i].dur() > cand[j].dur()
	})
	return cand
}

// soloTurn is whether a turn has the recording to itself, refPad included.
func soloTurn(turns []span, t span, dom string) bool {
	for _, o := range turns {
		if o.slot != dom && o.e > t.s-refPad && o.s < t.e+refPad {
			return false
		}
	}
	return true
}

// spokenIn narrows a turn to the words inside it and counts them. A row counts
// when its middle is in the turn: transcript rows run up to mergeMaxLen and
// diarization edges are approximate, so a row that begins before a turn and
// ends inside it is one sentence landing across the boundary, and it belongs
// to whichever side holds most of it rather than to both. The rows are not
// checked against the speaker: the turn is already this speaker's and already
// clear of everyone else, so what the transcript puts inside it is theirs by
// construction.
func spokenIn(t span, rows []seg4) refTake {
	out := refTake{s: t.s, e: t.e}
	first, last := 0.0, 0.0
	for _, r := range rows {
		if mid := (r.s + r.e) / 2; mid < t.s || mid >= t.e {
			continue
		}
		// a row with no words in it is not the end of the speech: letting it
		// through would stretch the take out over the silence it stands for
		n := len(strings.Fields(r.text))
		if n == 0 {
			continue
		}
		// clamped, because a row counted by its middle can still begin before
		// the turn or end after it, and the seconds outside are the pad
		if out.words == 0 {
			first = max(r.s, t.s)
		}
		last = min(r.e, t.e)
		out.words += n
	}
	if out.words > 0 {
		out.s, out.e = first, last
	}
	return out
}
