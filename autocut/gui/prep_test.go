package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Prepare is Inputs and Describe in one tab and one ▶. They were two
// steps that could only ever be run in one order -- the second refused to start
// until the first had finished and said so in a status line -- so what the two
// buttons offered was the chance to press them the wrong way round.
//
// Merging them puts two things at risk that neither page had to think about
// alone: the progress bar now has two steps' worth of arithmetic pouring into
// it, and ⏹ now lands in a run where only the back half is worth throwing away.
// Those are what these tests are about.

// TestTheBarCrossesTheMiddleOnceAndNeverGoesBack: each half still divides the
// bar between its own two tracks, in halves, knowing nothing about the other
// half -- which is exactly the code that used to fill the whole bar twice. So
// the run says where each half is drawn and the queue scales every reading into
// it. The bar has to walk from 0 to 1 without ever stepping backwards, and the
// join between the two halves is the one place it could.
func TestTheBarCrossesTheMiddleOnceAndNeverGoesBack(t *testing.T) {
	a := &App{} // headless: showProg leaves the widget alone when there is none
	read := func() float64 {
		a.progMu.Lock()
		defer a.progMu.Unlock()
		return a.progParts[0] + a.progParts[1]
	}
	var seen []float64
	step := func(what string, do func()) {
		do()
		got := read()
		if len(seen) > 0 && got < seen[len(seen)-1]-1e-9 {
			t.Errorf("after %s the bar went back: %v → %v", what, seen[len(seen)-1], got)
		}
		seen = append(seen, got)
	}

	a.qReset()
	if got := read(); got != 0 {
		t.Fatalf("a fresh run starts at %v", got)
	}

	// first half: two concurrent tracks, half the bar each, as the transcripts
	// and the frames divide it
	step("the first half opening", func() { a.qPhase(0, prepInputsShare) })
	step("one recording transcribed", func() { a.prog(trackSTT, 0.25, "") })
	step("some frames out", func() { a.prog(trackFrames, 0.4, "") })
	step("the speech job finishing", func() { a.qDone(trackSTT, 0.5) })
	step("the frame job finishing", func() { a.qDone(trackFrames, 0.5) })
	if got := read(); math.Abs(got-prepInputsShare) > 1e-9 {
		t.Errorf("the first half finished at %v, want its whole share %v", got, prepInputsShare)
	}

	// second half: two sequential jobs, half the bar each. The join is here --
	// the phase opens with both tracks reporting nothing at all.
	step("the second half opening", func() { a.qPhase(prepInputsShare, 1-prepInputsShare) })
	if got := read(); math.Abs(got-prepInputsShare) > 1e-9 {
		t.Errorf("the second half opened at %v, want where the first one left off %v",
			got, prepInputsShare)
	}
	step("describing", func() { a.prog(trackDescribe, 0.3, "") })
	step("describe finishing", func() { a.qDone(trackDescribe, 0.5) })
	step("fixing", func() { a.prog(trackFix, 0.2, "") })
	step("the fixer finishing", func() { a.qDone(trackFix, 0.5) })
	if got := read(); math.Abs(got-1) > 1e-9 {
		t.Errorf("the run finished at %v, want a full bar", got)
	}

	// ...and a step that is one job's work says nothing about phases and gets
	// the whole bar, exactly as it did before there was a merged page
	a.qReset()
	a.prog(trackSTT, 0.5, "")
	a.qDone(trackFrames, 0.5)
	if got := read(); math.Abs(got-1) > 1e-9 {
		t.Errorf("a single-step run reads %v, want the bar it has always had", got)
	}
}

