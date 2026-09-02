package main

// The Improve button: what goes back to the model when someone says the tool
// did the wrong thing. Nothing here talks to a server -- the question is built
// and read, never sent.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The recorded pages are the only record of what the model was told, and they
// are HTML because a person has to be able to open one. Reading them back has
// to survive that: the escaping undone, the images named rather than quoted --
// a data URL is most of the file and the model being asked cannot see it
// anyway -- and the thinking dropped, which is the same size problem with a
// worse payoff.
func TestARecordedExchangeReadsBackAsText(t *testing.T) {
	img := map[string]any{"type": "image_url",
		"image_url": map[string]any{"url": "data:image/jpeg;base64,QUJD"}}
	msgs := []map[string]any{
		msg("system", "Keep <b>every</b> save & every death."),
		msg("user", []any{txtPart("frame 0012"), img}),
	}
	page := chatHTML("suggest", "a-model", true, msgs,
		"<think>maybe the chest?</think>{\"segments\":[]}", 3*time.Second, nil, false)

	got := chatText(page)
	for _, want := range []string{
		"suggest",                               // which step asked
		"Keep <b>every</b> save & every death.", // unescaped, as it was sent
		"frame 0012",
		"[image]",           // named
		"{\"segments\":[]}", // the answer
		"system", "user", "assistant",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page read back without %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"data:image/", "maybe the chest?", "<pre>", "&amp;", "&lt;"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the page read back still holding %q:\n%s", unwanted, got)
		}
	}
	// the live page -- request written, reply still arriving -- is the one an
	// impatient question is asked about, so it has to read back too
	pending := chatText(chatHTML("suggest", "a-model", true, msgs, "", 0, nil, true))
	if !strings.Contains(pending, "frame 0012") {
		t.Errorf("a page whose reply is still arriving read back as:\n%s", pending)
	}
}

// A session's exchanges are megabytes and a request is not, so the newest fit
// and the rest are counted out loud. Order is the other half: a model reads
// forward, so what comes last in the question is what happened last.
func TestTheNewestExchangesGoBackFirstAndSayWhatDidNot(t *testing.T) {
	a := &App{outDir: t.TempDir()}
	if err := os.MkdirAll(a.llmDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// named out of order on purpose: what orders them is when they were
	// written, not what they are called
	for i, name := range []string{"1231-235959-01-describe.html", "0102-000001-02-cut.html",
		"0102-000002-03-audit.html"} {
		p := filepath.Join(a.llmDir(), name)
		body := chatHTML(name, "m", false, []map[string]any{msg("user", name+" body")}, "ok", 0, nil, false)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}

	all := a.recentExchanges(1 << 20)
	first, last := strings.Index(all, "1231-235959"), strings.Index(all, "0102-000002")
	if first < 0 || last < 0 {
		t.Fatalf("not every exchange came back:\n%s", all)
	}
	if first > last {
		t.Error("the exchanges came back newest first -- the last thing in the question " +
			"should be the last thing that happened")
	}
	if strings.Contains(all, "left out for length") {
		t.Error("everything fitted, but the brief says otherwise")
	}

	// and with room for one, it is the newest one
	one := a.recentExchanges(len(chatText(chatHTML("0102-000002-03-audit.html", "m", false,
		[]map[string]any{msg("user", "0102-000002-03-audit.html body")}, "ok", 0, nil, false))) + 60)
	if !strings.Contains(one, "0102-000002") {
		t.Errorf("with room for one, the newest was not the one:\n%s", one)
	}
	if strings.Contains(one, "1231-235959") {
		t.Errorf("a budget of one exchange fitted two:\n%s", one)
	}
	if !strings.Contains(one, "left out for length") {
		t.Errorf("two exchanges were dropped without saying so:\n%s", one)
	}

	// a session that has not run anything says so rather than sending nothing:
	// "no evidence" and "evidence of nothing" are different answers
	if got := (&App{}).recentExchanges(1 << 20); !strings.Contains(got, "no exchanges recorded") {
		t.Errorf("a session with no output folder offered %q", got)
	}
}

// The question is the whole session: what the user said, what this machine
// sends, the notes it sends with everything, the log and the exchanges. Any one
// of those missing turns the answer into a guess that reads exactly like an
// answer.
func TestTheQuestionCarriesTheWholeSession(t *testing.T) {
	ownConfig(t)
	a := &App{outDir: t.TempDir()}
	a.setSessionCtx("Beans was told not to open the chest.")
	if err := os.MkdirAll(a.llmDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.llmDir(), "0102-000001-02-cut.html"),
		chatHTML("cut", "m", false, []map[string]any{msg("user", "the timeline went out")},
			"kept 12 s", 0, nil, false), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs := a.improveBrief("it cut away the best save")
	if len(msgs) != 2 {
		t.Fatalf("the question is %d messages, want a system prompt and the question", len(msgs))
	}
	// the system message is the shared context and then Improve's own wording,
	// which is a const and not a bench row: the bench is the steps of an edit,
	// and this is the tool asking about itself
	sys, _ := msgs[0]["content"].(string)
	if !strings.HasPrefix(sys, strings.TrimSpace(sysSystem)) || !strings.HasSuffix(sys, improveSystem) {
		t.Errorf("Improve is not sent the system context and then its own prompt: %q", sys)
	}
	if promptDefFor("improve").def != "" {
		t.Error(`the "improve" prompt is on the bench again -- the bench is the prompts ` +
			"the run sends, and an answer that rewrites them is not one of them")
	}

	body, _ := msgs[1]["content"].(string)
	for _, want := range []string{
		"it cut away the best save",             // the complaint
		"Beans was told not to open the chest.", // the notes every other call got
		"the timeline went out", "kept 12 s",    // the exchange, both halves
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the question does not carry %q", want)
		}
	}
	for _, d := range promptDefs {
		if !strings.Contains(body, "=== prompt "+d.key) {
			t.Errorf("the question does not carry the %q prompt, so a fix to it "+
				"would be written blind", d.key)
		}
	}
}

