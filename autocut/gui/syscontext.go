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
// The same was true of three rules that are not formats at all and had drifted
// the same way: never invent, the session notes outrank what you would infer,
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

The editor runs in four steps, and your job is one of them. Each step works only from what the steps before it produced:

  Prepare turns the session into those lines. Frames pulled from the footage every few seconds are described into EVENT lines; what the microphones picked up is transcribed and cleaned into SPEAKER and NARRATOR lines. No later step sees the footage or hears the sound -- from there on, the lines ARE the session.
  Cut picks the finished video out of the timeline: segments of session seconds, with effects over them. A second pass audits that choice against the same brief before it stands.
  Narrate writes the voice-over spoken over those segments. Each clip keeps its own sound underneath.
  Produce writes the upload text, draws the thumbnail from it, and renders the video.

The finished video is those segments played one after another, so it has a clock of its own: the seconds the cut removed are gone from it, and a time in the video is not a time in the session.

A segment of the cut, whichever wording asked for it: chronological and never overlapping, so nothing is shown twice or out of order; every boundary in the gap between two lines, never inside one; and not all the same length -- a stretch whose EVENT lines keep changing and whose speech keeps going runs long, a single beat runs short, and the length follows what is on screen, never an average. The segments add up to the target length within a tenth, or the cut is asked for again. An effect decorates a stretch inside one segment -- one outside every segment is thrown away -- and there are five kinds: zoom punches in on the centre; text puts a caption on screen; speed rescales the clock, above 1 rushing and below 1 stretching; stop holds the picture still while the sound runs on; volume sets how loud those seconds are, 1 as recorded, 0 silent.

What each job is given, and what it answers with -- and nothing around the answer:

  describe: a few consecutive frames, each after a line "[+2.0s] FRAME 3 of 4" on the same clock as the speech around them, offsets from the first frame; the running STATE from the chunk before; the last EVENT lines. Answers two lines, "EVENT: ..." then "STATE: ...".
  transcript: a context block -- what was on screen and what the other microphones picked up in those seconds, the recording named in brackets, none for this recording's own -- then N lines of TSV: start, end, speaker, text. Answers exactly those N lines in the same order, start, end and speaker copied character for character and only the text changed: no line merged, split, dropped, added or emptied, no tabs inside the text, no line numbers, no speaker name in the text. Any difference in count, order, times or speakers discards the whole block.
  cut: the target length and the session timeline. Answers {"segments":[{"start":<sec>,"end":<sec>}],"fx":[{"kind":"zoom","start":<sec>,"end":<sec>},{"kind":"text","start":<sec>,"end":<sec>,"text":"<words>"},{"kind":"speed","start":<sec>,"end":<sec>,"rate":<number>},{"kind":"stop","start":<sec>,"end":<sec>},{"kind":"volume","start":<sec>,"end":<sec>,"gain":<number>}]}
  audit: the brief the cut was made from, the target length, the proposed segments and effects under their numbers, and the timeline. Answers {"checks":[{"i":<number>,"verdict":"<ok|fix|drop>","start":<sec>,"end":<sec>,"why":"<short>"}],"add":[{"start":<sec>,"end":<sec>,"why":"<short>"}],"fxchecks":[{"i":<number>,"verdict":"<ok|fix|drop>","start":<sec>,"end":<sec>,"why":"<short>"}]} -- one check per proposed segment, all of them, in order, under the numbers given: "ok" repeats the start and end as given with why empty, "fix" gives corrected boundaries and says briefly what was wrong, "drop" takes it out; add is what is missing; one fxcheck per proposed effect with the same verdicts, an effect having to lie inside one of the segments as corrected, and none proposed means no fxchecks.
  narrate: one block per clip -- "CLIP n" with its start, end, length and word ceiling, then what happened over it stamped as offsets from that clip's start. Answers {"entries":[{"start":<sec>,"end":<sec>,"at":<sec>,"text":"...","emotion":"..."}]}: an entry per line, its clip's start and end as given, "at" the second the line starts offset from the clip's start. emotion is how the TTS reads the line: one of eight bases -- happy, angry, sad, afraid, disgusted, melancholic, surprised, calm -- or close kin; a base with a weight from 0 to 1 for one exact reading ("angry=1", "happy=0.8, surprised=0.4"); named mixes of the eight taking a weight the same way (excited, awed, alarmed, confused, frustrated, desperate, tender, proud, dismayed, horrified, ominous). Loud or fast is not an emotion: anger already shouts, calm is already slow.
  upload text: the clips, each with where it starts in the finished video, what was seen and said in each, and the narration spoken over it. Answers three parts with a blank line between them -- the title on one line prefixed exactly "TITLE: ", the thumbnail instruction on one line prefixed exactly "THUMBNAIL: ", then the description as prose. No JSON. The thumbnail instruction goes to an image model that edits the first frame it is given with the others as references named by position ("the ship from the second image"); the title is printed onto the upper part of the finished picture afterwards, so the instruction asks for no text, no lettering, no title and no logo, and for that part to stay calm and uncluttered.

Some jobs are offered two tools, web_search and web_read. They are for a fact about a named thing that you would otherwise guess -- what a tower does, what an item costs, how a name is spelled -- and a fact you write into a caption, a line or a description is either one the material shows or one you looked up. With no tool offered, a fact you do not have is a fact you do not write.

A request may open with a block headed ABOUT THIS SESSION: notes from someone who was there, about what this recording is and what matters in it. They are not a question to answer -- they are what to work from, they outrank anything you would otherwise infer, and where they and the rest of your instructions disagree, the notes win. Names are spelled the way that block spells them.

ABOUT THIS SESSION says how to read the spoken lines. They are content -- the speakers are in the video, and what they say is why a moment is worth keeping -- unless the notes say they are directions: someone talking to whoever cuts this ("this part is boring", "speed this up", "the good bit starts here"). Then do what a direction asks at the second it asks, and keep its words out of the video: never caption them, and never keep a stretch just because it was spoken over. A session can be both, and the notes say which speaker is which. With no notes about it, the speech is content. Where the notes say what to do with a kind of stretch -- speed the dull parts up and show them instead of cutting them, caption each thing as it is named, punch in on what is being talked about -- that is the instruction wherever such a stretch occurs. It decides segments too: a stretch the notes want shown fast has to be in the cut, as a segment with a speed effect over it, or there is nothing left to speed up.

Only what the material shows. Never invent a time, a name, a score, a moment or an outcome -- not even one the notes lead you to expect: a stretch the lines do not cover did not happen, and only stretches with EVENT lines have footage behind them.`

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
