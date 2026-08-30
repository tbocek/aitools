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
// So it is one box, the first row of the bench on Prepare (prepedit.go), and
// every request carries it: the frame describer and the transcript fixer on
// that page, the cut and its audit, the narration and the upload text. One
// thing to write, before the first run, and the whole pipeline is told.
//
// The prompts say HOW to work; this says WHAT this session was. That is the
// line between the first row of that menu and the rest of it, and it is why the
// context is stored in full while a prompt is stored only when it differs from
// the built-in.

import "strings"

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
//
// No box is the ordinary case rather than the early one: the bench shows one
// row at a time, so a project loaded while a prompt is on screen updates the
// cache and nothing else, and the box fills from the cache when the context is
// switched back to.
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

// logCtx says, in the log, that this step's requests carried the context. A
// second input that changes the result and appears nowhere in the run is how
// an editor ends up baffled by their own note from three sessions ago; naming
// the step matters, because the log is read long after the run.
func (a *App) logCtx(step string) {
	if c := a.sessionCtx(); c != "" {
		a.logfIdle(">>> %s: sending the session context from Prepare (%d characters)", step, len(c))
	}
}
