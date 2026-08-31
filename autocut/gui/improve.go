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
// The reply is not applied. A prompt is the user's wording -- it is why the
// store is per machine (promptstore.go) -- and a button that rewrote it from a
// model's suggestion would be the tool editing the one thing it was told to
// obey. The answer is text to read, disagree with, and paste in if it is right.

import (
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

// improveSystem is registered like every other prompt (promptDefs) and read
// through a.prompt("improve"), so this is the built-in and not necessarily what
// gets sent. It is the odd one in that registry -- the others are steps of an
// edit -- but an Improve that keeps answering beside the point is exactly a
// prompt that wants a sentence added, and making the one prompt about improving
// prompts the unreachable one would be a joke at the user's expense.
const improveSystem = `You are reviewing a session of an automated video editor and answering
the person who ran it. You are given: what they are unhappy about, the system
prompts this machine currently sends, the run log, and the recorded exchanges
with the model -- what was sent and what came back.

The request may open with a block headed ABOUT THIS SESSION: the editor's own
notes on what this recording is and what matters in it. The same block was sent
with every call you are being shown, so it is evidence, not a complaint -- if
what went wrong is that the notes said one thing and a prompt said another, say
so.

Answer in exactly two parts, plain text, no markdown:

WHY: what actually happened, in two or three sentences, pointing at the
exchange that decided it and quoting the line from the reply that shows it. If
the record does not show it, say that instead of guessing. Never invent a log
line or a reply.

FIX: the smallest change to one prompt that would have avoided it. Name the
prompt, quote the sentence you would change, and write the replacement sentence
out in full so it can be pasted in. If the cause is not in the prompts -- a
setting, a missing source, something the tool itself did -- say so plainly and
say what to change instead. Do not repeat a whole prompt back.`

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
		fmt.Fprintf(&b, "\n=== prompt %s (wording: %s) ===\n%s\n",
			d.key, a.promptPickName(d.key), a.prompt(d.key))
	}

	b.WriteString("\n\nThe run log, most recent last:\n\n")
	b.WriteString(tailOf(a.logText(), improveLogMax))

	b.WriteString("\n\nThe recorded exchanges, oldest first:\n\n")
	b.WriteString(a.recentExchanges(improveExchangeMax))

	return []map[string]any{msg("system", a.prompt("improve")), msg("user", b.String())}
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
	// against what was typed is most of what makes it useful
	answer := gtk.NewTextView()
	answer.SetEditable(false)
	answer.SetWrapMode(gtk.WrapWordChar)
	answer.SetLeftMargin(6)
	answer.SetRightMargin(6)
	answer.SetTopMargin(6)
	ansScroll := gtk.NewScrolledWindow()
	ansScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	ansScroll.SetChild(answer)
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
				note.SetText("")
				ansFrame.SetVisible(true)
				setViewText(answer, strings.TrimSpace(answerOf(reply)))
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