// A log longer than the budget is cut at the end that matters -- what just
// happened -- and says how much is missing. An elided middle that does not
// admit it is one reads as a complete record with holes in it, which is how a
// model ends up explaining an absence.
func TestTheLogIsCutAtTheOldEndAndSaysSo(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(">>> line about something that happened\n")
	}
	full := b.String() + ">>> the last thing that happened\n"

	if got := tailOf(full, len(full)+1); got != full {
		t.Error("a log that fits was trimmed anyway")
	}
	cut := tailOf(full, 400)
	if !strings.Contains(cut, ">>> the last thing that happened") {
		t.Error("the tail was cut off the wrong end")
	}
	if !strings.Contains(cut, "not shown") {
		t.Error("the log was cut without saying so")
	}
	lines := strings.Split(cut, "\n")
	if len(lines) < 2 || lines[1] != ">>> line about something that happened" {
		t.Errorf("the cut landed mid-line: the first line kept is %q", lines[min(1, len(lines)-1)])
	}
	if len(cut) > 400+80 {
		t.Errorf("the tail is %d bytes for a budget of 400", len(cut))
	}
}

// The thinking is not the answer. It is longer than the answer, it is the model
// arguing with itself, and the whole of it is in the recorded page for anyone
// who wants it.
func TestTheBoxShowsTheAnswerAndNotTheThinking(t *testing.T) {
	if got := answerOf("<think>hmm, or maybe not</think>WHY: it read the notes."); got != "WHY: it read the notes." {
		t.Errorf("the box would show %q", got)
	}
	if got := answerOf("WHY: it read the notes."); got != "WHY: it read the notes." {
		t.Errorf("a reply with no thinking came out as %q", got)
	}
}

// The wiring. The button is on the bar that is on every page, because the
// complaint is never about the page you are standing on -- a cut is judged
// while watching the produced file -- and the ask is refused while a run is
// going, because it would go out over that run's context and die with ⏹.
func TestImproveIsOnTheGlobalBarAndNotDuringARun(t *testing.T) {
	m := readSrc(t, "main.go")
	i, j := strings.Index(m, `improve := gtk.NewButtonWithLabel("Improve")`), strings.Index(m, "ctlRow.Append(improve)")
	if i < 0 || j < 0 {
		t.Fatal("the Improve button is no longer built into the bottom bar")
	}
	if !strings.Contains(m, "improve.ConnectClicked(a.improveClicked)") {
		t.Error("the Improve button does not open the box")
	}
	body := funcBody(t, "improve.go", `func \(a \*App\) improveClicked\(`)
	if !strings.Contains(body, "if a.running {") || !strings.Contains(body, "return") {
		t.Error("Improve can be asked mid-run, and the call would die with the run it asks about")
	}
	// and the call is not a step: it must not take the run controls over
	for _, no := range []string{"a.running = true", "a.runCtx", "a.qJob("} {
		if strings.Contains(body, no) {
			t.Errorf("improveClicked touches %s -- asking a question is not a run", no)
		}
	}
}

