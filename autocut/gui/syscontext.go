package main

// The system context: what every job is told about the tool it is part of, the
// material it works on and the answer it owes, said once.
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
// The user context is not here at all, and that is the point. Everything
// about it -- that it outranks what a job would infer and every rule a
// wording states, and how it changes the reading of the spoken lines --
// travels WITH it, in the request (ctxBlock) and on the wording's tail
// (ctxRule). A session with an empty box then sends none of it, where this
// prompt sent every job a paragraph about a block that was not there; and
// where there is a context, the rule sits beside it rather than six thousand
// characters earlier.
//
// The same was true of three rules that are not formats at all and had drifted
// the same way: never invent, the user context outranks what you would infer,
// and the answer carries nothing around it. Between them they were written into
// every wording in the app -- "never invent a moment", "never invent a part, a
// name or a price", "invent nothing that is not in it" -- which is one rule the
// model meets six times and no rule it meets in the one place it could be
// tightened.
//
// It also says what the four steps ARE. A job used to be told its own step and
// nothing else, which reads fine until you notice what each one is doing: the
// cut is choosing seconds that the narration will later have to talk over, and
// the describing step is writing the only record of the footage that any later
// step will ever see. A model that knows it is second of four writes for the
// third; one that thinks it is alone writes for nobody.
//
// None of this is taste. Which moments are worth keeping is what a style is FOR
// and is why there are several; how a second is spelled, what the step after
// this one will do with the answer, and that nothing may be made up are facts
// about this tool, true for every style and every job it will ever have. So the
// facts are one prompt, in front of all of them, and a style is only the part
// that could reasonably differ.
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

import (
	"regexp"
	"strings"
)

