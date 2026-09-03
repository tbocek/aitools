package main

// Suggest cut is two calls that think for minutes each, and the bar over them
// pulsed for the whole run: alive, but saying nothing about how far along it
// was. Both replies are countable while they arrive -- the segments and the
// checks close one object at a time -- so the bar can say something true.
//
// The denominators are different, and that is the interesting part. The audit
// has one: it was asked for one check per proposed segment. Choosing has none,
// because how many moments a session has is the model's decision. What it has
// instead is order: the timeline is walked from the front, so the end time of
// the last finished segment is how far through the session, and through the
// job, it has got.

import (
	"os"
	"strings"
	"testing"
)

func TestASegmentCountsOnlyOnceItIsWritten(t *testing.T) {
	// a reply as it arrives, piece by piece
	for _, c := range []struct {
		name   string
		in     string
		want   int
		though float64
	}{
		{"nothing yet", `Let me think about which moments matter.`, 0, 0},
		{"the key, no items", `{"segments":[`, 0, 0},
		{"one half-written", `{"segments":[{"start":10,"end":`, 0, 0},
		{"one closed", `{"segments":[{"start":10,"end":48}`, 1, 48},
		{"two closed, one open", `{"segments":[{"start":10,"end":48},{"start":90,"end":140},{"start":300`, 2, 140},
		{"whole reply", `{"segments":[{"start":10,"end":48},{"start":90,"end":140}]}`, 2, 140},
	} {
		n, through := jsonItemsDone(c.in, "segments")
		if n != c.want || through != c.though {
			t.Errorf("%s: %d segment(s) up to %gs, want %d up to %gs", c.name, n, through, c.want, c.though)
		}
	}
}

// The model thinks out loud before it answers, and it thinks about the shape of
// the answer -- so the words and the punctuation of the real thing are in there
// first. The count belongs to the last one, or a bar reads a worked example in
// the reasoning as the work.
func TestTheCountBelongsToTheAnswerNotTheThinkingAboutIt(t *testing.T) {
	s := `I should return {"segments":[{"start":0,"end":30}]} shaped output.` +
		` Something like {"segments": [ {"start":1,"end":2} ] } is the format.` +
		"\n" + `{"segments":[{"start":600,"end":650},{"start":900,"end":960}]}`
	n, through := jsonItemsDone(s, "segments")
	if n != 2 || through != 960 {
		t.Errorf("counted %d up to %gs, want the 2 of the real answer up to 960s", n, through)
	}
}

// The audit answers a different shape with the same arithmetic, and its objects
// carry prose that is full of the punctuation the scanner reads.
func TestTheAuditIsCountedTheSameWay(t *testing.T) {
	s := `{"checks":[` +
		`{"i":1,"verdict":"ok","start":10,"end":48,"why":""},` +
		`{"i":2,"verdict":"fix","start":90,"end":175,"why":"ends before the chest is opened {see 2:55}"},` +
		`{"i":3,"verdict":"drop"`
	n, through := jsonItemsDone(s, "checks")
	if n != 2 {
		t.Errorf("counted %d finished checks, want 2 -- the third is still being written", n)
	}
	if through != 175 {
		t.Errorf("the last finished check reaches %gs, want 175", through)
	}
}

// Narration entries have no end time in them, and the bar over the writing
// counts clips instead. Sharing the scanner must not have cost that.
func TestNarrationStillCountsWithoutAnEndTime(t *testing.T) {
	s := `{"entries":[{"i":1,"text":"here we go"},{"i":2,"text":"and \"then\" {this}"}]}`
	if got := narrEntriesDone(s); got != 2 {
		t.Errorf("narrEntriesDone read %d clips, want 2", got)
	}
	if _, through := jsonItemsDone(s, "entries"); through != 0 {
		t.Errorf("clips with no end time placed themselves at %gs", through)
	}
}