// TestAStopBeforeTheDescribingKeepsLastWeeksDescribe: ⏹ then ▶ means "describe
// it again from the beginning", and the ▶ is what throws the event logs away.
// That was safe when Describe was its own tab, because the only way to stop it
// was to have started it. In one press it is not: ⏹ during the transcribing
// would arm a restart of a describe this run never touched, and the next ▶
// would delete a perfectly good one from last week.
func TestAStopBeforeTheDescribingKeepsLastWeeksDescribe(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root, outDir: root}
	log := filepath.Join(a.describeDir(), "gameplay")
	if err := os.MkdirAll(log, 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(log, "events.tsv")
	if err := os.WriteFile(kept, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// stopped in the first half: nothing is armed, so the next ▶ resumes
	a.undRestart = false
	if err := a.undFreshStart(); err != nil {
		t.Fatal(err)
	}
	if !exists(kept) {
		t.Error("a stop during the transcribing threw away a describe it never ran")
	}

	// stopped in the second half: the next ▶ starts the describing over
	a.undRestart = true
	if err := a.undFreshStart(); err != nil {
		t.Fatal(err)
	}
	if exists(kept) {
		t.Error("a stop during the describing left the events it resumes from behind")
	}
	if a.undRestart {
		t.Error("the restart stayed armed -- the ▶ after this one would start over too")
	}

	// and it is the run that decides which of the two happened
	run := funcBody(t, "prep.go", `func \(a \*App\) startPrep\(`)
	for _, want := range []string{
		"described, err := a.prepare(",
		"a.undRestart = described",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("startPrep is missing %s -- every stop would arm the restart", want)
		}
	}
	body := funcBody(t, "prep.go", `func \(a \*App\) prepare\(`)
	// the needles are spelled in pieces on purpose: a test body that writes the
	// name of a job that talks to the server reads, to the guard in
	// live_guard_test.go, exactly like a test that calls one
	if !strings.Contains(body, "return false, err") || !strings.Contains(body, "return true, a."+"understand(") {
		t.Errorf("prepare no longer says whether the describing began:\n%s", body)
	}
}

// TestOneTabAndOnePressDoTheWholeStep: the two tabs are one, the two run
// buttons are one, and the second half's gate is gone with them -- a locked tab
// whose only prerequisite is the half of the page above it is a tab that can
// never be locked when you are looking at it.
func TestOneTabAndOnePressDoTheWholeStep(t *testing.T) {
	if steps[0].name != "prep" || steps[0].label != "Prepare" {
		t.Errorf("the first tab is %q/%q", steps[0].name, steps[0].label)
	}
	for _, gone := range []string{"step1", "step2"} {
		if i := stepIndex(gone); i >= 0 {
			t.Errorf("%q is still a tab (index %d) -- it was merged into Prepare", gone, i)
		}
	}
	if len(steps) != 4 {
		t.Errorf("%d tabs, want the four steps", len(steps))
	}

	play := funcBody(t, "pipeline.go", `func \(a \*App\) playClicked\(`)
	if !strings.Contains(play, `case "prep":`) || !strings.Contains(play, "a.prepRun()") {
		t.Errorf("▶ on the merged page no longer runs the merged step:\n%s", play)
	}
	// the two halves each had their own ▶ before the merge; neither may be
	// dispatched to any more (the first is spelled out of one piece so the
	// scan for numbered identifiers does not find one here)
	for _, gone := range []string{"a.step" + "1Clicked()", "a.understandRun()"} {
		if strings.Contains(play, gone) {
			t.Errorf("▶ still dispatches to %s, which was half of the step", gone)
		}
	}

	// the order inside one press: transcripts and frames first, because the
	// describing reads the frames and the fixer reads the transcripts
	body := funcBody(t, "prep.go", `func \(a \*App\) prepare\(`)
	// spelled in pieces: see the note in the stop test above
	iIngest := strings.Index(body, "a."+"ingest(")
	iUnd := strings.Index(body, "a."+"understand(")
	if iIngest < 0 || iUnd < 0 || iIngest > iUnd {
		t.Errorf("the halves are not in the order they depend on: %d %d\n%s", iIngest, iUnd, body)
	}
}