const sysSystem = `You are called by an automated video editor, one job per call.

THE ANSWER
Read by a machine, not a person: exactly what the job asks for and nothing around it -- no markdown, no code fence, no preamble, no report of what you did. Where a job asks for JSON, strict JSON is the whole answer; where it asks for lines or columns, their number and order are part of it.

THE MATERIAL
One session -- pictures and microphones, all on one clock -- written as stamped lines:

  [12:04] EVENT: what the picture showed in those seconds, and whether it was hectic or calm
  [12:07] SPEAKER_01: something said out loud, which the video plays
  [12:11] NARRATOR: something said on a microphone the video does not play, so only a job that uses it is heard

A clip's block may also carry MARKED: the editor's own name for a moment in it -- not something said and not something the picture showed, but what they call it.

THE CLOCKS
Every line is stamped, and the request says which clock. Answer on the same one.

  [12:04] session time, mm:ss from its start, minutes counting past 59 -- [72:30] is 4350 seconds. Times you return on it are session seconds: mm*60+ss.
  [+2.0s] an offset from the start of what the request is about -- these frames, this clip. Negative is before it.
  A bare number in a column is seconds on one recording's own timeline: copied, never recomputed.

THE FOUR STEPS
  Prepare writes those lines: frames described into EVENT lines, microphones transcribed into SPEAKER and NARRATOR lines. No later step sees the footage -- from here the lines ARE the session.
  Cut picks the segments the video is made of. Captions and effects are asked for after it, clip by clip.
  Narrate writes the voice-over over those segments. Each clip keeps its own sound underneath.
  Produce writes the upload text, draws the thumbnail, renders the video.

The finished video is those segments played one after another, so it has a clock of its own: a time in the video is not a time in the session.

THE CUT
A segment is a stretch of session seconds: chronological, never overlapping, each boundary in the gap between two lines, and not all the same length -- what is on screen decides, never an average.

An effect decorates a stretch inside one clip; one outside every clip is thrown away. zoom punches in on the centre. text puts a caption on screen. speed rescales the clock, above 1 rushing. stop holds the picture still while the sound runs on. volume sets how loud those seconds are, 1 as recorded, 0 silent.

WHAT EACH JOB IS GIVEN, AND WHAT IT ANSWERS WITH -- nothing around the answer:

  describe: a few frames, each after a line "[+2.0s] FRAME 3 of 4" on the same clock as the speech around them; the running STATE from the chunk before; the last EVENT lines. Answers two lines:
    EVENT: Calm; the tower fires into the crowd and a health bar empties.
    STATE: On the second map, defending the left lane with the new tower.
  transcript: a context block, then N lines of TSV -- start, end, speaker, text. Answers exactly those N lines in order, start, end and speaker copied character for character and only the text changed. No line merged, split, dropped, added or emptied; no tabs in the text; no line numbers. Any difference in count, order, times or speakers discards the block.
  cut: the footage range and the session timeline. Answers the segments alone, in session seconds:
    {"segments":[{"start":0,"end":28},{"start":104,"end":232},{"start":232,"end":301}]}
  captions: a few clips, "CLIP n" with its length, then the lines said over it stamped as offsets from that clip's start. Answers one entry per clip given, its number as given, "start" and "end" as offsets from that clip's start:
    {"clips":[{"i":6,"fx":[{"start":2,"end":5,"text":"I will be showcasing the new tower"}]}]}
  speed: the clips with their lengths and which carry captions, the footage they come to, the target and the range. Answers a rate for the clips that run fast, the rest at 1:
    {"speeds":[{"clip":2,"rate":4},{"clip":5,"rate":2}]}
  effects: every clip of the cut, the same way. Answers effects named to a clip, "start" and "end" as offsets from that clip's start:
    {"fx":[{"clip":4,"kind":"zoom","start":0,"end":3},{"clip":7,"kind":"stop","start":11,"end":13},{"clip":9,"kind":"volume","start":0,"end":20,"gain":0}]}
  narrate: one block per clip -- "CLIP n" with its start, end, length and word ceiling, then what happened over it stamped as offsets from that clip's start. Answers an entry per line, its clip's start and end as given, "at" the second the line starts inside the clip ("at":<sec>):
    {"entries":[{"start":104,"end":232,"at":3,"text":"And there it goes.","emotion":"surprised=0.6"}]}
    emotion is how the TTS reads it: a base -- happy, angry, sad, afraid, disgusted, melancholic, surprised, calm -- or close kin, weighted 0 to 1 ("angry=1", "happy=0.8, surprised=0.4"); named mixes (excited, awed, alarmed, confused, frustrated, desperate, tender, proud, dismayed, horrified, ominous) take a weight the same way. Loud or fast is not an emotion.
  upload text: the clips, each with where it starts in the finished video, what was seen and said in each, and the narration over it. Answers three parts with a blank line between them -- the title on one line prefixed exactly "TITLE: ", the thumbnail instruction on one line prefixed exactly "THUMBNAIL: ", then the description as prose. No JSON. The instruction goes to an image model that edits the first frame it is given, the others as references named by position ("the ship from the second image"); the title is printed onto the finished picture afterwards, so ask for no text, no lettering and no logo, and for the part it lands in to stay calm and uncluttered.

TOOLS
Some jobs are offered web_search and web_read. They are for a fact about a named thing you would otherwise guess -- what a tower does, what an item costs, how a name is spelled. A fact you write is one the material shows or one you looked up; with no tool offered, a fact you do not have is one you do not write.

NEVER INVENT
Only what the material shows. Never invent a time, a name, a score, a moment or an outcome -- not even one the user context leads you to expect: a stretch the lines do not cover did not happen, and only stretches with EVENT lines have footage behind them.`

// sysPrompt is the system message a job goes out with: the shared context, then
// that job's own prompt. Every call that sends a system prompt is built through
// here -- there is no second way to assemble one, which is what stops a job
// added later from quietly being the one that is never told how a stamp reads.
//
// An emptied box takes the block away rather than sending a heading with
// nothing under it, the way ctxBlock does: a model reading rules that are not
// there will happily supply its own.
func (a *App) sysPrompt(key string) string {
	job := a.prompt(key)
	// the precedence rule rides on the wording, not in it: it is assembled
	// here, like the system context in front, so a project's edited copy of
	// a wording carries it too and a wording added later cannot forget it.
	// Only when there is a user context to outrank anything -- a session with
	// an empty box is told nothing about a block it will not get.
	if key != "system" && a.sessionCtx() != "" {
		job += "\n\n" + ctxRule
	}
	sys := sysFor(key, strings.TrimSpace(a.prompt("system")))
	if sys == "" {
		return job
	}
	return sys + "\n\n" + job
}