// The wiring, which needs a display and an LLM to run and so is read instead:
// both calls streamed, both reporting, and the pulse ending on the first real
// fraction rather than on a flag -- Pulse and SetFraction drive the same needle.
func TestSuggestReportsWhileItRunsInsteadOfOnlyPulsing(t *testing.T) {
	b, err := os.ReadFile("cut_suggest.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []struct{ frag, why string }{
		{`a.llmChatRetryTools("suggest", msgs, think, tools, a.webRunner("suggest", ffx), onText)`,
			"a call that is not streamed cannot be counted while it runs"},
		{`a.llmChatRetryOn("audit", msgs, true, onText)`,
			"the audit call is streamed and recorded under its own name"},
		{`jsonItemsDone(s, "segments")`, "choosing has to count its own segments"},
		{`jsonItemsDone(s, "checks")`, "the audit has to count its own checks"},
		{`a.prog(trackSTT, suggestChooseShare`, "the audit half has to start where the choosing half ended"},
		{`a.progParts[trackSTT] > 0`,
			"the pulse has to end on the first real fraction, or it fights the bar it shares"},
	} {
		if !strings.Contains(src, want.frag) {
			t.Errorf("suggest no longer contains %q -- %s", want.frag, want.why)
		}
	}
	if strings.Count(src, ", onText)") != 2 {
		t.Errorf("%d of the two suggest calls are streamed",
			strings.Count(src, ", onText)"))
	}
	// the two halves have to add up to the whole bar, or a finished run stops short
	if suggestChooseShare <= 0 || suggestChooseShare >= 1 {
		t.Errorf("suggestChooseShare is %g -- one of the two calls owns none of the bar", suggestChooseShare)
	}
}

// The target crosses the wire as a RANGE, not a number.
//
// The wording asks for a total near the target and suggestWindow is what the
// gate actually accepts, and for a long time only the number was sent: a model
// told "300 seconds" treats 300 as the answer and spends the call trying to
// hit it exactly. One 11-minute call came back with 85 kB of arithmetic --
// "Total 134!! Over 100 by 34. I keep ballooning." -- and no JSON at all. Both
// numbers now come from the one function the gate uses, so the prompt and the
// gate cannot drift apart again.
func TestTheModelIsToldTheRangeItWillBeJudgedBy(t *testing.T) {
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"lo, hi := a.suggestWindow(target)",
		`"ACCEPTED: %.0f to %.0f seconds, and at most %d segments. Stop at the first set of "`,
		"alo, ahi := a.suggestWindow(target)", // the audit is judged the same way
		"total := cutLen(applyFx(segs, fx))",  // and measured the same way
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go no longer contains %q", want)
		}
	}
	// the wording's own check counts what the video RUNS, effects included --
	// the sum it used to ask for was raw seconds, which a speed effect makes a
	// different number from the one the gate measures
	cut := readSrc(t, "cut.go")
	for _, want := range []string{
		"add up how long your segments RUN",
		"that divided by the rate wherever a speed effect covers them",
		"the total is inside the accepted range you were given",
		"One pass at the total.",
	} {
		if !strings.Contains(cut, want) {
			t.Errorf("the cut wording no longer says %q", want)
		}
	}
	if strings.Contains(cut, "within a tenth of the target length") {
		t.Error("the wording asks for a tenth of the target again, which is tighter " +
			"than the gate accepts — that difference is what the model spends the call on")
	}
	// and a retry stops searching: the moments are in the timeline it has
	if !strings.Contains(src, "tools = nil") {
		t.Error("a rejected attempt still has the web tools, and a retry that searches " +
			"is a retry that does not answer")
	}
}

// The other end of the same gate: an answer of hundreds of segments is not a
// long cut, it is a model that stopped choosing moments and started counting.
// One run answered with 548 of them -- five real, then a march of 24-second
// blocks every 32 seconds running eleven times past the end of the session --
// and nothing rejected it for its shape.
func TestARunawayAnswerIsRejectedForItsShape(t *testing.T) {
	if got := maxSuggestSegs(300); got < 3*(300/20) {
		t.Errorf("the ceiling for a 300 s target is %d, tighter than the wording's own guide", got)
	}
	if maxSuggestSegs(20) < 40 {
		t.Error("a Short's ceiling is under the floor, so a normal answer would be refused")
	}
	if maxSuggestSegs(3000) <= maxSuggestSegs(300) {
		t.Error("a longer target does not get more room")
	}
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"} else if n := maxSuggestSegs(target); len(out.Segments) > n {",
		`"%d segments, which is not a cut -- keep it under %d, "`,
		"maxSuggestSegs(target), session)", // and the model is told the ceiling
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go no longer contains %q", want)
		}
	}
	// and the wording says how a dull stretch is meant to be expressed, which
	// is the thing the runaway was a broken attempt at
	if !strings.Contains(readSrc(t, "cut.go"),
		"ONE segment with a speed effect over it, never a row of small segments") {
		t.Error("the effect rules no longer say how to show a stretch without watching it")
	}
}
