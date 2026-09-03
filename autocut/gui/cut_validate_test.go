package main

// What a cut answer is told about itself.
//
// One run's second attempt had three faults -- half its timestamps past the
// end of the recording (stamps read as decimals), the speed it meant written
// onto its segments where the parser dropped it, and a total nowhere near the
// target -- and was told one of them. The check is a function now, it says
// everything it found, and a rate on a segment is read as the speed effect it
// means.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func validEd(t *testing.T) *App {
	t.Helper()
	ed := newTestEd(t)
	ed.vids = []tlVideo{{base: "a", path: "a.mkv", start: 0, dur: 1695, interval: 4, fps: 30}}
	ed.relayout()
	ed.a.ed = ed
	return ed.a
}

// A rate on a segment is the speed effect it means: the same seconds, at that
// rate, counted into the total the model is judged by.
func TestASpeedWrittenOnASegmentIsTheEffectItMeans(t *testing.T) {
	a := validEd(t)
	reply := `{"segments":[{"start":0,"end":60},{"start":60,"end":300,"speed":4},{"start":300,"end":360},{"start":360,"end":600,"rate":4}],"fx":[]}`
	segs, fx, problem := a.checkCutReply(reply, 300, 1695, 1)
	if problem != "" {
		t.Fatalf("a cut that lands inside the window with its speeds counted was refused: %s", problem)
	}
	if len(segs) != 4 {
		t.Errorf("%d segments came back, want 4", len(segs))
	}
	speeds := 0
	for _, f := range fx {
		if f.Kind == "speed" && f.Rate == 4 {
			speeds++
		}
	}
	if speeds != 2 {
		t.Errorf("%d speed effects came back for two sped-up segments", speeds)
	}
	// 60 + 240/4 + 60 + 240/4 = 240 s of video, inside 180-450; read without
	// the speeds it would be 600 and refused
	if total := cutLen(applyFx(segs, fx)); total < 235 || total > 245 {
		t.Errorf("the total with the segments' own speeds is %.0f, want about 240", total)
	}
}

// Every fault at once, worst first, with the one conversion done on the
// model's own number.
func TestEveryFaultIsNamedInOneCorrection(t *testing.T) {
	a := validEd(t)
	reply := `{"segments":[{"start":0,"end":100},{"start":100,"end":232,"speed":4},{"start":232,"end":350},{"start":350,"end":1320}],` +
		`"fx":[{"kind":"text","start":2801,"end":2804,"text":"What we want"},{"kind":"text","start":10,"end":14,"text":"ok"}]}`
	_, _, problem := a.checkCutReply(reply, 300, 1695, 1)
	for _, want := range []string{
		"1 of your timestamps lie after the session ends at 1695 s",
		"2801 is not a second: a stamp [28:01] is mm*60+ss, 1681",
		"s of finished video from 1320 s of footage (the speed effects counted)",
		"where 180 to 450 is accepted",
	} {
		if !strings.Contains(problem, want) {
			t.Errorf("the correction does not say %q:\n%s", want, problem)
		}
	}
	// the total counts the segment's own speed: 132 s at 4 is about 33 s of
	// video, so the whole comes to roughly 1220 rather than the 1320 of footage
	if m := regexp.MustCompile(`total (\d+) s of finished video`).FindStringSubmatch(problem); m == nil {
		t.Errorf("no total in the correction:\n%s", problem)
	} else if n, _ := strconv.Atoi(m[1]); n < 1150 || n > 1300 {
		t.Errorf("the total is %d, which does not count the segment's speed (want about 1220)", n)
	}
	// and the order is the order of the damage: the stamps before the total,
	// because the total is a consequence of them
	if i, j := strings.Index(problem, "timestamps"), strings.Index(problem, "total"); i < 0 || j < i {
		t.Errorf("the total is named before the fault underneath it:\n%s", problem)
	}
	// a number that does not read as a stamp gets no invented conversion
	if h := mmssHint(2875, 1695); h != "" {
		t.Errorf("2875 was read as a stamp: %q", h)
	}
	if h := mmssHint(99999, 1695); h != "" {
		t.Errorf("a number past any stamp got a hint: %q", h)
	}
}

// The floors and ceilings still hold, and an empty or truncated answer is
// still named as such rather than parsed.
func TestTheShapeGatesStillHold(t *testing.T) {
	a := validEd(t)
	if _, _, p := a.checkCutReply("", 300, 1695, 1); !strings.Contains(p, "no answer at all") {
		t.Errorf("an empty answer was not named: %q", p)
	}
	if _, _, p := a.checkCutReply(`{"segments":[{"start":0,"end":20`, 300, 1695, 1); !strings.Contains(p, "stopped in the middle") {
		t.Errorf("a truncated answer was not named: %q", p)
	}
	if _, _, p := a.checkCutReply(`{"segments":[{"start":0,"end":300}],"fx":[]}`, 300, 1695, 1); !strings.Contains(p, "fewer than") {
		t.Errorf("one segment was not refused: %q", p)
	}
	var b strings.Builder
	b.WriteString(`{"segments":[{"start":0,"end":60},{"start":60,"end":120},{"start":120,"end":180},{"start":180,"end":240}],"fx":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"kind":"text","start":1,"end":2,"text":"x"}`)
	}
	b.WriteString("]}")
	if _, _, p := a.checkCutReply(b.String(), 300, 1695, 1); !strings.Contains(p, "subtitle track") {
		t.Errorf("forty captions were not refused: %q", p)
	}
	// the loop hands the reply to the check and takes the cut from it
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"segs, fx, problem := a.checkCutReply(reply, target, span, try+1)",
		"return segs, fx, nil",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go no longer contains %q", want)
		}
	}
	// and the reply shape names the field, so writing it is not a guess
	if !strings.Contains(readSrc(t, "syscontext.go"), `"speed":<rate, only on a segment that runs at that rate from end to end>`) {
		t.Error("the system context does not name speed on a segment, so the model is guessing when it writes one")
	}
}
