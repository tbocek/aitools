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
	got := clipBriefs(segs, []string{"", "keep this one short"}, rows, "")

	for _, want := range []string{
		"CLIP 1: 751.0–800.0 (49 s, at most 30 words -- fewer is better, none is fine)",
		"[+3s] EVENT: A glowing green figure floats near a treasure chest.",
		// no narrator named, so every line is assumed audible: the assumption
		// that cannot embarrass the narration
		"[+7s] SPEAKER_00: there's not much time",
		"[+47s] EVENT: The player swings a pickaxe at a green wall.",
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
	for _, want := range []string{"Never repeat a SPEAKER line", "the viewer hears it"} {
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
	if !strings.Contains(string(b), "amix=inputs=%d:duration=first") ||
		!strings.Contains(string(b), `fmt.Sprintf("[bg]%samix`) {
		t.Error("the narration no longer mixes over the clip's own audio -- the prompt's " +
			"no-quoting rule was written for a mix and needs rereading")
	}
}

// TestTheNarrationIsCommentaryAndNotMemoir pins the difference between the two
// voices this prompt has had. Told it had been there and that this had happened
// to it, the model wrote a man remembering his own body: "I'm already
// spinning", "my hands are not listening", six entries out of nine opening with
// I'm, every clip a summary of what it had cost him. What the video is actually
// up against talks over the picture in the present with a verdict on every
// second of it, and the old prompt forbade exactly that in one line.
//
// The wording is pinned because there is nothing else to catch this: a prompt
// that has drifted back to memoir still parses, still fills every clip, and is
// only noticeable by watching the video.
func TestTheNarrationIsCommentaryAndNotMemoir(t *testing.T) {
	for _, gone := range []string{"You were there", "this happened to you",
		"Do not describe the picture"} {
		if strings.Contains(narrSystem, gone) {
			t.Errorf("the prompt says %q again, which is the voice that wrote "+
				"nine clips of what it had felt like", gone)
		}
	}
	for _, want := range []string{
		"Never report your own body", // the memoir, in one line
		"Quote at most one NARRATOR", // a run of them is a transcript, not a joke
		"broken speech-to-text",      // which is what the runs were made of
	} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the prompt no longer says %q", want)
		}
	}
	// the worked example is the load-bearing part of a prompt this size, so it
	// has to survive an edit to the rules above it
	if !strings.Contains(narrSystem, "  -> \"") {
		t.Error("the example of a clip block and the line it should get is gone")
	}
}

// TestTheNarrationLeavesRoom is the other half of it, and the one the second
// rewrite got wrong: told to have an opinion about every second, the model had
// an opinion about every second. Nine clips came back filled to a budget that
// was a speaking rate, so the voice-over ran from the first clip to the last
// with the same pause in the same place every time -- and a voice that talks
// over everything is not commenting on anything.
//
// Nothing downstream measures this. A wall-to-wall narration renders, plays and
// sounds professional in the first ten seconds, which is why the ask is pinned
// here in the words that carry it: the ceiling, the silence, and the empty line
// that makes a clip nobody talks over possible at all.
func TestTheNarrationLeavesRoom(t *testing.T) {
	for _, want := range []string{
		"Less is more", // the instruction, in the words the model reads first
		"ceiling across all its entries, not a target", // ...what the number under each clip means
		"Silence is part of this",                      // ...and what happens in the space that leaves
		`gets text ""`,                                 // a clip may get no line at all
		`  -> ""`,                                      // and the example shows one, which is what gets copied
		"one or two clips",                             // ...but as the exception: shown two examples of which
		"Most clips get a line",                        // one was "", the model went half-silent (5 of 9)
		"the offset it happens",                        // a line lands on a moment, not on a clip
		"it's a bit crowded",                           // describing the picture is allowed when it is funny
	} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the prompt no longer says %q -- the narration goes back to "+
				"talking through every second of every clip", want)
		}
	}

	// and the budget has to agree with it: the prompt can ask for restraint all
	// it likes while the brief hands out a word for every 0.4 s of video
	for _, c := range []struct {
		dur  float64
		want int
	}{{3, 8}, {12, 9}, {20, 15}, {49, 30}, {120, 30}, {600, 30}} {
		if got := narrBudget(c.dur); got != c.want {
			t.Errorf("a %.0f s clip is allowed %d words, want %d", c.dur, got, c.want)
		}
	}
	// the shape that matters more than any single number: what a clip is allowed
	// is a fraction of what fits in it. Speech runs at about 2.5 words a second.
	if narrBudget(40) > int(40*2.5)/3 {
		t.Error("a clip's word count is within reach of filling it end to end")
	}
}

