package main

import (
	"strings"
	"testing"
)

// The chain is a LIST that endRun advances: the step that finished hands it
// on, and nothing about a step has to know it is in one.
func TestTheChainIsAdvancedByTheStepThatFinished(t *testing.T) {
	if !strings.Contains(funcBody(t, "runqueue.go", `func \(a \*App\) endRun\(`), "defer a.chainDone()") {
		t.Error("a finished step does not hand the chain on")
	}
	// ...and a stopped run abandons the rest: a chain saves presses, it does
	// not carry on past the answer going wrong
	done := funcBody(t, "runchain.go", `func \(a \*App\) chainDone\(`)
	if !strings.Contains(done, "a.stopFlag.Load()") || !strings.Contains(done, "a.chain = nil") {
		t.Error("⏹ does not end the chain")
	}
	// the steps are the pipeline's order, not the tick order
	var pages []string
	for _, s := range chainSteps {
		pages = append(pages, s.page)
	}
	if strings.Join(pages, ",") != "prep,cut,narrate,produce" {
		t.Errorf("the chain runs %v", pages)
	}
	// every step names a page the dispatch actually knows
	run := funcBody(t, "runchain.go", `func \(a \*App\) runPage\(`)
	play := funcBody(t, "pipeline.go", `func \(a \*App\) playClicked\(`)
	for _, s := range chainSteps {
		if !strings.Contains(run, `case "`+s.page+`":`) {
			t.Errorf("the chain cannot run %q", s.page)
		}
		if !strings.Contains(play, `case "`+s.page+`":`) {
			t.Errorf("%q is not a page the play button knows", s.page)
		}
	}
}

// Two steps skip themselves rather than halting the chain: a video with no
// narration has nothing to narrate, and a cut with hand edits IS the answer --
// Cut refuses over them, and in a chain that refusal would be a halt nobody
// asked for.
func TestTheChainSkipsWhatHasNothingToDo(t *testing.T) {
	body := funcBody(t, "runchain.go", `func \(a \*App\) chainNext\(`)
	for _, want := range []string{
		`page == "narrate" && a.narrOff`,
		"Narrate skipped",
		`page == "cut" && a.ed != nil`,
		"!sameCut(a.ed.segs, a.ed.base.segs)",
		"Cut skipped",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the chain does not skip cleanly: want %q", want)
		}
	}
	// a skip continues the loop rather than returning: the rest of the chain
	// still has work in it
	if strings.Count(body, "continue") < 3 {
		t.Error("a skipped step ends the chain instead of passing it on")
	}
	// ...and a step that declined to start carries the chain itself, since no
	// endRun is coming for it
	if !strings.Contains(body, "if !a.running {") {
		t.Error("a step that never started leaves the chain hanging")
	}
}

// Everything is ticked by default -- that is what "run it all" means -- and a
// project written before this has no answer, which reads the same way.
func TestTheChainDefaultsToEveryStep(t *testing.T) {
	a := &App{}
	a.applyChain(nil) // safe before the ticks exist: a project loads first
	if len(a.chainSteps) != 0 {
		t.Errorf("an absent answer was stored as %v", a.chainSteps)
	}
	src := readSrc(t, "runchain.go")
	if !strings.Contains(src, "c.SetActive(true)") {
		t.Error("the steps are not ticked to begin with")
	}
	if !strings.Contains(src, "if pages == nil {") {
		t.Error("a project with no answer does not get every step")
	}
	for _, want := range []string{`RunSteps []string `, "RunSteps:   a.chainPicked(),", "a.applyChain(p.RunSteps)"} {
		if !strings.Contains(readSrc(t, "project.go"), want) {
			t.Errorf("project.go does not contain %q", want)
		}
	}
}

// ▶▶ and its menu are one control in two halves -- the split button's shape:
// press the left for the thing, press the arrow for which thing. GTK4 has no
// AdwSplitButton without libadwaita and does not need one: a linked box of a
// button and a menu button is that control, and it is what the tab row and the
// frame stepper on this window are already made of.
func TestTheChainButtonIsOneSplitControl(t *testing.T) {
	body := funcBody(t, "runchain.go", `func \(a \*App\) buildChainMenu\(`)
	if !strings.Contains(body, "linked(a.chainBtn, a.chainPick)") {
		t.Error("the run button and its menu are two loose buttons, not one control")
	}
	// the house helper, not a hand-rolled box: three places already draw a
	// segmented control and they must not drift apart
	if !strings.Contains(readSrc(t, "widgets.go"), `b.AddCSSClass("linked")`) {
		t.Error("the linked helper no longer makes a segmented control")
	}
}