// The answer is edits, not advice: which prompt, the sentence to find, what to
// put there. What is parsed is what a card can act on, so an edit naming a
// prompt this build does not have is dropped rather than shown under a button
// that could only fail.
func TestTheAnswerComesBackAsEditsToApply(t *testing.T) {
	reply := "<think>hmm</think>\n```json\n" + `{"why":"the audit dropped it",
	  "changes":[
	    {"prompt":"cut","find":"Never cut into a sentence","replace":"Never cut mid-word","why":"too strict"},
	    {"prompt":"nosuchjob","find":"whatever","replace":"x","why":"invented"},
	    {"prompt":"audit","find":"","replace":"y","why":"nothing to find"}]}` + "\n```"
	why, fixes := improveParse(reply)
	if why != "the audit dropped it" {
		t.Errorf("the explanation came out as %q", why)
	}
	if len(fixes) != 1 {
		t.Fatalf("%d edits offered, want the one that can be made: %+v", len(fixes), fixes)
	}
	if fixes[0].Key != "cut" || fixes[0].Find != "Never cut into a sentence" ||
		fixes[0].Replace != "Never cut mid-word" || fixes[0].Why != "too strict" {
		t.Errorf("the edit came back as %+v", fixes[0])
	}

	// a model that answers in prose is not an error: the explanation is the
	// useful half and it is still readable, there is just nothing to press
	why, fixes = improveParse("WHY: it read the notes and kept the wrong minute.")
	if why != "WHY: it read the notes and kept the wrong minute." || fixes != nil {
		t.Errorf("a prose answer came back as %q / %+v", why, fixes)
	}
}

// Apply is the only thing that writes, and it writes what typing in the box
// writes: the picked wording, on this machine, marked as the user's own. A
// sentence quoted wrong changes nothing -- there is no fuzzy match, because a
// near-miss rewrite is this tool editing a wording nobody approved.
func TestAnAppliedEditRewritesTheStoredWordingAndOnlyThen(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.setPrompt("cut", "Keep the best bits.\nNever cut into a sentence.\nEnd on the payoff.")

	if a.applyFix(promptFix{Key: "cut", Find: "Never cut mid-word", Replace: "x"}) {
		t.Error("an edit whose sentence is not in the prompt was applied anyway")
	}
	if !strings.Contains(a.prompt("cut"), "Never cut into a sentence.") {
		t.Error("a failed edit changed the prompt")
	}

	if !a.applyFix(promptFix{Key: "cut", Find: "Never cut into a sentence.",
		Replace: "Never cut mid-word."}) {
		t.Fatal("an exact sentence was not applied")
	}
	got := a.prompt("cut")
	if !strings.Contains(got, "Never cut mid-word.") || strings.Contains(got, "into a sentence") {
		t.Errorf("the prompt now reads:\n%s", got)
	}
	if !strings.Contains(got, "Keep the best bits.") || !strings.Contains(got, "End on the payoff.") {
		t.Errorf("the rest of the prompt did not survive the edit:\n%s", got)
	}
	// stored, not just in hand: it is the same write the box makes, so the
	// bench shows it and the ✎ says the wording is this machine's
	if !a.promptOwned("cut") {
		t.Error("an applied edit is not marked as this machine's own wording")
	}
	a.flushPrompts()
	b := &App{}
	b.loadGlobalPrompts()
	if !strings.Contains(b.prompt("cut"), "Never cut mid-word.") {
		t.Error("the applied edit did not reach the store, so the next run sends the old wording")
	}

	// an empty replacement is how a sentence is taken out
	if !a.applyFix(promptFix{Key: "cut", Find: "\nEnd on the payoff."}) {
		t.Fatal("a deletion was refused")
	}
	if strings.Contains(a.prompt("cut"), "End on the payoff") {
		t.Error("an empty replacement did not remove the sentence")
	}
}

// Nothing is applied by arriving. Every edit is a card with its own Apply, and
// the only call that writes is behind that button -- an answer that rewrote the
// prompts as it came back would be the tool editing the one thing it was told
// to obey.
func TestEveryOfferedEditIsDecidedByHand(t *testing.T) {
	clicked := funcBody(t, "improve.go", `func \(a \*App\) improveClicked\(`)
	if strings.Contains(clicked, "a.applyFix(") {
		t.Error("the answer applies itself somewhere in improveClicked")
	}
	if !strings.Contains(clicked, "a.fixCard(f, cards)") {
		t.Error("the offered edits are no longer shown as cards")
	}
	card := funcBody(t, "improve.go", `func \(a \*App\) fixCard\(`)
	for _, want := range []string{
		`gtk.NewButtonWithLabel("Apply")`,
		`gtk.NewButtonWithLabel("Dismiss")`,
		"apply.ConnectClicked(",
		"a.applyFix(f)",
		"list.Remove(frame)",
		"apply.SetSensitive(false)", // a sentence that is not there cannot be pressed
	} {
		if !strings.Contains(card, want) {
			t.Errorf("a card no longer has %q", want)
		}
	}
}