// A note is the clip's, and a clip has several rows now. The regression this
// pins: a note saved against the clip's second row was collected by taking the
// FIRST best-overlapping entry's note -- the first row's stale or empty one --
// so the user's note went into the file and the next write never saw it.
func TestANoteOnAnyRowOfAClipReachesTheWriter(t *testing.T) {
	n := &narrator{entries: []narrEntry{
		{S: 392, E: 419, Text: "welcome"},
		{S: 392, E: 419, Text: "weee", Instr: "scream the zipline"},
		{S: 585, E: 653, Text: "fbi", Instr: "pause after FBI"},
	}}
	if got := n.noteFor(cutSeg{S: 392, E: 419}); got != "scream the zipline" {
		t.Errorf("the note on the clip's second row came back as %q", got)
	}
	// notes on two rows of one clip both arrive, joined
	n.entries[0].Instr = "short welcome first"
	got := n.noteFor(cutSeg{S: 392, E: 419})
	for _, want := range []string{"short welcome first", "scream the zipline"} {
		if !strings.Contains(got, want) {
			t.Errorf("the clip's combined note %q lost %q", got, want)
		}
	}
	// and the neighbouring clip's note stays its own
	if got := n.noteFor(cutSeg{S: 585, E: 653}); got != "pause after FBI" {
		t.Errorf("clip 2's note is %q", got)
	}
}

