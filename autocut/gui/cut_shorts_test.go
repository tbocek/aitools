package main

// The YouTube Shorts cut style: 20 to 30 seconds built on the one subject the
// user names in the session notes. Effects ride in the cut reply -- what to
// cut and whether to decorate it is one judgement, made once -- and the audit
// then reads BOTH back, its fxchecks correcting or dropping effects as it
// corrects the segments under them. Every style asks for effects now (fxRules,
// and cut_fxreply_test.go for the kinds); what these pin is Shorts itself: it
// is on the menu, it words its own effects for a phone, the audit is told
// about effects at all, a tiny target relaxes the segment-count arithmetic
// instead of fighting it, the proposed effects become the page's own zooms,
// speeds and captions with the dialogs' defaults for everything the model is
// not trusted with, and the seams -- the reply parse, both validators, and the
// judgement that reads a leftover five-minute target as the box from other
// work rather than as a wish, made once in the box itself when the wording is
// picked and again at ▶.

import (
	"math"
	"os"
	"strings"
	"testing"
)

func TestTheShortsStyleIsOnTheMenu(t *testing.T) {
	ownConfig(t)
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
	// EVERY style asks for fx, in the same reply as the segments: an effect is
	// part of the same judgement as the cut it decorates, and a cut proposed
	// with nothing on it is a cut nobody finished. Shorts words it for a phone
	// and the other three share fxRules, but the reply shape is one shape --
	// suggestParse reads it without knowing which wording asked.
	for _, s := range a.promptStyleList("cut") {
		sent := strings.TrimSpace(sysSystem) + "\n\n" + s.Text
		if !strings.Contains(sent, `"fx":[`) {
			t.Errorf("cut style %q no longer asks for effects in its reply", s.Name)
			continue
		}
		for _, want := range []string{
			`"kind":"zoom"`, `"kind":"speed"`, `"kind":"text"`,
			"inside one of your segments",
		} {
			if !strings.Contains(sent, want) {
				t.Errorf("the %q prompt does not say %q", s.Name, want)
			}
		}
	}
	// ...and the three that are not Shorts take the same wording from one
	// place, so a rule added to effects cannot reach two cuts out of three
	for _, s := range a.promptStyleList("cut") {
		if s.Name == shortsStyleName {
			continue
		}
		if !strings.Contains(s.Text, strings.TrimSpace(fxRules)) {
			t.Errorf("cut style %q spells its own effects rules out instead of sharing fxRules", s.Name)
		}
	}
	// what the user wrote about the session directs the effects too, not just
	// the choosing: "speed the boring parts up and show them" is an
	// instruction about which seconds are cut as much as about the decoration.
	// A rule about the notes, so the system context's (every cut is sent
	// behind it), and not the effects wording's
	withNotes := &App{}
	withNotes.setSessionCtx("speed the boring parts up and show them")
	for _, want := range []string{
		"caption each thing as it is named",
		"It decides segments too",
	} {
		if !strings.Contains(withNotes.ctxBlock(), want) {
			t.Errorf("the notes block does not say %q, so the session notes "+
				"cannot ask for a dull stretch to be kept at speed", want)
		}
	}
	// ...and the audit is told to read them back: one fxcheck per effect,
	// held inside the segments as corrected
	audP := a.sysPrompt("audit")
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
	many := make([]sugFx, fxMaxProposed*2)
	for i := range many {
		many[i] = sugFx{Kind: "zoom", Start: float64(i * 10), End: float64(i*10 + 2)}
	}
	if got := fxFromReply(many); len(got) != fxMaxProposed {
		t.Errorf("%d effects survived of %d, want the lid of %d", len(got), len(many), fxMaxProposed)
	}
	// ...and the lid has to clear what the wording itself budgets, or the
	// parser is the thing dropping effects rather than the model choosing
	// them. fxRules asks for three or four per five minutes of finished
	// video; a twenty-minute cut asking at that rate must fit under the lid.
	if want := 4 * (20 / 5); fxMaxProposed < want {
		t.Errorf("the lid is %d, under the %d a %d-minute cut may ask for at the rate "+
			"fxRules budgets -- the effects past it vanish without a word", fxMaxProposed, want, 20)
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
	ownConfig(t)
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
			"return segs, fx, nil",
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
			"add up how long your segments RUN",
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
