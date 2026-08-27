package main

// The session context: what the editor knows and the material does not say.
//
// Who is in this session, what they were trying to do, what happened that the
// frames only hint at, what to call the thing everyone keeps mispronouncing --
// none of it is in the footage in a form a model can read, and all of it
// changes what every step produces. It used to be typed into whichever system
// prompt was in reach, which meant it was written per step, went out of sync
// between them, and stopped the project from tracking improved built-in
// wording (see prompts.go: an edited prompt is frozen forever).
//
// So it is one box, on Describe, and every request carries it: the frame
// describer and the transcript fixer on that page, the cut and its audit, and
// the narration. One thing to write, before the first run, and the whole
// pipeline is told.
//
// The prompts say HOW to work; this says WHAT this session was. That is the
// line between the two boxes, and it is why the context is stored in full
// while a prompt is stored only when it differs from the built-in.

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// sessionCtx is the box's text, callable from a runner's goroutine.
func (a *App) sessionCtx() string {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	return strings.TrimSpace(a.ctxTxt)
}

func (a *App) setSessionCtx(s string) {
	a.promptMu.Lock()
	a.ctxTxt = s
	a.promptMu.Unlock()
}

// applySessionCtx loads a project's context into the box as well as the cache.
// Called from the GUI thread only, like applyPromptStyles, and for the same reason:
// it touches a GtkTextBuffer.
func (a *App) applySessionCtx(s string) {
	a.setSessionCtx(s)
	if a.ctxView != nil {
		a.ctxView.Buffer().SetText(s)
	}
}

// ctxBlock is the context as a request carries it, or nothing at all when the
// box is empty -- an empty heading is worse than no heading, since a model
// reading "ABOUT THIS SESSION:" followed by nothing will happily invent what
// belongs under it.
//
// It goes in the USER message, ahead of the material, never in the system
// prompt. Two reasons: the prompt boxes stay what the user wrote rather than
// what the tool assembled, and a step's rules stay separable from this
// session's facts when one of them turns out to be wrong.
func (a *App) ctxBlock() string {
	s := a.sessionCtx()
	if s == "" {
		return ""
	}
	return "ABOUT THIS SESSION, FROM THE EDITOR -- written by someone who was " +
		"there, so it outranks anything you infer from the material:\n" + s + "\n\n"
}

// contextEditor is the right-hand half of the Describe page.
//
// Deliberately not a promptEditor -- there is no built-in text and so nothing
// to reset to, and no "edited" marker to earn, since every session's context is
// edited by definition -- but literally the same box otherwise (editorBody):
// same font, same frame, same floor, same heading height. It holds the same
// kind of thing as the two prompts beside it, and looking different only asks
// the question why.
func (a *App) contextEditor() gtk.Widgetter {
	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetMonospace(true) // the same box as the two prompts beside it, and it holds the same kind of thing
	tv.SetTopMargin(4)
	tv.SetBottomMargin(4)
	tv.SetLeftMargin(6)
	tv.SetRightMargin(6)
	tv.Buffer().ConnectChanged(func() {
		b := tv.Buffer()
		a.setSessionCtx(b.Text(b.StartIter(), b.EndIter(), false))
	})
	a.ctxView = tv

	// A heading and nothing else, like the two prompts. What to write here was
	// three lines of grey prose under it, which is a paragraph of instructions
	// standing between you and the box on every visit after the first -- it is
	// on the heading now, where the rest of this page keeps its explanations.
	lbl := gtk.NewLabel("User Context")
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	lbl.SetEllipsize(pango.EllipsizeEnd) // one line, like every other heading row
	lbl.AddCSSClass("heading")
	lbl.SetTooltipText("Who is in this session, what they were doing, how names are " +
		"spelled, what to make sure ends up in the video.\n\nSent with every request " +
		"this project makes: the frame describer, the transcript fixer, the cut and " +
		"its audit, and the narration. Left empty, nothing is sent.")

	// through editorBody, like every prompt box: same frame, same floor, same
	// heading height. Its row is a label where theirs is a label and a button,
	// and building the two boxes separately is how the one on the right ended up
	// starting a button's height above the one on the left.
	head := gtk.NewBox(gtk.OrientationHorizontal, 8)
	head.Append(lbl)
	return a.editorBody(head, tv)
}

// logCtx says, in the log, that this step's requests carried the context. A
// second input that changes the result and appears nowhere in the run is how
// an editor ends up baffled by their own note from three sessions ago; naming
// the step matters, because the log is read long after the run.
func (a *App) logCtx(step string) {
	if c := a.sessionCtx(); c != "" {
		a.logfIdle(">>> %s: sending the session context from Describe (%d characters)", step, len(c))
	}
}
