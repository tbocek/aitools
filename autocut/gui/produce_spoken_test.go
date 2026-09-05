package main

// Captions from what was said.
//
// "Put the speaker's words on screen" was being asked of the cut step as one
// text effect per line of speech -- hundreds of them -- and the answer ran into
// the token ceiling three attempts out of three. The transcript is already
// there, cleaned by Prepare's fix step, on the corrected clock; putting it on
// the finished video is arithmetic the app does, not a job for a model.

import (
	"strings"
	"testing"
)

func TestCaptionsFromWhatWasSaidLandInTheClipsOwnSeconds(t *testing.T) {
	c := prodClip{sessS: 100, length: 30, tempo: 1, rate: 2} // 60 s of session at double speed
	rows := []tsvRow{
		{s: 90, e: 95, spk: "SPEAKER_01", text: "before the clip"},
		{s: 110, e: 114, spk: "SPEAKER_01", text: "inside it"},
		{s: 120, e: 121, spk: "EVENT", text: "the picture, not a caption"},
		{s: 150, e: 170, spk: "NARRATOR", text: "straddles the cut"},
		{s: 130, e: 130.1, spk: "SPEAKER_01", text: "a flash"},
		{s: 200, e: 205, spk: "SPEAKER_01", text: "after it"},
	}
	caps := captionLines(c, 100, 160, rows, "spoken")
	if len(caps) != 2 {
		t.Fatalf("got %d captions, want the two spoken inside the clip: %+v", len(caps), caps)
	}
	// 110-114 at double speed: 5 s in, 2 s long
	if caps[0].text != "inside it" || caps[0].at != 5 || caps[0].dur != 2 || caps[0].wav != "" {
		t.Errorf("the line inside the clip came out %+v", caps[0])
	}
	// 150-170 clipped to the clip's end at 160: 25 s in, 5 s long
	if caps[1].text != "straddles the cut" || caps[1].at != 25 || caps[1].dur != 5 {
		t.Errorf("the line across the cut came out %+v", caps[1])
	}
	// the narration's own lines are what the track carries otherwise
	c.lines = []prodLine{{text: "the writer's line", at: 3, delay: 3}}
	if got := captionLines(c, 100, 160, rows, ""); len(got) != 1 || got[0].text != "the writer's line" {
		t.Errorf("with subtitles from the narration the track carries %+v", got)
	}
	// the setting is a stored field, absent in every older project, which
	// reads as the narration
	var st prodSettings
	if err := st.UnmarshalJSON([]byte(`{"subs":"burn"}`)); err != nil || st.SubsFrom != "" {
		t.Errorf("an older project's settings read as subs_from %q, want the narration", st.SubsFrom)
	}
	// both subtitle writers read the track through captionLines, and both read
	// the window the SOUND covers -- which on a clip whose sound has come away
	// from the picture is not the seconds the picture shows (cut_fxsound.go)
	src := readSrc(t, "produce.go")
	if n := strings.Count(src, "captionLines(c, "); n != 2 {
		t.Errorf("%d subtitle writers read their cues through captionLines, want 2", n)
	}
	if n := strings.Count(src, "c.audSess, c.audSess+c.length"); n < 2 {
		t.Error("a subtitle writer still takes its window off the picture's clock")
	}
	// and the placement inside the clip is on the sound's clock too
	body := funcBody(t, "produce.go", `func captionLines\(`)
	if !strings.Contains(body, "if c.audOwn {\n\t\t\trate = 1") {
		t.Error("captions on a shifted clip are still divided by the picture's rate")
	}
}
