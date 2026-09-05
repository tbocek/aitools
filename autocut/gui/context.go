package main

// The user context: what the person editing knows and the material does not say.
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
// reading a heading followed by nothing will happily invent what belongs under
// it.
//
// It is headed USER CONTEXT because that is what it is: what the person
// running this tool typed into the box. It used to be headed "ABOUT THIS
// SESSION", which named a thing that does not exist -- the session is the
// footage, and the footage says nothing here. Everything downstream reads the
// heading, so a prompt that talks about it uses the same words the block
// wears.
//
// It goes in the USER message, ahead of the material, never in the system
// prompt. Two reasons: the prompt boxes stay what the user wrote rather than
// what the tool assembled, and a step's rules stay separable from this
// session's facts when one of them turns out to be wrong.
func (a *App) ctxBlock() string { return a.ctxBlockFor("cut") }

// ctxBlockFor is the block as one job carries it. The speech rule under it is
// about what to DO with spoken lines -- keep them, caption them, cut on them
// -- and only the jobs that decide that get it: the cut and the narration. The frame describer, the transcript fixer and the upload text
// are told the context and nothing about a decision they never make.
func (a *App) ctxBlockFor(key string) string {
	s := a.sessionCtx()
	if s == "" {
		return ""
	}
	b := "USER CONTEXT -- written by the person who made this recording and " +
		"is editing it. It outranks anything you infer from the material, and it " +
		"outranks the rules of the job you were given wherever the two disagree; " +
		"only the mechanics of the answer -- its shape, its clock, what may be " +
		"invented -- are not its to change:\n" + s + "\n\n"
	switch key {
	case "cut", "narrate":
		b += ctxSpeech + "\n\n"
	}
	return b
}

// ctxSpeech rides under the user context, and only under it.
//
// It is how that context bears on the spoken lines, and it used to be a paragraph
// in the system prompt -- sent to every job of every session, including the
// ones whose notes box is empty, where it described a block that was not
// there. Here it is sent exactly when there is something for it to be about,
// and it sits beside that something instead of six thousand characters
// earlier.
//
// The rule itself is the same rule, and it is worth its words: read the wrong
// way it is the worst answer this app can give. An aside to the editor
// captioned into the video, or the video thrown away as asides.
const ctxSpeech = `The speech is content unless the user context above says otherwise: the speakers are in the video, and what they say is why a moment is worth keeping. Where the user context calls it directions ("this part is boring", "speed this up"), do what a direction asks at the second it asks and keep its words out of the video -- never caption them, and never keep a stretch just because it was spoken over. An instruction about a kind of stretch -- speed the dull parts up and show them instead of cutting them, caption each thing as it is named -- holds wherever such a stretch occurs. It decides segments too: a stretch to be shown fast has to be in the cut, with a speed effect over it, or there is nothing left to speed up.`

// logCtx says, in the log, that this step's requests carried the context. A
// second input that changes the result and appears nowhere in the run is how
// an editor ends up baffled by their own note from three sessions ago; naming
// the step matters, because the log is read long after the run.
func (a *App) logCtx(step string) {
	if c := a.sessionCtx(); c != "" {
		a.logfIdle(">>> %s: sending the session context from Prepare (%d characters)", step, len(c))
	}
}