// sysFor is the system context cut down to what one job can use.
//
// The box is one text, and it stays one text: the formats are the formats and
// there is one place to edit them. But it was also sent whole, to every job --
// and most of it is about some OTHER job. The describing step was handed the
// audit's reply shape and the narration's list of emotions; the transcript
// fixer was told what a zoom does. Seven kilobytes in front of every call, of
// which a describe call could use two. A 27B model weighs what it is given,
// and what it was given was mostly noise about jobs it is not doing.
//
// So the sections are chosen per job, by their headings, which are the app's
// own vocabulary (TestTheSystemContextIsUnderHeadings). The list of what each
// job answers with keeps only this job's own line. Anything under a heading
// this does not know -- a section somebody added to the box -- goes to every
// job, because the safe reading of an unknown section is that it matters.
func sysFor(key, sys string) string {
	if sys == "" {
		return ""
	}
	want := sysSections[key]
	var out []string
	head := "" // the heading the current block sits under; "" before the first
	for _, blk := range sysBlocks(sys) {
		if h := sysHeading(blk); h != "" {
			head = h
		}
		switch {
		case want == nil:
			// the system context itself, or a key nobody registered: whole
		case head == "" || !knownSection(head):
			// the run-up before the first heading, and sections this does
			// not know about: every job
		case !want[head]:
			continue
		case head == sysJobsHead:
			blk = ownJobLine(key, blk)
			if blk == "" {
				continue
			}
		}
		out = append(out, blk)
	}
	return strings.Join(out, "\n\n")
}

// sysBlocks is the box as paragraphs: runs of blank lines divide them, and a
// paragraph carries no blank lines of its own -- so a heading with an extra
// blank line in front of it still opens its block rather than hiding behind a
// blank first line.
func sysBlocks(sys string) []string {
	var out []string
	for _, blk := range blankRuns.Split(sys, -1) {
		// the blank lines around a block go; the indent its first line wears
		// stays, because the lists in the box are indented and a first line
		// that lost its indent would read as a paragraph of its own
		blk = strings.TrimRight(strings.Trim(blk, "\n"), " \t\n")
		if strings.TrimSpace(blk) != "" {
			out = append(out, blk)
		}
	}
	return out
}

var blankRuns = regexp.MustCompile(`\n[ \t]*\n+`)

// sysHeading is the heading a block opens with, or "".
//
// The app's own sections are known by their opening words, which is what
// lets the jobs heading carry a lower-case tail ("-- nothing around the
// answer:") and still be a heading. A section somebody adds to the box is a
// heading when its first line is upper-case words and nothing else.
func sysHeading(blk string) string {
	line := blk
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	for _, h := range sysKnownHeads {
		if strings.HasPrefix(line, h) {
			return h
		}
	}
	if line == "" || strings.HasSuffix(line, ".") || line != strings.ToUpper(line) {
		return ""
	}
	for _, r := range line {
		if !(r >= 'A' && r <= 'Z' || r == ' ' || r == ',' || r == '-') {
			return ""
		}
	}
	return line
}

// sysKnownHeads is every section the table below has an opinion on, by the
// words it opens with; longest first where one is a prefix of another.
var sysKnownHeads = []string{
	"THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE FOUR STEPS", "THE CUT",
	sysJobsHead, "TOOLS", "NEVER INVENT",
}

// The sections, as the headings name them, and which jobs read which.
//
//	THE ANSWER, THE MATERIAL, THE CLOCKS, NEVER INVENT   every job
//	THE FOUR STEPS         the jobs downstream of Prepare, which write for
//	                       the step after them; describe and fix are Prepare
//	THE CUT                the two jobs that produce or check segments
//	WHAT EACH JOB ...      this job's own line, and no other's
//	TOOLS                  the jobs that are offered any (webToolsFor)
const sysJobsHead = "WHAT EACH JOB IS GIVEN"

