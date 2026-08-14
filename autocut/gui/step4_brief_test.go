package main

// What the narration writer is actually told about a clip.
//
// This is the regression behind narration that reads well and describes
// something else: loadTSVRows took column 4 as the line, which is the line in a
// single recording's transcript and the SPEAKER_00/EVENT label in the merged
// session timeline. Step 4 reads the merged one, so every narration request was
// built from a column of the words "SPEAKER_00" and "EVENT" -- the model knew a
// clip's length and nothing whatever about what happened in it, and wrote what
// a session like that usually contains.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Both shapes this app writes, read by the one function. Four columns is a
// single recording (step1/<base>/transcript.tsv), five is the merged session
// timeline with the recording's name in between (step2/transcript/session.tsv).
func TestLoadTSVRowsReadsBothTimelineShapes(t *testing.T) {
	dir := t.TempDir()
	four := filepath.Join(dir, "transcript.tsv")
	if err := os.WriteFile(four, []byte(
		"4.40\t10.80\tSPEAKER_00\tOh my god. I'll be down.\n"+
			"17.60\t28.48\tSPEAKER_01\tCome back the way you came.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := loadTSVRows(four)
	if len(rows) != 2 {
		t.Fatalf("read %d rows from the four-column file, want 2", len(rows))
	}
	if rows[0].text != "Oh my god. I'll be down." || rows[0].spk != "SPEAKER_00" || rows[0].s != 4.40 {
		t.Errorf("row 0 = %+v", rows[0])
	}

	five := filepath.Join(dir, "session.tsv")
	if err := os.WriteFile(five, []byte(
		"742.00\t746.00\tgorillatag-0\tEVENT\tThe player leaps from a high vantage point.\n"+
			"748.00\t760.00\tgorillatag-0\tSPEAKER_00\tFind my three objects, those be mine.\n"+
			"765.60\t772.00\tmic-1\tSPEAKER_02\tIncrease your volume\tthen I'm recording.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows = loadTSVRows(five)
	if len(rows) != 3 {
		t.Fatalf("read %d rows from the session timeline, want 3", len(rows))
	}
	if rows[0].spk != "EVENT" {
		t.Errorf("the screen description came back as speaker %q", rows[0].spk)
	}
	if rows[0].text != "The player leaps from a high vantage point." {
		t.Errorf("row 0's line is %q -- the narration is being written from the label column", rows[0].text)
	}
	if rows[1].text != "Find my three objects, those be mine." {
		t.Errorf("row 1's line is %q", rows[1].text)
	}
	// a tab inside a line keeps the whole line, not its tail
	if rows[2].text != "Increase your volume\tthen I'm recording." {
		t.Errorf("a line containing a tab came back as %q", rows[2].text)
	}
}

// The brief has to carry three things the model cannot guess: the words, which
// of them are the picture rather than the talk, and WHEN inside the clip each
// one falls. The last is what stops a line about the pickaxe that comes out in
// the final two seconds from being written over the forty before it.
func TestClipBriefsCarryTheWordsTheKindAndTheTiming(t *testing.T) {
	rows := []tsvRow{
		{s: 700, e: 704, spk: "EVENT", text: "The camera swings across a beach."},
		{s: 754, e: 758, spk: "EVENT", text: "A glowing green figure floats near a treasure chest."},
		{s: 758, e: 762, spk: "SPEAKER_00", text: "there's not much time"},
		{s: 798, e: 802, spk: "EVENT", text: "The player swings a pickaxe at a green wall."},
		{s: 900, e: 904, spk: "EVENT", text: "Everyone regroups on the dock."},
	}
	segs := []cutSeg{{S: 751, E: 800}, {S: 880, E: 890}}
	got := clipBriefs(segs, []string{"", "keep this one short"}, rows, nil)

	for _, want := range []string{
		"CLIP 1: 751.0–800.0 (49 s, narration budget ~122 words)",
		"[+3s] ON SCREEN: A glowing green figure floats near a treasure chest.",
		// nothing said which recordings the video carries, so every line is
		// assumed audible: the assumption that cannot embarrass the narration
		"[+7s] HEARD SPEAKER_00: there's not much time",
		"[+47s] ON SCREEN: The player swings a pickaxe at a green wall.",
		"EDITOR'S NOTE for this clip -- follow it: keep this one short",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief is missing %q:\n%s", want, got)
		}
	}
	// far outside the clip is not this clip's material
	if strings.Contains(got, "swings across a beach") {
		t.Errorf("a line 47 s before the clip was handed to it:\n%s", got)
	}
	// ...and a clip with nothing over it says so, rather than leaving the model
	// to fill an unexplained silence out of the story so far
	clip2 := got[strings.Index(got, "CLIP 2"):]
	if !strings.Contains(clip2, "invent nothing") {
		t.Errorf("a clip with no material says nothing about it:\n%s", clip2)
	}
	// the offsets are what make the order legible, so they have to be there for
	// every line, not only the ones inside the clip
	if n := strings.Count(got, "[+"); n < 3 {
		t.Errorf("only %d stamped lines in:\n%s", n, got)
	}
}

// TestTheNarrationDoesNotTalkOverItself pins the one rule that follows from
// how step 5 mixes: the clip keeps its own audio under the voice-over, so a
// narration that quotes what is said in the clip is heard twice. The prompt
// used to ask for the opposite in as many words -- "reuse the speaker's own
// quotable lines VERBATIM" -- and wrote a narrator that read the transcript
// back over the people saying it. The two files are edited in different places
// and nothing at run time notices they disagree.
func TestTheNarrationDoesNotTalkOverItself(t *testing.T) {
	if strings.Contains(narrSystem, "VERBATIM") {
		t.Error("the prompt asks for the clip's own lines back, which the render then plays underneath them")
	}
	for _, want := range []string{"Never repeat a HEARD line", "the viewer hears it"} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the prompt never says %q, so nothing stops the narration repeating the clip", want)
		}
	}
	// ...and the render is what makes it true: the original is ducked, not
	// dropped. If this ever becomes a replace, the rule above is wrong.
	b, err := os.ReadFile("step5.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "[bg][nr]amix=inputs=2") {
		t.Error("the narration no longer mixes over the clip's own audio -- the prompt's " +
			"no-quoting rule was written for a mix and needs rereading")
	}
}

// The prompt describes the format the brief is written in. They are edited in
// different places -- one is a const, the other is a box in the project the
// user can rewrite -- so this pins the words that have to mean the same thing
// in both.
func TestTheNarratePromptDescribesTheBriefItGets(t *testing.T) {
	for _, want := range []string{"ON SCREEN", "HEARD SPEAKER_00",
		"SAID SPEAKER_00 (not in the video's audio)", "[+12s]"} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the narration prompt never mentions %q, so the model is left to "+
				"work out the shape of its own input", want)
		}
	}
	brief := clipBriefs([]cutSeg{{S: 0, E: 10}}, nil, []tsvRow{
		{s: 1, e: 2, spk: "EVENT", text: "x"},
		{s: 3, e: 4, spk: "SPEAKER_00", text: "y", src: "capture"},
		{s: 5, e: 6, spk: "SPEAKER_00", text: "z", src: "his-own-mic"},
	}, map[string]bool{"capture": true})
	for _, want := range []string{"ON SCREEN:", "HEARD SPEAKER_00:",
		"SAID SPEAKER_00 (not in the video's audio):"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief no longer marks %q, which the prompt says it will:\n%s", want, brief)
		}
	}
}