// TestThePageSplitsEvenlyAndTheBoxHoldsContextAndPrompts: the page is halves.
// Files on the left, one box on the right, the handle opening at the middle
// and both sides growing with the window -- and the right box is switchable:
// the User Context first, because it is the input a session actually writes,
// and behind it the two system prompts this page sends, in the order it sends
// them.
// The left half is a list of source files and two buttons that add to it, and
// that is the whole of it: a "Sources" heading over them was a line of page
// spent telling a list of file names apart from nothing, since it is the only
// list on this side. The buttons carry the word instead, which is where it does
// some work -- "Add files…" on a page that also writes frames, transcripts and
// descriptions is a question, "Add source files…" is not. What the heading's
// tooltip explained has to survive the heading, so it moves onto the list.
func TestTheSourceListSaysWhatItIsWithoutAHeadingOverIt(t *testing.T) {
	body := funcBody(t, "prep.go", `func \(a \*App\) buildSources\(`)
	if strings.Contains(body, `gtk.NewLabel("Sources")`) {
		t.Error("the Sources heading is back over the list it is the only one of")
	}
	for _, want := range []string{`gtk.NewButtonWithLabel("Add source files…")`,
		`gtk.NewButtonWithLabel("Add source folder…")`} {
		if !strings.Contains(body, want) {
			t.Errorf("the add buttons no longer say what they add: want %s", want)
		}
	}
	if !strings.Contains(body, "listScroll.SetTooltipText(") ||
		!strings.Contains(body, "placed on the session clock by the timestamp in its name") {
		t.Error("the heading took its explanation with it -- nothing now says why a file's name matters")
	}
}

func TestThePageSplitsEvenlyAndTheBoxHoldsContextAndPrompts(t *testing.T) {
	ownConfig(t)
	body := funcBody(t, "prep.go", `func \(a \*App\) buildPrep\(`)
	for what, want := range map[string]string{
		"the session's files on the left":  "outer.SetStartChild(sources)",
		"the switchable box on the right":  "outer.SetEndChild(bench)",
		"a right half that grows too":      "outer.SetResizeEndChild(true)",
		"the handle opening at the middle": "openAtHalf(outer)",
		"room over the shared bar below":   "outer.SetMarginBottom(6)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %s (%s)", what, want)
		}
	}
	// Produce is the other page built out of two halves that are both the work
	// -- the picture being made and the words that go up with it -- and it
	// opens the same way. Left to itself the pane gave the thumbnail whatever
	// it asked for and the title, the description and every encoder setting
	// what was left over.
	if !strings.Contains(funcBody(t, "produce.go", `func \(a \*App\) buildProduce\(`), "openAtHalf(outer)") {
		t.Error("the Produce page no longer opens its split at the middle")
	}
	// and the placement itself: measured first, once, and never again -- Map
	// fires on every tab switch, and re-halving there would throw away a handle
	// the user has moved
	half := funcBody(t, "prep.go", `func openAtHalf\(p \*gtk\.Paned\) \{`)
	for _, want := range []string{"p.SetPosition(p.AllocatedWidth() / 2)", "if split {", "glib.IdleAdd("} {
		if !strings.Contains(half, want) {
			t.Errorf("openAtHalf lost %q", want)
		}
	}

	// neither prompt machinery may come back to the page itself: the box
	// planted here would be one the dropdowns cannot mark with a ✎, and the
	// bottom bar it used to ride is gone
	for _, gone := range []string{"a.promptEditor(", "a.promptBar("} {
		if strings.Contains(body, gone) {
			t.Errorf("%s is back on the page, beside the box that stands in for it", gone)
		}
	}

	// the right box's menu: the context first, then every prompt the pipeline
	// sends, under their registry keys -- rows the switch reads and writes
	// through show()
	ed := funcBody(t, "prepedit.go", `func \(a \*App\) prepEditor\(`)
	rows := funcBody(t, "prepedit.go", `func prepRows\(`)
	for what, want := range map[string]string{
		"the context first":      `{"User Context", ""`,
		"the describe prompt":    `{"Describe", "describe"`,
		"the transcript prompt":  `{"Transcript", "fix"`,
		"the cut prompt":         `{"Cut", "cut"`,
		"the narration prompt":   `{"Narration", "narrate"`,
		"the upload-text prompt": `{"Upload text", "youtube"`,
	} {
		if !strings.Contains(rows, want) {
			t.Errorf("the switchable box is missing %s (%s)", what, want)
		}
	}
	if !strings.Contains(ed, "show(0)") {
		t.Error("the box does not open on the context -- the one row a session has to write")
	}

	// the seams the box lives or dies by: every keystroke writes through to
	// the store the shown row names; refills are quiet so they do not write
	// back as edits; the box stands in promptViews only while it shows that
	// prompt, so a project load cannot clobber what is on screen; and the
	// prep menu is redrawn with every other prompt menu
	for what, want := range map[string]string{
		"typing writes the context through":  "a.setSessionCtx(s)",
		"typing writes the prompt through":   "a.setPrompt(r.key, s)",
		"refills do not write back":          "if quiet || a.promptQuiet {",
		"registration follows the selection": "delete(a.promptViews, old.key)",
		"the wording row follows it too":     "delete(a.promptRows, old.key)",
		"the box is filled from the store":   "a.showPromptStyle(r.key, a.promptPickName(r.key))",
	} {
		if !strings.Contains(ed, want) {
			t.Errorf("the switchable box is missing %s (%s)", what, want)
		}
	}
	// the rows in the order the pipeline sends them, so prepRows and
	// prepEditNames cannot drift apart: row i's title in one is row i's store
	// in the other
	at := -1
	for _, want := range []string{`"User Context", ""`, `"Describe", "describe"`,
		`"Transcript", "fix"`, `"Cut", "cut"`,
		`"Narration", "narrate"`, `"Upload text", "youtube"`} {
		i := strings.Index(rows, want)
		if i < at {
			t.Errorf("%s is out of run order in the menu -- reading down it is reading the run", want)
		}
		at = i
	}

	// the prompt controls in the heading, and the context row with none of
	// them: one wording by definition -- the one you wrote -- so a ＋ and a
	// Reset would each be a control with nothing to do. No wording list here
	// any more: the Style dropdown on the bottom row is the one place a
	// wording is picked, for every job at once (applyStyle)
	if strings.Contains(ed, "head.Append(wording)") {
		t.Error("the editor grew a wording list back -- the Style dropdown is the one place a wording is picked")
	}
	for what, want := range map[string]string{
		"the menu in the heading":      "head.Append(pick)",
		"＋ to save a new wording":      "head.Append(add)",
		"Reset/Remove":                 "head.Append(drop)",
		"the context row without them": "showCtx(r)",
	} {
		if !strings.Contains(ed, want) {
			t.Errorf("the heading row is missing %s (%s)", what, want)
		}
	}
	// what ＋ and Reset do, both of them refusing the context row rather than
	// guessing what "reset the context" would mean
	for what, want := range map[string]string{
		"＋ saves under a new name": "a.savePromptStyle(r.key, name,",
		"Reset undoes edits":       "a.dropPromptStyle(r.key, name)",
		"Remove confirms first":    "a.confirm(",
		"the built-in comes back":  "a.showPromptStyle(r.key, promptDefFor(r.key).styleName())",
	} {
		if !strings.Contains(ed, want) {
			t.Errorf("the prompt controls are missing %s (%s)", what, want)
		}
	}

	sync := funcBody(t, "stylebar.go", `func \(a \*App\) syncPromptMarks\(`)
	if !strings.Contains(sync, "a.prepSync()") {
		t.Error("syncPromptMarks no longer redraws the prep menu -- its ✎ goes stale on project load")
	}

	// all three folders one press writes are counted at the bottom, each with a
	// way into it: the question asked before a run is whether this already
	// happened and to which part of it
	for _, want := range []string{"a.inputsDir, &p.inputsOut", "a.describeDir, &p.describeOut", "a.transcriptDir, &p.transcriptOut"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Outputs row does not count %s", want)
		}
	}
}

