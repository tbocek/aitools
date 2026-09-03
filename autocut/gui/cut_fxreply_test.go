package main

// The two effects a cut style could not ask for until now -- the held frame
// and the volume change -- and the two rules that decide what the model does
// with the rest: which kind fits a moment, and whether the spoken lines are
// the video or instructions about it.

import (
	"strings"
	"testing"
)

// A stop is stored as a speed of 0 (frozenFx), which is why it cannot be asked
// for as one: an omitted rate and a rate of 0 are the same JSON number, and
// reading that as a freeze would hand a still to every reply that forgot to
// name a speed. So the reply names it as its own kind and the parser turns it
// into the storage.
func TestAHeldFrameIsAskedForByNameAndStoredAsASpeedOfZero(t *testing.T) {
	got := fxFromReply([]sugFx{
		{Kind: "stop", Start: 30, End: 32},
		{Kind: "stop", Start: 40, End: 40},  // no length: the app's own hold
		{Kind: "speed", Start: 50, End: 54}, // ...and a rate nobody named is STILL a slow-mo
	})
	if len(got) != 3 {
		t.Fatalf("kept %d effects, want 3: %+v", len(got), got)
	}
	s := got[0]
	if !s.frozenFx() || s.Kind != "speed" || s.T != 30 || s.Dur != 2 {
		t.Errorf("the hold came out %+v, want a speed of 0 over its 2 s", s)
	}
	if s.Trans != 0.3 || s.Tout != 0.3 {
		t.Errorf("the hold fades %g/%g, want the same fade a suggested caption gets", s.Trans, s.Tout)
	}
	if d := got[1].Dur; d <= 0 {
		t.Errorf("a hold with no length came out %g s long — it would never be seen", d)
	}
	if r := got[2].Rate; r != 0.5 {
		t.Errorf("a speed with no rate came out %g; only the kind \"stop\" may mean a freeze", r)
	}
}

// Volume is the one kind whose own field is the whole effect, and 0 is a real
// gain -- silence, which is exactly what a session saying "do not use this
// audio" is asking for. It is also what an absent field parses to, so the two
// are told apart by the field being a pointer: nil is a reply that never
// mentioned a gain and there is nothing to do about it, and an explicit 0 is
// an instruction to obey.
func TestAVolumeChangeNeedsAGainSomebodyMeant(t *testing.T) {
	gain := func(g float64) *float64 { return &g }
	got := fxFromReply([]sugFx{
		{Kind: "volume", Start: 10, End: 18, Gain: gain(3)},
		{Kind: "volume", Start: 20, End: 28},                  // no gain named: nothing to do
		{Kind: "volume", Start: 30, End: 38, Gain: gain(1)},   // as recorded: nothing to do
		{Kind: "volume", Start: 40, End: 48, Gain: gain(500)}, // over the lid
		{Kind: "volume", Start: 50, End: 50, Gain: gain(2)},   // no length
		{Kind: "volume", Start: 60, End: 68, Gain: gain(0)},   // silence, and meant
	})
	if len(got) != 3 {
		t.Fatalf("kept %d effects, want the 3 that say something: %+v", len(got), got)
	}
	v := got[0]
	if v.Kind != "volume" || v.T != 10 || v.Dur != 8 || v.Gain != 3 {
		t.Errorf("the volume came out %+v, want x3 over its 8 s", v)
	}
	if v.Trans != 1 || v.Tout != 1 {
		t.Errorf("the volume ramps %g/%g, want the second each way a suggested zoom gets", v.Trans, v.Tout)
	}
	if g := got[1].Gain; g != fxMaxGain {
		t.Errorf("gain 500 survived as %g — clampGain is not applied", g)
	}
	if m := got[2]; m.T != 60 || m.Gain != 0 {
		t.Errorf("the silenced stretch came out %+v, want the 60 s one muted", m)
	}
	// and the model is offered the number, since the parser now obeys it. The
	// scale is a fact about the effect rather than a judgement about when to
	// use one, so it is said once in the system context every cut goes out
	// behind (syscontext.go) -- what is checked is therefore what is SENT, not
	// the wording on its own
	for _, p := range []string{shortsSystem, fxRules} {
		if !strings.Contains(strings.TrimSpace(sysSystem)+"\n\n"+p, "0 silent") {
			t.Error("nothing the cut is sent says which number means silence")
		}
	}
}

