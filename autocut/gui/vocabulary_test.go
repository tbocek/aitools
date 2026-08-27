package main

// One vocabulary across the five prompts.
//
// Every step hands a model a list of things that happened, and every step used
// to name them differently: the describing step labelled a line with the file
// it came off, the fixer with "[heard in NAME]", the cut with "[base
// SPEAKER_00]", the narration with "HEARD" and "ON SCREEN". Each was clear on
// its own and none of them was wrong. Together they were four formats to learn
// on a 27B model that has no room to spare for that, and -- worse -- nothing to
// carry between the steps: a rule about NARRATOR lines in the narration prompt
// could not refer to anything the cut had ever seen.
//
// So there are three words, and the same function decides them everywhere:
//
//	EVENT       the picture
//	NARRATOR    the narrator's own microphone, which the render never plays
//	SPEAKER_nn  a voice the render does play
//
// These tests are what keeps that true. A prompt is a const in one file and the
// input it describes is built in another, and nothing at run time notices they
// have drifted apart -- the model just gets quietly worse.

import (
	"os"
	"strings"
	"testing"
)

// The one function, used by all three renderers. A row means the same thing in
// the describing step's speech block, in the session timeline the cut reads and
// in the narration brief, so it has to come out labelled the same in all three.
func TestOneRowIsLabelledTheSameEverywhere(t *testing.T) {
	rows := []tsvRow{
		{s: 10, e: 12, spk: "EVENT", text: "a chest sits on the sand", src: "capture-0"},
		{s: 13, e: 14, spk: "SPEAKER_01", text: "open this chest", src: "capture-0"},
		{s: 15, e: 16, spk: "SPEAKER_00", text: "Open up, FBI.", src: "his-own-mic"},
	}
	const narr = "his-own-mic"

	timeline := sessionText(rows, narr)
	brief := clipBriefs([]cutSeg{{S: 10, E: 20}}, rows, narr)
	var speech []speechSrc
	for _, src := range []string{"capture-0", "his-own-mic"} {
		var mine []tsvRow
		for _, r := range rows {
			if r.src == src && r.spk != "EVENT" {
				mine = append(mine, r)
			}
		}
		speech = append(speech, speechSrc{label: src, rows: mine})
	}
	chunk := speechBlock(speech, narr, 10, 20)

	for _, c := range []struct{ where, text string }{
		{"the session timeline", timeline},
		{"the narration brief", brief},
	} {
		for _, want := range []string{"EVENT: a chest sits on the sand",
			"SPEAKER_01: open this chest", "NARRATOR: Open up, FBI."} {
			if !strings.Contains(c.text, want) {
				t.Errorf("%s does not say %q:\n%s", c.where, want, c.text)
			}
		}
	}
	for _, want := range []string{"SPEAKER_01: open this chest", "NARRATOR: Open up, FBI."} {
		if !strings.Contains(chunk, want) {
			t.Errorf("the describing step's speech block does not say %q:\n%s", want, chunk)
		}
	}
	// and the retired spellings are gone from all of them: two names for one
	// thing is the whole fault this guards
	for _, gone := range []string{"HEARD", "ON SCREEN", "not in the video's audio",
		"his-own-mic:", "capture-0:", "[his-own-mic ", "[capture-0 "} {
		for _, s := range []string{timeline, brief, chunk} {
			if strings.Contains(s, gone) {
				t.Errorf("a rendered line still says %q:\n%s", gone, s)
			}
		}
	}
}

// Every prompt names the three labels, and none of them describes the input in
// the words it used to be described in. A prompt the user has overridden is
// their business; this is about the ones shipped.
func TestEveryPromptDescribesTheSameThreeLabels(t *testing.T) {
	for _, p := range []struct {
		name, text string
	}{
		{"describe", describeSystem},
		{"fix", fixSystem},
		{"cut", suggestSystem},
		{"audit", auditSystem},
		{"narrate", narrSystem},
	} {
		for _, want := range []string{"EVENT", "NARRATOR", "SPEAKER_"} {
			if !strings.Contains(p.text, want) {
				t.Errorf("the %s prompt never mentions %s, so it describes an input "+
					"nobody builds", p.name, want)
			}
		}
		for _, gone := range []string{"[on screen", "[heard in", "ON SCREEN", "HEARD SPEAKER"} {
			if strings.Contains(p.text, gone) {
				t.Errorf("the %s prompt still describes its input as %q", p.name, gone)
			}
		}
	}
}

// The describing step is the one place where a stamp could come off a different
// clock: its request carries frames, speech and the events it wrote for the
// chunks before, and the last of those used to arrive stamped with its absolute
// time in the video while everything else was relative to the first frame. The
// prompt says "everything is on one clock", and a model that believes it reads
// [1836.00s] as thirty minutes into the future.
func TestTheDescribeRequestIsAllOnOneClock(t *testing.T) {
	b, err := os.ReadFile("describe.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `"[%+.0fs] EVENT: %s\n", r.s-t0`) {
		t.Error("the events from earlier chunks are no longer stamped relative to this one")
	}
	if !strings.Contains(src, `"[%+.1fs] FRAME %d of %d"`) {
		t.Error("the frame markers no longer carry a stamp on the same clock as the speech")
	}
	if !strings.Contains(describeSystem, "on one clock") {
		t.Error("the prompt no longer promises one clock, which the request goes to some trouble to keep")
	}
}

// The cut reads the timeline out of session.tsv and renders it here, rather
// than reading the rendered session.txt off disk. That is what lets the labels
// change without a project having to re-run an hour of speech through the fixer
// to see them -- and it is also why session.txt is written by the same
// function: what the user reads is then what the model reads.
func TestTheCutRendersTheTimelineItSends(t *testing.T) {
	cut, err := os.ReadFile("cut_suggest.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cut), `"session.txt"`) {
		t.Error("the cut step is reading session.txt again -- an old project's file is in the old format")
	}
	if !strings.Contains(string(cut), "sessionText(rows, a.narratorMic())") {
		t.Error("the cut no longer renders the timeline from session.tsv")
	}
	tr, err := os.ReadFile("transcript.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tr), "sessionText(tl, a.narratorMic())") {
		t.Error("session.txt is written some other way than the string the cut is sent")
	}
}