// The row is one box, "[excited] Weee, ziplining!", but the wire is two
// fields: the TTS server takes the emotion as its own request parameter, and
// whatever stays in the text gets pronounced. lineParts/lineText are the seam,
// and a slip there either speaks the word "excited" out loud or loses the
// delivery -- both only audible, never visible.
func TestOneBoxCarriesEmotionAndWords(t *testing.T) {
	for _, c := range []struct {
		box, emo string
		at       float64
		hasAt    bool
		text     string
	}{
		{"[excited] Weee, ziplining!", "excited", 0, false, "Weee, ziplining!"},
		{"[excited @13] Weee, ziplining!", "excited", 13, true, "Weee, ziplining!"},
		{"[panicking, laughing @6.5] and down we go", "panicking, laughing", 6.5, true, "and down we go"},
		{"[@62] no emotion, placed", "", 62, true, "no emotion, placed"},
		{"no tag at all", "", 0, false, "no tag at all"},
		{"  [deadpan]   spaced out  ", "deadpan", 0, false, "spaced out"},
		{"[half open so it is words", "", 0, false, "[half open so it is words"},
		{"say it @9 sharp", "", 0, false, "say it @9 sharp"}, // an @ in the words is words
		{"", "", 0, false, ""},
	} {
		emo, at, hasAt, text := lineParts(c.box)
		if emo != c.emo || at != c.at || hasAt != c.hasAt || text != c.text {
			t.Errorf("lineParts(%q) = %q @%g(%v) + %q, want %q @%g(%v) + %q",
				c.box, emo, at, hasAt, text, c.emo, c.at, c.hasAt, c.text)
		}
	}
	// and the box shows what the parts will be read back from
	for _, c := range []struct {
		e    narrEntry
		want string
	}{
		{narrEntry{Text: "Weee, ziplining!", Emotion: "excited"}, "[excited] Weee, ziplining!"},
		{narrEntry{Text: "Weee, ziplining!", Emotion: "excited", At: 13}, "[excited @13] Weee, ziplining!"},
		{narrEntry{Text: "placed only", At: 62}, "[@62] placed only"},
		{narrEntry{Text: "plain"}, "plain"},
		{narrEntry{Text: "", Emotion: "excited", At: 5}, ""}, // a silent clip shows nothing to speak
	} {
		if got := lineText(c.e); got != c.want {
			t.Errorf("lineText(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
	// the round trip is what the save path actually does
	for _, e := range []narrEntry{
		{Text: "Weee, ziplining!", Emotion: "excited", At: 13},
		{Text: "Weee, ziplining!", Emotion: "excited"},
		{Text: "plain"},
	} {
		emo, at, hasAt, text := lineParts(lineText(e))
		if emo != e.Emotion || text != e.Text || (hasAt && at != e.At) || (!hasAt && e.At != 0) {
			t.Errorf("round trip of %+v came back %q @%g(%v) + %q", e, emo, at, hasAt, text)
		}
	}
}

// The worked example must not be made of the session it will be run against.
// It was, once: the example quoted "Open up, FBI." -- a line off this repo's
// own test session -- and the model's handling of that real line became
// unreadable: quoted while the prompt showed it, avoided the moment the rules
// around it changed, with the replacement paraphrasing the example's own words.
// The fixture line below is the same one the vocabulary tests feed through the
// brief, which is exactly why the prompt may not contain it.
// What stays is the style line in the voice paragraph ("so many gorillas") --
// the user supplied it as the tone to hit, and tone is not material: the
// hazard is a quotable line or a scene the block can also contain.
func TestTheExampleIsInventedAndNotTheSessions(t *testing.T) {
	for _, gone := range []string{"Open up, FBI", "chest"} {
		if strings.Contains(narrSystem, gone) {
			t.Errorf("the prompt's example says %q, which is the test session's own "+
				"material -- the model cannot be read against footage its prompt "+
				"already used", gone)
		}
	}
}

// TestTheLineLandsWhereTheWriterPutIt: an entry carries "at", the second inside
// its clip where the line starts. Without it every line played at a fixed 0.3 s
// into its clip, which put "Open up, FBI" a minute before the chest was on
// screen and a sign-off 30 s before the video ended -- and made every pause the
// same length, because the line's position never depended on the clip. The
// placement has to hold in three places at once: the prompt asks for it, the
// preview starts the voice there, and the render's adelay puts it there in the
// mix. Each is edited separately and none notices the others drift.
func TestTheLineLandsWhereTheWriterPutIt(t *testing.T) {
	for _, want := range []string{
		`"at" is the second your line starts`, // the field, defined where the model reads it
		"never before that moment is on screen",
		`"at":<sec>`,           // ...and in the JSON it returns
		"like and subscribe",   // the sign-off exists
		"near that clip's end", // ...and sits at the end, not the head, of the last clip
	} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the prompt no longer says %q", want)
		}
	}

	// the preview: a line placed at +20 into a 60 s clip is silence until then
	n := &narrator{entries: []narrEntry{{S: 100, E: 160, At: 20, Text: "there it is"}}}
	for _, c := range []struct {
		t    float64
		want int
	}{{100, -1}, {119.9, -1}, {120, 0}, {159, 0}, {160, -1}} {
		if got := n.entryAt(c.t); got != c.want {
			t.Errorf("at %.1f s the preview speaks entry %d, want %d", c.t, got, c.want)
		}
	}

	// the render: the mix delays the voice by the clip's own delay, not by the
	// constant, and the subtitles ride the same number
	b, err := os.ReadFile("step5.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "math.Max(narrLead, ln.delay)") {
		t.Error("encodeClip no longer delays each voice to its line's placement")
	}
	if !strings.Contains(src, "srtTime(cum+ln.delay)") || !strings.Contains(src, "srtTime(ln.delay)") {
		t.Error("the subtitles no longer start where the voice does")
	}
	if !strings.Contains(src, "ln.delay = math.Max(narrLead, ln.at)") {
		t.Error("the writer's placement never reaches the mix")
	}

	// ...and the row's ▶ cues just ahead of the line, not the head of the clip:
	// with a placement a minute in, an audition from the top is a minute of
	// silence that reads as a line that was never spoken
	s4, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s4), "n.cue(math.Max(e.S, e.S+e.At-3), true)") {
		t.Error("the audition no longer cues to the line's placement")
	}
}

