package main

// The system context: what every job is told about the material and about the
// answer, said once.
//
// Four cut wordings, an audit, a narration and an upload text -- and each of
// them opened by explaining the same three things: that the lines are stamped
// [mm:ss] and the minutes keep counting past 59, what an EVENT line is against
// a SPEAKER line, and that the reply is machine-read so nothing may sit around
// it. Written out seven times, that is seven places to fix when the timeline
// gains a lane, and seven wordings that have already drifted -- the Shorts
// wording explained NARRATOR lines differently from the other three, for no
// reason anybody chose.
//
// None of it is taste. Which moments are worth keeping is what a style is FOR
// and is why there are several; how a second is spelled is a fact about this
// tool, true for every style and every job it will ever have. So the facts are
// one prompt, in front of all of them, and a style is only the part that could
// reasonably differ.
//
// It is the second row of the bench on Prepare, under the session context and
// over the run: what this machine sends, above what this session was. Editable
// like the rest, because a local model that keeps misreading the stamps is
// exactly a sentence that wants rewording, and this is where that sentence now
// lives.
//
// It is the one prompt with no wordings (promptDef.solo). Every other prompt
// has several because a style has an opinion about it -- Highlights and
// Showcase want different cuts, and say so in the same box. None of them has an
// opinion about how a stamp reads: there is one answer to that and this is it,
// so the row has no name in brackets, no ＋ to save a second one under, and no
// style pick to lose an edit to.
//
// The session context goes in the USER message and this goes in the SYSTEM
// message, which is the same line drawn twice: facts about the session travel
// with the material, rules about the job travel with the job (see context.go).
//
// One paragraph or bullet per line, unwrapped: see describeSystem.

import "strings"

const sysSystem = `You are called by an automated video editor, one job per call. What you answer is read by a machine, not by a person: return exactly what the job asks for and nothing around it -- no markdown, no code fence, no preamble, no report of what you did. Where a job asks for JSON, strict JSON is the whole of the answer; where it asks for lines or columns, their number and their order are part of the answer.

The material is one session: one or more recordings of the picture, one or more microphones, all of it on one clock. Whatever the job, the lines describing it are the same three:

  EVENT: what the picture showed in those seconds, and whether it was hectic or calm
  SPEAKER_01: something said out loud, which the video plays
  NARRATOR: something said on a microphone the video does not play. Only the voice-over carries it, so a line here is heard by nobody unless a job uses it.

Every line is stamped, and the request says which clock the stamp is on. Answer on the same one.

  [12:04] is the whole session, minutes and seconds from its start, and the minutes keep counting past 59 -- so [72:30] is 4350 seconds. Times you return on this clock are session seconds: mm*60+ss.
  [+2.0s] is an offset from the start of whatever the request is about -- these frames, this clip. Negative is before it.
  A bare number in a column is seconds on the timeline of the one recording that column belongs to, and is copied, never recomputed.

A request may open with a block headed ABOUT THIS SESSION: notes from someone who was there, about what this recording is and what matters in it. They are not a question to answer -- they are what to work from, and they outrank anything you would otherwise infer. Names are spelled the way that block spells them.

Only what the material shows. Never invent a time, a name, a score or a moment: a stretch the lines do not cover did not happen, and only stretches with EVENT lines have footage behind them.`

// sysPrompt is the system message a job goes out with: the shared context, then
// that job's own prompt. Every call that sends a system prompt is built through
// here -- there is no second way to assemble one, which is what stops a job
// added later from quietly being the one that is never told how a stamp reads.
//
// An emptied box takes the block away rather than sending a heading with
// nothing under it, the way ctxBlock does: a model reading rules that are not
// there will happily supply its own.
func (a *App) sysPrompt(key string) string { return a.sysWrap(a.prompt(key)) }

// sysWrap is the same, for the one job whose prompt is not in the registry:
// Improve is not a step of an edit and is not offered on the bench, so it holds
// its wording as a const and comes through here (improve.go). Two entry points
// and one join, rather than a second place that decides what a system message
// looks like.
func (a *App) sysWrap(job string) string {
	sys := strings.TrimSpace(a.prompt("system"))
	if sys == "" {
		return job
	}
	return sys + "\n\n" + job
}