var sysSections = map[string]map[string]bool{
	"describe": sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", sysJobsHead, "NEVER INVENT"),
	"fix":      sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", sysJobsHead, "NEVER INVENT"),
	"cut":      sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE FOUR STEPS", "THE CUT", sysJobsHead, "TOOLS", "NEVER INVENT"),
	"captions": sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE CUT", sysJobsHead, "NEVER INVENT"),
	"speed":    sysSet("THE ANSWER", "THE CUT", sysJobsHead),
	"effects":  sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE CUT", sysJobsHead, "NEVER INVENT"),
	"narrate":  sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE FOUR STEPS", sysJobsHead, "TOOLS", "NEVER INVENT"),
	"youtube":  sysSet("THE ANSWER", "THE MATERIAL", "THE CLOCKS", "THE FOUR STEPS", sysJobsHead, "TOOLS", "NEVER INVENT"),
}

func sysSet(heads ...string) map[string]bool {
	m := map[string]bool{}
	for _, h := range heads {
		m[h] = true
	}
	return m
}

// knownSection is whether a heading is one the table above has an opinion on.
func knownSection(head string) bool {
	for _, h := range sysKnownHeads {
		if h == head {
			return true
		}
	}
	return false
}

// sysJobLabel is how the jobs list names each key's own line.
var sysJobLabel = map[string]string{
	"describe": "describe:", "fix": "transcript:", "cut": "cut:",
	"narrate": "narrate:", "youtube": "upload text:",
	"captions": "captions:", "speed": "speed:", "effects": "effects:",
}

// ownJobLine is a block under the jobs heading with every other job's line
// taken out. The heading is one block and the lines are another, so this sees
// each in turn: a heading line stays, this job's line stays, the rest go. A
// block left with nothing is nothing.
func ownJobLine(key, blk string) string {
	label := sysJobLabel[key]
	var out []string
	mine := false
	for _, line := range strings.Split(blk, "\n") {
		switch {
		case sysHeading(line) != "":
			out = append(out, line)
		case strings.HasPrefix(strings.TrimSpace(line), label):
			// this job's line, and what is indented under it: the worked
			// example a job answers by is written on its own lines beneath
			// the sentence, and a filter that kept the sentence alone would
			// send every job its shape with the example cut off
			mine = true
			out = append(out, line)
		case mine && strings.HasPrefix(line, "    "):
			out = append(out, line)
		default:
			mine = false
		}
	}
	return strings.Join(out, "\n")
}

// ctxRule is what the user context is allowed to change: everything a job's
// wording says, and nothing the system context says.
//
// A wording is a set of defaults for a session nobody described -- how many
// effects, how long a segment, what a line sounds like, what to lead with --
// and every one of them was written without this video in view. The USER
// CONTEXT was written by the person whose video it is. So where the two
// disagree the context wins, and the wording says so itself, at its end,
// where the model has the rules in its hands: a model that has just read
// "three or four effects" as a rule and then meets "caption every thing as it
// is named" in the request otherwise resolves the contradiction in favour of
// the rule, and the person who asked gets nothing and is told nothing.
//
// The system context is the exception, and it is named here as one. It holds
// the mechanics -- the shape of the answer, the clocks, what may be invented,
// the ranges the reply is judged by -- and those are how the answer is READ,
// not how the video is made. A context that changed them would break the
// machine that reads the reply, not improve the video.
const ctxRule = `WHERE THIS DISAGREES WITH THE USER CONTEXT
Everything above is a default for a session nobody described. The USER CONTEXT in the request was written by the person whose recording this is, and wherever it asks for something these rules would not -- more of an effect or none, a longer segment, another voice, a different subject, a line kept that this would drop, a language this did not expect -- the user context wins and the rule above gives way. What it does not change is the mechanics you were given first: the shape of the answer, the clock, what may be invented, and the ranges the reply is judged by. Those are how the answer is read, not how the video is made.`
