package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Preprocessing is Inputs and Describe in one tab and one ▶. They were two
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
		"described, err := a.preprocess(",
		"a.undRestart = described",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("startPrep is missing %s -- every stop would arm the restart", want)
		}
	}
	body := funcBody(t, "prep.go", `func \(a \*App\) preprocess\(`)
	// the needles are spelled in pieces on purpose: a test body that writes the
	// name of a job that talks to the server reads, to the guard in
	// live_guard_test.go, exactly like a test that calls one
	if !strings.Contains(body, "return false, err") || !strings.Contains(body, "return true, a."+"understand(") {
		t.Errorf("preprocess no longer says whether the describing began:\n%s", body)
	}
}

// TestOneTabAndOnePressDoTheWholeStep: the two tabs are one, the two run
// buttons are one, and the second half's gate is gone with them -- a locked tab
// whose only prerequisite is the half of the page above it is a tab that can
// never be locked when you are looking at it.
func TestOneTabAndOnePressDoTheWholeStep(t *testing.T) {
	if steps[0].name != "prep" || steps[0].label != "Preprocessing" {
		t.Errorf("the first tab is %q/%q", steps[0].name, steps[0].label)
	}
	for _, gone := range []string{"step1", "step2"} {
		if i := stepIndex(gone); i >= 0 {
			t.Errorf("%q is still a tab (index %d) -- it was merged into Preprocessing", gone, i)
		}
	}
	if len(steps) != 5 {
		t.Errorf("%d tabs, want the five steps", len(steps))
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
	body := funcBody(t, "prep.go", `func \(a \*App\) preprocess\(`)
	// spelled in pieces: see the note in the stop test above
	iIngest := strings.Index(body, "a."+"ingest(")
	iUnd := strings.Index(body, "a."+"understand(")
	if iIngest < 0 || iUnd < 0 || iIngest > iUnd {
		t.Errorf("the halves are not in the order they depend on: %d %d\n%s", iIngest, iUnd, body)
	}
}

// TestThePageKeepsTheContextOnTheRightAndThePromptsBehindAButton: what the
// merged page owes its two halves. The sources need the width, so the context
// box -- a paragraph of the user's own text, which a wider window does nothing
// for -- keeps the fixed right-hand column it had on Describe, and the two
// system prompts go behind the dropdown instead of filling the page.
func TestThePageKeepsTheContextOnTheRightAndThePromptsBehindAButton(t *testing.T) {
	body := funcBody(t, "prep.go", `func \(a \*App\) buildPrep\(`)
	for what, want := range map[string]string{
		"the session's files on the left":  "outer.SetStartChild(sources)",
		"the context box on the right":     "outer.SetEndChild(ctxPane)",
		"a context column of a fixed size": "outer.SetResizeEndChild(false)",
		"the describe prompt in the menu":  `promptSlot{"describe", "Describe"`,
		"the fixer prompt in the menu":     `promptSlot{"fix", "Transcript"`,
		"one menu for both":                "a.promptBar(nil,",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %s (%s)", what, want)
		}
	}
	// the boxes themselves are gone from the page: a prompt planted here is a
	// prompt the dropdown does not know about and cannot mark with a ✎
	if strings.Contains(body, "a.promptEditor(") {
		t.Error("a prompt box is back on the page, beside the dropdown that stands in for it")
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