// TestOnlyWhatTheVideoCarriesIsOffLimits: the no-quoting rule is about a
// collision in the mix, not about ownership of the words. The render takes each
// clip's sound from the footage it was cut from; a separate microphone
// recording -- which is where a session's best lines usually are, because that
// is the good mic on the person talking -- is transcribed, aligned, and then
// never heard again. Forbidding the narration to say those lines forbids it the
// only voice that will ever carry them.
func TestOnlyWhatTheVideoCarriesIsOffLimits(t *testing.T) {
	// the merged timeline says which recording each line came off, and that has
	// to survive the read: it is the whole basis of the distinction
	dir := t.TempDir()
	f := filepath.Join(dir, "session.tsv")
	if err := os.WriteFile(f, []byte(
		"646.08\t648.96\this-own-mic\tSPEAKER_00\tOpen up, FBI.\n"+
			"651.20\t653.20\tcapture-0\tSPEAKER_01\topen this chest.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := loadTSVRows(f)
	if len(rows) != 2 || rows[0].src != "his-own-mic" || rows[1].src != "capture-0" {
		t.Fatalf("the recording each line came off was dropped: %+v", rows)
	}

	// the footage is what the finished video sounds like
	a := &App{selVid: []string{"/in/capture-0.mp4"}, selAud: []string{"/in/his-own-mic.flac"}}
	heard := a.heardSources()
	if !heard["capture-0"] || heard["his-own-mic"] {
		t.Fatalf("heardSources = %v -- a microphone recording is not in the render", heard)
	}

	brief := clipBriefs([]cutSeg{{S: 640, E: 660}}, nil, rows, heard)
	if !strings.Contains(brief, "SAID SPEAKER_00 (not in the video's audio): Open up, FBI.") {
		t.Errorf("the joke is not marked quotable:\n%s", brief)
	}
	if !strings.Contains(brief, "HEARD SPEAKER_01: open this chest.") {
		t.Errorf("a line the video carries is not marked audible:\n%s", brief)
	}

	// ...and the prompt has to ask for it, or the mark means nothing
	for _, want := range []string{"not in the video's audio", "Do quote a"} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the prompt never says %q, so the narration stays silent about "+
				"lines nobody else will ever say", want)
		}
	}

	// the fact the whole distinction rests on: a clip's audio is its video's,
	// plus the narration. If the mic recordings are ever mixed in, every line
	// above becomes a collision again and these rules need rereading.
	b, err := os.ReadFile("step5.go")
	if err != nil {
		t.Fatal(err)
	}
	enc := string(regexp.MustCompile(`(?s)func \(a \*App\) encodeClip\(.*?\n}\n`).Find(b))
	if enc == "" {
		t.Fatal("encodeClip is gone")
	}
	if !strings.Contains(enc, `game := "0:a"`) || strings.Contains(enc, "selAud") {
		t.Error("the render no longer takes a clip's audio from its own video alone")
	}
}
