package main

import (
	"regexp"
	"strings"
	"testing"
)

// What every job is told about the material, and that no job assembles a system
// message any other way. The point of the prompt is not that it exists -- it is
// that there is one of it: a wording added tomorrow is told how a stamp reads
// without its author having to remember to say so.
func TestEveryJobIsToldTheFormatsOnce(t *testing.T) {
	ownConfig(t)
	a := &App{}

	// it is a prompt like the others, and it is the first of them: the bench
	// lists them in the order they are sent, and this one is sent in front of
	// all of them
	if d := promptDefFor("system"); strings.TrimSpace(d.def) != strings.TrimSpace(sysSystem) {
		t.Fatal(`prompt "system" is not the registered system context`)
	}
	if promptDefs[0].key != "system" {
		t.Errorf("the system context is registered %q, not first -- the registry order "+
			"is the order the bench shows and the order the calls go out in", promptDefs[0].key)
	}

	for _, key := range []string{"describe", "fix", "cut", "audit", "narrate", "youtube"} {
		got := a.sysPrompt(key)
		if !strings.HasPrefix(got, strings.TrimSpace(sysSystem)) {
			t.Errorf("the %s job is not told the formats first:\n%s", key, got)
		}
		if !strings.HasSuffix(got, a.prompt(key)) {
			t.Errorf("the %s job's own prompt is not what follows the context", key)
		}
	}

	// and editing the box reaches every one of them, which is the only reason
	// it is a box and not a const
	a.setPrompt("system", "stamps are in minutes")
	for _, key := range []string{"cut", "narrate"} {
		if !strings.HasPrefix(a.sysPrompt(key), "stamps are in minutes") {
			t.Errorf("the edited system context is not what the %s job is sent", key)
		}
	}
	// emptied, it takes itself away rather than sending a blank run-up
	a.setPrompt("system", "   ")
	if got := a.sysPrompt("cut"); got != a.prompt("cut") {
		t.Errorf("an emptied system context still sends something in front of the job: %q",
			got[:min(80, len(got))])
	}
}

// Every system message in the app is built through the one seam. A job that
// reaches for its prompt directly is a job that is never told the formats, and
// it would look exactly like the others until its answers came back in the
// wrong clock.
func TestNoJobAssemblesItsOwnSystemMessage(t *testing.T) {
	for file, key := range map[string]string{
		"describe.go":    "describe",
		"transcript.go":  "fix",
		"cut_suggest.go": "cut",
		"narrate.go":     "narrate",
		"publish.go":     "youtube",
	} {
		src := readSrc(t, file)
		if !strings.Contains(src, `a.sysPrompt("`+key+`")`) {
			t.Errorf(`%s no longer sends a.sysPrompt(%q)`, file, key)
		}
		for _, raw := range []string{`msg("system", a.prompt("` + key, `system := a.prompt("` + key} {
			if strings.Contains(src, raw) {
				t.Errorf(`%s builds a system message out of a.prompt(%q), so that job is `+
					"the one that is never told how a stamp reads", file, key)
			}
		}
	}
	if src := readSrc(t, "cut_suggest.go"); !strings.Contains(src, `a.sysPrompt("audit")`) {
		t.Error(`the audit no longer goes out through sysPrompt`)
	}
}

// The wordings stop repeating what the context now says. Leaving both in is not
// wrong to read, but it is what the change was for: the sentences drifted apart
// precisely because they were written four times.
func TestTheCutWordingsDoNotRepeatTheSystemContext(t *testing.T) {
	for _, p := range []struct{ name, text string }{
		{"general", genericSystem},
		{"highlights", suggestSystem},
		{"rating", ratingSystem},
		{"showcase", showcaseSystem},
		{"shorts", shortsSystem},
		{"audit", auditSystem},
	} {
		for _, gone := range []string{"[12:04] EVENT", "counting past 59", "4350 seconds"} {
			if strings.Contains(p.text, gone) {
				t.Errorf("the %s wording still explains %q itself, which the system "+
					"context says to every job already", p.name, gone)
			}
		}
	}
	// what a style is for stays in the style: this took the facts out, not the
	// judgement
	if !strings.Contains(shortsSystem, "20 to 30 seconds") {
		t.Error("the Shorts wording lost the length that makes it Shorts")
	}
	if !strings.Contains(sysSystem, `{"segments":`) {
		t.Error("the system context lost the cut's reply shape, which the wordings no longer spell")
	}
}

// shippedPrompts is every wording the binary ships, the system context aside:
// what a job is sent behind that context, and therefore what must not say
// again what the context already said.
func shippedPrompts() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"describe", describeSystem},
		{"fix", fixSystem},
		{"cut (general)", genericSystem},
		{"cut (highlights)", suggestSystem},
		{"cut (rating)", ratingSystem},
		{"cut (showcase)", showcaseSystem},
		{"cut (shorts)", shortsSystem},
		{"cut (shared effects)", fxRules},
		{"cut (shared reply)", cutReply},
		{"audit", auditSystem},
		{"narrate", narrSystem},
		{"youtube", youtubeSystem},
		{"improve", improveSystem},
	}
}