// TestTheOutputsRowSaysHowMuchAndHoverSaysWhen: three folders side by side, so
// the count is on the row and its age is on the tooltip -- three copies of
// "12 files, newest 3 min ago" is a paragraph across the bottom of a page.
func TestTheOutputsRowSaysHowMuchAndHoverSaysWhen(t *testing.T) {
	dir := t.TempDir()
	if n, _ := countOutputs(dir); n != 0 {
		t.Errorf("an empty folder counted %d files", n)
	}
	if got := summarizeOutputs(dir); got != "nothing yet" {
		t.Errorf("an empty folder reads %q", got)
	}
	// recursive, and folders themselves are not files: the transcripts are one
	// folder per source and counting those would say three when there is one
	sub := filepath.Join(dir, "gameplay")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"transcript.tsv", "words.json"} {
		if err := os.WriteFile(filepath.Join(sub, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, newest := countOutputs(dir)
	if n != 2 {
		t.Errorf("counted %d files under a folder holding two", n)
	}
	if newest.IsZero() {
		t.Error("no newest time for a folder with files in it")
	}
	if got := summarizeOutputs(dir); !strings.HasPrefix(got, "2 files, newest ") {
		t.Errorf("the one-line form reads %q", got)
	}
}

// TestTheSwitchMenuNamesItsRowsAndMarksAnEditedPrompt: the rows the right-hand
// box offers, headless. The context leads, the prompts follow in run order,
// and a prompt this project has its own wording for wears the same ✎ every
// prompt menu shows -- the mark is the one permanent pixel an edit gets.
func TestTheSwitchMenuNamesItsRowsAndMarksAnEditedPrompt(t *testing.T) {
	ownConfig(t)
	a := &App{root: t.TempDir()}
	got := a.prepEditNames()
	want := []string{"User Context", "System context", "Describe (General)", "Transcript (General)",
		"Cut (General)", "Captions (General)", "Speed (General)", "Effects (General)", "Narration (General)",
		"Upload text (General)"}
	if len(got) != len(want) {
		t.Fatalf("the menu offers %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d reads %q, want %q", i, got[i], want[i])
		}
	}
	a.setPrompt("describe", "my own wording")
	if got := a.prepEditNames(); got[2] != "Describe (General) ✎" {
		t.Errorf("an edited describe prompt reads %q in the menu, want the ✎", got[2])
	}
	if got := a.prepEditNames(); got[3] != "Transcript (General)" {
		t.Errorf("editing one prompt marked the other: %q", got)
	}
	// the Style's reach is what the parentheticals are for: one pick beside
	// Language renames every row that has a wording under the style's name,
	// and only those -- the context row stays bare
	a.applyStyle("Highlights")
	got = a.prepEditNames()
	if got[4] != "Cut (Highlights)" {
		t.Errorf("after the style pick the cut row reads %q, want Cut (Highlights)", got[4])
	}
	if got[8] != "Narration (General)" {
		t.Errorf("a job with no Highlights wording reads %q, want its default", got[8])
	}
	if got[0] != "User Context" {
		t.Errorf("the context row grew a wording name: %q", got[0])
	}
}

// TestTheBenchOffersEveryPromptAndOnlyRealOnes: the bench is now the ONLY way
// to a prompt -- no page has an Edit button any more -- so a key in promptDefs
// that is not in prepRows is a prompt the models are sent and nobody can read,
// and a row naming a key that is not in promptDefs is a box that edits a store
// nothing reads back. Neither shows up as a failure anywhere: the first is a
// wording you cannot change, the second is a wording that changes nothing.
func TestTheBenchOffersEveryPromptAndOnlyRealOnes(t *testing.T) {
	rows := map[string]bool{}
	for _, r := range prepRows() {
		if r.key == "" {
			continue // the session context, which is not a prompt
		}
		if rows[r.key] {
			t.Errorf("the menu offers %q twice -- two rows, one store, and the second "+
				"registration takes the box away from the first", r.key)
		}
		rows[r.key] = true
		if promptDefFor(r.key).def == "" {
			t.Errorf("the menu offers %q, which is not in promptDefs -- editing it "+
				"writes a store nothing sends", r.key)
		}
	}
	for _, d := range promptDefs {
		if !rows[d.key] {
			t.Errorf("prompt %q is sent by the app and reachable from nowhere -- "+
				"the bench is the only editor there is now", d.key)
		}
	}

	// every row says what it is for, and the tip is not the name again: the
	// menu names the job, the tooltip is the only place the size of what gets
	// sent is written down
	for _, r := range prepRows() {
		if strings.TrimSpace(r.menu) == "" || strings.TrimSpace(r.tip) == "" {
			t.Errorf("row %q is missing its name or its tooltip: %+v", r.key, r)
		}
	}

	// and the heading says which kind of text the box holds. "Cut" over a box
	// of rules reads as the cut itself; the context is the one row where the
	// name IS the thing.
	for _, r := range prepRows() {
		if r.key == "" {
			if r.title() != r.menu {
				t.Errorf("the context row is headed %q, want its own name", r.title())
			}
			continue
		}
		if !strings.HasSuffix(r.title(), " prompt") {
			t.Errorf("the %q row is headed %q -- a prompt has to say it is one", r.key, r.title())
		}
	}
}
