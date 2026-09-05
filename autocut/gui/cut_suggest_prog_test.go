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
		{`jsonItemsDone(s, "segments")`, "choosing has to count its own segments"},
		{`a.progParts[trackSTT] > 0`,
			"the pulse has to end on the first real fraction, or it fights the bar it shares"},
	} {
		if !strings.Contains(src, want.frag) {
			t.Errorf("suggest no longer contains %q -- %s", want.frag, want.why)
		}
	}
	// one streamed call: the cut is the long one and the only one with
	// anything to count while it runs; the two passes after it answer in a
	// minute from a brief of a few clips
	if strings.Count(src, ", onText)") != 1 {
		t.Errorf("%d suggest calls are streamed, want the cut alone",
			strings.Count(src, ", onText)"))
	}
	// the cut's share leaves room for the passes after it, or a finished run stops short
	if suggestChooseShare <= 0 || suggestChooseShare >= 1 {
		t.Errorf("suggestChooseShare is %g -- the cut owns all of the bar or none of it", suggestChooseShare)
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
		"lo, hi := a.footageWindow(target)",
		`"between %.0f and %.0f seconds of footage, in at most %d segments. Stop at the "`,
		"total := cutLen(applyFx(segs, fx))", // and measured the same way
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
		"the footage they come to -- end minus start, added up -- lands in the range you were given",
		"Anywhere inside it is right; do not trim towards its middle.",
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
		"keep such a stretch whole, as ONE segment") {
		t.Error("the effect rules no longer say how to show a stretch without watching it")
	}
}

// A segment under a speed effect is allowed to be long, because that is what
// makes a dull stretch affordable: it costs the video its seconds divided by
// the rate, so two minutes at 4 spends thirty of the target. Without that the
// only way to show half a session inside a five-minute target is to cut
// between every line of speech, which is the answer that ran to 548 segments.
func TestTheWordingsAllowALongSegmentUnderSpeed(t *testing.T) {
	src := readSrc(t, "cut.go")
	if !strings.Contains(src, "keep such a stretch whole, as ONE segment") {
		t.Error("the cut's rules no longer say a stretch to be shown fast stays one segment")
	}
	// the lengths are guides and say so once, in the tail every wording ends
	// with: a model told 45 seconds as a limit packs a long stretch into a row
	// of small segments instead
	if strings.Contains(src, "8 to 45 seconds") {
		t.Error("a wording still states the old fixed 8-to-45-second segment")
	}
	if !strings.Contains(cutReply, "about 8 seconds to a minute, longer where a stretch has to be shown but not watched") {
		t.Error("nothing says how long a segment runs")
	}
	// said once, in the tail every wording ends with, and nowhere else
	if n := strings.Count(src, "8 seconds to a minute"); n != 1 {
		t.Errorf("the segment length is stated %d times in cut.go, want once (in cutReply)", n)
	}
}

// The speed budget is arithmetic, and the wording does it rather than leaving
// the model to derive it. "Show half the session" against a shorter target is
// two numbers and a rate: the seconds that must run fast follow from them, and
// a model told the formula with a worked example spends its call choosing
// WHICH stretches rather than rediscovering how many seconds -- which is what
// eleven minutes of reasoning once went on.
func TestTheWordingSaysHowManySecondsMustRunFast(t *testing.T) {
	for _, want := range []string{
		"(F-T)*r/(r-1) seconds must run at r",
		"F 850, T 720, r 4 means 173 seconds fast",
		"A clip with captions on it runs at 1",
	} {
		if !strings.Contains(speedSystem, want) {
			t.Errorf("the speed pass's wording no longer says %q", want)
		}
	}
	// and the formula is right, or the model learns the wrong sum: 850 s of
	// footage into a 720 s target at 4 is 173 s fast and 677 at 1
	f, target, r := 850.0, 720.0, 4.0
	b := (f - target) * r / (r - 1)
	if int(b+0.5) != 173 || int(f-b+0.5) != 677 {
		t.Errorf("the formula gives B=%.0f, N=%.0f", b, f-b)
	}
	if got := (f - b) + b/r; got < 719.5 || got > 720.5 {
		t.Errorf("677 s at 1 and 173 s at 4 come to %.1f s, not the target", got)
	}
}

// The length the run aims at is the one the user context names. It was a box
// on the Cut page as well, and for a week of runs the two disagreed -- the box
// quietly winning at 300 while the sentence beside it read 12 minutes, and
// every attempt refused for a length nobody had asked for.
func TestTheLengthComesFromTheUserContext(t *testing.T) {
	for _, c := range []struct {
		ctx  string
		want float64
		ok   bool
	}{
		{"Show half of the video (~12min), but speed the rest", 720, true},
		{"about 15 min of it", 900, true},
		{"keep it to 5 minutes", 300, true},
		{"a 90 s teaser", 90, true},
		{"you get a bonus at 500 wins, and level 2 is where it starts", 0, false},
		{"", 0, false},
	} {
		got, ok := ctxLength(c.ctx)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ctxLength(%q) = %g, %v; want %g, %v", c.ctx, got, ok, c.want, c.ok)
		}
	}
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"target := defTargetSecs",
		"if want, ok := ctxLength(a.sessionCtx()); ok {",
		"the user context names no length",
		"which is the length named ",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go no longer contains %q", want)
		}
	}
	// and there is no second place to say it
	if strings.Contains(readSrc(t, "cut.go"), "ed.target") {
		t.Error("the target box is back on the Cut page's toolbar")
	}
}
