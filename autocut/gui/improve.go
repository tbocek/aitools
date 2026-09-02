package main

// The Improve button on the bottom bar: say what you did not like, and the
// model reads back over its own session and answers with why it did that and
// which sentence of which prompt would have stopped it.
//
// It exists because the prompts are the only real control surface in this tool
// and they are edited blind. A cut that keeps the wrong sixty seconds is not a
// bug you can step through -- somewhere in a system prompt is a sentence the
// model read differently than it was meant, and the evidence for which sentence
// is in llm/, in ten thousand lines of recorded exchange nobody is going to
// read. So the complaint, the prompts as they stand, the run log and the
// exchanges all go back to the model at once, and the answer is a quote and a
// replacement sentence rather than advice.
//
// The answer comes back as edits, one at a time: which prompt, the sentence as
// it stands, the sentence as it should read, and why. Each is a card with Apply
// and Dismiss, and nothing changes until Apply is pressed. A prompt is the
// user's wording -- it is why the store is per machine (promptstore.go) -- so
// the model proposes and the user decides; what the button removes is the copy
// out of a text box and back into another one, which is where a quoted sentence
// went wrong by a character and silently did nothing.
//
// The edit is literal: the quoted sentence is searched for in the prompt as it
// stands and replaced once. A sentence that is not there cannot be applied and
// the card says so rather than guessing at what was meant -- a fuzzy match on a
// prompt would be this tool rewriting a wording nobody approved.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// How much of the session goes in the question. The exchanges are the bulk and
// the budget is theirs; the log is small and the tail of it is the part that
// still matters. Generous rather than tight -- the whole point is that the
// model gets to see what happened, and one long request beats an answer that
// had to guess.
const (
	improveExchangeMax = 120_000
	improveLogMax      = 20_000
)

// improveSystem is not on the bench (see promptDefs). Improve is the tool asking
// about itself, not a step of the edit, and there is nothing in here a project
// would want worded differently: what the user brings to it is the complaint
// they type, and the half of this that used to be worth editing -- how a stamp
// reads, what a machine-read answer looks like -- is the system context every
// job gets now (syscontext.go), which is where a local model that keeps
// answering in markdown gets talked out of it.
//
// It asks for the edits as data because they are acted on: a card per change
// with an Apply under it, and find copied character for character so the
// replacement can be made without anyone retyping it.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const improveSystem = `You are reviewing a session of an automated video editor and answering the person who ran it. You are given: what they are unhappy about, the prompts this machine currently sends, the run log, and the recorded exchanges with the model -- what was sent and what came back.

Each prompt is shown under a heading of the form "=== prompt <key> (wording: <name>) ===". That key is how you name a prompt below.

The ABOUT THIS SESSION block was sent with every call you are being shown, so here it is evidence rather than an instruction: if what went wrong is that the notes said one thing and a prompt said another, that is the answer.

Return strict JSON, nothing else:
{"why":"<what happened>","changes":[{"prompt":"<key>","find":"<the sentence exactly as it stands>","replace":"<what it should say instead>","why":"<what this changes>"}]}

