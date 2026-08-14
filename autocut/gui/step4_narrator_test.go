package main

// The narrator tags reaching step 5. What a slot means is "whose voice this is",
// so it is the tag -- not the order the files happen to sit in -- that decides
// who the narration is spoken by, and the picker has to offer exactly the people
// the session named.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// voiceApp is an App whose voices folder is empty and whose sources are the
// given snapshot: enough for the picker without a window, a server or the CC0
// samples this machine may or may not have installed.
func voiceApp(t *testing.T, narr ...string) *App {
	t.Helper()
	root := t.TempDir()
	a := &App{root: root, outDir: root}
	models := filepath.Join(root, "models")
	if err := os.MkdirAll(filepath.Join(models, "voices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.writeConf(appConf{Voices: filepath.Join(models, "voices")}); err != nil {
		t.Fatal(err)
	}
	for i, p := range narr {
		if i < narratorSlots {
			a.selNarr[i] = p
		}
		if p != "" {
			a.selAud = append(a.selAud, p)
		}
	}
	return a
}

// The ids are what voice.txt and every synthesis cache key hold, so they have to
// survive a round trip -- and slot 1 has to stay spelled "own", or every project
// written before the other three existed starts speaking in something else and
// re-synthesizes narration that was already paid for.
func TestNarratorVoiceIDsRoundTripAndSlotOneIsStillOwn(t *testing.T) {
	if got := narratorVoiceID(1); got != ownVoice {
		t.Errorf("narrator 1's id is %q, want %q", got, ownVoice)
	}
	for n := 1; n <= narratorSlots; n++ {
		if got := narratorSlot(narratorVoiceID(n)); got != n {
			t.Errorf("slot %d survived the round trip as %d", n, got)
		}
	}
	// a wav out of the voices folder is not a narrator, whatever it is called
	for _, id := range []string{"cv-gb-female-30s", "narrator", "narrator0",
		"narrator1", "narrator5", "narratorX", "2"} {
		if got := narratorSlot(id); got != 0 {
			t.Errorf("voice %q was read as narrator %d", id, got)
		}
	}
}

// The picker offers the people the session tagged: slot 1 always, since somebody
// speaks the narration either way, and 2..4 only once a recording carries them.
func TestThePickerOffersExactlyTheTaggedNarrators(t *testing.T) {
	a := voiceApp(t, "/rec/me.flac", "", "/rec/mate.flac")
	var ids []string
	for _, v := range a.listVoices() {
		ids = append(ids, v.id)
	}
	want := []string{ownVoice, "narrator3"}
	if len(ids) != len(want) {
		t.Fatalf("the picker offers %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("row %d is %q, want %q", i, ids[i], want[i])
		}
	}
	// and each row names the file it clones, because that is the thing being
	// chosen -- "My own voice" alone says nothing about who that is
	if got := a.narratorVoiceName(1); got != "My own voice — me.flac" {
		t.Errorf("slot 1 reads %q", got)
	}
	if got := a.narratorVoiceName(3); got != "Narrator 3 — mate.flac" {
		t.Errorf("slot 3 reads %q", got)
	}
}

// Where each slot is cut from. Slot 1 falls back to the session's first
// recording -- that is what "my own voice" meant before the tags existed, so a
// project from back then keeps speaking in the same voice -- while an untagged
// slot 2..4 is nobody and must not quietly borrow somebody else's recording.
func TestNarratorSourceFallsBackOnlyForSlotOne(t *testing.T) {
	tagged := voiceApp(t, "/rec/me.flac")
	if got := tagged.narratorSource(1); got != "/rec/me.flac" {
		t.Errorf("slot 1 is cut from %q", got)
	}
	if got := tagged.narratorSource(2); got != "" {
		t.Errorf("an untagged slot 2 is cut from %q, want nothing", got)
	}

	untagged := &App{selAud: []string{"/rec/first.flac", "/rec/second.flac"}}
	if got := untagged.narratorSource(1); got != "/rec/first.flac" {
		t.Errorf("a project with no tags speaks as %q, want the first recording", got)
	}
	// a session that is one screen capture and nothing else: the capture is
	// still where a voice can be cut from
	video := &App{selVid: []string{"/rec/screen.mkv"}}
	if got := video.narratorSource(1); got != "/rec/screen.mkv" {
		t.Errorf("a footage-only session speaks as %q, want the footage", got)
	}
}

// ---- what ▶ owes you --------------------------------------------------------

// TestStaleForOnlyRewritesWhenItMust guards the rule that makes one button
// safe: ▶ asks the model again when the narration cannot be right, and never
// merely because it could be different. Get this wrong in the permissive
// direction and every press quietly throws away hand-written lines; wrong the
// other way and a cut you have re-edited keeps narration written for clips that
// are no longer there.
func TestStaleForOnlyRewritesWhenItMust(t *testing.T) {
	segs := []cutSeg{{S: 1, E: 5}, {S: 20, E: 26}}
	n := &narrator{}
	if n.staleFor(segs) == "" {
		t.Error("no narration at all counted as current")
	}
	n.entries = []narrEntry{{S: 1, E: 5, Text: "one"}, {S: 20, E: 26, Text: "two"}}
	if why := n.staleFor(segs); why != "" {
		t.Errorf("a narration matching its cut wants a rewrite: %s", why)
	}
	// the case that has to stay quiet: your own words, kept
	n.entries[0].Text = "the line I wrote myself"
	n.entries[1].Emotion = "dry"
	if why := n.staleFor(segs); why != "" {
		t.Errorf("editing a line asked for a rewrite that would delete it: %s", why)
	}
	if n.staleFor([]cutSeg{{S: 1, E: 5}, {S: 21, E: 26}}) == "" {
		t.Error("a clip moved a second and the narration still counted as current")
	}
	if n.staleFor(segs[:1]) == "" {
		t.Error("a clip removed and the narration still counted as current")
	}
	if n.staleFor(append(segs, cutSeg{S: 40, E: 44})) == "" {
		t.Error("a clip added and the narration still counted as current")
	}
	n.rewrite = true // a ✎ note, which is a request for exactly this
	if n.staleFor(segs) == "" {
		t.Error("a pending ✎ note did not reach the next run")
	}
}

// TestTheNoteSurvivesTheAppBeingClosed: the note is written in the evening and
// applied the next time, so the flag it sets belongs in the file. In memory it
// would be lost on the one path it exists for.
func TestTheNoteSurvivesTheAppBeingClosed(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root, outDir: root}
	n := &narrator{a: a, entries: []narrEntry{{S: 0, E: 3, Text: "x", Instr: "shorter"}}, rewrite: true}
	n.save()
	back := &narrator{a: a}
	back.load()
	if !back.rewrite {
		t.Error("the pending rewrite was not stored, so the note is silently dropped")
	}
	if len(back.entries) != 1 || back.entries[0].Instr != "shorter" {
		t.Errorf("entries came back as %+v", back.entries)
	}
	// ...and a narration with nothing pending leaves no key behind
	back.rewrite = false
	back.save()
	b, err := os.ReadFile(a.narrPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "rewrite") {
		t.Errorf("a narration with nothing pending still wrote the flag: %s", b)
	}
}

// TestNarrateIsOneButton: the two halves of this step were two buttons in two
// places, and the split was the tool's, not the user's. Source-level, because
// what it guards is a page's wiring -- nothing at run time notices a second
// button growing back or ▶ losing half its job.
func TestNarrateIsOneButton(t *testing.T) {
	page, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), `NewButtonWithLabel("Generate narration")`) {
		t.Error("the page grew its own generate button back; ▶ is the way in")
	}
	run := regexp.MustCompile(`(?s)func \(a \*App\) narrateRun\(\).*?\n}\n`).Find(page)
	if run == nil {
		t.Fatal("narrateRun is gone")
	}
	for _, half := range []string{"a.writeNarration(", "a.synthesize("} {
		if !strings.Contains(string(run), half) {
			t.Errorf("narrateRun does not reach %s — ▶ only does half the step", half)
		}
	}
	bar, err := os.ReadFile("pipeline.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bar), "a.narrateRun()") {
		t.Error("the run bar's ▶ no longer starts the narration step")
	}
}
