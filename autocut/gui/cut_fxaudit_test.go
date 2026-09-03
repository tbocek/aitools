package main

// The audit's second job: reading the proposed effects back. The cut reply
// carries segments and fx as one judgement, and the audit corrects both --
// its fxchecks verdicts land here, with the segment checks' own discipline,
// so a model that answers with a number never proposed, a fix that changes
// nothing, or a band of no length cannot damage the proposal.

import (
	"strings"
	"testing"
)

func fxAuditIn() []cutFx {
	return []cutFx{
		{Kind: "zoom", T: 15, Dur: 6, Cx: 0.5, Cy: 0.5, Hf: 0.6, Trans: 2, Tout: 2},
		{Kind: "speed", T: 60, Dur: 8, Rate: 0.5, Trans: 1, Tout: 1},
		{Kind: "text", T: 80, Dur: 3, Text: "hello"},
	}
}

func TestAnOkAndNonsenseVerdictsChangeNoEffect(t *testing.T) {
	a := &App{}
	in := fxAuditIn()
	out, changed := a.applyFxChecks(in, []fxCheck{
		{I: 1, Verdict: "ok", Start: 15, End: 21},
		{I: 2, Verdict: "fix", Start: 60, End: 68}, // nothing changed: an ok
		{I: 3, Verdict: "fix", Start: 90, End: 85}, // ends before it starts
		{I: 9, Verdict: "drop"},                    // never proposed
	})
	if changed != 0 || len(out) != 3 {
		t.Fatalf("changed=%d, %d effects left — the proposal was damaged by noise", changed, len(out))
	}
	for i := range out {
		if out[i] != in[i] {
			t.Errorf("effect %d came back as %+v, want untouched", i, out[i])
		}
	}
}

func TestAFixMovesTheBandAndKeepsItPlayable(t *testing.T) {
	a := &App{}
	out, changed := a.applyFxChecks(fxAuditIn(), []fxCheck{
		{I: 1, Verdict: "fix", Start: 20, End: 23, Why: "follows the moved segment"},
	})
	if changed != 1 || len(out) != 3 {
		t.Fatalf("changed=%d, %d effects", changed, len(out))
	}
	z := out[0]
	if z.T != 20 || z.Dur != 3 {
		t.Errorf("the zoom went to T=%.0f Dur=%.0f, want 20/3", z.T, z.Dur)
	}
	// six seconds down to three: the 2 s glides shrink in proportion, so the
	// camera still holds the region in the middle (trimFades)
	if z.Trans != 1 || z.Tout != 1 {
		t.Errorf("the glides came along unshrunk: %.1f/%.1f, want 1/1", z.Trans, z.Tout)
	}
}

func TestADropTakesTheEffectOutAndOnlyThatOne(t *testing.T) {
	a := &App{}
	out, changed := a.applyFxChecks(fxAuditIn(), []fxCheck{
		{I: 1, Verdict: "drop", Why: "its segment was dropped"},
	})
	if changed != 1 || len(out) != 2 {
		t.Fatalf("changed=%d, %d effects left, want 2", changed, len(out))
	}
	if out[0].Kind != "speed" || out[1].Kind != "text" {
		t.Errorf("the wrong effect went: %v", out)
	}
}

func TestAFixedSpeedKeepsARateTheRenderCanBuild(t *testing.T) {
	a := &App{}
	in := []cutFx{{Kind: "speed", T: 10, Dur: 20, Rate: 8}}
	out, _ := a.applyFxChecks(in, []fxCheck{{I: 1, Verdict: "fix", Start: 10, End: 12}})
	if len(out) != 1 {
		t.Fatal("the speed vanished")
	}
	// playable means clampSpeed has nothing left to take: what the audit
	// hands on is already inside the render's limits
	if r, d := clampSpeed(out[0].Rate, out[0].Dur); r != out[0].Rate || d != out[0].Dur {
		t.Errorf("the fixed speed is not playable: rate %.2f over %.1f s (clamp says %.2f over %.1f)",
			out[0].Rate, out[0].Dur, r, d)
	}
}

// The wiring: the cut reply's fx reach the audit, the audit request names
// them by number, and the reply's fxchecks come back through applyFxChecks.
func TestTheAuditIsAskedAboutTheEffects(t *testing.T) {
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"segs, fx = a.auditCut(session, target, span, segs, fx)",
		`fxBlock = "PROPOSED EFFECTS:\n" + b.String() + "\n"`,
		"fxOut, fxChanged := a.applyFxChecks(fx, out.FxChecks)",
		"fixed+dropped+len(out.Add)+fxChanged == 0",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go does not contain %q — the effects never meet their audit", want)
		}
	}
}
