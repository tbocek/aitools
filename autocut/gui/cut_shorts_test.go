package main

// The YouTube Shorts cut style: 20 to 30 seconds built on the one subject the
// user names in the session notes, and the one style whose cut gets effects.
// The effects are chosen by their own call (suggestFx) once the audit has
// settled the segments -- NOT in the cut reply, where they used to ride and
// where the audit then rewrote the segments under them. What these pin: the
// style is on the menu and no cut style asks for fx any more, the effects
// prompt is its own registered key, a tiny target relaxes the segment-count
// arithmetic instead of fighting it, the proposed effects become the page's
// own zooms, speeds and captions with the dialogs' defaults for everything
// the model is not trusted with, and the seams -- the reply parse, both
// validators, the clamp that reads a leftover five-minute target as the box
// from other work rather than as a wish.

import (
	"os"
	"strings"
	"testing"
)

func TestTheShortsStyleIsOnTheMenu(t *testing.T) {
	a := &App{root: t.TempDir()}
	var shorts string
	for _, s := range a.promptStyleList("cut") {
		if s.Name == shortsStyleName {
			shorts = s.Text
		}
	}
	if shorts == "" {
		t.Fatalf("the cut styles do not offer %q", shortsStyleName)
	}
	for _, want := range []string{
		"20 to 30 seconds",   // the format's length, said to the model
		"ABOUT THIS SESSION", // the subject comes from the user's own notes
	} {
		if !strings.Contains(shorts, want) {
			t.Errorf("the Shorts prompt does not say %q", want)
		}
	}
	// NO cut style asks for fx: effects are chosen by suggestFx against the
	// audited segments, and a cut reply's fx (a project's edited prompt may
	// still send them) are only a fallback. A shipped style asking again
	// would reintroduce the misalignment the third call exists to end.
	for _, s := range a.promptStyleList("cut") {
		if strings.Contains(s.Text, `"fx"`) {
			t.Errorf("cut style %q asks for effects in its reply — they are chosen after the audit now", s.Name)
		}
	}
	// ...and the effects prompt is where that schema lives instead, told in
	// plain words that the segments are final and effects lie inside them
	fxP := a.prompt("effects")
	for _, want := range []string{
		`{"fx":[`, `"kind":"zoom"`, `"kind":"speed"`, `"kind":"text"`,
		"The segments are final",
		"inside one of the segments",
	} {
		if !strings.Contains(fxP, want) {
			t.Errorf("the effects prompt does not say %q", want)
		}
	}
}

// Four segments minimum made sense when every cut was minutes long; a 25 s
// Short holds one to three beats, and rejecting a two-segment answer to a
// 25 s target burns attempts on a rule the target itself contradicts.
func TestATinyTargetNeedsFewerSegments(t *testing.T) {
	for _, c := range []struct {
		target float64
		want   int
	}{{25, 1}, {45, 2}, {60, 3}, {90, 4}, {300, 4}} {
		if got := minSuggestSegs(c.target); got != c.want {
			t.Errorf("minSuggestSegs(%.0f) = %d, want %d", c.target, got, c.want)
		}
	}
}

// The model is trusted with WHEN and WHICH KIND; the HOW is the app's own
// defaults -- the centre punch-in the zoom dialog opens with, half speed for
// a slow-mo with no rate, the caption box left empty for textBox() to fill.
// Entries that make no sense are dropped, never fatal.
func TestTheRepliedEffectsBecomeCutFx(t *testing.T) {
	got := fxFromReply([]sugFx{
		{Kind: "zoom", Start: 10, End: 13},
		{Kind: "speed", Start: 20, End: 24},             // no rate: a slow-mo
		{Kind: "speed", Start: 30, End: 40, Rate: 1000}, // beyond fxMaxRate
		{Kind: "text", Start: 50, End: 52, Text: " NO WAY "},
		{Kind: "text", Start: 60, End: 61},     // wordless: dropped
		{Kind: "confetti", Start: 70, End: 71}, // unknown kind: dropped
		{Kind: "zoom", Start: 80, End: 79},     // inside out: dropped
	})
	if len(got) != 4 {
		t.Fatalf("kept %d effects, want 4: %v", len(got), got)
	}
	z := got[0]
	if z.Kind != "zoom" || z.T != 10 || z.Dur != 3 || z.Cx != 0.5 || z.Cy != 0.5 || z.Hf != 0.6 {
		t.Errorf("the zoom came out %+v — the centre punch-in is the app's own default", z)
	}
	if z.Trans != 1 || z.Tout != 1 {
		t.Errorf("a 3 s zoom glides %g/%g, want the dialog's second each way", z.Trans, z.Tout)
	}
	if s := got[1]; s.Kind != "speed" || s.Rate != 0.5 || s.Dur != 4 {
		t.Errorf("an unnamed rate came out %+v, want half speed over its 4 s", s)
	}
	if s := got[2]; s.Rate > fxMaxRate {
		t.Errorf("rate 1000 survived as %g — clampSpeed is not applied", s.Rate)
	}
	tx := got[3]
	if tx.Kind != "text" || tx.Text != "NO WAY" || tx.Dur != 2 {
		t.Errorf("the caption came out %+v", tx)
	}
	if tx.Wf != 0 || tx.Hf != 0 {
		t.Errorf("the caption brought a box (%g×%g) — no box means textBox()'s caption default", tx.Wf, tx.Hf)
	}
	// seasoning has a lid
	many := make([]sugFx, 20)
	for i := range many {
		many[i] = sugFx{Kind: "zoom", Start: float64(i * 10), End: float64(i*10 + 2)}
	}
	if got := fxFromReply(many); len(got) != 8 {
		t.Errorf("%d effects survived of 20, want the lid of 8", len(got))
	}
}

func TestTheShortsWiringIsInPlace(t *testing.T) {
	for file, wants := range map[string][]string{
		"cut_suggest.go": {
			"`json:\"fx\"`", // a cut reply carrying fx still parses -- the fallback
			"} else if len(out.Segments) < minSuggestSegs(target) {",
			"if len(merged) < minSuggestSegs(target) ||", // ...and the audit accepts by the same count
			`shorts := a.promptPickName("cut") == shortsStyleName`,
			"shortsClamped := shorts && (target < 15 || target > 45)",
			"return segs, fxFromReply(out.Fx), nil",
			// the third call comes AFTER auditCut and only replaces the fallback
			// when it actually delivered; an empty answer keeps the inline fx
			"if got := a.suggestFx(rows, segs); len(got) > 0 {",
			// the clamp runs against the segments as applied, snapEdge and
			// coalesce included, which is the guarantee the prompts cannot give
			"kept := clampFxToSegs(fx, a.ed.segs)",
			"a.ed.fx = fx", // the effects land on the page, replacing the old
		},
		// still reachable: third in the bar's menu, after the two it follows
		"cut.go": {`promptSlot{"effects", "Effects",`},
	} {
		src := readSrc(t, file)
		for _, want := range wants {
			if !strings.Contains(src, want) {
				t.Errorf("%s does not contain %q — the Shorts flow came unwired", file, want)
			}
		}
	}
	p, err := os.ReadFile("prompts.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p), "{shortsStyleName, strings.TrimSpace(shortsSystem)}") {
		t.Error("prompts.go does not offer the Shorts style under the cut key")
	}
	if !strings.Contains(string(p), `{key: "effects", def: strings.TrimSpace(effectsSystem)}`) {
		t.Error("prompts.go does not register the effects prompt — suggestFx would send a project's dead air")
	}
}
