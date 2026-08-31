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
	// the system prompt is the registered one, so editing Improve on the bench
	// changes what Improve does -- which is the only reason to expose it
	a.setPrompt("improve", "answer in Swiss German")
	if got := a.improveBrief("x")[0]["content"]; got != "answer in Swiss German" {
		t.Errorf("the edited Improve prompt is not what gets sent: %q", got)
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