// The audit judges the effects from a list it is handed, so that list has to
// say what each one does. A stop reads as a stop and not as "speed rate 0",
// which is the storage; a volume says its gain, as a speed says its rate.
func TestTheAuditIsToldWhatEachEffectDoes(t *testing.T) {
	src := funcBody(t, "cut_suggest.go", `func \(a \*App\) auditCut\(`)
	for _, pin := range []string{
		`kind = "stop"`,
		`extra = fmt.Sprintf("  gain %g", f.Gain)`,
		`extra = fmt.Sprintf("  rate %g", f.Rate)`,
		"mmss(t0), mmss(t1), extra)",
	} {
		if !strings.Contains(src, pin) {
			t.Errorf("the proposed-effects block no longer says %q", pin)
		}
	}
	if strings.Contains(src, "mmss(t0), mmss(t1)") && !strings.Contains(src, "kind, mmss(t0)") {
		t.Error("the block prints f.Kind, so a held frame reaches the audit as a speed")
	}
}

// svg is the one kind of the app's five a reply cannot ask for: the ink is a
// file on this machine and nothing in a reply can name one. It is left out of
// the wording rather than accepted and dropped, so the model is never told to
// do something whose answer is thrown away.
func TestTheOverlayStaysAThingAHandPlaces(t *testing.T) {
	a := &App{root: t.TempDir()}
	for _, s := range a.promptStyleList("cut") {
		if strings.Contains(s.Text, `"kind":"svg"`) {
			t.Errorf("cut style %q asks for an svg, which needs a file path it cannot know", s.Name)
		}
	}
	if got := fxFromReply([]sugFx{{Kind: "svg", Start: 10, End: 12}}); len(got) != 0 {
		t.Errorf("an svg off a reply parsed to %+v — it would draw nothing, from no file", got)
	}
}

// Every kind the parser accepts is a kind the wording asks for, and every kind
// the wording asks for is one the parser accepts. A prompt naming a kind that
// parses to nothing spends the model's attention on an answer that is thrown
// away; a parser accepting one nobody asks for is code no reply reaches.
func TestTheWordingAndTheParserNameTheSameEffects(t *testing.T) {
	kinds := []string{"zoom", "text", "speed", "stop", "volume"}
	// the reply shape is one shape, spelled once in the system context every
	// cut wording is sent behind (syscontext.go) -- so it is the context that
	// has to offer every kind, not each wording
	for _, k := range kinds {
		if !strings.Contains(sysSystem, `"kind":"`+k+`"`) {
			t.Errorf("the system context's cut shape never offers %q", k)
		}
	}
	src := funcBody(t, "cut_suggest.go", `func fxFromReply\(`)
	for _, k := range kinds {
		if !strings.Contains(src, `case "`+k+`"`) {
			t.Errorf("the wording asks for %q and fxFromReply drops it on the floor", k)
		}
	}
}

// Which kind goes where is a judgement, and the wording makes it for the
// model: the moment says which effect it needs. Zoom onto the thing that
// matters, caption what is going on, rush what has to be shown but not
// watched.
func TestTheWordingSaysWhichEffectAMomentNeeds(t *testing.T) {
	for _, want := range []string{
		"Pick the kind by what the moment needs",
		"easy to miss -> zoom onto it",
		"would not know what is happening -> text saying it",
		"shown but not watched -> speed",
	} {
		if !strings.Contains(fxRules, want) {
			t.Errorf("the shared effects wording no longer says %q, so the kind is a "+
				"free choice rather than one the moment makes", want)
		}
	}
}

// What the speakers say is either the video or instructions about it, the
// session decides which, and only the editor knows which session this is. Read
// the wrong way it is the worst answer the app can give -- an aside to the
// editor captioned into the video, or the video thrown away as asides -- so
// the rule reads the notes rather than guessing, and defaults to content. It
// is a rule about the notes, so it is the system context's: every cut wording
// is sent behind it, Shorts included, and none of them says it again.
func TestTheNotesSayWhetherTheSpeechIsTheVideoOrInstructionsAboutIt(t *testing.T) {
	// it rides with the notes it is about (ctxSpeech), so a session with an
	// empty notes box is told none of it
	a0 := &App{}
	a0.setSessionCtx("the tower is Kenos")
	sent := a0.ctxBlock()
	for _, want := range []string{
		"The speech is content unless the user context above says otherwise", // the default...
		"Where the user context calls it directions",                         // ...and the one thing that changes it
		"never caption them",
		"It decides segments too",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("the notes block no longer says %q", want)
		}
	}
	if strings.Contains(sysSystem, "Where the user context calls it directions") {
		t.Error("the system prompt says it too, to every job of every session")
	}
	a := &App{root: t.TempDir()}
	list := a.promptStyleList("cut")
	if len(list) < 4 {
		t.Fatalf("the cut offers %d wordings, want the four styles", len(list))
	}
	for _, s := range list {
		if strings.Contains(s.Text, "the speech is content") {
			t.Errorf("cut style %q says how to read the spoken lines itself; the system context does", s.Name)
		}
	}
}