- why is two or three sentences on what actually happened, pointing at the exchange that decided it and quoting the line from the reply that shows it. If the record does not show it, say so instead of guessing. Never invent a log line or a reply.
- Each change is the smallest edit to one prompt that would have avoided it.
- find is copied character for character out of the prompt as it is shown to you: one sentence or one bullet, not a paragraph and not your paraphrase. It is searched for literally, so a find that is not in the prompt is an edit that cannot be made.
- replace is that sentence rewritten in full, in the voice of the prompt around it. An empty replace deletes the sentence.
- At most three changes, best first, and one good change beats three hedged ones. Two changes to the same sentence is one change.
- If the cause is not in the prompts -- a setting, a missing source, something the tool itself did -- return an empty changes list and say so in why. An edit invented for an innocent prompt is worse than no edit.`

// improveBrief is the question: the complaint first, because it is what is
// being asked, then the material, largest last. Built on the GUI thread -- the
// prompts and the log live there -- and handed to the goroutine whole.
func (a *App) improveBrief(complaint string) []map[string]any {
	var b strings.Builder
	// the session notes open the request, as they open every other request the
	// tool makes -- and here they are also evidence: half of "why did it keep
	// that" is what the editor told every call about what mattered
	b.WriteString(a.ctxBlock())
	fmt.Fprintf(&b, "What I am unhappy about:\n\n%s\n", complaint)

	b.WriteString("\n\nThe prompts this machine sends, one per job:\n")
	for _, d := range promptDefs {
		head := d.key
		if !d.solo { // a prompt with one wording has no name to give
			head += fmt.Sprintf(" (wording: %s)", a.promptPickName(d.key))
		}
		fmt.Fprintf(&b, "\n=== prompt %s ===\n%s\n", head, a.prompt(d.key))
	}

	b.WriteString("\n\nThe run log, most recent last:\n\n")
	b.WriteString(tailOf(a.logText(), improveLogMax))

	b.WriteString("\n\nThe recorded exchanges, oldest first:\n\n")
	b.WriteString(a.recentExchanges(improveExchangeMax))

	return []map[string]any{msg("system", a.sysWrap(improveSystem)), msg("user", b.String())}
}

// logText is the run log as it stands. Empty without a window, which is every
// test and every headless run: the log is a widget, and there is no second copy.
func (a *App) logText() string {
	if a.log == nil {
		return ""
	}
	return viewText(a.log)
}

// tailOf keeps the last max bytes of s, cut at a line so the first line in is a
// whole one, and says how much was left off -- an elided middle that does not
// say it was elided reads as a complete record that is missing things.
func tailOf(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return fmt.Sprintf("(the first %d bytes of the log are not shown)\n", len(s)-len(cut)) + cut
}

// improveClicked opens the box. One window, not a page: it is asked from
// wherever you noticed the problem, and the page you noticed it on is what you
// want to keep looking at while you type.
func (a *App) improveClicked() {
	if a.running {
		// the call would go out over the run's own context and die with it,
		// and the log it is meant to read is still being written
		a.setStatus("a run is under way — ask again when it has finished")
		return
	}
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetTitle("Improve")
	win.SetDefaultSize(680, 520)

	q := gtk.NewLabel("What went wrong?")
	q.SetXAlign(0)
	q.SetWrap(true)
	q.AddCSSClass("heading")
	d := gtk.NewLabel("Say it the way you would say it to a person — \"it cut away the " +
		"best save\", \"the narration talks over the punchline\". The model reads this " +
		"session's log and everything it was sent, answers why it did that, and names " +
		"the sentence in the prompt that would change it.")
	d.SetXAlign(0)
	d.SetWrap(true)
	d.AddCSSClass("dim-label")

	says := gtk.NewTextView()
	says.SetWrapMode(gtk.WrapWordChar)
	says.SetLeftMargin(6)
	says.SetRightMargin(6)
	says.SetTopMargin(6)
	saysScroll := gtk.NewScrolledWindow()
	saysScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	saysScroll.SetChild(says)
	saysScroll.SetSizeRequest(-1, 90)
	saysFrame := gtk.NewFrame("")
	saysFrame.SetChild(saysScroll)

	// the answer, under the question rather than in a second window: reading it
	// against what was typed is most of what makes it useful. Why it happened
	// on top as text, and under it one card per edit it offers
	why := gtk.NewLabel("")
	why.SetXAlign(0)
	why.SetWrap(true)
	why.SetSelectable(true)
	cards := gtk.NewBox(gtk.OrientationVertical, 8)
	ansBox := gtk.NewBox(gtk.OrientationVertical, 10)
	ansBox.SetMarginTop(8)
	ansBox.SetMarginBottom(8)
	ansBox.SetMarginStart(8)
	ansBox.SetMarginEnd(8)
	ansBox.Append(why)
	ansBox.Append(cards)
	ansScroll := gtk.NewScrolledWindow()
	ansScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	ansScroll.SetChild(ansBox)
	ansScroll.SetVExpand(true)
	ansFrame := gtk.NewFrame("")
	ansFrame.SetChild(ansScroll)
	ansFrame.SetVisible(false) // nothing to frame until there is an answer

	note := gtk.NewLabel("")
	note.SetXAlign(0)
	note.SetWrap(true)
	note.AddCSSClass("dim-label")
	note.SetHExpand(true)

	ask := gtk.NewButtonWithLabel("Ask")
	ask.AddCSSClass("suggested-action")
	closeB := gtk.NewButtonWithLabel("Close")
	closeB.ConnectClicked(func() { win.Close() })

	ask.ConnectClicked(func() {
		text := strings.TrimSpace(viewText(says))
		if text == "" {
			return // nothing to answer, and an empty complaint answered anyway
		} //        would be the model guessing what annoyed you
		ask.SetSensitive(false)
		note.SetText("reading the log and the recorded exchanges…")
		brief := a.improveBrief(text)
		sent, _ := chatSent(brief)
		note.SetText("asking the model — this takes about as long as a suggest does…")
		a.logf(">>> improve: %s of prompts, log and exchanges went back to the model", sizeOf(sent))
		go func() {
			// no runCtx: this is not a step, it must not be stopped by ⏹, and
			// it must not be what ⏹ is for
			reply, err := a.llmChatRetry("improve", brief, true)
			glib.IdleAdd(func() {
				ask.SetSensitive(true)
				if err != nil {
					note.SetText("the model could not be reached: " + err.Error())
					return
				}
				text, fixes := improveParse(reply)
				why.SetText(text)
				// asking twice replaces the first answer: two sets of cards
				// against the same prompts would be two edits to one sentence
				for c := cards.FirstChild(); c != nil; c = cards.FirstChild() {
					cards.Remove(c)
				}
				for _, f := range fixes {
					cards.Append(a.fixCard(f, cards))
				}
				note.SetText("")
				if len(fixes) == 0 {
					note.SetText("no prompt change offered — the answer is below")
				}
				ansFrame.SetVisible(true)
			})
		}()
	})

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetMarginTop(4)
	btns.Append(note)
	btns.Append(ask)
	btns.Append(closeB)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(q)
	box.Append(d)
	box.Append(saysFrame)
	box.Append(btns)
	box.Append(ansFrame)
	win.SetChild(box)
	says.GrabFocus()
	win.SetVisible(true)
}

// answerOf drops the thinking, which the box must not show: it is longer than
// the answer and it is the model arguing with itself. The whole of it is in the
// recorded page, like every other call's.
func answerOf(reply string) string {
	if i := strings.LastIndex(reply, "</think>"); i >= 0 {
		return reply[i+len("</think>"):]
	}
	return reply
}

// promptFix is one suggested edit, as the model returns it: which prompt, the
// sentence to find, what to put there instead, and what it changes. Nothing is
// done with one until an Apply is pressed.
type promptFix struct {
	Key     string `json:"prompt"`
	Find    string `json:"find"`
	Replace string `json:"replace"`
	Why     string `json:"why"`
}

// improveParse reads the answer: the explanation, and the edits that can be
// acted on. A reply that is not JSON at all is not an error worth a dialog --
// the explanation is the useful half and it is still readable -- so it comes
// back whole as the why, with no cards under it. Same for an edit naming a
// prompt this build does not have: it is dropped rather than shown with a
// button that could only fail.
func improveParse(reply string) (string, []promptFix) {
	text := strings.TrimSpace(answerOf(reply))
	clean := text
	if i := strings.Index(clean, "{"); i >= 0 {
		clean = clean[i:]
	}
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var out struct {
		Why     string      `json:"why"`
		Changes []promptFix `json:"changes"`
	}
	if json.Unmarshal([]byte(clean), &out) != nil {
		return text, nil
	}
	var fixes []promptFix
	for _, f := range out.Changes {
		f.Key = strings.TrimSpace(f.Key)
		if f.Find == "" || promptDefFor(f.Key).def == "" {
			continue
		}
		fixes = append(fixes, f)
	}
	return strings.TrimSpace(out.Why), fixes
}

// applyFix makes the edit, once, in the wording the project has picked -- the
// same write typing in the box makes (setPrompt), so it is stored, it is marked
// as this machine's own, and Reset still puts the built-in back.
//
// It replaces the FIRST occurrence and only if the sentence is there verbatim.
// A sentence quoted with a word wrong is an edit nobody can check, and the card
// says as much instead: this returns false and changes nothing.
func (a *App) applyFix(f promptFix) bool {
	cur := a.prompt(f.Key)
	if f.Find == "" || !strings.Contains(cur, f.Find) {
		return false
	}
	a.setPrompt(f.Key, strings.Replace(cur, f.Find, f.Replace, 1))
	// the bench may be showing this very prompt: the box is the editor, so it
	// has to say what the store now says (as a project load does, project.go)
	if tv := a.promptViews[f.Key]; tv != nil {
		a.promptQuiet = true
		tv.Buffer().SetText(a.prompt(f.Key))
		a.promptQuiet = false
	}
	a.markPromptRow(f.Key)
	a.syncPromptMarks() // the row wears a ✎ from here on
	a.logf(">>> improve: applied an edit to the %s prompt", f.Key)
	return true
}

// promptMenuName is what the bench calls the prompt, so a card names the row
// the user would go and look at rather than the key the model was given.
func promptMenuName(key string) string {
	for _, r := range prepRows() {
		if r.key == key {
			return r.menu
		}
	}
	return key
}

// fixCard is one offered edit and the two buttons that decide it: the sentence
// as the prompt has it, the sentence as the model would have it, why, and Apply
// / Dismiss. Nothing is written until Apply, and a card whose sentence is not
// in the prompt says so with its Apply dead rather than offering a button that
// could only fail.
func (a *App) fixCard(f promptFix, list *gtk.Box) gtk.Widgetter {
	head := gtk.NewLabel(promptMenuName(f.Key) + " prompt")
	head.SetXAlign(0)
	head.AddCSSClass("heading")

	// diff-style, because that is what it is: the line as it stands, then the
	// line as it would read
	from := gtk.NewLabel("− " + f.Find)
	repl := strings.TrimSpace(f.Replace)
	if repl == "" {
		repl = "(the sentence is taken out)"
	} else {
		repl = "+ " + f.Replace
	}
	to := gtk.NewLabel(repl)
	for _, l := range []*gtk.Label{from, to} {
		l.SetXAlign(0)
		l.SetWrap(true)
		l.SetSelectable(true)
	}
	from.AddCSSClass("dim-label")

	because := gtk.NewLabel(f.Why)
	because.SetXAlign(0)
	because.SetWrap(true)
	because.AddCSSClass("dim-label")

	state := gtk.NewLabel("")
	state.SetXAlign(0)
	state.SetWrap(true)
	state.SetHExpand(true)
	state.AddCSSClass("dim-label")

	apply := gtk.NewButtonWithLabel("Apply")
	apply.AddCSSClass("suggested-action")
	dismiss := gtk.NewButtonWithLabel("Dismiss")
	dismiss.AddCSSClass("flat")

	frame := gtk.NewFrame("")
	if !strings.Contains(a.prompt(f.Key), f.Find) {
		apply.SetSensitive(false)
		state.SetText("this sentence is not in the prompt as it stands — nothing to replace")
	}
	apply.ConnectClicked(func() {
		if !a.applyFix(f) { // the wording changed under it since the answer came back
			apply.SetSensitive(false)
			state.SetText("this sentence is not in the prompt as it stands — nothing to replace")
			return
		}
		apply.SetVisible(false)
		dismiss.SetLabel("Close")
		state.SetText("applied — it is on Prepare, under " + promptMenuName(f.Key))
	})
	dismiss.ConnectClicked(func() { list.Remove(frame) })

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.Append(state)
	btns.Append(apply)
	btns.Append(dismiss)

	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.SetMarginTop(10)
	box.SetMarginBottom(10)
	box.SetMarginStart(10)
	box.SetMarginEnd(10)
	box.Append(head)
	box.Append(from)
	box.Append(to)
	box.Append(because)
	box.Append(btns)
	frame.SetChild(box)
	return frame
}
