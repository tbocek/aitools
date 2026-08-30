package main

// The YouTube Shorts cut style: 20 to 30 seconds built on the one subject the
// user names in the session notes, and the one style whose cut gets effects.
// The effects ride in the cut reply -- what to cut and whether to decorate it
// is one judgement, made once -- and the audit then reads BOTH back, its
// fxchecks correcting or dropping effects as it corrects the segments under
// them. What these pin: the style is on the menu and is the only one asking
// for fx, the audit is told about effects, a tiny target relaxes the
// segment-count arithmetic instead of fighting it, the proposed effects
// become the page's own zooms, speeds and captions with the dialogs' defaults
// for everything the model is not trusted with, and the seams -- the reply
// parse, both validators, and the judgement that reads a leftover five-minute
// target as the box from other work rather than as a wish, made once in the
// box itself when the wording is picked and again at ▶.

import (
	"math"
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
	// the Shorts style is the ONE style that asks for fx, in the same reply
	// as the segments: an effect is part of the same judgement as the cut it
	// decorates. The other styles never mention them, so their replies stay
	// cheap to parse and their prompts stay short.
	for _, s := range a.promptStyleList("cut") {
		asks := strings.Contains(s.Text, `"fx":[`)
		if s.Name == shortsStyleName {
			if !asks {
				t.Error("the Shorts style no longer asks for effects in its reply")
			}
			for _, want := range []string{
				`"kind":"zoom"`, `"kind":"speed"`, `"kind":"text"`,
				"inside one of your segments",
			} {
				if !strings.Contains(s.Text, want) {
					t.Errorf("the Shorts prompt does not say %q", want)
				}
			}
		} else if asks {
			t.Errorf("cut style %q asks for effects — only the Shorts style decorates", s.Name)
		}
	}
	// ...and the audit is told to read them back: one fxcheck per effect,
	// held inside the segments as corrected
	audP := a.prompt("audit")
	for _, want := range []string{`"fxchecks":[`, "inside one of the segments"} {
		if !strings.Contains(audP, want) {
			t.Errorf("the audit prompt does not say %q", want)
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

// TestATargetIsAWishOrALeftover: the one judgement both the pick and the run
// make about the ▶ target box when the style is Shorts. The long cut's
// default (300) becomes 25; a number already inside the format is a wish and
// stays, the window's edges included; an empty box parses to 0 and is fixed.
func TestATargetIsAWishOrALeftover(t *testing.T) {
	for _, c := range []struct {
		in, want float64
		changed  bool
	}{
		{300, 25, true}, // the long cut's default, left over
		{0, 25, true},   // an empty or unreadable box
		{14, 25, true},
		{15, 15, false}, // the edges are believed
		{45, 45, false},
		{30, 30, false}, // a wish
		{46, 25, true},
	} {
		got, changed := shortsTargetFix(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("shortsTargetFix(%g) = %g, %v; want %g, %v",
				c.in, got, changed, c.want, c.changed)
		}
	}
}

// A highlight cut may run half over its target -- it is a wish. A Short may
// not: 20 to 30 seconds is a promise, so its ceiling is a fifth over. This is
// the gate that stopped a 25 s target shipping as a 53 s "Short".
func TestAShortMayRunAFifthOverNeverHalfOver(t *testing.T) {
	a := &App{root: t.TempDir()}
	if lo, hi := a.suggestWindow(25); lo != 15 || hi != 37.5 {
		t.Errorf("default window = %.1f..%.1f, want 15..37.5", lo, hi)
	}
	a.pickPromptStyle("cut", shortsStyleName)
	if lo, hi := a.suggestWindow(25); lo != 15 || math.Abs(hi-30) > 1e-9 {
		t.Errorf("shorts window = %.1f..%.1f, want 15..30", lo, hi)
	}
}

func TestTheShortsWiringIsInPlace(t *testing.T) {
	for file, wants := range map[string][]string{
		"cut_suggest.go": {
			"`json:\"fx\"`", // a cut reply carrying fx still parses -- the fallback
			"} else if len(out.Segments) < minSuggestSegs(target) {",
			`shorts := a.promptPickName("cut") == shortsStyleName`,
			"target, shortsClamped = shortsTargetFix(target)",
			// both acceptance gates ask the style-aware window, not a shared 1.5x
			"if len(merged) < minSuggestSegs(target) || total < lo || total > hi {",
			"if lo, hi := a.suggestWindow(target); total < lo || total > hi {",
			"return segs, fxFromReply(out.Fx), nil",
			// the audit gets the effects with the segments and hands both back
			"segs, fx = a.auditCut(session, target, segs, fx)",
			// the clamp runs against the segments as applied, snapEdge and
			// coalesce included, which is the guarantee the prompts cannot give
			"kept := clampFxToSegs(fx, a.ed.segs)",
			"a.ed.fx = fx", // the effects land on the page, replacing the old
		},
		"cut.go": {
			// the pick corrects the box itself, through the shared judgement
			"if fixed, changed := shortsTargetFix(cur); changed {",
			// the prompt makes the model budget: divide the target across the
			// notes' parts, then trade seconds -- a directed plan, not a vibe
			"Divide the target length by the number of beats",
			"trade seconds between beats, keeping the same total",
			"add up end minus start across your segments",
			// generic over the notes: the beat count is whatever the editor
			// wrote -- one part or five -- and the segment count follows the
			// beats rather than a fixed 1-to-3
			"one part, three, five",
			"As many segments as the beats need",
		},
		"prompts.go": {
			// ...and picking a wording is what asks for that correction
			"a.styleTarget(key, name)",
		},
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
	if strings.Contains(string(p), `{key: "effects"`) {
		t.Error("prompts.go still registers the effects prompt — the third call is gone, its key goes the way of \"thumbnail\"")
	}
}