// An entry with no words is a supported answer, not a broken one, and three
// places have to agree about that or the feature is worse than not having it:
// the writer must not reject the model for using it, the preview must not stop
// the picture and send the empty string to the TTS server, and the render must
// leave that clip on its own audio. The first two are checked here; the third is
// step5's `strings.TrimSpace(e.Text) != ""` before it takes a wav.
func TestAClipWithNoLineIsLeftAlone(t *testing.T) {
	n := &narrator{entries: []narrEntry{
		{S: 0, E: 10, Text: "look at this idiot"},
		{S: 10, E: 20, Text: ""},
		{S: 20, E: 30, Text: "   "},
		{S: 30, E: 40, Text: "and he does it again"},
	}}
	for _, c := range []struct {
		t    float64
		want int
	}{{1, 0}, {9.9, 0}, {10, -1}, {19, -1}, {25, -1}, {35, 3}} {
		if got := n.entryAt(c.t); got != c.want {
			t.Errorf("at %.1f s the preview would speak entry %d, want %d", c.t, got, c.want)
		}
	}
	if !strings.Contains(narrSystem, `still an entry`) {
		t.Error("the prompt no longer says an empty line is still an entry, and the " +
			"writer rejects any answer whose entry count does not match the cut")
	}
	// the writer's own validation: an empty line passes, an answer that is
	// nothing but empty lines does not
	b, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, `problem = "empty text"`) {
		t.Error("writeNarration still rejects a clip the model chose to leave silent")
	}
	if !strings.Contains(src, `said == 0`) {
		t.Error("nothing catches a narration that came back empty from end to end")
	}
}

// The prompt describes the format the brief is written in. They are edited in
// different places -- one is a const, the other is a box in the project the
// user can rewrite -- so this pins the words that have to mean the same thing
// in both.
func TestTheNarratePromptDescribesTheBriefItGets(t *testing.T) {
	for _, want := range []string{"EVENT", "SPEAKER_01", "NARRATOR", "[+12s]"} {
		if !strings.Contains(narrSystem, want) {
			t.Errorf("the narration prompt never mentions %q, so the model is left to "+
				"work out the shape of its own input", want)
		}
	}
	brief := clipBriefs([]cutSeg{{S: 0, E: 10}}, nil, []tsvRow{
		{s: 1, e: 2, spk: "EVENT", text: "x"},
		{s: 3, e: 4, spk: "SPEAKER_01", text: "y", src: "capture"},
		{s: 5, e: 6, spk: "SPEAKER_00", text: "z", src: "his-own-mic"},
	}, "his-own-mic")
	for _, want := range []string{"EVENT:", "SPEAKER_01:", "NARRATOR:"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief no longer marks %q, which the prompt says it will:\n%s", want, brief)
		}
	}
}

// TestOnlyWhatTheVideoCarriesIsOffLimits: the no-quoting rule is about a
// collision in the mix, not about ownership of the words. The render takes each
// clip's sound from the footage it was cut from; the narrator's own microphone
// -- which is where a session's best lines usually are, because that is the
// good mic on the person talking, and because he is the one player the capture
// never plays back -- is transcribed, aligned, and then never heard again.
// Forbidding the narration to say those lines forbids it the only voice that
// will ever carry them.
//
// And the label is the name of the person, not a note about the mix: the model
// is told NARRATOR, because that is the word it already knows what to do with.
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

	// whoever wears the narrator tag is the exempt one, and it is the tag that
	// says so -- the same tag the voice picker clones from
	a := &App{root: dir, outDir: dir,
		selVid: []string{"/in/capture-0.mp4"}, selAud: []string{"/in/his-own-mic.flac"}}
	a.selNarr[0] = "/in/his-own-mic.flac"
	if got := a.narratorMic(); got != "his-own-mic" {
		t.Fatalf("narratorMic = %q -- the narration is quoting the wrong microphone", got)
	}
	// a session that is one capture and nothing else has no exempt recording:
	// then the narrator is on the footage like everybody else
	if got := (&App{root: dir, outDir: dir, selVid: []string{"/in/capture-0.mp4"}}).narratorMic(); got != "" {
		t.Errorf("a footage-only session exempts %q, which the video plays out loud", got)
	}

	brief := clipBriefs([]cutSeg{{S: 640, E: 660}}, nil, rows, a.narratorMic())
	if !strings.Contains(brief, "NARRATOR: Open up, FBI.") {
		t.Errorf("the joke is not marked quotable:\n%s", brief)
	}
	if !strings.Contains(brief, "SPEAKER_01: open this chest.") {
		t.Errorf("a line the video carries is not marked audible:\n%s", brief)
	}

	// ...and the prompt has to ask for it, or the mark means nothing
	for _, want := range []string{"NARRATOR", "Quote at most one NARRATOR line per clip"} {
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