// No wording says a shared rule again. The formats were the easy half and are
// already guarded above; these are the three that had spread furthest, and they
// are the ones that rot quietest -- a format written twice announces itself the
// moment a model answers on the wrong clock, while "never invent" written seven
// times is invisible until the seven have drifted apart, which they had: "never
// invent a moment", "never invent a part, a name or a price", "invent nothing
// that is not in it", "Invent no names, places or outcomes". One rule the model
// met six times, and six places to edit to tighten it once.
func TestNoWordingRepeatsASharedRule(t *testing.T) {
	// how the answer is read: the context's first paragraph
	machineRead := []string{"strict JSON, nothing else", "no markdown", "no code fence", "no code fences"}
	// what may be made up: the context's last
	invented := regexp.MustCompile(`(?i)never invent|invent no|invent nothing`)

	for _, p := range shippedPrompts() {
		for _, gone := range machineRead {
			if strings.Contains(p.text, gone) {
				t.Errorf("the %s wording says %q itself -- the system context tells every "+
					"job the answer is machine-read already", p.name, gone)
			}
		}
		if m := invented.FindString(p.text); m != "" {
			t.Errorf("the %s wording says %q -- not inventing is the system context's "+
				"last paragraph, and it is the rule that must have exactly one home", p.name, m)
		}
	}
	// ...and the context does still say all three, or the above passes because
	// nothing anywhere says them
	for _, want := range []string{"no markdown", "no code fence", "strict JSON", "Never invent",
		"they outrank anything you would otherwise infer"} {
		if !strings.Contains(sysSystem, want) {
			t.Errorf("the system context never says %q, and the wordings were emptied of it "+
				"on the promise that it does", want)
		}
	}
}

// The context says what the four steps ARE, not just what this one is. A job
// that knows it is second of four writes for the third: the describing step is
// writing the only record of the footage the cut will ever see, and the cut is
// choosing seconds the narration will have to talk over. A job that thinks it
// is alone writes for nobody.
func TestTheSystemContextNamesTheWholePipeline(t *testing.T) {
	for _, s := range steps {
		if !strings.Contains(sysSystem, s.label) {
			t.Errorf("the system context never names the %s step, so a job in it is not "+
				"told what becomes of its answer", s.label)
		}
	}
	// the mechanisms every step shares, said here so no wording has to: the two
	// clocks a finished video has, and what the five effect kinds do
	if !strings.Contains(sysSystem, "a time in the video is not a time in the session") {
		t.Error("the system context no longer distinguishes the cut's clock from the session's")
	}
	for _, kind := range []string{"zoom", "text", "speed", "stop", "volume"} {
		if !strings.Contains(sysSystem, kind) {
			t.Errorf("the system context does not say what a %q effect is, so the cut "+
				"wordings and the audit each have to", kind)
		}
	}
}

// TestTheSystemContextHasNoWordings: the formats are the formats -- a session
// stamp reads the same whether the video is cut for Highlights or for Showcase
// -- so this one prompt is outside the wording machinery. That is one flag
// (promptDef.solo) read in four places, and all four are checked here: picking
// a style must not touch its text, the bench row must not name a wording it
// does not have, the ＋ that saves a new wording must not be offered, and the
// improve brief must not head it with one.
func TestTheSystemContextHasNoWordings(t *testing.T) {
	ownConfig(t)
	d := promptDefFor("system")
	if !d.solo {
		t.Fatal("the system context is back in the wording machinery, and the rest of this test says why it is not")
	}
	if len(d.alts) != 0 {
		t.Errorf("the system context has %d alternative wordings, which no style has anything to say about", len(d.alts))
	}
	a := &App{}
	before := a.prompt("system")
	for _, style := range []string{"Highlights", "Rating / tier list", "Showcase", shortsStyleName} {
		a.applyStyle(style)
		if got := a.prompt("system"); got != before {
			t.Fatalf("style %q rewrote the system context, so a job's formats depend on how the video is cut:\n%s", style, short(got))
		}
	}
	// the cut did move, or applyStyle was doing nothing at all and the check
	// above passes for the wrong reason
	if a.prompt("cut") == strings.TrimSpace(genericSystem) {
		t.Error("applyStyle left the cut prompt on the default too, so nothing above was actually exercised")
	}
	for _, name := range a.prepEditNames() {
		if strings.HasPrefix(name, "System context") && strings.Contains(name, "(") {
			t.Errorf("the bench row reads %q, naming a wording the system context does not have", name)
		}
	}
	if src := readSrc(t, "prepedit.go"); !strings.Contains(src, "add.SetVisible(prompt && !promptDefFor(r.key).solo)") {
		t.Error("the ＋ is offered on the system context row, and there is nowhere for a second wording to go")
	}
	user, _ := improveBriefText(t, a)
	if !strings.Contains(user, "=== prompt system ===") {
		t.Error("the improve brief does not show the system context, which is in front of every call it is being asked about")
	}
	if strings.Contains(user, "prompt system (wording:") {
		t.Error("the improve brief names a wording for the system context, and the model can then ask for a change to a wording that does not exist")
	}
}

// improveBriefText is the user half of the brief, which is where the prompts
// are listed.
func improveBriefText(t *testing.T, a *App) (string, []map[string]any) {
	t.Helper()
	msgs := a.improveBrief("nothing in particular")
	for _, m := range msgs {
		if m["role"] == "user" {
			s, _ := m["content"].(string)
			return s, msgs
		}
	}
	t.Fatal("the improve brief has no user message")
	return "", nil
}
