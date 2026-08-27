package main

// Which recording a session-second belongs to.
//
// One question, asked from two places that must not disagree: the preview cues
// a file and an offset from it (cut.go), and the render walks a snapshot of the
// same timeline with no editor behind it (produce.go). A cut made while looking
// at one clip and rendered as the other is the sort of mistake that is only
// noticed in the finished file, so both go through pickVideo and this is what
// pickVideo promises.

import "testing"

// The seam is the whole of it. Two recordings laid end to end share an instant
// -- the second one's first frame and the second past the first one's last are
// the same session-second -- and exactly one of them has to own it.
//
// It is the one starting there. Half-open, [start, start+dur): closing the far
// end instead would hand the seam to the recording ENDING on it, and a preview
// cued at second `dur` of a file is cued one frame past its last, which is
// black. What anyone scrubbing onto a seam wants to see is the next clip.
func TestASeamBelongsToTheRecordingThatStartsOnIt(t *testing.T) {
	vids := []tlVideo{
		{base: "a", start: 0, dur: 60},
		{base: "b", start: 60, dur: 30},  // butted straight onto a
		{base: "c", start: 100, dur: 10}, // ...and after a ten-second gap
	}
	for _, c := range []struct {
		t    float64
		want string // "" means no recording is playing then
	}{
		{0, "a"},    // the very first instant is inside the first recording
		{59.9, "a"}, // ...and so is the last one before the seam
		{60, "b"},   // the seam itself: the one starting here, not the one ending
		{89.9, "b"},
		{90, ""},   // the gap between two recordings is nobody's
		{95, ""},   // ...for all of it
		{100, "c"}, // and picks up again on the far side
		{110, ""},  // past the end of the last: the session is over
		{-1, ""},   // and before the start of the first
	} {
		got := pickVideo(vids, c.t)
		name := ""
		if got != nil {
			name = got.base
		}
		if name != c.want {
			t.Errorf("second %g plays %q, want %q", c.t, name, c.want)
		}
	}

	// no footage at all is a question with an answer, not a crash: the Cut page
	// is reachable before a reload has found anything to put on it
	if pickVideo(nil, 0) != nil {
		t.Error("a session with no recordings claims to be playing one")
	}

	// a zero-length recording is empty on both sides of its own start, or the
	// seam rule would make it swallow the instant the next one begins on
	if v := pickVideo([]tlVideo{{base: "z", start: 5}, {base: "y", start: 5, dur: 1}}, 5); v == nil ||
		v.base != "y" {
		t.Error("a zero-length recording took the second the one after it starts on")
	}

	// and the editor asks the same question of its own timeline -- one
	// implementation, so the preview cannot drift from the render
	ed := &cutEditor{vids: vids}
	if v := ed.videoAt(60); v == nil || v.base != "b" {
		t.Error("videoAt disagrees with pickVideo about the seam")
	}
}
