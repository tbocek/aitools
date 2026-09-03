package main

// Cut. One thumbnail track over the session timeline, with what the cut
// keeps tinted green over the footage, and the waveform lanes under it. (There
// was a second track showing the cut as its own row of thumbnails; it repeated
// the green and cost a band.) Mouse wheel zooms around the cursor and the bar's
// + and − do it around the middle of the view, in both cases no further out
// than the whole session (minPps). Drag selects on the track or on a lane, and a
// rough selection is fine: Add snaps its edges to a nearby scene change or
// speech gap, computed from data earlier steps already produced. "Suggest
// cut" (LLM) fills the cut to the target length; from then on the human owns
// it and the total simply is what it is. That suggestion (or, before any
// suggestion, whatever was on disk) is the checkpoint Revert returns to, so
// hand edits are always a separate, droppable layer.
//
// Gaps between recordings (wall-clock holes with no video) draw as a fixed
// narrow hatched band, not proportionally -- a 30 minute break should not be
// 30 minutes of scrollbar.
//
// cut/cut.json {"segs":[{"s":..,"e":..}]}   session-time seconds, sorted

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // frames are jpeg; the decoder registers itself with image.Decode
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	rulerH = 18 // tick zone on top of the picture track
	// the selection band, between the ruler's clock and the thumbnails. Its own
	// row rather than a tint on the pictures: a tint has nothing to take hold
	// of, and this is the one thing on the page you are always about to move,
	// resize or throw away. See drawSelBand.
	//
	// 22 and not 16 because the green bar wears the ✕ that drops a scene, and
	// that mark is a plated badge like every other ✕ in the app (drawKillBadge):
	// 14 px of plate needs a row it can sit inside rather than straddle.
	selBandH = 22
	gapPx    = 26  // display width of an unfilmed hole between two runs
	laneGap  = 3   // px between two cameras' rows of the picture band
	snapTol  = 5.0 // seconds the Add edges may move to find a better cut point
	minSegLn = 1.0 // segments shorter than this are dropped when editing
	undoDeep = 50  // how many edits back Undo reaches
	edgeGrab = 6.0 // px either side of a clip edge that hovers and trims it
	// px either side of the HELD edge that a press takes hold of it by. Wider
	// than edgeGrab because by then you are aiming at something you can see --
	// the white bar -- and because missing costs more: a press that lands clear
	// of it drops the edge and starts a selection over the clip you were in the
	// middle of trimming.
	edgeMove = 12.0
	// smallest the marker for a spliced insert is drawn. It is a POINT on the
	// session timeline -- the footage is cut there and carries on afterwards, so
	// it takes no session seconds (see cutSeg) -- but it does have a length, and
	// spliceSpan draws that length at the current zoom. This is the floor under
	// it: a two-second card at the zoom where a whole session fits on screen is a
	// few px of violet, and below about this it is not a marker any more. The
	// same idea, and deliberately about the same size, as the hatched hole drawn
	// between two recordings.
	splicePx = 22.0
	// px a dragged clip snaps to its neighbour within. Small: it has to be
	// reachable by hand and it must not swallow a deliberate one-second hole
	// between two clips.
	snapPx = 8.0
	// live scrubbing while an edge is dragged. A flushing accurate seek decodes
	// from the previous keyframe, and a drag fires one motion event per frame of
	// the UI; asking gstreamer for sixty of those a second gets a picture that
	// lags the mouse by more the longer you drag. One every scrubEvery keeps up,
	// and the drag's end always seeks exactly where it landed.
	scrubEvery = 90 * time.Millisecond

	// How the run bar is shared between Suggest's two calls. Choosing gets the
	// bigger half because it is the one that can be asked again: its answer is
	// validated and rejected up to three times, where the audit runs once and is
	// kept or discarded whole.
	suggestChooseShare = 0.6
	// the most segments the cut prompt asks for. Only a fallback denominator,
	// for a reply whose segments have no readable end to place them by.
	suggestMaxSegs = 20.0
)

// One paragraph or bullet per line, unwrapped: see describeSystem.
//
// Every suggest-pipeline prompt -- the five cut styles and the audit that reads
// their answer back -- is written for a local model of Qwen-27B's class: one
// narrow job each, every rule load-bearing, no rule explained twice, and the
// job given as a numbered procedure rather than as taste. "Keep what matters"
// is a judgement such a model makes badly and differently every run; "list
// what the user context names, find where each happens, divide the target by the
// count" is arithmetic it does the same way every time. So each wording is a
// procedure: derive the subject from the user context, enumerate, budget, place --
// and the rules every style shares (boundaries, length, the reply) are one
// tail (cutReply) rather than five copies.
//
// genericSystem is the one shipped as the default, and the only one that does
// not assume what it is looking at. The other three are shapes: a gaming
// highlight reel, a rating video, a Short. They cut better than the generic one
// WHEN the footage is that shape and worse when it is not -- the highlights
// wording says "gaming session" in its first sentence and will look for wins
// and disasters in a woodworking video -- so the wording that guesses least is
// the one a new project gets, and picking a shape is a thing you do once you
// know you have one.
const genericSystem = `You choose the moments a video is cut from. The recording is a session of something happening -- a game, a build, a lesson, a conversation, a drive -- and nobody has told you which. Read it first, then cut what is worth watching.

Work in this order.

1. Say what this session is, in one line. The USER CONTEXT usually says it in its first sentence; with none, work it out from the timeline -- what the speakers say they are doing in the first minutes, and what the EVENT lines keep showing. Cut for THAT, not for what a session like it usually contains.

2. List what the user context names: every thing, moment, person or part. Each one gets a segment where it happens on the timeline. Find the place by the speech and the EVENT lines around it, and take the whole beat -- the setup, the thing itself, and what came of it, even when they are minutes apart. These come first and are never dropped for length.

3. Fill the rest of the target length by the kind of video this is:
- a showcase of things (a model, a unit, a build, a piece of kit): every thing named, each seen whole, seen close, seen working, and judged -- and never a stretch where the thing is off screen.
- a rating or a verdict: every item once where it is played or tried, then the verdict whole at the end.
- a lesson, a build or a walkthrough: each step once, the result, and what went wrong.
- a session played or watched for its own sake: the peaks -- the thing working, the thing failing, the point being made, the answer to a question asked earlier -- and what people responded to, since a laugh, a groan or someone confidently wrong outranks a competent stretch nobody said anything about. Split the session into four equal quarters and take something from each.
- anything else: what would be missed if it were gone.

4. Shape the whole. The first segment establishes what this is: wherever the speakers say what they are doing or what they are after. Set busy stretches (EVENT says hectic) against calm ones. Finish on something that reads as an ending -- the result, the verdict, the last word.

Each segment starts a beat before the first word you want and ends after the reaction to it: 8 seconds to about a minute, longer when a moment needs its whole build-up and payoff, and longer again under a speed effect (see below). These are rough guides, not limits. How many: about one segment per 20 seconds of target length, never fewer than two.` + fxRules + cutReply

const suggestSystem = `You choose the moments for a highlight video of a gaming session, cut for YouTube. Someone who was not there should enjoy it from start to finish.

Work in this order.

1. List what the USER CONTEXT names: every moment, person or thing. Each gets a segment where it happens -- find the place by the speech and the EVENT lines around it, and take the whole beat: setup, the thing itself, and the reaction after it, even when they are minutes apart. Cutting at the first mention hands the viewer a setup with no payoff. These come first and are never dropped for length.

2. List the peaks, from the timeline: an EVENT line that says hectic, a win, a disaster, a near miss, a reveal, a callback to something set up earlier -- and every line people reacted to: a laugh, a scream, swearing, someone confidently wrong. A joke beats a technically impressive moment nobody reacted to.

3. Pick from that list until the target length is spent, in this order: the first segment, wherever the speakers say what they are doing or what they are after; then the peaks with the loudest reaction; then enough calm stretches to set the loud ones against, because all peaks is as tiring as none. Split the session into four equal quarters and take at least one segment from each. The last segment is something that feels like an ending.

Each segment starts a beat before the first word you want and ends after the reaction to it: 8 seconds to about a minute, longer when a moment needs its whole build-up and payoff, longer again under a speed effect (see below), and a joke gets its setup. These are rough guides, not limits. How many: about one segment per 20 seconds of target length, never fewer than two.` + fxRules + cutReply

// ratingSystem is the cut for a session whose shape is a verdict: a group plays
// several things and ranks them. suggestSystem cuts for the best moments, which
// on this footage is exactly wrong -- it happily takes four segments from the
// map everyone found funny and never shows two of the others at all, and then
// the ranking at the end names things the viewer has not seen.
//
// So this one cuts for coverage and for the shape of the argument instead: what
// is being rated, then every item at least once, then the verdict in full. The
// one thing it cannot do is reorder. Segments are session seconds and the cut is
// strictly chronological and non-overlapping -- a montage of all nine maps
// pulled from all over the hour is not something this model can express, and the
// prompt says so rather than asking for a plan that gets silently flattened.
// What makes that survivable is that a rating session is already in that order:
// the browse at the start shows the items, play works through them, the ranking
// is last.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const ratingSystem = `You cut a rating video: a session where people play, watch or try several things and end by ranking them. The viewer should finish knowing what was rated, what each one was like, and where each landed.

Work in this order.

1. List the items. If the USER CONTEXT names them or the scoring, that is the list. Otherwise take it from the speech and the EVENT lines: the maps, levels, weapons, songs -- whatever this session scores. Write every item down with the seconds where it is played or tried.

2. Find the ranking. It is almost always near the end: the tier list, the countdown, the "so the winner is". Note where it starts and where the top item is named.

3. Reserve the verdict first. The ranking goes in WHOLE, as several consecutive segments if it runs long, ending on the segment where the top item is named. A ranking that stops at third place makes the video pointless. Subtract its length from the target length; what is left is for the rest.

4. Divide what is left across every item, once each, in the order they come up: the stretch that shows what it is like, plus the reaction or the score said out loud. One good segment per item beats three from the best item. An item with no segment is named in the ranking without ever having been seen, so when length is tight every item gets something short rather than some of them something generous.

5. Spend what is still left, in this order: what this is, where the speakers say what they are doing and how the scoring works; the line-up, where the session shows the items before playing them -- a menu, a list, the names read out; then the items the group argued about or changed their mind on, and funny lines that land on items already covered.

You cannot gather the items into a montage: cover each one where it happens on the timeline. Roughly 8 seconds to a minute each, longer under a speed effect (see below); a verdict or a reveal runs as long as it needs. End a segment on the judgement -- someone saying what they think -- not the moment the action stops. If the timeline does not show where an item was rated, take the nearest stretch where it is discussed.` + fxRules + cutReply

// showcaseSystem is the cut for a session whose subject is a THING rather than
// a stretch of time: an unboxing, a paint job, a new unit, a printed model, a
// tool on a bench. Highlights cuts for the loudest reactions and finds none,
// because nothing loud happens; the generic wording cuts for what would be
// missed if it were gone and takes the talking, because the talking is where
// the sentences are. Both give the viewer a video about a thing they never
// properly saw.
//
// So this one cuts for LOOKING. Its unit is not a moment, it is a pass over the
// object -- named, seen whole, seen close, seen working, judged -- and its one
// hard rule is that a segment where the thing is not on screen is not a
// showcase segment however good the line over it is. Several objects is the
// same shape repeated, which is why the wording says to split the length before
// choosing anything: three figures on a 3-minute cut is a minute each, and a
// showcase that spends four minutes on the first of them is the fault this
// wording exists to prevent.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const showcaseSystem = `You cut a showcase: a session where someone shows a thing -- a model, a figure, a unit, a tower, a machine, a build, a piece of kit -- to a viewer who wants to see it. They should finish knowing what it is, what it looks like up close, and what it does.

Work in this order.

1. Name the subject. The USER CONTEXT says what is being shown -- take its word for it, spelled its way. With none, take it from the speech and the EVENT lines: what is on the table, in the hand, on the screen.

2. Count the things. A showcase of towers is one entry per tower; a showcase of one figure is one entry. Find them where the speech or an EVENT line names one, and write each down with the second it first enters and the second the session leaves it -- everything about it is between those two, and nothing outside them belongs to it. Several things is the same job repeated, and going long on the first leaves the last with nothing.

3. Budget: divide the target length by the number of things. That is each one's share before you look at any of them. A thing the user context calls out may take more; the extra comes out of the others and the total does not move.

4. For each thing, in the order they come up, spend its share on these, in this order:
- What it is: where it is first named or first properly in frame. One segment, and the viewer knows what they are looking at.
- The whole of it: the pass where the camera holds on it or goes around it, so its size, shape and finish are seen once. Every showcase needs this segment and it is the one most often missing. A slow pan runs as long as the camera takes: cutting it in half wastes the one shot that shows the whole thing.
- The details: the close views, the parts the speaker points out, the things they say are good or wrong. This is where most of the share goes: a close view with the explanation over it is the best segment this video has.
- It doing what it is for: assembled, switched on, placed, played, driven, fired, worn, put next to something for scale.
- The verdict on it: what the speaker makes of it, what it cost, whether they would have another. End the thing here.

5. Check every segment against the EVENT lines: the thing must be on screen. A stretch where it is out of frame is not a showcase segment however good the line over it is. Then drop repeats: two segments of the same view of the same part is one segment -- every segment shows something the viewer has not seen yet. Skip the box, the packaging and the setting-up unless something in it is worth seeing.

Roughly 8 seconds to a minute each, longer under a speed effect (see below); a pan as long as it takes.` + fxRules + cutReply

// shortsStyleName is how the Shorts wording is picked and stored; the style
// clamp in suggestClicked reads the same name, so the two cannot drift apart.
const shortsStyleName = "YouTube Shorts"

// A Short's length, as the rest of the app reads it: shortsLen is what the
// format aims at, and [shortsMin, shortsMax] is how far a number in the ▶
// target box is still believed to MEAN a Short. The box is usually still set
// for the long cut -- minutes -- and a five-minute "Short" is a mistake
// nobody means, so shortsTargetFix reads a target outside the window as the
// box left over from other work rather than as a wish. Two callers make that
// one judgement: picking the Shorts wording corrects the box itself
// (styleTarget), and the run makes it again at ▶ (suggestClicked), for the
// box edited back out of the window in between.
const (
	shortsLen            = 25.0
	shortsMin, shortsMax = 15.0, 45.0
)

func shortsTargetFix(t float64) (float64, bool) {
	if t < shortsMin || t > shortsMax {
		return shortsLen, true
	}
	return t, false
}

// styleTarget makes the target box follow a wording that has a length of its
// own. The run already aimed at 25 s when the Shorts style met a long-cut
// target, but it said so only in the log, and the box went on showing a
// number the run was not going to use. Correcting the box on the pick is the
// same judgement made where it can be seen -- and overridden: the box stays a
// box. Called from pickPromptStyle, so a project whose saved pick is Shorts
// gets the box set on load too; before the page exists it is a no-op.
func (a *App) styleTarget(key, name string) {
	if key != "cut" || name != shortsStyleName || a.ed == nil || a.ed.target == nil {
		return
	}
	cur := 0.0
	fmt.Sscanf(a.ed.target.Text(), "%f", &cur)
	if fixed, changed := shortsTargetFix(cur); changed {
		a.ed.target.SetText(strconv.Itoa(int(fixed)))
	}
}

// shortsSystem is the cut for a YouTube Short: one subject, 20 to 30 seconds,
// watched on a phone mid-scroll. suggestSystem builds a video someone watches
// from start to finish; a Short is scrolled INTO, so it opens mid-action,
// stays on its one subject, and is gone before it needs a second one. The
// subject comes from the user -- the USER CONTEXT block -- and this
// prompt's job is everything else: finding where that subject happens,
// cutting it hard, and marking the few effects that make a phone read it.
//
// The cut is budgeted, not browsed. Left to feel its way, the model kept
// overshooting -- a 25-second target came back as twice that -- because
// "about 25 seconds" gave it nothing to compute. So the prompt makes it do
// arithmetic first: the context's important parts are the beats, the target
// divided by their count is each beat's opening share, and seconds are traded
// between beats before any timestamp is chosen -- a lull squeezed to the 5
// seconds it needs, the surplus spent on the beat that earns it. The sum is
// checked against the target before answering, and suggestWindow holds a
// Short to a fifth over the target where other styles get half.
//
// Its effects are asked for in the same reply as the segments, as every style's
// now are (fxRules): choosing what to cut and whether to decorate it is one
// judgement, made once. What keeps them lined up is downstream -- the audit
// reads the effects back against the segments as it corrects them
// (fxchecks), and clampFxToSegs holds whatever survives to the cut as
// applied, snapEdge and coalesce included.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const shortsSystem = `You cut a YouTube Short from a gaming session: one vertical clip of 20 to 30 seconds, watched on a phone mid-scroll. The first two seconds have to already be the good part, or the viewer is gone.

Work in this order. Budget the seconds before you touch the timeline.

1. The USER CONTEXT says what this Short is about. Count the parts it calls important -- those are the beats, told in order. Find where each one happens on the timeline. If it names nothing, there is one beat and it takes the whole budget: the single best moment of the session -- the loudest reaction, the biggest surprise, the funniest line.

2. Divide the target length by the number of beats: that is each beat's opening share. The user context decides how many there are -- one part, three, five -- and the arithmetic is the same at every count: five beats against a 25-second target open at 5 seconds each; a single beat opens with all 25.

3. Now trade seconds between beats, keeping the same total. A beat that is only setup or a lull gets squeezed to what it takes to be understood -- 5 seconds is often plenty -- and every second it gives up goes to the beat that earns it, usually the opener or the payoff. A beat that carries the clip may take more than one segment; a dull one never gets more time.

4. Place each beat's segment. Open mid-action, no introduction: the hook IS the clip. Cut HARD: start on the last line of setup that still makes the payoff land, and end on the reaction's peak, not its tail. The last second decides the rewatch.

5. Land the length by trimming inside segments, never by dropping a named beat.

The beats are one story told in parts, not a compilation: each segment is there because the user context asked for it. A good moment outside the named parts belongs to a different Short -- leave it. As many segments as the beats need and no more, usually one per beat.

Effects, for a phone.

- Two or three across the whole Short is plenty; an empty list is fine for a clip that carries itself.
- A zoom of two to four seconds onto the thing that matters at the moment it matters. A caption under about eight words on the key line or the punchline -- a Short is watched with the sound off, so this is often the only way the words land. speed 0.5 for the one impact worth savouring, 2 or more to rush a stretch the viewer does not need. At most one stop, a second on the face or the score, on the beat the clip is about. volume for a line recorded too quiet to hear on a phone in public.` + cutReply

// fxRules is the effects half of a cut prompt, shared by the three styles that
// cut for a screen rather than for a phone. Shorts keeps its own wording: two
// or three effects on a 25-second clip watched with the sound off is a
// different instruction from a handful spread over five minutes with the sound
// on, and one paragraph trying to cover both would say neither well.
//
// It is appended rather than written out three times because it is the same
// instruction three times. The reply shape is not in here either: it is in
// cutReply, which every wording ends on, Shorts included.
//
// The user context comes first in it on purpose. What to do with a dull stretch is
// exactly what the editor writes down -- "the boring parts you can speed up
// and show instead of cutting them" -- and that sentence is an instruction
// about the SEGMENTS as much as about the effects: the stretch has to be kept
// for there to be anything left to speed up. A model told to decorate a cut it
// has already chosen cannot act on it; told both at once, it can.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const fxRules = `

Effects.

- Few and deliberate: three or four across five minutes of finished video, each with a reason you could say out loud. Not one on every segment, and not none.
- That count is a DEFAULT, and the USER CONTEXT outranks it. Asked for a caption on each thing as it is named, write those captions. Asked to speed every dull stretch, write those speeds. Asked for none, write none. A rule here that contradicts what the person editing asked for is this list being wrong about their video.
- The one thing this list cannot become is a subtitle track. One text effect per line of speech is hundreds of them -- more than a single answer can hold, and not what these are for. Putting everything said on screen is the narration step with its captions voice, which writes the lines and Produce burns into the picture; a cut that tries to do it here gets cut off in the middle and lands nothing at all.
- Pick the kind by what the moment needs, not by variety. Something important on screen and easy to miss -> zoom onto it. A viewer who would not know what is happening -> text saying it. A stretch that must be shown but not watched -> speed. The one beat worth landing on -> stop. Sound that does not sit right against the rest -> volume.
- A stretch that has to be shown but not watched is ONE segment with a speed effect over it, never a row of small segments with the dull seconds left out. The cut is what the video contains; the effects are how it plays. Cutting between every line of speech to skip the pauses is how an answer turns into hundreds of segments and stops being a cut.
- A segment under a speed effect may be two to four times the ordinary length -- minutes rather than a minute -- because it costs the finished video its seconds DIVIDED by the rate: two minutes at 4 spends thirty seconds of the target. That is what makes showing a long dull stretch affordable, and it is a rough guide like the others, not a limit.
- A zoom runs two to four seconds, onto the score, the face, the mistake, while the speech is about it. A caption is under about eight words -- the name of a thing as it is first shown, the number someone just said, what the footage does not say out loud -- and says something true, from the material or from a search. speed 2 rushes a few seconds of the walk back or the loading screen, 4 to 8 a whole minute of it; 0.5 savours one impact, once in a video. One stop per video, on the beat everything else was leading to. volume for a stretch recorded too quiet or too loud, for ducking a background under a line that matters, and for muting seconds the session says are not to be heard.`

// cutReply is the end of every cut wording, Shorts included: where a segment
// may start and end, the length arithmetic, the reply, and the check to run
// before answering. Last on purpose -- it is the part a model acts on with the
// answer in its hands, and the part that was written out five times before,
// which is how the five came to disagree about the tolerance. The shape of
// the reply is here and nowhere else: suggestParse reads one shape whichever
// wording asked, so one wording is where it is spelled.
//
// The tolerance it asks for is tighter than the one the run accepts
// (suggestWindow): a model aimed at a tenth lands inside a half, and one aimed
// at a half does not.
const cutReply = `

Where a segment ends: on the payoff, never just before it. A moment that only makes sense because of something earlier needs that earlier thing in the cut too, or neither. Too long: shorten the weakest segments. Too short: extend to the payoff first, then add the next moment on your list.

Answer in the cut's shape. Before you answer, add up how long your segments RUN -- end minus start, and that divided by the rate wherever a speed effect covers them, because a stretch at 4 costs the video a quarter of its seconds -- and check: the total is inside the accepted range you were given; every segment has an EVENT line inside it; every start is later than the end before it; every effect lies inside one of your segments; everything the user context names is in.

One pass at the total. Land inside the range and answer; do not trim and re-add to reach an exact number. If the total is outside it you will be told what it came to and given your answer back to correct, which costs one short reply -- where working it out to the second before answering costs the whole call.`

// cutSeg is one piece of the finished video. Normally it is a stretch of the
// session: S and E are session seconds and the footage under them is what plays.
//
// Ins turns it into an insert instead -- a file that plays in that slot rather
// than any recording. A card reading "a few moments later", a title, a diagram,
// an animated tier list at the end. The times still mean something, and mean the
// same thing: S is where it sits in the cut, and E-S is how long it runs. That
// it costs session seconds is the point of putting it on the session timeline
// rather than in a list beside it -- everything downstream already reasons in
// those seconds. The narration can be written over an insert like any other
// clip, which is the whole reason a ranking card is worth having: the voice
// reads it out while it is on screen.
//
// So an insert is a segment everywhere except where footage is required, and
// those places name it: coalesce never merges one, keepFilmed never drops one
// for having no recording under it, and the cut prompts never see one.
//
// That is one of the two ways to put a file in a cut, and it is the one that
// PAYS for the card in footage: those seconds of session are gone, the card is
// on screen instead. The other is to splice it in -- cut the footage at an
// instant, play the card, carry on with the frame that was next -- and that one
// costs no footage at all, so it cannot have session seconds either. A spliced
// insert therefore sits at a POINT: S == E, and how long it runs is Dur.
//
// Everything that asks "how long is this clip" asks length(), which is the one
// place the two spellings meet. Everything that asks WHERE it is still reads S,
// and the answer is still a session time. And the sequence that gets rendered --
// footage, card, footage again -- is not the segment list itself but
// splitSpliced of it, since cutting the footage in half at the splice is the
// renderer's business and not something the timeline should be storing.
type cutSeg struct {
	S float64 `json:"s"`
	E float64 `json:"e"`
	// the asset, absolute or relative to the project root. Empty for footage,
	// which is nearly every segment, so an ordinary cut.json is unchanged.
	Ins string `json:"ins,omitempty"`
	// how long a SPLICED insert runs. Only a spliced one has it -- an insert
	// that replaces footage runs for exactly the footage it replaces -- so an
	// ordinary cut.json is unchanged by this too.
	Dur float64 `json:"dur,omitempty"`
	// playback rate for footage: 0.5 is half speed. 0 or 1 is normal, so an
	// ordinary cut.json is unchanged by this as well. Only produceSegs writes
	// rates -- the editor's own segments never carry one.
	Rate float64 `json:"rate,omitempty"`
	// where an inserted SOUND starts inside its file. Nought for a file
	// chosen from disk, which plays from its own beginning, and the copied
	// second for a stretch of a lane copied out of the session -- which is
	// the whole of what makes a copy of sound different from a file.
	Ss float64 `json:"ss,omitempty"`
	// this insert brings no sound of its own -- the one tick the insert form
	// asks (askInsertParams). The one flag reads
	// two ways, and which one is decided by the mode rather than by a second
	// flag: SPLICED, the cut is open and there is nothing else in the slot, so
	// the insert plays silent; OVERWRITING, the footage is still underneath and
	// keeps being heard, so only the picture is replaced. Both are the same
	// sentence -- this insert contributes no audio -- and an ordinary cut.json
	// is unchanged by it.
	Mute bool `json:"mute,omitempty"`
	// which row of the picture band this scene's PICTURE comes from -- which
	// camera. Nought on a session shot on one camera, which is every session
	// this page has ever cut, so an ordinary cut.json is unchanged by it.
	//
	// It is the row and not the file because a row is a camera and a camera is
	// several files: one scene can run from the end of one of them into the
	// start of the next, and it is still one scene of one camera. Which FILE
	// is then a question about a second, and pickVideoOn answers it.
	Cam int `json:"cam,omitempty"`
	// for a sound laid over the footage: which recording it was put in place
	// OF. Empty is the answer a selection scoped to picture-and-sound gives --
	// the file stands in for everything audible, which is what overwriting the
	// sound has always meant. Named -- the selection was drawn in that lane --
	// it stands in for that one recording
	// and the rest keep playing under it. Meaningless on anything but a sound
	// insert, and an ordinary cut.json is unchanged by it.
	Lane string `json:"lane,omitempty"`
	// which lanes this scene does NOT hear, by the name every audio row on the
	// page already carries: a camera's own sound is its recording's name, a
	// separate recording is its file's. Both kinds go in one list because the
	// page shows them as one kind of thing -- a row with a waveform -- and a
	// scene that is "my voice only" has to be able to say so about both.
	//
	// The SILENT ones rather than the audible ones, for two reasons. A cut
	// written before this, and a scene nobody has touched, both come out as the
	// empty list, which is every lane playing -- what the render has always
	// done. And a lane that appears later (another source split) arrives
	// audible everywhere rather than silent everywhere, which is the answer
	// that can be heard and corrected rather than the one that is missed.
	Quiet []string `json:"quiet,omitempty"`
	// this clip STARTS at a border somebody made on purpose: | Split put it
	// there (cut_split.go). Two clips of one camera that touch are otherwise
	// one clip, and coalesce joins them the moment anything rearranges the
	// list -- which is right for two selections that turned out to meet, and
	// wrong for a border drawn deliberately to give a stretch its own camera,
	// its own sound or its own place in the order. The flag is what tells the
	// two apart. Nothing else sets it, so an ordinary cut.json is unchanged.
	Split bool `json:"split,omitempty"`
}

// laneQuiet is the one reading of that list, so the page, the preview and the
// render cannot disagree about what a scene hears.
func laneQuiet(quiet []string, base string) bool {
	for _, q := range quiet {
		if q == base {
			return true
		}
	}
	return false
}

// hears is the question every caller actually asks.
func (s cutSeg) hears(base string) bool { return !laneQuiet(s.Quiet, base) }

func (s cutSeg) isInsert() bool { return s.Ins != "" }

// spliced is "the footage is not replaced here, it is cut open and continues
// after the card". Dur rather than S == E as the test: a footage segment can be
// squeezed to nothing by an edit and would then answer yes to the other one.
func (s cutSeg) spliced() bool { return s.Ins != "" && s.Dur > 0 }

// length is how long this clip runs in the finished video: a spliced card
// runs for its own Dur, slowed footage runs longer than the footage it shows.
func (s cutSeg) length() float64 {
	if s.Dur > 0 {
		return s.Dur
	}
	if s.Rate > 0 {
		return (s.E - s.S) / s.Rate
	}
	return s.E - s.S
}

type tlVideo struct {
	base     string
	path     string
	start    float64 // session time of this video's t=0
	wall     float64 // the same instant on the wall clock, for naming outputs
	dur      float64
	interval float64
	fps      float64
	frames   []string
	w, h     int     // pixel size, for the framing overlay's arithmetic
	pxOrigin float64 // display x of the video's left edge (at current zoom)
	lane     int     // which row of the picture band this one is drawn on
	// the second of the FILE this row starts at. Nought for a recording, which
	// is shown whole from its first frame; a cut lane is a window on a file and
	// may open partway into it (cut_lane.go).
	off float64
}

// at is the second of v's file that session-second t shows, and it is the
// question every -ss and every seek on this page is asking. Two things stand
// between the two clocks: where the recording sits in the session (start), and,
// on a lane the cut put there, how far into the file the row begins (off).
func (v *tlVideo) at(t float64) float64 { return t - v.start + v.off }

// sessionAt is the same sum read the other way: the session second at which this
// file's second local is on screen.
func (v *tlVideo) sessionAt(local float64) float64 { return v.start + local - v.off }

// ffprobeFPS reads the average frame rate; r_frame_rate lies on VFR captures.
func ffprobeFPS(path string) float64 {
	out, err := exec.Command(ffTool("ffprobe"), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=avg_frame_rate", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 30
	}
	var num, den float64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f/%f", &num, &den)
	if den > 0 && num/den >= 1 && num/den <= 240 {
		return num / den
	}
	return 30
}

type cutEditor struct {
	a    *App
	vids []tlVideo
	segs []cutSeg
	// the separate recordings, drawn as waveform lanes under the cut. They are
	// not part of the timeline's geometry -- the footage is the master, and an
	// audio recording that starts before it or runs on after it is simply drawn
	// where it overlaps (see cut_audio.go).
	auds  []tlAudio
	waves map[string]*waveform // per recording, filled in by a background decode

	// the tracks are built from what Prepare wrote, and that is a
	// snapshot taken at reload time -- so anything that writes into those
	// folders, or changes which project's folders they are, leaves this page
	// showing the past. stale says so, and the page catches up when it is next
	// looked at (see refreshCut). Rebuilding on every arrival instead would
	// re-probe every recording for a tab click and throw away the undo history
	// for nothing.
	stale   bool
	pending bool // a catch-up is already queued for the end of this turn

	pps    float64 // pixels per second (zoom)
	lastX  float64 // cursor x, for zoom centering
	totalW float64
	// the stretches of the session that got filmed, and where each is drawn.
	// The one map every lane is measured against; rebuilt by relayout.
	spans []tlSpan
	laneN int // how many rows the picture band is stacked into (at least 1)
	// nRows is the row count the band holds on to even when the HIGHEST rows
	// are empty. An empty row between two full ones survives relayout because
	// the pins hold the gap open; an empty row at the bottom has nothing under
	// it and would fold up the moment a drag vacated it -- before its ✕ could
	// ever be pressed (killRow). moveRow raises this floor, closeRow lowers
	// it, and 0 means no floor: the footage alone says how many rows there are.
	nRows int

	sel struct {
		t0, t1 float64
		active bool
		// which sound the selection is OF, by recording base name, or "" when
		// it was dragged on the pictures. The span is a stretch of session
		// time either way; what the verbs make of it is not. On the pictures
		// ⧉ Copy takes the footage and ⧉ Insert puts a card in the cut; in a
		// lane the same two buttons take that lane's sound and lay a sound
		// over those seconds, because a selection means the thing it was
		// drawn on.
		aud string
		// ...and, when it was dragged on the pictures, whether it is about the
		// picture ALONE: the frames without the sound filmed with them. Three
		// what a selection is OF is what it was drawn on: the pictures, or a
		// lane's wave. See selSnd (cut_audio.go)
		// for why that stopped being the same sentence as "these seconds".
		// Meaningless while aud names a recording, and cleared with it.
		pic bool
		// ...and, when it was dragged on the pictures, WHICH camera's row it
		// was dragged on. The green says what will be shown, so a selection
		// has to know which picture it is offering: ＋ Add on the second row
		// keeps the second camera for those seconds and takes them off the
		// first. Nought on a one-camera session, where there is only one row
		// to have drawn it on.
		lane int
	}
	// the copied selection, in hand: where the footage it plays again starts,
	// and for how long. Not yet in the cut -- ⧉ Paste is what places it -- so
	// it lives here and not in segs, and it outlives the selection it was
	// taken from: clearing the band does not empty the hand.
	copyFrom float64
	copyLen  float64
	copyOn   bool
	// which recording the copy in hand is OF, empty for footage. Kept beside
	// the seconds rather than read off the selection at paste time, for the
	// same reason the seconds are: the band may have been cleared or redrawn
	// somewhere else entirely by then, and the hand still holds what it took.
	copyAud string
	// ...and which camera's row those seconds were taken off. A copy plays
	// footage again, and with two cameras rolling "the footage at 4:10" is two
	// different pictures: without this the paste would come out as whichever
	// recording happened to be first in the list. Read at the same moment as
	// the seconds, for the same reason.
	copyCam int
	// the selection band's own hold, the same shape as the edge's and the
	// effect's: whether the band is in hand, and whether the pointer is over
	// it. Its row is cut_selband.go.
	selOn       bool
	selHov      bool
	bandHov     bool   // the pointer is over the green bar's clip, ends included
	bandKillHov int    // ...and the bar whose ✕ it is on, which lights red; -1 for none
	selCur      string // the cursor name the source area last asked for
	audCur      string // ...and the lanes
	thumbHt     int    // thumbnail height; the 🔍 buttons change it
	srcHt       int    // the height the source area was last asked for; see fitSrc
	playhead    float64
	hasPlay     bool
	// where ⏸ stopped, and whether it did. ▶ has places it starts from that
	// are not the line (a held edge, a held clip); resuming is not one of
	// them, and this is how the two are told apart. See toggle.
	resumeT  float64
	resumeOn bool
	// the row the preview is WATCHING, plus one; 0 for the cut's own answer,
	// so a zero editor answers to the cut. Inside a kept scene the preview
	// shows the scene's camera (camAt), which leaves no way to see what
	// another camera saw at the same second -- and that is most of how a
	// scene gets stolen for it. A click on a row asks exactly that, and ▶
	// withdraws it: playback is the cut's (toggle).
	monRow    int
	player    *Player
	playVideo *tlVideo // which recording the preview is playing
	// the preview has been started and not stopped, which is what makes the run
	// bar its transport. Same rule as narrate and produce: a recording merely
	// LOADED -- which clicking the timeline does, just to show the frame there
	// -- must not take ▶ away from suggesting, which is what ▶ means here.
	started bool

	markIn, markOut float64 // editor-style in/out points, session time
	hasIn, hasOut   bool

	// the clip edge a press near a border has picked up: which segment, which
	// side of it, and whether this hold has moved anything yet. The undo snapshot
	// is taken on the first move, so picking an edge up and putting it back down
	// is not an edit. edgeOn rather than an index-or-minus-one because a zero value
	// has to mean "nothing held", and 0 is a perfectly good segment.
	edgeOn    bool
	edgeSeg   int
	edgeEnd   bool
	edgeDirty bool
	// the border the pointer is over, which is the one a left press would take
	// hold of. Held as its time rather than as an index because that is all the
	// picture of it needs, and because a hover is answered on every motion
	// event: two fields to compare against beats working out afresh which clip
	// the highlight belonged to.
	edgeHovOn bool
	edgeHovT  float64
	lastScrub time.Time // when the preview last followed a dragged edge

	// the whole clip a double click has picked up, held the same way and for
	// the same reason: an edge is what you grab near a border, and a clip is what
	// you grab anywhere else on one. A drag then slides it, keeping its
	// length, which is the edit that has no other spelling here -- "this scene,
	// four seconds later" used to be two edge drags that had to agree.
	segOn    bool
	segSel   int
	segDirty bool

	// The tracks are NOT a wide widget in a scrolled window (see drawTrack):
	// they are exactly as wide as the space they have, and this adjustment is
	// the window onto the timeline that they draw.
	hadj         *gtk.Adjustment
	hbar         *gtk.Scrollbar
	viewX, viewW float64          // scroll offset and width of that window, in timeline px
	srcArea      *gtk.DrawingArea // the footage, with the cut tinted green over it
	// the recorders' band: waveform lanes for the sound nobody filmed -- every
	// recording that is not some row's own track. A camera's sound is drawn
	// under its own pictures in srcArea instead (drawPairStrip), so in the
	// common cameras-only session this band is not there at all.
	audArea *gtk.DrawingArea
	// the red line's own transparent layer over both bands, so the playback
	// tick has something thin to repaint (cut_playline.go); lineIdx is the
	// scene the green bar stood on when the bands were last painted, the one
	// other thing on them the running clock alone can move
	lineArea *gtk.DrawingArea
	lineIdx  int
	total    *gtk.Label
	clock    *gtk.Label // the red line's time in numbers, beside the transport
	marks    *gtk.Label // the two marks in numbers, under the buttons that set them

	target *gtk.Entry
	inputs *gtk.Label // what this page reads, and what Suggest is sent
	out    *gtk.Label // what cut/ holds, the same line every other page shows

	// the form column beside the video (cut_form.go): its heading, the words
	// it shows when it is empty, the form in it and who to tell when that form
	// is taken out. formBox nil means no page has been built, which is what
	// every headless test is.
	formBox     *gtk.Box
	formHead    *gtk.Box
	formFoot    *gtk.Box // pinned under the scroller: the form's buttons live here
	formTitle   *gtk.Label
	formIdle    *gtk.Label
	formCur     gtk.Widgetter
	formFootCur gtk.Widgetter
	formGone    func()
	formArm     string // the kind whose "now draw it" note the column is holding

	thumbs map[string]*gdkpixbuf.Pixbuf
	// the insert currently under the playhead, and its rendered frames. Nil
	// until the playhead first lands on one (cut_insview.go).
	film *insFilm
	// a spliced card playing with the footage stopped, and the insert whose own
	// sound the preview is playing. Both cut_insview.go's.
	hold    insHold
	cardSnd string
	scores  map[string][]float64 // per video: visual change per frame
	gaps    map[string][]float64 // per video: session-time speech-gap points

	// the two hand-made corrections to the timeline (cut_shift.go): how many
	// seconds each source's clock was out, and the rows as they were when the
	// first correction froze them. Both keyed by source base, both saved.
	shift map[string]float64
	rows  map[string]int
	// the rows the cut put on the band itself: copied or inserted material that
	// no recording is behind, and that Prepare has never heard of
	// (cut_lane.go). laneHov is the one whose ✕ is under the pointer.
	cutLanes []cutLane
	laneHov  string
	// rowHov is the empty row whose ✕ is under the pointer, -1 for none
	// (cut_lane.go: an emptied row wears the same badge a cut lane does)
	rowHov int

	undo []cutState // one snapshot per edit; every edit is reversible
	redo []cutState // what Undo took back, so Redo can put it in again
	base cutState   // the cut at the last checkpoint; Revert returns to this

	// the camera and the clock (cut_fx.go): the cut's aspect ratio ("" is
	// the source's own), the effects, and which one is currently held.
	// Held the same way a clip is -- one thing held at a time, so taking hold
	// of an effect drops a held clip or edge and the other way round.
	aspect string
	fx     []cutFx
	// the preview plays the CUT rather than the recording: the stretches the
	// edit removed are skipped instead of played through, so ▶ shows what the
	// finished video will run. A view mode -- nothing here is saved, and the
	// track still draws every second of the session underneath.
	cutOnly    bool
	cutPlayBtn *gtk.Button // the second ▶: plays the CUT (cutOnly follows it)
	// the clip a gap was last skipped to, so a jump that cannot be made is not
	// attempted again on every tick. -1 is "not in a gap"; see skipGap.
	jumped int
	fxOn   bool
	fxSel  int
	// which effect the pointer is over, and whether it is over one at all.
	// Purely a drawing matter -- nothing is held until it is pressed -- but it
	// is what makes a lane of shoulder-to-shoulder markers clickable.
	fxHovOn bool
	fxHov   int
	// the effect whose ✕ the pointer is on, or -1 (cut_fxkill.go)
	fxKillHov int
	// this hold has moved its effect; the undo snapshot is taken on the first
	// move, so lifting an effect and putting it back is not an edit
	fxDirty bool
	// an effect is being dragged along its lane right now. The line follows a
	// dragged effect, and syncFxHold must not read that as the line walking
	// away from it -- see there.
	fxMoving bool
	// the three layers of the finished picture over the preview -- the camera,
	// a stop's frozen frame, the mask and the titles -- and the smoothed clock
	// they are drawn on. Embedded, not held, because this page had all of it
	// first and every ed.fxArea / ed.livePlayhead() / ed.syncPreviewZoom() in
	// the file still means what it did; what changed is that the Narrate
	// preview now runs the SAME code (cut_fxscreen.go) instead of its own
	// second opinion about the same render.
	fxScreen
	// what the next drag on the video draws: "zoom", "text" or "svg" while
	// one of the effect buttons is armed, "" when the video is just a picture.
	fxArm string
	// the drawing an armed "svg" is waiting to place -- chosen before the
	// drag, because a box means nothing until you know what goes in it
	fxSrc string
	// the drawings already rasterized for the preview, by file (fxsvg.go)
	svgs map[string]*fxSVG
	// the pointer's current shape over the overlay, remembered so the motion
	// handler only touches the cursor when it actually changes
	fxCursor string
	aspectDD *gtk.DropDown // the toolbar's aspect choice
	aspectMu bool          // the dropdown is being set by code, not by hand

	undoBtn, redoBtn, revertBtn *gtk.Button
	playBtn                     *gtk.Button // ▶/⏸ for the preview; drawn by syncPlayIcons
	insBtn                      *gtk.Button // ⧉ Insert, or ✎ Edit while a card is held
	// ⇲ Lane, which is on the bar only while a copy of footage is in hand: it
	// is the other place a copy can go (cut_lane.go), and a permanent button
	// for it would be greyed out for the whole of every session that never
	// takes one.
	laneBtn *gtk.Button
	copyBtn *gtk.Button // ⧉ Copy, greyed until there is a selection to take
	// ＋ Add: the footage's verb, greyed while the selection is a sound's (see
	// syncSelBtns).
	addBtn *gtk.Button
	// | Split, between them: the span kept AND cut free of what it lay in.
	// Greyed by Add's own rule, because it is the same kind of verb about the
	// same kind of selection (cut_split.go).
	splitBtn *gtk.Button
	// － Remove, the same span the other way round. It stood beside ＋ Add
	// once, guessed what it was aimed at, and was taken off the bar for it;
	// this one is the selection's verb and nothing else's (cut_selrm.go), so
	// it can cut a hole in a scene, which the green bar's ✕ cannot.
	remBtn *gtk.Button
}

// ---- data ------------------------------------------------------------------

// mmss is how this page says a duration. It was three identical closures in
// three functions before something outside them needed it too.
func mmss(t float64) string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }

func (a *App) cutDir() string  { return filepath.Join(a.outDir, "cut") }
func (a *App) cutPath() string { return filepath.Join(a.cutDir(), "cut.json") }

// cutFile is cut.json, whole. There used to be an anonymous struct at every
// place that read or wrote it -- reload, persist, the render -- and they drifted
// apart every time a field was added: a field the editor saved and the render
// had never heard of is a setting that works until you close the tab. One shape,
// three users, and adding to it is one edit.
//
// Shift and Rows are the timeline's own corrections rather than the cut's, and
// they live here because they are this project's, not the files': the recordings
// are untouched and every step re-derives the placement from these two maps.
type cutFile struct {
	Segs   []cutSeg `json:"segs"`
	Aspect string   `json:"aspect,omitempty"`
	Fx     []cutFx  `json:"fx,omitempty"`
	// READ ONLY, and kept only so an old project still opens the way it was
	// left: one lane the whole cut was heard on. Nothing writes it -- reload
	// spreads it across the scenes (migrateSound, cut_hear.go) and the next
	// save leaves the field out, which is what makes the move a one-way door.
	Sound string `json:"sound,omitempty"`
	// per source base: seconds its clock was out, as dragged by hand
	Shift map[string]float64 `json:"shift,omitempty"`
	// per source base: the row it sat on when the first drag froze the rows
	Rows map[string]int `json:"rows,omitempty"`
	// the rows that are not recordings: copied or inserted material given a
	// band of its own for the cut to reach (cut_lane.go)
	Lanes []cutLane `json:"lanes,omitempty"`
	// how many rows the band keeps even while the highest stand empty: a row
	// vacated by a drag waits for its ✕ across a restart too (cutEditor.nRows)
	NRows int `json:"nrows,omitempty"`
}

// reload rebuilds the timeline from the current selection + step outputs.
func (ed *cutEditor) reload() error {
	a := ed.a
	vids, auds := a.snapSources()
	if len(vids) == 0 {
		return fmt.Errorf("nothing to cut — no source on the Prepare step is marked as footage")
	}
	type st struct {
		path  string
		start float64
	}
	// same zero convention as session.tsv: the earliest moment any source
	// names, and 0:00 when none of them names one (srcClock)
	var all []st
	paths := append(append([]string{}, vids...), auds...)
	at, zero := srcClock(paths)
	for _, p := range paths {
		all = append(all, st{p, at[p]})
	}
	ed.vids = nil
	ed.film = nil // another project's card is not this one's
	for _, s := range all[:len(vids)] {
		p, err := a.planVideo(s.path, a.describeDir())
		if err != nil {
			return err
		}
		dur, _ := ffprobeDur(s.path)
		vw, vh, _ := ffprobeSize(s.path)
		ed.vids = append(ed.vids, tlVideo{
			base: p.base, path: s.path, start: s.start - zero, wall: s.start, dur: dur,
			interval: p.interval, fps: ffprobeFPS(s.path), frames: p.frames, w: vw, h: vh,
		})
	}
	sort.Slice(ed.vids, func(i, j int) bool { return ed.vids[i].start < ed.vids[j].start })

	// Every sound in the session gets a lane, the footage's own first. It is the
	// master and it is the one already coming out of the speakers, and it is
	// exactly what a separate recording has to be read against: two waveforms
	// with the same shout in the same column is the page saying, without a word,
	// that the two clocks agree. Its lane is its own video's stretch of the
	// timeline, so there is no placing to do -- it starts where the video does.
	//
	// Nothing is decoded here: what this needs is where each one sits and how
	// many lanes it has, and the envelopes arrive later (below) without holding
	// the page up.
	//
	// One lane per track the Prepare row asked for, and not one per file: a
	// capture with the game on one track and a headset on the other is two
	// recordings that happen to share a container, and this is where they stop
	// being one (cut_tracks.go).
	ed.auds = srcLanes(ed.vids, a.snappedTracks())
	for _, s := range all[len(vids):] {
		dur, _ := ffprobeDur(s.path)
		ed.auds = append(ed.auds, tlAudio{
			base: baseName(s.path), path: s.path, start: s.start - zero,
			dur: dur, chans: max(1, ffprobeChannels(s.path)),
		})
	}
	sortLanes(ed.auds)

	// cut state; the undo history belongs to the cut that produced it
	ed.segs = nil
	ed.undo = nil
	ed.edgeOn = false
	ed.jumped = -1 // the clip a gap was skipped to belonged to the last cut
	ed.fx = nil
	ed.fxOn = false
	ed.cutLanes = nil // another cut's own rows are not on this band
	ed.nRows = 0      // nor its empty rows
	ed.setAspect("")
	ed.syncButtons()
	var c cutFile
	if b, err := os.ReadFile(a.cutPath()); err == nil {
		if json.Unmarshal(b, &c) == nil {
			ed.segs = c.Segs
			ed.fx = migrateFx(c.Fx)
			// a cut written when the sound was one choice for the whole
			// project, said the way this one says it (cut_hear.go)
			if segs, note := migrateSound(ed.segs, c.Sound, ed.vids, ed.auds); note != "" {
				ed.segs = segs
				ed.a.logf("cut: %s", note)
			}
			ed.shift, ed.rows = c.Shift, c.Rows
			ed.nRows = c.NRows
			ed.setAspect(c.Aspect)
		}
	}
	// the hand-made corrections go on before anything is measured: the gaps
	// below, the rows and the spans in relayout, and every x on the page are
	// all read off these starts
	ed.applyShift()
	// and then the rows the cut put on the band itself, because that is what
	// they are: Prepare settled what was RECORDED, and this settles what
	// the cut has to reach for (cut_lane.go). After the corrections and not
	// before, because setLanes places them itself and applyShift would
	// otherwise move them a second time. Their waveform lanes come with them,
	// windowed to the rows the way the pictures are, which is why this runs
	// before loadWaves below rather than after it.
	ed.setLanes(c.Lanes)
	ed.setBase() // what is on disk now is the checkpoint this session edits from

	// speech-gap candidates: midpoints of silence between anything anyone
	// says, per video, in session time -- Add prefers cutting there
	ed.gaps = map[string][]float64{}
	var speech [][2]float64
	for _, s := range all {
		base := baseName(s.path)
		rows := loadSeg4(filepath.Join(a.transcriptDir(), base, "transcript.fixed.tsv"))
		if rows == nil {
			rows = loadSeg4(filepath.Join(a.transcriptDir(), base, "commentary.fixed.tsv"))
		}
		if rows == nil {
			rows = loadSeg4(filepath.Join(a.inputsDir(), base, "transcript.tsv"))
		}
		for _, r := range rows {
			speech = append(speech, [2]float64{s.start - zero + r.s, s.start - zero + r.e})
		}
	}
	sort.Slice(speech, func(i, j int) bool { return speech[i][0] < speech[j][0] })
	for vi := range ed.vids {
		v := &ed.vids[vi]
		var pts []float64
		last := v.start
		for _, sp := range speech {
			if sp[0] > last && sp[0] < v.start+v.dur {
				pts = append(pts, (last+sp[0])/2) // silence midpoint
			}
			if sp[1] > last {
				last = sp[1]
			}
		}
		ed.gaps[v.base] = pts
	}

	ed.loadWaves()

	// visual-change scores in the background; snapping works without them
	// (speech gaps only) until they land
	if ed.scores == nil {
		ed.scores = map[string][]float64{}
	}
	for _, v := range ed.vids {
		if _, ok := ed.scores[v.base]; ok {
			continue
		}
		v := v
		go func() {
			sc := frameChangeScores(v.frames)
			glib.IdleAdd(func() { ed.scores[v.base] = sc })
		}()
	}
	ed.relayout()
	ed.updateInputs() // the recordings just changed, and so did their lengths
	return nil
}

// frameChangeScores diffs consecutive frames at postage-stamp size; local
// maxima are scene-change candidates.
//
// The decoding is Go's own and not GdkPixbuf's, deliberately: this runs on a
// worker goroutine (a long recording is hundreds of frames and the caller must
// not stall the window), and every pixbuf is a GObject whose reference
// bookkeeping gotk4 finishes back on the main loop. Making and dropping
// hundreds of them off the main thread raced that bookkeeping and corrupted the
// heap -- GLib said so by the hundred lines (g_atomic_rc_box_release_full:
// assertion 'real_box->magic == G_BOX_MAGIC' failed) before the abort landed in
// an unrelated finalizer. image/jpeg touches nothing but Go memory, so it is
// safe anywhere. Thumbnails on screen (ed.thumb) stay on pixbufs: those are
// made on the main thread, where they belong.
func frameChangeScores(frames []string) []float64 {
	out := make([]float64, len(frames))
	var prev []byte
	for i, f := range frames {
		px := framePostage(f)
		if px == nil {
			continue
		}
		if prev != nil && len(prev) == len(px) {
			sum := 0
			for j := 0; j < len(px); j++ {
				d := int(px[j]) - int(prev[j])
				if d < 0 {
					d = -d
				}
				sum += d
			}
			out[i] = float64(sum) / float64(len(px))
		}
		prev = append(prev[:0], px...)
	}
	return out
}

// postage size: what a scene change has to survive being shrunk to before we
// call it one. Small enough that camera noise and a wobbling hand average out,
// big enough that a person walking through the shot moves a box or two.
const postW, postH = 24, 14

// framePostage reads one frame down to a postW x postH grid of brightness,
// each cell the average of the whole box of source pixels under it, so a moved
// edge changes a cell instead of falling between two sampled points. It returns
// nil for anything it cannot read -- a half-written frame, a format the
// stdlib does not know -- and the caller then leaves that frame's score at zero.
func framePostage(file string) []byte {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil
	}
	sum := make([]int, postW*postH)
	cnt := make([]int, postW*postH)
	// jpeg decodes to YCbCr, whose Y plane already is the brightness -- reading
	// it straight is worth it here, where the loop runs over every pixel of
	// every frame of the recording
	yc, _ := img.(*image.YCbCr)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := (y - b.Min.Y) * postH / b.Dy() * postW
		for x := b.Min.X; x < b.Max.X; x++ {
			k := row + (x-b.Min.X)*postW/b.Dx()
			if yc != nil {
				sum[k] += int(yc.Y[yc.YOffset(x, y)])
			} else {
				r, g, bl, _ := img.At(x, y).RGBA()
				sum[k] += int((r + 2*g + bl) / 4 >> 8)
			}
			cnt[k]++
		}
	}
	px := make([]byte, postW*postH)
	for k := range px {
		if cnt[k] > 0 {
			px[k] = byte(sum[k] / cnt[k])
		}
	}
	return px
}

// ---- geometry --------------------------------------------------------------

// tlSpan is one stretch of the session somebody filmed, and where it is drawn.
//
// WHY THE AXIS IS NOT SIMPLY THE RECORDINGS. It used to be: each file laid down
// after the last, gapPx between them, x measured from the file's own origin.
// That reads exactly like a clock for as long as no two cameras were rolling at
// once, and stops meaning anything the moment they were -- two files covering
// the same minute would be drawn as two minutes end to end, and session-second
// 3:00 would be at two different places at the same time. Every lane that has
// to line up with another (the sound, the effects, the green) would be lining
// up with a different story.
//
// So the axis is TIME, and these are the stretches of it that got filmed: the
// union of the recordings' spans, merged where they touch or overlap. What
// nobody filmed is not drawn -- it is collapsed to the same gapPx hatch that
// used to sit between two files, because an hour of nothing between two clips
// is an hour of dead pixels to scroll past.
//
// With one recording, or several that do not overlap, this is the old layout
// span for span: one run per file, in the same order, at the same x.
type tlSpan struct {
	t0, t1 float64 // the session seconds this run covers
	px     float64 // where t0 is on the timeline
}

// dur is the run's length in seconds.
func (s tlSpan) dur() float64 { return s.t1 - s.t0 }

// timeSpans is that union: the filmed runs, in order, none touching.
func timeSpans(vids []tlVideo) []tlSpan {
	raw := make([]tlSpan, 0, len(vids))
	for _, v := range vids {
		if v.dur > 0 {
			raw = append(raw, tlSpan{t0: v.start, t1: v.start + v.dur})
		}
	}
	// ed.vids is sorted by start, but a lane shifted in time by hand is not,
	// and the merge below is only right on a sorted list
	sort.Slice(raw, func(i, j int) bool { return raw[i].t0 < raw[j].t0 })
	var out []tlSpan
	for _, r := range raw {
		if n := len(out); n > 0 && r.t0 <= out[n-1].t1 {
			out[n-1].t1 = math.Max(out[n-1].t1, r.t1)
			continue // this one carries on where the run so far had got to
		}
		out = append(out, r)
	}
	return out
}

// runs is the filmed stretches, laid out or not. relayout caches them with
// their pixel origins, and everything that draws reads that cache; anything
// that only wants the TIMES -- what got dropped, what a selection can cover --
// asks here instead, so it still answers on an editor that has no widgets and
// has never been laid out.
func (ed *cutEditor) runs() []tlSpan {
	if len(ed.spans) > 0 {
		return ed.spans
	}
	return timeSpans(ed.vids)
}

// assignLanes says which row of the picture band each recording is drawn on,
// and how many rows there are.
//
// One camera's files, however many, share a row: they follow one another in
// time, so laying them out side by side on one line is exactly what they are.
// A second camera rolling THROUGH the first cannot share it -- there is no x
// where both could be drawn -- so it gets a row of its own, and the two are
// stacked with the same second above the same second.
//
// This is interval colouring, and greedy is optimal for it: walk the
// recordings in start order and put each on the lowest row that is already
// finished by the time it begins. A session shot on one camera comes out as
// one row at the same y as before there were rows at all.
// pin, when it is not nil, is the rows written down by hand (cutFile.Rows): a
// source listed there keeps its number whatever it now overlaps, and the rest
// are coloured around it. That is what a shifted timeline needs -- dragging two
// cameras apart until they no longer overlap would otherwise collapse them onto
// one row, and every scene that said "camera 2" would point at a row that is
// not there any more.
func assignLanes(vids []tlVideo, pin map[string]int) int {
	ord := make([]int, len(vids))
	for i := range ord {
		ord[i] = i
	}
	// ed.vids is sorted by start on load, but a row shifted in time by hand is
	// not, and colouring out of order would stack two recordings that do not
	// overlap
	sort.SliceStable(ord, func(a, b int) bool { return vids[ord[a]].start < vids[ord[b]].start })
	var rows [][][2]float64 // per row: the stretches already on it
	grow := func(r int) {
		for len(rows) <= r {
			rows = append(rows, nil)
		}
	}
	free := func(r int, s, e float64) bool {
		for _, iv := range rows[r] {
			if s < iv[1]-1e-9 && e > iv[0]+1e-9 {
				return false
			}
		}
		return true
	}
	put := func(v *tlVideo, r int) {
		grow(r)
		v.lane = r
		rows[r] = append(rows[r], [2]float64{v.start, v.start + v.dur})
	}
	// the written-down rows first and in full, so the greedy pass below sees
	// every row they claim, whichever end of the session they claim it at
	for _, i := range ord {
		if r, ok := pin[vids[i].base]; ok && r >= 0 {
			put(&vids[i], r)
		}
	}
	for _, i := range ord {
		v := &vids[i]
		if r, ok := pin[v.base]; ok && r >= 0 {
			continue // already placed above
		}
		// the lowest row that is already finished by the time this one begins.
		// Greedy in start order is optimal for interval colouring, and on a
		// session with no pins and no shifts it puts one camera's files back
		// on one row exactly as it always did.
		lane := len(rows)
		for r := range rows {
			if free(r, v.start, v.start+v.dur) {
				lane = r
				break
			}
		}
		put(v, lane)
	}
	return max(1, len(rows))
}

func (ed *cutEditor) relayout() {
	ed.laneN = max(assignLanes(ed.vids, ed.rows), ed.nRows)
	// a camera unplugged between two visits takes its row with it, and a
	// selection still pointing at that row would keep footage nobody can see
	if ed.sel.lane >= ed.laneN {
		ed.sel.lane = 0
	}
	ed.spans = timeSpans(ed.vids)
	x := 0.0
	for i := range ed.spans {
		if i > 0 {
			x += gapPx
		}
		ed.spans[i].px = x
		x += ed.spans[i].dur() * ed.pps
	}
	ed.totalW = x
	// a recording is contiguous and the runs are the union of all of them, so
	// each file sits inside exactly one run, and x is linear in t inside a run:
	// a file's origin is just its start read off the map. Still kept as a field
	// because the thumbnails are walked by frame index, not by second.
	for i := range ed.vids {
		ed.vids[i].pxOrigin = ed.xOf(ed.vids[i].start)
	}
	if ed.srcArea != nil {
		// height only: the width is whatever the page gives us. The +8 is the
		// picture band's own breathing room; the lane below it is where the
		// camera and clock effects live (cut_fx.go).
		ed.fitSrc()
		ed.fitAudio()
		ed.fitSelAud() // a reload may have taken the recording the selection was of
		ed.syncScroll()
		ed.redrawTracks()
	}
	ed.updateTotal()
}

// picTop is where the picture band starts: under the ruler's clock and the
// selection band, in that order. With more than one camera it is
// the top of the FIRST row; the rest are stacked under it (laneTop).
func (ed *cutEditor) picTop() float64 { return float64(rulerH) + float64(selBandH) }

// laneH is one row of the picture band: a thumbnail and its border.
func (ed *cutEditor) laneH() float64 { return float64(ed.thumbHt) + 4 }

// laneTop is where row i starts. Not a stride any more: every row above it is
// its pictures AND the wave strip paired under them (pairH), and a row with
// sound is deeper than a row without.
func (ed *cutEditor) laneTop(i int) float64 {
	t := ed.picTop()
	for j := 0; j < i; j++ {
		t += ed.laneH() + ed.pairH(j) + laneGap
	}
	return t
}

// pairH is how deep row i's wave strip is: one waveform lane per channel of
// the row's own sound, and nothing at all for a row whose footage has none --
// a silent screen capture is a row of pictures, not a row over an empty band
// pretending it recorded something. Two sources sharing a row share the strip,
// so it is as deep as the deepest of them needs.
func (ed *cutEditor) pairH(i int) float64 {
	h := 0.0
	for _, v := range ed.vids {
		if v.lane != i {
			continue
		}
		if au := ed.pairAud(v.base); au != nil {
			h = math.Max(h, float64(ed.lanes(*au))*waveLaneH)
		}
	}
	return h
}

// pairAud is the sound drawn under this row's pictures: the footage's own
// track (masterLanes, laneAudios), which pairs with the pictures because
// footage is picture and the sound filmed with it in one piece. A separate
// recorder is nobody's pair and keeps its lane in the band below.
func (ed *cutEditor) pairAud(base string) *tlAudio {
	if au := ed.audByBase(base); au != nil && au.master {
		return au
	}
	return nil
}

// picBottom is where the whole stack of rows ends. Everything that is about the
// CUT rather than about one camera -- the green, the scrim, the markers, the
// playhead's own band -- is drawn from picTop to here, so it reads as one thing
// across every row it crosses.
func (ed *cutEditor) picBottom() float64 {
	last := max(0, ed.laneN-1)
	return ed.laneTop(last) + ed.laneH() + ed.pairH(last)
}

// hitPics is whether a y of the source area is on the picture band -- the
// thumbnails and the green over them. The two rows above it and the effects
// lane below it are their own objects with their own rules, so "is this press
// about the cut itself" is a question worth having one answer to.
func (ed *cutEditor) hitPics(y float64) bool {
	return y >= ed.picTop() && y < ed.picBottom()
}

// segTop is where a scene is drawn: its own camera's row. Clamped, because a
// cut.json can name a row this session has not got -- a camera unplugged since
// it was written -- and a scene drawn off the bottom of the band is a scene
// nobody can see to fix.
func (ed *cutEditor) segTop(s cutSeg) float64 {
	return ed.laneTop(min(max(0, s.Cam), max(0, ed.laneN-1)))
}

// laneAt is which camera's row of PICTURES a y is on, or -1 for the thin
// space between two rows, for a row's wave strip (pairAt) and for anything off
// the stack.
func (ed *cutEditor) laneAt(y float64) int {
	for i := 0; i < max(1, ed.laneN); i++ {
		if t := ed.laneTop(i); y >= t && y < t+ed.laneH() {
			return i
		}
	}
	return -1
}

// pairAt is the sound half of the same question: which row's wave strip a y is
// on, or -1.
func (ed *cutEditor) pairAt(y float64) int {
	for i := 0; i < max(1, ed.laneN); i++ {
		if t := ed.laneTop(i) + ed.laneH(); y >= t && y < t+ed.pairH(i) {
			return i
		}
	}
	return -1
}

// pairAudAt is WHOSE sound that strip is at timeline-x px: two sources sharing
// a row each bring the stretch under their own pictures, so the answer is the
// one under the pointer -- or the nearest along the row, audAtY's rule, so a
// press in the hatch between them is a miss and not a void.
func (ed *cutEditor) pairAudAt(px, y float64) string {
	row := ed.pairAt(y)
	if row < 0 {
		return ""
	}
	best, dist := "", math.Inf(1)
	for _, v := range ed.vids {
		if v.lane != row || ed.pairAud(v.base) == nil {
			continue
		}
		d := 0.0
		if x0, x1 := v.pxOrigin, v.pxOrigin+v.dur*ed.pps; px < x0 {
			d = x0 - px
		} else if px > x1 {
			d = px - x1
		}
		if d < dist {
			best, dist = v.base, d
		}
	}
	return best
}

// fitSrc gives the source-track area the height it currently needs. Not a
// constant any more: the effects lane is as deep as the effects in it pile up
// (fxRows), so adding an effect over an existing one makes the area taller and
// removing it gives the room back.
//
// The +8 is the picture band's own breathing room.
func (ed *cutEditor) fitSrc() {
	if ed.srcArea == nil {
		return
	}
	h := int(ed.picBottom()) + 4 + int(ed.fxLaneHeight())
	if h == ed.srcHt {
		return // SetSizeRequest during a draw is how you get a resize loop
	}
	ed.srcHt = h
	ed.srcArea.SetSizeRequest(-1, h)
}

// fitAudio gives the lane area the height its lanes need.
//
// The lanes are a fixed height each, not a share of the page: a waveform is read
// for where it starts and stops, and 30 px says that as well as 300 would. A
// session with no separate recording has no lanes and the area goes away rather
// than sitting there as an empty black strip.
//
// Called again as each envelope lands, because how many lanes a recording draws
// is not known until it is decoded: a stereo file with the same signal on both
// sides collapses to one, and the area has to give the row back rather than
// leave a hole where the second lane was.
func (ed *cutEditor) fitAudio() {
	if ed.audArea == nil {
		return
	}
	if ah := ed.audioHeight(); ah > 0 {
		ed.audArea.SetSizeRequest(-1, ah)
		ed.audArea.SetVisible(true)
	} else {
		ed.audArea.SetVisible(false)
	}
}

// syncScroll points the scrollbar at the timeline as it now is. It is also
// where the bar disappears: a bar that cannot move is a bar that says there is
// something off to the right, and at the zoom floor there is not.
func (ed *cutEditor) syncScroll() {
	if ed.hadj == nil {
		return
	}
	ed.hadj.SetUpper(ed.totalW)
	ed.hadj.SetPageSize(ed.viewW)
	ed.hadj.SetStepIncrement(ed.viewW / 8)
	ed.hadj.SetPageIncrement(ed.viewW * 0.9)
	ed.hadj.SetValue(ed.hadj.Value()) // re-clamps against the new upper
	ed.viewX = ed.hadj.Value()
	ed.hbar.SetVisible(ed.totalW > ed.viewW+0.5)
}

// setOff scrolls to a timeline x; the adjustment does the clamping.
func (ed *cutEditor) setOff(x float64) {
	if ed.hadj == nil {
		ed.viewX = math.Max(0, x)
		return
	}
	ed.hadj.SetValue(x)
}

func (ed *cutEditor) setThumbH(h int) {
	ed.thumbHt = max(40, min(160, h))
	ed.thumbs = map[string]*gdkpixbuf.Pixbuf{} // cached at the old height
	ed.relayout()
}

// setPlayhead drops the red line and cues the preview there. Whatever the
// player was doing continues: paused stays paused (showing the new frame),
// playing keeps playing from the new spot.
func (ed *cutEditor) setPlayhead(t float64) {
	ed.playhead = t
	ed.reLive(t) // the live clock is re-based with the line; see livePlayhead
	ed.hasPlay = true
	// a card holding the footage is holding it for the line, and the line has
	// just been put somewhere else
	ed.cancelHold()
	ed.syncFxHold() // and an effect being aimed is being aimed at THIS frame
	ed.showTime()
	ed.syncPlayGain() // the line may have landed inside a volume effect
	if v := ed.videoAt(t); v != nil && ed.player != nil {
		wasPlaying := ed.player.playing
		// before the seek, never after: a rate only takes hold at a seek, and
		// this is the seek. Setting it afterwards would need a second one.
		ed.player.SetRate(fxPreviewRateAt(ed.fx, t))
		same := ed.playVideo == v
		if !same {
			ed.playVideo = v
			// which recordings are under THIS piece of footage, and by how far
			// their clocks differ from its own -- both change with the file, so
			// they are settled before the file is
			ed.player.SetMix(ed.mixUnder(v))
		}
		// and which of them this scene hears, before either line below tells
		// them to play: a lane the scene silences is not started at all rather
		// than started and hushed (Player.applyMute). showInsert settles it
		// again at the bottom of this function, which is where every OTHER
		// path reaches it -- but by then these pipelines are already running,
		// and a lane switched off is not to be heard for that moment either.
		ed.syncHush()
		if same {
			ed.player.SeekTo(v.at(t)) // same file: cheap in-place seek
		} else {
			ed.player.PlaySegment(v.path, v.at(t), -1, wasPlaying)
		}
	}
	// and if the line landed inside a card, the card is what the preview shows,
	// whatever the footage under it is doing
	ed.showInsert()
	ed.redrawTracks()
}

// mixUnder is the separate recordings the preview should be playing while it
// shows v, and where each of them is when v is at 0.
//
// The footage's own sound is not in it: that comes out of the file the preview
// is already playing, and adding it again would be the same seconds twice, half
// a frame apart, which sounds like a broken speaker rather than like a mistake.
// A recording that was not running while this video was is left out too -- it
// has nothing to contribute to any second of it.
//
// A further track of the capture itself IS in it. It shares the footage's
// path, and the preview used to leave it out for that -- the same file is
// already playing -- which left the one place a second microphone usually
// lives (OBS records the mic as track 2) with a lit badge, a drawn waveform
// and no sound at all, however the hush was set. It is a lane like any other:
// its own pipeline, on the same file, told which track to decode (mixTrack.
// track). The master track is still not here, for the reason above.
//
// delta is the whole difference between the two clocks, off included: the
// master's file second is t - v.start + v.off (tlVideo.at), the lane's is t -
// au.start + au.off, so the lane's is the master's plus this. A cut lane opens
// partway into its file (cut_lane.go), and a delta that forgot its off seeked
// the lane a whole window early -- past nothing, into silence.
func (ed *cutEditor) mixUnder(v *tlVideo) []mixTrack {
	var out []mixTrack
	for _, au := range ed.auds {
		if au.master || (au.path == v.path && au.track == 0) {
			continue
		}
		if au.start+au.dur <= v.start || au.start >= v.start+v.dur {
			continue
		}
		out = append(out, mixTrack{base: au.base, path: au.path,
			delta: (v.start - v.off) - (au.start - au.off),
			lo:    au.off, hi: au.off + au.dur, track: au.track})
	}
	return out
}

// showTime prints the red line's time. The line by itself locates the playhead
// only to the nearest pixel, and zoomed out a pixel is several seconds -- so the
// one number a reader wants after clicking (to type into a mark, to compare with
// a narration time, to say where something is) was the one number the page never
// showed. Same mm:ss.d spelling the edge readouts and the Narrate page use.
//
// It has to be pushed rather than drawn, because three different paths move the
// playhead -- a click, ‹f/f›, and playback following the player's own clock --
// and because the line may well be scrolled out of view while the time is not.
func (ed *cutEditor) showTime() {
	if ed.clock == nil {
		return
	}
	// While the preview is the cut (▶✂) the readout is the cut's own clock:
	// how far into the
	// FINISHED video this is, which is the question that mode is asked. Same
	// format either way, so switching modes cannot shove the bar sideways --
	// the tooltip is what says which of the two clocks is being read.
	// ...unless the cut is empty: then its clock has no reading -- every second
	// of the session maps to 0:00.0 -- and a toolbar stuck on zero while the
	// red line moves answers nothing. The session clock is the one thing that
	// can still say where the line is, so it stays until there is a cut to read.
	t, tip := ed.playhead, ed.playheadTip()
	if ed.cutOnly && len(ed.segs) > 0 {
		// through the effects, not over them: this clock claims to be the
		// finished video's, and a ×2 halves the seconds the video spends on
		// the footage the line is walking through. Read straight off the
		// segments it drifted further from the picture with every effect --
		// the preview was already playing at the effect's rate (syncPlayRate)
		// while the number counted session seconds.
		t = ed.cutPos(ed.playhead)
		tip = fmt.Sprintf("%s into the cut, of %s — the finished video's own clock "+
			"(the ▶✂ preview), the speed effects included. Session time here is %s.",
			mmss(t), mmss(ed.cutLen()), playheadClock(ed.playhead, ed.hasPlay))
	}
	ed.clock.SetMarkup("<small>" + playheadClock(t, ed.hasPlay) + "</small>")
	ed.clock.SetTooltipText(tip)
}

// playheadClock is the toolbar's reading of the playhead, and the dashes are
// exactly as wide as a time so that placing the line for the first time does not
// shove the rest of the bar sideways.
func playheadClock(t float64, has bool) string {
	if !has {
		return "--:--.-"
	}
	return fmtClock(t)
}

// showMarks keeps the small line under ⟦ in and out ⟧ reading the two marks.
// Pushed like the clock above it, because two paths change a mark -- setting
// one and clearing both -- and a readout only one of them updates is right
// after ⟦ in and silently stale after ✕, which is the worse failure.
func (ed *cutEditor) showMarks() {
	if ed.marks == nil {
		return
	}
	ed.marks.SetMarkup("<small>" + marksClock(ed.markIn, ed.markOut, ed.hasIn, ed.hasOut) + "</small>")
}

// marksClock is the small print under the in/out buttons: both marks in the
// mm:ss.d spelling the clock beside them uses, and dashes while a mark is
// unset, exactly as wide as a time, so setting one never changes the line's
// width and the bar never twitches.
func marksClock(in, out float64, hasIn, hasOut bool) string {
	return playheadClock(in, hasIn) + " – " + playheadClock(out, hasOut)
}

// playheadTip is the long form of the same answer, for the hover: where the line
// falls inside the recording it is over (which is the number ffmpeg and the
// player think in, and it is not the session time the label shows), which frame
// that is, and whether the cut currently keeps it.
func (ed *cutEditor) playheadTip() string {
	if !ed.hasPlay {
		return "No playhead yet — left-click a track to place the red line"
	}
	where := "in the gap between recordings"
	if v := ed.videoAt(ed.playhead); v != nil {
		where = fmt.Sprintf("%s at %s", filepath.Base(v.path), fmtClock(v.at(ed.playhead)))
		if v.fps > 0 {
			where += fmt.Sprintf(", frame %d", int(math.Round(v.at(ed.playhead)*v.fps)))
		}
	}
	kept := "cut away"
	if ed.inCut(ed.playhead) {
		kept = "kept"
	}
	return fmt.Sprintf("The red line: %.2f s into the session — %s — %s here",
		ed.playhead, where, kept)
}

// frameStep pauses and nudges the preview by whole frames -- or, while a clip
// edge or a whole clip is held, that. ‹f on a boundary you have just picked up
// can only mean one thing, and it is not "move the playhead somewhere else".
func (ed *cutEditor) frameStep(n int) {
	// A hold that has evaporated -- undone, re-cut, the project swapped out
	// from under the page -- must not swallow the press. Each nudge says
	// whether it moved anything, and when none of them did, the button means
	// what it says on its face and the line moves.
	if ed.edgeOn && ed.nudgeEdge(n) {
		return
	}
	if ed.segOn && ed.nudgeSeg(n) {
		return
	}
	if ed.fxOn && ed.nudgeFx(n) {
		return
	}
	if ed.playVideo == nil || ed.player == nil {
		ed.a.setStatus("click a track first to place the playhead")
		return
	}
	v := ed.playVideo
	ed.cancelHold() // stepping is a hand on the line, the same as clicking it
	ed.player.Pause()
	local := math.Max(v.off, math.Min(v.off+v.dur, v.at(ed.playhead)+float64(n)/v.fps))
	ed.playhead = v.sessionAt(local)
	ed.reLive(ed.playhead) // a hand on the line: the live clock comes with it
	// the rate before the seek, never after -- it only takes hold at one, and
	// this is the seek. The same bargain setPlayhead makes.
	ed.player.SetRate(fxPreviewRateAt(ed.fx, ed.playhead))
	ed.player.SeekTo(local)
	ed.showTime()
	ed.revealPlayhead() // a step must never move the line somewhere you cannot see
	ed.showInsert()     // stepping through a card steps through the card
	ed.redrawTracks()
}

// revealOff is where the view must scroll for a playhead at timeline x: kept
// where it is while x is on screen, centered on x once it is not. Centered
// rather than nudged just inside the edge, because a line brought back to the
// very edge is one more step from leaving again.
func revealOff(x, viewX, viewW float64) (float64, bool) {
	if x >= viewX && x <= viewX+viewW {
		return viewX, false
	}
	return x - viewW/2, true
}

// revealPlayhead scrolls the timeline so the red line is on screen. Stepping
// and playback both move the line without moving the view, so either could
// walk it silently off the page -- and a transport whose subject is somewhere
// off screen is a transport you operate blind.
func (ed *cutEditor) revealPlayhead() {
	if !ed.hasPlay || ed.viewW <= 0 {
		return
	}
	if off, out := revealOff(ed.xOf(ed.playhead), ed.viewX, ed.viewW); out {
		ed.setOff(off)
	}
}

// wheelFrames is the transport on the wheel: a notch is a frame, five with
// Shift, exactly the arrow keys' spelling -- and like ‹f and f› it nudges a
// held edge, clip or effect instead of the line. One controller per widget,
// because a controller cannot be in two places.
func (ed *cutEditor) wheelFrames() *gtk.EventControllerScroll {
	sc := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical)
	sc.ConnectScroll(func(_, dy float64) bool {
		if dy == 0 {
			return false
		}
		n := 1
		if sc.CurrentEventState()&gdk.ShiftMask != 0 {
			n = 5
		}
		if dy < 0 {
			n = -n
		}
		ed.frameStep(n)
		return true
	})
	return sc
}

// playTick is how often the page reads the player's clock. Ten times a second
// is more than a red line and a clock face need and less than a moving camera
// wants -- see livePlayhead, which is how the second one is paid for without
// redrawing the timeline sixty times a second.
const playTick = 100

// liveClock is that extrapolation written as arithmetic instead of as reads of
// one page's fields, because the Narrate preview draws the same effects on the
// same 100ms position and needs the same clamp (narrate_fxview.go). Two copies
// of a smoothing rule this particular would have drifted, and the drift would
// have shown as one page's titles flickering and the other's not.
//
// Returns the clock now and the high-water mark to keep for next time; the
// caller stores the mark, which is the only state there is.
func liveClock(playhead, posT float64, posAt time.Time, liveMax, rate float64, playing bool) (now, mark float64) {
	if !playing || posAt.IsZero() {
		return playhead, playhead // nothing to smooth; re-arm on the line itself
	}
	d := time.Since(posAt).Seconds()
	if d < 0 {
		d = 0
	}
	span := float64(playTick) / 1000
	if d > span {
		d = span
	}
	if v := posT + d*rate; v > liveMax {
		liveMax = v
	}
	if hi := posT + span*rate; liveMax > hi {
		liveMax = hi // a whole tick of headroom, and not one second more
	}
	return liveMax, liveMax
}

// followPlayback keeps the red line on the player's clock while it runs;
// on pause the queries stop and the line simply stays put.
// syncPlayRate puts the preview on the clock the footage under the line runs
// on, so a slowed stretch is slow to watch and not just rose-tinted on the
// track. Called as the line moves under playback -- a rate change with no seek
// on the way to carry it, which is the one case SetRate deliberately leaves to
// its caller. SetRateNow asks the running pipeline for an instant rate change
// and falls back to a flushing seek-in-place only where that is refused
// (player.go has the story), so a ramp's stairs no longer each cost a hitch.
// syncPlayGain puts the preview at the loudness the volume effects give the
// second under the line, on top of whatever the slider says (SetFxGain). The
// preview's half of a volume effect, and the reason it is beside syncPlayRate
// rather than inside it: a gain needs no seek to take hold, so it can be set
// while the picture is stopped, which is what makes scrubbing across a boosted
// stretch sound like the boosted stretch.
func (ed *cutEditor) syncPlayGain() {
	if ed.player != nil {
		ed.player.SetFxGain(fxGainAt(ed.fx, ed.playhead))
	}
}

func (ed *cutEditor) syncPlayRate() {
	if ed.player != nil {
		ed.player.SetRateNow(fxPreviewRateAt(ed.fx, ed.playhead))
	}
}

// skipGap is what makes the cut-only preview a preview of the cut: the stretch
// between two clips is footage this page removed, the one thing the finished
// video will never contain, so the line jumps over it instead of playing
// through it. Reports whether it took the line somewhere, in which case the
// caller is done -- setPlayhead has already moved everything that follows one.
//
// Re-entry is guarded twice, the same way the Narrate page's preview guards it:
// setPlayhead's seek drops player.playing until the new position prerolls,
// which keeps the tick from arriving meanwhile, and jumped covers the case
// where the jump cannot happen at all because no recording covers that second.
func (ed *cutEditor) skipGap() bool {
	if !ed.cutOnly || ed.player == nil || len(ed.segs) == 0 {
		ed.jumped = -1
		return false
	}
	cur, next := gapAt(ed.segs, ed.playhead)
	if cur >= 0 {
		ed.jumped = -1
		return false
	}
	switch {
	case next < 0:
		// past the last clip: the finished video has ended, and stopping here
		// is what that looks like
		ed.jumped = -1
		ed.player.Pause()
		ed.a.updateRunControls()
	case next != ed.jumped:
		ed.jumped = next
		ed.setPlayhead(ed.segs[next].S)
	}
	return true
}

// cutOnlySnap puts the line on kept material before the picture starts, so a ▶
// pressed with the line standing in a gap does not open on a frame the cut
// throws away. The tick would move it a moment later anyway; this is only so
// that moment is never on screen.
func (ed *cutEditor) cutOnlySnap() {
	if !ed.cutOnly || len(ed.segs) == 0 {
		return
	}
	if cur, next := gapAt(ed.segs, ed.playhead); cur < 0 && next >= 0 {
		ed.setPlayhead(ed.segs[next].S)
	}
}

func (ed *cutEditor) followPlayback() bool {
	// ...except while a spliced card is playing, when there is no clock to
	// follow: the footage is held and the card runs on the wall clock instead
	if ed.hold.on {
		ed.tickHold()
		return true
	}
	if ed.player == nil || !ed.player.playing || ed.playVideo == nil {
		ed.syncPreviewZoom() // playback may have just stopped; the live zoom goes with it
		return true
	}
	if pos, ok := ed.player.Position(); ok {
		was := ed.playhead
		ed.playhead = ed.playVideo.sessionAt(pos)   // off included: a cut lane's window starts partway in
		ed.posT, ed.posAt = ed.playhead, time.Now() // the camera's clock; see livePlayhead
		ed.syncFxHold()                             // ▶ walks the line off whatever was picked up
		ed.showTime()
		// a card comes up as the line reaches it and goes as the line leaves,
		// and while it is up this is what advances it frame by frame -- unless
		// the line has just run into a card the footage is cut open for, which
		// stops the footage here and plays the card before either goes on
		if s := ed.splicedCrossed(was, ed.playhead); s != nil {
			ed.startHold(s)
		} else {
			ed.showInsert()
		}
		// after the card check above, so a card standing at a clip boundary
		// still plays: a held card stops this tick at the top, and the skip is
		// never reached while one is up
		if ed.skipGap() {
			return true // setPlayhead did the rest of this
		}
		// the cut has run onto another camera, and the preview follows it.
		//
		// That means loading the other file, which is a visible hiccup at every
		// change of camera, and it is the accepted price: playing a cut
		// smoothly across cameras needs a second pipeline kept warm on every
		// other row, prerolled at the right frame, swapped at the seam. Not
		// while a card is up -- the picture is the card's, and re-cueing the
		// footage underneath it would jog the sound for nothing.
		if card, _ := ed.cardNow(); card == nil || card.audioIns() {
			if v := ed.videoAt(ed.playhead); v != nil && v != ed.playVideo {
				ed.setPlayhead(ed.playhead)
				return true
			}
		}
		ed.syncPlayRate()   // the line has crossed into or out of a speed effect
		ed.syncPlayGain()   // and into or out of a volume one
		ed.revealPlayhead() // playback runs the line off the view; recenter and follow
		// the bands' pixels only change when the green bar walks onto another
		// scene; every other tick moves nothing but the line's own layer
		if ed.bandClipIdx() != ed.lineIdx {
			ed.redrawTracks()
		} else {
			ed.redrawLine()
		}
	}
	return true // keep the timer alive
}

func (ed *cutEditor) xOf(t float64) float64 {
	for _, s := range ed.spans {
		if t <= s.t1 {
			if t < s.t0 {
				return s.px // in an unfilmed stretch: the next run's edge
			}
			return s.px + (t-s.t0)*ed.pps
		}
	}
	return ed.totalW
}

func (ed *cutEditor) tAt(x float64) float64 {
	if len(ed.spans) == 0 {
		return 0 // nothing loaded: every x on an empty track is time zero
	}
	for _, s := range ed.spans {
		if x < s.px {
			return s.t0 // inside a hatched hole: clamp to the next run's start
		}
		if x <= s.px+s.dur()*ed.pps {
			return s.t0 + (x-s.px)/ed.pps
		}
	}
	return ed.spans[len(ed.spans)-1].t1
}

// tAtView is the same for an x on the widget, which is a window onto the
// timeline scrolled viewX px along it.
func (ed *cutEditor) tAtView(x float64) float64 { return ed.tAt(x + ed.viewX) }

// thumbStep is how many frames apart the thumbnails on a row stand: one
// thumbnail's WIDTH, in frames, for a row th px tall drawn at pps.
//
// The width is whatever shape this source is. Assuming 16:9 for every source
// left the row striped on a 4:3 capture, on a phone held upright, on anything
// else odd -- the step was measured for a picture wider than the one that got
// drawn, and what showed through between two frames was the band's own ground.
// A source that has not said what shape it is keeps the old assumption, which
// is right for most cameras and no worse than what it replaced.
//
// Rounded DOWN on purpose: too small a step overlaps by a hair, too large a
// step is a stripe, and only one of those can be seen.
func (v *tlVideo) thumbStep(th, pps float64) int {
	ar := 16.0 / 9
	if v.w > 0 && v.h > 0 {
		ar = float64(v.w) / float64(v.h)
	}
	return max(1, int(th*ar/(pps*v.interval)))
}

// frameRange is which of a recording's frames drawTrack has to paint for the
// px window x0..x1: a half-open range walked in strides of step.
//
// The first index is snapped DOWN to a stride, which is the point of doing this
// here rather than inline. Every frame is a candidate but only every step'th is
// drawn, so an unsnapped start would pick a different set of frames for every
// scroll position and the thumbnails would visibly reshuffle as the timeline
// moved under them.
//
// Snapped down to a stride from the ROW's own first frame, not from the file's.
// The two are the same thing for a recording and are not for a lane, which is a
// window opening partway into a file: measured from the file's frame nought the
// first stride to land inside the window can be most of a thumbnail past the
// row's left edge, so the row began with a bite of empty band -- the picture
// did not start where the row did. On a short lane, which is what ⇲ Lane makes
// of a copy, there was no such stride inside the window at all and the row came
// out with no picture in it whatsoever.
func (v *tlVideo) frameRange(pps, x0, x1 float64, step int) (first, last int) {
	// a row with no frames to walk, or none it could tell apart: an editor
	// built for a test, a source whose frames have not been extracted yet.
	// perFrame would be nought and every index below would come out as whatever
	// a division by it makes of them
	if len(v.frames) == 0 || v.interval <= 0 || step <= 0 {
		return 0, 0
	}
	perFrame := pps * v.interval // px of timeline per frame
	// counted from where the file's OWN first frame would be drawn, which is
	// where the row starts for a recording and off seconds left of it for a
	// lane showing a file from partway in (cut_lane.go)
	org := v.pxOrigin - v.off*pps
	// the row's own first frame: nought for a recording, the window's start for
	// a lane. Never before it, nor past the window's end -- a lane cut from a
	// recording borrows that recording's whole frame folder, and the frames
	// either side of its window are another row's picture.
	lo := int(v.off / v.interval)
	first = max(lo, int((x0-org)/perFrame))
	first -= (first - lo) % step
	last = min(len(v.frames), int((x1-org)/perFrame)+1)
	if v.dur > 0 {
		last = min(last, int((v.off+v.dur)/v.interval)+1)
	}
	if last <= first {
		return 0, 0 // the window is off one end of this recording
	}
	return first, last
}

// pickVideo is the recording playing at session-second t, or nil when t falls
// in no recording -- the gap between two of them, or past the end of the last.
//
// Half-open, [start, start+dur), so a second on the seam between two recordings
// belongs to the one starting there. Closing the far end would hand it to the
// one ENDING there instead, and a preview cued at second `dur` of a file is
// cued one frame past its last: the seam is the one place the answer is worth
// getting right, and the answer everyone wants there is "the next clip".
//
// A free function over a slice rather than a method, because the render walks a
// snapshot of the timeline with no editor behind it (produce.go), and the
// preview and the render have to agree about which side of a seam a second is
// on or a cut made in one plays back as the other.
func pickVideo(vids []tlVideo, t float64) *tlVideo {
	for i := range vids {
		if t >= vids[i].start && t < vids[i].start+vids[i].dur {
			return &vids[i]
		}
	}
	return nil
}

// pickVideoOn is pickVideo for one camera: the recording on row cam that was
// running at t, or nil. Which FILE a scene shows is this question -- the scene
// carries the row, and the row is several files in a line.
//
// Falls back to whatever was rolling at t when that row has nothing there,
// because a cut.json written before the rows existed says row 0 for everything
// and a session may since have been re-shot with the cameras the other way up.
// A scene that draws no picture at all would be a black hole in the render for
// a reason nobody could see on this page.
func pickVideoOn(vids []tlVideo, cam int, t float64) *tlVideo {
	if v := videoOn(vids, cam, t); v != nil {
		return v
	}
	return pickVideo(vids, t)
}

// videoOn is the same question without the fallback: the recording on row cam
// at t, and nil when that row was not rolling.
func videoOn(vids []tlVideo, cam int, t float64) *tlVideo {
	for i := range vids {
		if vids[i].lane == cam && t >= vids[i].start && t < vids[i].start+vids[i].dur {
			return &vids[i]
		}
	}
	return nil
}

// videoAt is the recording the PAGE is showing at t -- which on a session shot
// on one camera is simply the one that was rolling, and on several is the one
// the cut chose. Everything that asks a question about the frame at t goes
// through here: what the preview cues, how many frames per second to step by,
// which file a still is pulled from.
func (ed *cutEditor) videoAt(t float64) *tlVideo { return pickVideoOn(ed.vids, ed.camAt(t), t) }

// cutVideoOn is the recording the FINISHED VIDEO shows at session time t: the
// scene covering t, on the camera that scene names. Nothing else -- no fallback
// to a row somebody clicked, no fallback to a row a drag was last on. It is the
// same lookup produce makes for every clip it encodes (pickVideoOn on the
// segment's Cam), said once so the pages that preview the render can ask it.
//
// videoAt is the OTHER question, and it belongs to the Cut page: what is that
// page showing. It starts from the row a click asked to watch (watchRow) --
// which is how one camera is compared against another at the same second, and
// most of how a scene gets stolen for it -- and outside a kept scene it falls
// back to the row the hand was last on. Both are the editor looking around.
// Everything that is not the editor was inheriting them: Narrate's preview
// played whatever row the Cut page had last been asked to watch, for the whole
// session, and the frame a stop is frozen from was cut from that row too.
//
// Nil in a gap. There is no finished video between two clips, and answering
// with the footage that is there anyway is how the cut stops being respected.
func cutVideoOn(segs []cutSeg, vids []tlVideo, t float64) *tlVideo {
	for _, s := range segs {
		if t >= s.S && t < s.E {
			return pickVideoOn(vids, s.Cam, t)
		}
	}
	return nil
}

func (ed *cutEditor) cutVideoAt(t float64) *tlVideo { return cutVideoOn(ed.segs, ed.vids, t) }

// videoShown is videoAt without the charity: the recording on the watched row
// at t, and nil when that row has nothing there. The standstill preview asks
// this one (showInsert), because a paused preview borrowing another camera's
// frame looks exactly like the row HAVING that footage -- the one thing a
// glance at the preview is for. Playback keeps the fallback: a black picture
// with running sound helps nobody, and the master pipeline is the clock.
func (ed *cutEditor) videoShown(t float64) *tlVideo { return videoOn(ed.vids, ed.camAt(t), t) }

// camAt is which camera's row the preview shows at t.
//
// The row a click asked to WATCH comes first (monRow): the kept scene's
// answer below is always the scene's own camera, which left no way at all to
// see what another camera saw at the same second -- and that is most of how a
// scene gets stolen for it. Clamped rather than trusted, since rows can go
// away between the click and the asking.
//
// Then the kept scene's, when t is in one -- that is the whole of what the
// green means. In a stretch the cut throws away there is no scene to ask, and
// every row's thumbnails are on the page at once, so the answer is the row the
// hand was last on: drag a selection across the second camera and the preview
// follows the second camera, before ＋ Add has been pressed and whether or not
// it ever is.
func (ed *cutEditor) camAt(t float64) int {
	if m := ed.watchRow(); m >= 0 {
		return m
	}
	for _, s := range ed.segs {
		if !s.isInsert() && t >= s.S && t < s.E {
			return s.Cam
		}
	}
	return ed.sel.lane
}

// watchRow is the row the preview is watching, or -1 when it answers to the
// cut. The ONE place monRow is read back: clamped, because rows can go away
// between the click and the asking, and a preview, an outline and a status
// line that clamp separately are three chances to disagree about the same
// stale watch.
func (ed *cutEditor) watchRow() int {
	if ed.monRow <= 0 || len(ed.vids) == 0 {
		return -1
	}
	return min(ed.monRow-1, max(0, ed.laneN-1))
}

// monStatus says when the preview and the cut disagree on purpose: the line
// stands in a kept scene, the scene shows one camera, and the preview is
// watching another because a click asked it to. Said only then -- a click on
// the scene's own row changes nothing worth a sentence.
func (ed *cutEditor) monStatus() {
	m := ed.watchRow()
	if m < 0 {
		return
	}
	for _, s := range ed.segs {
		if !s.isInsert() && ed.playhead >= s.S && ed.playhead < s.E && s.Cam != m {
			ed.a.setStatus(fmt.Sprintf("watching camera %d — the cut shows camera %d here; "+
				"▶ plays the cut", m+1, s.Cam+1))
			return
		}
	}
}

// ---- editing ---------------------------------------------------------------

// snapEdge moves a rough edge to the best nearby cut point: strongest visual
// change or a speech gap, with a bias outward so sloppy selections keep the
// action whole instead of clipping it.
func (ed *cutEditor) snapEdge(t float64, isStart bool) float64 {
	v := ed.videoAt(t)
	if v == nil {
		return t
	}
	best, bestScore := t, 0.35 // a candidate must beat "just leave it"
	try := func(c, score float64) {
		d := math.Abs(c - t)
		if d > snapTol || c < v.start || c > v.start+v.dur {
			return
		}
		score -= 0.4 * d / snapTol
		if (isStart && c <= t) || (!isStart && c >= t) {
			score += 0.3
		}
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	for _, g := range ed.gaps[v.base] {
		try(g, 0.8)
	}
	if sc := ed.scores[v.base]; sc != nil {
		mean := 0.0
		for _, s := range sc {
			mean += s
		}
		mean /= float64(len(sc) + 1)
		i0 := int(v.at(t-snapTol) / v.interval)
		i1 := int(v.at(t+snapTol) / v.interval)
		for i := max(1, i0); i <= min(len(sc)-1, i1); i++ {
			if sc[i] > 2*mean && sc[i] >= sc[i-1] {
				try(v.sessionAt(float64(i)*v.interval), math.Min(1, sc[i]/(4*mean)))
			}
		}
	}
	return best
}

// rangePieces is what Add would keep out of the stretch t0..t1: a selection may
// span several recordings and the hole between them, so it comes apart into one
// piece per recording, with the slivers too short to be a scene dropped.
//
// It is split out from addRange because the button needs the answer before it
// commits: an empty list means Add would change nothing, and the press has to
// say so rather than push an undo step over a cut that never moved.
func (ed *cutEditor) rangePieces(t0, t1 float64) []cutSeg {
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	t0 = ed.snapEdge(t0, true)
	t1 = ed.snapEdge(t1, false)
	var out []cutSeg
	// over the runs rather than the recordings: two cameras that both saw a
	// minute are one minute of cut, not the same minute kept twice. Which of
	// them is SHOWN is the row the selection was drawn on, carried here.
	for _, sp := range ed.runs() {
		s := math.Max(t0, sp.t0)
		e := math.Min(t1, sp.t1)
		if e-s >= minSegLn {
			out = append(out, cutSeg{S: s, E: e, Cam: ed.sel.lane})
		}
	}
	return out
}

// addRange keeps t0..t1, and says whether doing so took the seconds off
// another camera -- which is a thing the hand did not ask for by name and has
// to be told about.
func (ed *cutEditor) addRange(t0, t1 float64) bool {
	pieces := ed.rangePieces(t0, t1)
	kept := ed.keptLen()
	for _, p := range pieces {
		ed.stealSpan(p.S, p.E, p.Cam) // one camera at a time
	}
	// measured in seconds and not in scenes: taking the middle out of one scene
	// leaves two, so the count can go UP while footage went away
	stole := ed.keptLen() < kept-1e-9
	ed.segs = append(ed.segs, pieces...)
	ed.coalesce()
	ed.persist()
	return stole
}

// keptLen is how many seconds of the session the scenes cover between them.
func (ed *cutEditor) keptLen() float64 {
	d := 0.0
	for _, s := range ed.segs {
		d += s.E - s.S
	}
	return d
}

// camName is how a camera is referred to in a sentence: the row's first
// recording, which is the name written on its own band. Falls back to the row
// number when the row is empty, which is a session that has just lost a camera.
func (ed *cutEditor) camName(lane int) string {
	for _, v := range ed.vids {
		if v.lane == lane {
			return v.base
		}
	}
	return fmt.Sprintf("row %d", lane+1)
}

// insDefault is how long an insert runs when nothing in the file says. A card
// nobody reads in four seconds is a card that was too wordy to be a card.
const insDefault = 4.0

// addInsert drops a file onto the timeline at t, and is where "free items on the
// session timeline" stops being free: the cut is a sequence, and a sequence has
// nowhere to put something that overlaps. So the footage under it gives way --
// the segment it lands in is split around it, exactly as Remove would, and the
// insert takes the seconds between. That is the honest reading of dropping a
// title card into the middle of a clip, and it is undoable like every other
// edit.
//
// Landing in a gap costs nothing at all: the space between two recordings is
// session time nobody filmed, so an insert there is pure gain.
func (ed *cutEditor) addInsert(path string, t, dur float64, mute bool) {
	ed.layOver(cutSeg{S: t, E: t + dur, Ins: path, Mute: mute, Cam: ed.sel.lane})
}

// addSound lays a stretch of a sound file over the footage at t: for dur
// seconds what is heard is the file from ss, and the picture is untouched --
// it keeps its frames, and the video does not get longer by a second. That is
// the sound half of "insert", and it is what a selection dragged on a
// waveform means: these seconds sound like this instead.
//
// It answers how many stretches of footage it landed on, which is not always
// one: see layOverSound.
func (ed *cutEditor) addSound(path string, t, dur, ss float64, lane string) int {
	return ed.layOverSound(cutSeg{S: t, E: t + dur, Ins: path, Ss: ss, Lane: lane})
}

// layOver is the cut taking one insert over the footage, with the undo step
// and the save both ways of arriving here owe. Two ways arrive: a file chosen
// from disk and a stretch of a lane copied out of the session. What differs
// between them is what is IN the segment, never what the cut does with it, so
// the doing is written once.
func (ed *cutEditor) layOver(s cutSeg) {
	if s.E-s.S < minSegLn {
		s.E = s.S + insDefault
	}
	ed.pushUndo()
	ed.removeSpan(s.S, s.E)
	ed.segs = append(ed.segs, s)
	ed.coalesce()
	ed.persist()
}

// under this a piece is not worth a segment of its own: the sound would be a
// blink, and the footage either side of it would have been split for nothing.
const sndMinLn = 0.05

// layOverSound lays a sound over the footage WITHOUT moving the picture.
//
// A sound insert is stored as a segment like any other, which means it takes
// its seconds from the footage segment it lands in: the clip is split and the
// insert holds the middle, while the render re-derives those very frames for
// it (produce.go, case s.audioIns()). Over footage the cut keeps, that is invisible
// -- the same picture, differently sourced -- and that is what makes it legal.
// Over footage the cut DROPS it is not: a segment there is seconds put back in
// the video, picture and all, and "lay a sound over this" is not permission to
// un-cut a scene. Displacing a card is the same trespass by another road,
// since removeSpan drops an insert whole rather than trimming it.
//
// So the span is cut to the footage the cut already keeps, and one sound goes
// over each surviving piece with its own offset into the file -- a sound drawn
// across a hole in the cut plays on either side of the hole, in step with the
// picture that survived, which is what continuing to run means here. Cards are
// stepped over rather than displaced.
//
// The splits are exact, without removeSpan's minimum-scene guard: a quarter
// second of clip left beside a sound is not a sliver of a selection nobody
// meant, it is the picture running on, and dropping it is the one thing this
// function exists to prevent.
//
// It answers how many pieces went in, so a sound that landed entirely in cut
// footage can say so instead of claiming an edit that never happened.
func (ed *cutEditor) layOverSound(s cutSeg) int {
	if s.E-s.S < minSegLn {
		s.E = s.S + insDefault
	}
	out, n := make([]cutSeg, 0, len(ed.segs)+2), 0
	for _, f := range ed.segs {
		t0, t1 := math.Max(f.S, s.S), math.Min(f.E, s.E)
		if f.isInsert() || t1-t0 < sndMinLn {
			out = append(out, f)
			continue
		}
		if t0 > f.S {
			out = append(out, cutSeg{S: f.S, E: t0})
		}
		// Ss walks with the piece: the second of the file this piece begins at
		// is as far into it as the piece is into the span asked for, so two
		// pieces either side of a hole are two parts of one sound and not the
		// same opening seconds played twice. Lane does not walk -- every piece
		// stands in for the same recording, because the lane was named once
		// for the whole span and a hole in the footage is no reason to change
		// its mind.
		out = append(out, cutSeg{S: t0, E: t1, Ins: s.Ins, Ss: s.Ss + t0 - s.S, Lane: s.Lane})
		if t1 < f.E {
			out = append(out, cutSeg{S: t1, E: f.E})
		}
		n++
	}
	if n == 0 {
		return 0
	}
	ed.pushUndo()
	ed.segs = out
	ed.coalesce()
	ed.persist()
	return n
}

// addSplice drops a file into the cut the other way: not over the footage but
// between it. The clip is cut open at t, the card runs for dur, and then the
// footage picks up where it left off -- so nothing filmed is lost, and the video
// gets longer by exactly the card.
//
// On the timeline it takes no session time at all, which is why it is stored as
// a point (S == E) with its length in Dur. Session time is the footage's ruler,
// and this card is not on it: giving it seconds there would mean claiming
// seconds of footage, which is the other mode.
func (ed *cutEditor) addSplice(path string, t, dur float64, mute bool, cam int) {
	if dur < minSegLn {
		dur = insDefault
	}
	ed.pushUndo()
	ed.segs = append(ed.segs, cutSeg{S: t, E: t, Ins: path, Dur: dur, Mute: mute, Cam: cam})
	ed.coalesce()
	ed.persist()
}

// A copied selection is spelled as an insert whose "file" is the session
// itself: copy:SECONDS, the footage second it starts playing again from, with
// its length in Dur like any spliced card. The spelling buys every behaviour a
// card already has -- the violet marker with the hatching, pick up and move,
// Edit for its mode and seconds, Undo, removal, cut.json -- and costs one case
// at render time, where the "file" is cut from its recording like any other
// stretch of footage (see produce). No file can shadow the scheme: an insert's
// path is project-relative, and none of the suffixes the chooser admits leaves
// ":" in a name's way.
const copyScheme = "copy:"

// copySrc is the footage second a copy starts at, and whether ins is one.
func copySrc(ins string) (float64, bool) {
	if !strings.HasPrefix(ins, copyScheme) {
		return 0, false
	}
	t, err := strconv.ParseFloat(ins[len(copyScheme):], 64)
	return t, err == nil && t >= 0
}

func (s cutSeg) isCopy() bool {
	_, ok := copySrc(s.Ins)
	return ok
}

// audioIns says this insert is sound alone: an audio file placed on the cut.
// Its picture is the session's own, which is why its marker is drawn in the
// audio lanes and not in the picture band.
func (s cutSeg) audioIns() bool {
	return s.isInsert() && insKind(s.Ins) == "audio"
}

// The two readings of Mute, which is one flag because it is one sentence --
// this insert brings no sound of its own -- and two behaviours because the mode
// already says what is underneath it.
//
// keepsSoundUnder: the cut was NOT opened for it, so the footage it covers is
// still there being heard. Only the picture is replaced, and the render takes
// the sound from the recording instead of from the file (produce.go, the isInsert
// case, which puts it in prodClip.snd exactly where an audio insert's file
// would go).
//
// playsSilent: the cut WAS opened for it, so there is nothing underneath to
// keep and no sound anywhere in the slot. clipInput reports the input as having
// no audio and the silence comes from anullsrc, the same way a held frame's
// does.
func (s cutSeg) keepsSoundUnder() bool { return s.isInsert() && s.Mute && !s.spliced() }
func (s cutSeg) playsSilent() bool     { return s.isInsert() && s.Mute && s.spliced() }

// insName is what an insert is called on the track and in the status line: the
// file's name, or, for a copy, the footage it plays again -- a copy has no file
// to name, and the seconds are how the eye finds the original.
func insName(s cutSeg) string {
	if from, ok := copySrc(s.Ins); ok {
		return "copy of " + mmss(from)
	}
	return insBase(s.Ins)
}

// applyInsert is the Edit dialog's answer put into the cut: what the card says,
// which of the two modes it is in, and how long it runs. One call and one undo
// step, because the three were decided together and taking back the wording
// without the length is not an edit anybody asked for.
//
// The modes differ in what happens to the FOOTAGE, which is why this is not a
// field assignment. Going over the footage takes seconds out of it, exactly as
// placing a card there does; going between it hands those seconds back, or the
// card would cost its length in footage AND add its length to the video, which
// is neither mode.
func (ed *cutEditor) applyInsert(i int, ins string, m insMode) {
	if i < 0 || i >= len(ed.segs) || !ed.segs[i].isInsert() {
		return
	}
	if m.dur < minSegLn {
		m.dur = insDefault
	}
	ed.pushUndo()
	card := ed.segs[i]
	card.Ins = ins
	// the dialog asks this one whenever it is a live question (insMode.askMute);
	// otherwise m.mute came back exactly as the card handed it over
	card.Mute = m.mute
	if m.splice {
		if !card.spliced() {
			ed.returnFootage(i)
		}
		card.E, card.Dur = card.S, m.dur
		ed.segs[i] = card
	} else {
		card.E, card.Dur = card.S+m.dur, 0
		ed.putOver(i, card)
	}
	ed.coalesce()
	ed.persist()
	ed.reholdSeg(card)
}

// setSpliced switches a card between the modes and changes nothing else.
func (ed *cutEditor) setSpliced(i int, on bool) {
	if i < 0 || i >= len(ed.segs) || ed.segs[i].spliced() == on {
		return
	}
	s := ed.segs[i]
	ed.applyInsert(i, s.Ins, insMode{splice: on, dur: s.length()})
}

// returnFootage gives the clip before card i the seconds the card was covering,
// so that when it stops covering them the footage is whole again. The clip after
// it then touches this one and coalesce makes them one clip, which is what they
// were before the card was placed.
//
// A card that took nothing -- one dropped in the hole between two recordings --
// finds no clip ending where it starts, and nothing is given back.
func (ed *cutEditor) returnFootage(i int) {
	card := ed.segs[i]
	for j := range ed.segs {
		p := &ed.segs[j]
		if p.isInsert() || math.Abs(p.E-card.S) > 0.05 {
			continue
		}
		hi := card.E
		if v := ed.videoAt(p.S); v != nil {
			hi = math.Min(hi, v.start+v.dur) // never past the end of the file
		}
		if hi > p.E {
			p.E = hi
		}
		return
	}
}

// putOver seats card i over the footage in its own seconds: whatever is under it
// gives way, the same surgery addInsert does. Written as a removal because that
// is what it is -- the card is lifted out of the list first, or removeSpan would
// drop it along with the footage, an insert being a file and not a span.
func (ed *cutEditor) putOver(i int, card cutSeg) {
	rest := make([]cutSeg, 0, len(ed.segs))
	rest = append(rest, ed.segs[:i]...)
	ed.segs = append(rest, ed.segs[i+1:]...)
	ed.removeSpan(card.S, card.E)
	ed.segs = append(ed.segs, card)
}

// indexOfSeg finds a clip in the list again by what it is: an insert is its file
// at its own start time, and nothing else in a cut is both.
func (ed *cutEditor) indexOfSeg(want cutSeg) int {
	for i, s := range ed.segs {
		if s.Ins == want.Ins && math.Abs(s.S-want.S) < 0.001 {
			return i
		}
	}
	return -1
}

// reholdSeg takes hold of a clip again after the list has been rearranged under
// it -- found by what it is, not by where it was, since coalesce sorts and
// renumbers. An edit made from the toolbar should leave the thing being edited
// still held, or the next edit needs the card picked up a second time.
func (ed *cutEditor) reholdSeg(want cutSeg) {
	if i := ed.indexOfSeg(want); i >= 0 {
		ed.segOn, ed.segSel, ed.segDirty = true, i, false
	}
	ed.syncInsertBtn()
	ed.redrawTracks()
}

// removeSpan is removeRange without the bookkeeping -- the same surgery, so
// that a caller doing several things at once pushes one undo and saves once.
func (ed *cutEditor) removeSpan(t0, t1 float64) {
	var out []cutSeg
	for _, s := range ed.segs {
		if s.E <= t0 || s.S >= t1 { // untouched
			out = append(out, s)
			continue
		}
		if s.isInsert() {
			// an insert is a file, not a span: it is dropped whole or not at all.
			// Trimming one to a fraction of its length would leave a clip nobody
			// asked for, showing part of a card.
			continue
		}
		// the halves are the same scene shortened, not new ones: they keep
		// whatever it said about itself, and what it says now is which camera
		// it shows
		if s.S < t0 && t0-s.S >= minSegLn {
			h := s
			h.E = t0
			out = append(out, h)
		}
		if s.E > t1 && s.E-t1 >= minSegLn {
			h := s
			h.S = t1
			out = append(out, h)
		}
	}
	ed.segs = out
}

// stealSpan takes t0..t1 away from the scenes on every row BUT cam.
//
// This is what makes a camera a choice. Two rows green over the same second
// would be the page claiming both pictures at once, and the render would have
// to pick one behind your back. So the newer green wins: painting camera B over
// camera A's green is how you switch camera, and it is the same gesture as
// choosing the footage was in the first place.
//
// Inserts are left where they are. An insert is not a camera -- it replaces
// whatever was under it whichever row that came from -- so a camera switch has
// nothing to say about one.
func (ed *cutEditor) stealSpan(t0, t1 float64, cam int) {
	var out []cutSeg
	for _, s := range ed.segs {
		if s.isInsert() || s.Cam == cam || s.E <= t0 || s.S >= t1 {
			out = append(out, s)
			continue
		}
		if s.S < t0 && t0-s.S >= minSegLn {
			h := s
			h.E = t0
			out = append(out, h)
		}
		if s.E > t1 && s.E-t1 >= minSegLn {
			h := s
			h.S = t1
			out = append(out, h)
		}
	}
	ed.segs = out
}

func (ed *cutEditor) removeRange(t0, t1 float64) {
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	ed.removeSpan(t0, t1)
	ed.coalesce()
	ed.persist()
}

// ---- moving a clip edge by hand ---------------------------------------------
//
// Add and Remove work in whole regions, which is right for choosing a scene and
// wrong for the last thing you do to one: a clip that starts half a second too
// early is not a region you re-select, it is an edge you nudge. The green
// borders are the handles for that, a press within a few px of one picks it up,
// and until it is put down the frame buttons move it a frame at a time instead
// of the playhead -- the same gesture, aimed at the thing you just said you were
// working on.
//
// One button is enough because the hand is told first: the border under the
// pointer is highlighted and the cursor is a resize arrow before the press
// happens (hoverEdge), so a press near a border is never a surprise trim. From
// there it is ‹f and f› for a frame at a time, or the same drag carried on for a
// sweep -- and both put the picture on the frame the edge is now at, because an
// edge is judged by what it cuts on and not by where it reads on a ruler.

// edgeAt is the clip edge nearest a point of the timeline, within edgeGrab px:
// the segment's index and which side of it. The waveform lanes answer to it as
// the picture band does -- a cut point is a time, and every band is the same
// timeline seen a different way.
func (ed *cutEditor) edgeAt(px float64) (int, bool, bool) {
	seg, end, near := -1, false, edgeGrab
	for i, s := range ed.segs {
		// A spliced card has no borders to trim: it sits at one point of the
		// footage and its length is its own, typed in the dialog. Both its edges
		// are that one x -- the middle of the marker you press to take hold of
		// the card -- so answering with an edge here would hand you a border of a
		// clip with no length instead of the card you pressed, and the border
		// wins over the clip.
		if s.spliced() {
			continue
		}
		if d := math.Abs(ed.xOf(s.S) - px); d < near {
			seg, end, near = i, false, d
		}
		if d := math.Abs(ed.xOf(s.E) - px); d < near {
			seg, end, near = i, true, d
		}
	}
	return seg, end, seg >= 0
}

// grabEdge picks up the edge under a timeline x, and says whether it found one.
func (ed *cutEditor) grabEdge(px float64) bool {
	seg, end, ok := ed.edgeAt(px)
	if !ok {
		return false
	}
	ed.edgeOn, ed.edgeSeg, ed.edgeEnd, ed.edgeDirty = true, seg, end, false
	ed.segOn, ed.fxOn = false, false // one thing is held at a time, and this is now it
	ed.syncInsertBtn()
	side := "start"
	if end {
		side = "end"
	}
	ed.a.setStatus(fmt.Sprintf("clip %d's %s picked up at %s — drag it to trim, or nudge it a "+
		"frame with ‹f and f›; a click clear of it puts it down", seg+1, side, fmtClock(ed.edgeTime())))
	ed.redrawTracks()
	return true
}

// onHeldEdge says whether a timeline x is close enough to the held edge to take
// hold of it. This is what makes a left drag mean "move this" rather than "start
// a new selection": the press has to land on the bar, not merely somewhere on a
// page that happens to have an edge held.
func (ed *cutEditor) onHeldEdge(px float64) bool {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		return false
	}
	return math.Abs(ed.xOf(ed.edgeTime())-px) <= edgeMove
}

func (ed *cutEditor) dropEdge() {
	if !ed.edgeOn {
		return
	}
	ed.edgeOn = false
	ed.redrawTracks()
}

// edgeTime is where the held edge is now, or 0 when nothing is held.
func (ed *cutEditor) edgeTime() float64 {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		return 0
	}
	if ed.edgeEnd {
		return ed.segs[ed.edgeSeg].E
	}
	return ed.segs[ed.edgeSeg].S
}

// clampEdge is how far an edge may travel: never so far that its own clip is
// shorter than minSegLn, never onto the neighbouring clip, and never out of the
// recording it was cut from (lo..hi). The cut is the input to every step after
// this one, so this arithmetic sits on its own where it can be tested rather
// than inside a mouse handler.
func clampEdge(segs []cutSeg, i int, end bool, t, lo, hi float64) float64 {
	s := segs[i]
	if end {
		if i+1 < len(segs) {
			hi = math.Min(hi, segs[i+1].S)
		}
		return math.Min(math.Max(t, s.S+minSegLn), hi)
	}
	if i > 0 {
		lo = math.Max(lo, segs[i-1].E)
	}
	return math.Max(math.Min(t, s.E-minSegLn), lo)
}

// moveEdgeTo puts the held edge at a session time, as far as it may go. live
// says the mouse is still down, and then the cut is only redrawn: writing
// cut.json (and re-reading the folder it is in, and re-gating two tabs) on
// every motion event is a lot of work for a version of the cut that exists for
// sixteen milliseconds. The drag's end writes the one that matters.
func (ed *cutEditor) moveEdgeTo(t float64, live bool) {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		ed.edgeOn = false
		return
	}
	s := &ed.segs[ed.edgeSeg]
	lo, hi := math.Inf(-1), math.Inf(1)
	if v := ed.videoAt((s.S + s.E) / 2); v != nil {
		lo, hi = v.start, v.start+v.dur
	}
	t = clampEdge(ed.segs, ed.edgeSeg, ed.edgeEnd, t, lo, hi)
	if (ed.edgeEnd && t == s.E) || (!ed.edgeEnd && t == s.S) {
		return // against a stop: not an edit, and not worth an undo step
	}
	// one undo entry for the whole hold, not one per mouse move: a drag is a
	// single act, and fifty of them would be the entire history
	if !ed.edgeDirty {
		ed.pushUndo()
		ed.edgeDirty = true
	}
	if ed.edgeEnd {
		s.E = t
	} else {
		s.S = t
	}
	if live {
		ed.updateTotal()
		ed.redrawTracks()
		return
	}
	ed.persist()
}

// edgeFPS is the frame rate of the recording the held edge falls in, which is
// what a "frame" means for it. 30 when there is nothing there to ask.
func (ed *cutEditor) edgeFPS() float64 {
	if v := ed.videoAt(ed.edgeTime()); v != nil && v.fps > 0 {
		return v.fps
	}
	return 30
}

// edgeFrame is the frame the held edge is judged by. An end edge is a frame
// short of itself: the boundary's own frame is the first one the cut does NOT
// keep, and what you are looking at is the last one it does.
func (ed *cutEditor) edgeFrame() float64 {
	t := ed.edgeTime()
	if ed.edgeEnd {
		t -= 1 / ed.edgeFPS()
	}
	return t
}

// showEdge puts the preview on that frame, so the edge is moved against the
// picture rather than against a ruler. live is a drag still in progress, and
// then it is thinned to scrubEvery -- the seeks would otherwise queue up behind
// the mouse and the picture would arrive after the drag had ended.
func (ed *cutEditor) showEdge(live bool) {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		return
	}
	if live {
		if time.Since(ed.lastScrub) < scrubEvery {
			return
		}
		ed.lastScrub = time.Now()
	}
	ed.setPlayhead(ed.edgeFrame())
}

// edgeStatus reads the whole clip out, because moving one border is how you get
// a clip of the length you wanted and the length is the thing you are watching.
func (ed *cutEditor) edgeStatus() {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		return
	}
	s := ed.segs[ed.edgeSeg]
	ed.a.setStatus(fmt.Sprintf("clip %d: %s – %s (%s)", ed.edgeSeg+1,
		fmtClock(s.S), fmtClock(s.E), ed.spanSecs(s.S, s.E)))
}

// nudgeEdge moves the held edge by whole frames and shows the frame it lands
// on. False means there was no edge to move after all (see frameStep).
func (ed *cutEditor) nudgeEdge(n int) bool {
	if !ed.edgeOn || ed.edgeSeg >= len(ed.segs) {
		ed.edgeOn = false
		return false
	}
	ed.moveEdgeTo(ed.edgeTime()+float64(n)/ed.edgeFPS(), false)
	ed.showEdge(false)
	ed.edgeStatus()
	return true
}

// ---- moving a whole clip by hand ---------------------------------------------
//
// The same gesture as an edge, aimed one level up. A press near a border picks
// that border up; a DOUBLE click anywhere else on a clip picks up the whole
// clip, and a drag then slides it with its length intact. It is a double one
// because a single press over the footage is already how the playhead is put
// somewhere. That is the edit the page had no spelling for: "this scene, but
// four seconds later" was two edge drags that had to agree with each other to the
// frame, and if they disagreed the clip changed length instead of moving.
//
// A dragged clip snaps to the clip either side of it, so "put these two
// together" is a gesture rather than an arithmetic exercise -- and it stops
// there, because clips may not overlap and a clip may not leave the recording it
// was cut from: its frames are that file's frames, and sliding it into the next
// recording would show footage nobody selected.

// spliceSpan is where the marker for a spliced card is: from x to x, in view
// px, STARTING at the point the footage is cut open at.
//
// The card owns no session time, so there is no span of the timeline that IS it
// -- but it does have a length, the seconds it plays for, and that length is a
// width at the zoom you are looking at. Drawn that way it grows and shrinks with
// the footage around it, which is the whole point of zooming: a fixed 22 px of
// violet beside a clip that doubles every time you zoom says the card is getting
// shorter, and it is not. It starts AT the point rather than being centred on it
// because the point is where the card was placed: the red line was there when
// Insert was pressed, and a marker reaching back left of the line says the card
// starts before the moment that was chosen, which it does not.
//
// Everything that hits the marker -- the press that picks the card up, the
// playhead scrubbing through it, the outline drawn round it -- goes through
// here, or the thing you can see and the thing you can press come apart.
func (ed *cutEditor) spliceSpan(s cutSeg) (float64, float64) {
	x := ed.xOf(s.S)
	return x, x + math.Max(splicePx, s.Dur*ed.pps)
}

// segSpan is where a clip is on the timeline: its own two borders, or the marker
// for a spliced card, which has no borders of its own.
func (ed *cutEditor) segSpan(s cutSeg) (float64, float64) {
	if s.spliced() {
		return ed.spliceSpan(s)
	}
	return ed.xOf(s.S), ed.xOf(s.E)
}

// segAtPx is the clip under a point of the timeline, or -1. Searched from the
// top down -- inserts are painted over the footage, so an insert dropped inside
// a kept clip is the thing you are pointing at, not the clip behind it.
func (ed *cutEditor) segAtPx(px float64) int {
	for i := len(ed.segs) - 1; i >= 0; i-- {
		x0, x1 := ed.segSpan(ed.segs[i])
		if px >= x0 && px <= x1 {
			return i
		}
	}
	return -1
}

// segOnGreen is the scene a point of the picture band is actually DRAWN on:
// segAtPx's answer about the second, and then the row, because a scene is
// drawn on its own camera's row (segTop). The two questions differ wherever
// the cut is showing one camera and the eye is on another: the green at that
// second is one row up, and the row under the pointer is plain footage.
//
// segAtPx alone is right for everything measured along the clock -- the borders,
// the band -- and wrong for anything answering "what did I press ON".
func (ed *cutEditor) segOnGreen(px, y float64) int {
	i := ed.segAtPx(px)
	if i < 0 {
		return -1
	}
	if t := ed.segTop(ed.segs[i]); y < t || y >= t+ed.laneH() {
		return -1
	}
	return i
}

// grabSeg picks up the whole clip under a timeline x.
func (ed *cutEditor) grabSeg(px float64) bool {
	i := ed.segAtPx(px)
	if i < 0 {
		return false
	}
	ed.edgeOn, ed.fxOn = false, false // one thing is held at a time, and this is now it
	ed.segOn, ed.segSel, ed.segDirty = true, i, false
	s := ed.segs[i]
	what := fmt.Sprintf("clip %d (%s – %s)", i+1, fmtClock(s.S), fmtClock(s.E))
	if s.isInsert() {
		what = fmt.Sprintf("%s at %s", insBase(s.Ins), fmtClock(s.S))
	}
	ed.a.setStatus(what + " picked up — drag it with the left button to move it, " +
		"‹f and f› nudge it a frame; a click clear of it puts it down")
	ed.syncInsertBtn()
	ed.redrawTracks()
	return true
}

func (ed *cutEditor) dropSeg() {
	if !ed.segOn {
		return
	}
	ed.segOn = false
	ed.syncInsertBtn()
	ed.redrawTracks()
}

// heldSeg is the clip being held, or nil.
func (ed *cutEditor) heldSeg() *cutSeg {
	if !ed.segOn || ed.segSel >= len(ed.segs) {
		return nil
	}
	return &ed.segs[ed.segSel]
}

// onHeldSeg is whether a left press lands on the held clip, which is what makes
// the drag a move rather than a new selection. Same rule as onHeldEdge: the
// press has to land on the thing, not merely on a page that has one held.
func (ed *cutEditor) onHeldSeg(px float64) bool {
	s := ed.heldSeg()
	if s == nil {
		return false
	}
	x0, x1 := ed.segSpan(*s)
	return px >= x0 && px <= x1
}

// clampSeg is where a clip may sit: inside the recording it was cut from, clear
// of the clips either side of it, and snapped flush to them when it comes close.
// Arithmetic on its own, away from the mouse handler, because it is the whole of
// what the gesture means and the only part of it worth testing.
//
// lo and hi bound the recording; segs must be in timeline order, and i is the
// clip being moved. snap is how many seconds count as "close enough to touch",
// which is a px tolerance at the current zoom -- a snap that is a fixed number of
// seconds would be unreachable zoomed in and unavoidable zoomed out.
func clampSeg(segs []cutSeg, i int, t, lo, hi, snap float64) float64 {
	s := segs[i]
	ln := s.E - s.S
	// the neighbours: the nearest clip before and after in time, which is what
	// "the next area" means to a hand that can see them
	before, after := math.Inf(-1), math.Inf(1)
	for j, o := range segs {
		if j == i {
			continue
		}
		if o.E <= s.S && o.E > before {
			before = o.E
		}
		if o.S >= s.E && o.S < after {
			after = o.S
		}
	}
	lo, hi = math.Max(lo, before), math.Min(hi, after)
	if math.Abs(t-lo) <= snap {
		t = lo // flush against what comes before
	} else if math.Abs(t+ln-hi) <= snap {
		t = hi - ln // ...or against what comes after
	}
	if t+ln > hi {
		t = hi - ln
	}
	if t < lo {
		t = lo
	}
	return t
}

// moveSegTo slides the held clip so that it starts at t. live is a drag still in
// progress, and then the cut is only redrawn -- the same rule moveEdgeTo works
// by, and for the same reason: cut.json is written once, when the hand stops.
func (ed *cutEditor) moveSegTo(t float64, live bool) {
	s := ed.heldSeg()
	if s == nil {
		ed.segOn = false
		return
	}
	lo, hi := math.Inf(-1), math.Inf(1)
	// footage may not leave its own recording; an insert is a file and belongs
	// to none, so it may go anywhere the clips around it leave room
	if !s.isInsert() {
		if v := ed.videoAt((s.S + s.E) / 2); v != nil {
			lo, hi = v.start, v.start+v.dur
		}
	}
	t = clampSeg(ed.segs, ed.segSel, t, lo, hi, snapPx/math.Max(ed.pps, 0.001))
	if t == s.S {
		return // against a stop: not an edit, and not worth an undo step
	}
	if !ed.segDirty {
		ed.pushUndo() // one entry for the whole drag, as with an edge
		ed.segDirty = true
	}
	s.E, s.S = t+(s.E-s.S), t
	if live {
		ed.updateTotal()
		ed.redrawTracks()
		return
	}
	ed.persist()
}

// showSeg puts the preview on the held clip's first frame, so a clip is moved
// against the picture it will start on. Throttled while dragging, exactly as
// showEdge is, and for the same reason.
func (ed *cutEditor) showSeg(live bool) {
	s := ed.heldSeg()
	if s == nil {
		return
	}
	if live {
		if time.Since(ed.lastScrub) < scrubEvery {
			return
		}
		ed.lastScrub = time.Now()
	}
	ed.setPlayhead(s.S)
}

func (ed *cutEditor) segStatus() {
	s := ed.heldSeg()
	if s == nil {
		return
	}
	ed.a.setStatus(fmt.Sprintf("clip %d: %s – %s (%s)", ed.segSel+1,
		fmtClock(s.S), fmtClock(s.E), ed.spanSecs(s.S, s.E)))
}

// nudgeSeg moves the held clip by whole frames, the same way ‹f and f› move a
// held edge.
func (ed *cutEditor) nudgeSeg(n int) bool {
	s := ed.heldSeg()
	if s == nil {
		ed.segOn = false
		return false
	}
	fps := 30.0
	if v := ed.videoAt(s.S); v != nil && v.fps > 0 {
		fps = v.fps
	}
	ed.moveSegTo(s.S+float64(n)/fps, false)
	ed.showSeg(false)
	ed.segStatus()
	return true
}

// what a press took hold of, which is what the gesture that made it has to do
// next: trim, move, or start a selection.
const (
	pickNone = iota
	pickEdge
	pickSeg
)

// pickAt is the whole of what a press at a point of the timeline means. It is
// here rather than in the gesture because the order of the four questions it
// asks is the gesture's entire meaning, and a mouse handler is a bad place to
// keep something that has to be reasoned about.
//
// A border wins over the clip it belongs to, and it has to: a clip is the whole
// green area and its borders are a few px inside the ends of it, so a border
// asked about second is a border that can never be picked up -- the clips of a
// cut sit edge to edge, so there is no press anywhere on the timeline that is
// near a border and not on a clip.
//
// The held edge is asked about before any other, and with a wider tolerance
// (edgeMove): by then you are aiming at something you can see, and the bar you
// are aiming at may sit a few px from the border of the next clip along.
//
// What must not happen, and did, is the picture moving because of it. Pressing
// the same area twice looks like the same click twice, and the second one
// landing a few px nearer the border used to swap the clip for its end edge AND
// cue the preview, so the red line jumped to the end of the area for no reason
// the hand could see. So: picking something up never moves the red line, edge or
// clip. The line follows what MOVES -- a drag or ‹f/f› on either of them puts
// the picture on the frame it landed on -- and choosing a thing is not moving it.
//
// clips says whether a whole clip may be taken here. A single left press may
// not take one: over the cut that press is how you put the red line somewhere,
// and a gesture that both navigates and picks things up is a gesture you cannot
// use for either. Borders are different -- they are a few px wide, they are
// highlighted under the pointer before you commit, and taking one is the thing
// you are there to do. So the left press asks with clips false, and the double
// click (see pickTrack) asks with it true.
func (ed *cutEditor) pickAt(px float64, clips bool) int {
	switch {
	case ed.onHeldEdge(px):
		ed.edgeStatus() // already yours, and the bar is what you aimed at
		return pickEdge
	case ed.grabEdge(px):
		return pickEdge
	case !clips:
		return pickNone
	case ed.onHeldSeg(px):
		ed.segStatus() // it is already yours; here is what you are holding
		return pickSeg
	case ed.grabSeg(px):
		return pickSeg
	}
	ed.dropEdge()
	ed.dropSeg()
	return pickNone
}

// redrawTracks repaints every band of the timeline. One call rather than a
// QueueDraw per area at each of a dozen call sites: the bands are one picture of
// one cut, and they have not stayed the same set -- the lanes arrived long after
// the footage, and the second picture band left again.
func (ed *cutEditor) redrawTracks() {
	if ed.srcArea == nil {
		return
	}
	ed.fitSrc() // the effects lane is as deep as the effects pile up
	ed.srcArea.QueueDraw()
	if ed.audArea != nil {
		ed.audArea.QueueDraw()
	}
	// the line layer scrolls and zooms with the bands, and the green bar the
	// bands were painted with is remembered so the playback tick can tell a
	// line that moved from a line that moved ONTO another scene (redrawLine)
	ed.lineIdx = ed.bandClipIdx()
	if ed.lineArea != nil {
		ed.lineArea.QueueDraw()
	}
	// the framing overlay is a view of the same state -- where the camera is
	// at the playhead, what is held -- and its pointer-grabbing follows the
	// same state, so both are settled here rather than at every call site
	if ed.fxArea != nil {
		ed.fxArea.QueueDraw()
		ed.syncFxCursor()
		ed.syncPreviewZoom()
	}
}

// cutState is everything Undo and Revert put back: the segments, the effects,
// the aspect, and the corrections made to the timeline itself. One snapshot,
// not four -- an edit that changed the camera and an edit that changed the cut
// undo the same way, and an undo that restored the segments but left the
// effects would un-mix two lists the user edited as one page.
type cutState struct {
	segs   []cutSeg
	fx     []cutFx
	aspect string
	// where the sources were, and which rows they were on (cut_shift.go). A
	// right drag is an edit like any other and has to come back the same way,
	// and the rows come with it because they were frozen BY a drag: an undo
	// that put the seconds back and left the rows frozen would leave the
	// project pinned to a shape it no longer has.
	shift map[string]float64
	rows  map[string]int
	// and the rows the cut itself put on the band. Adding one is an edit, so
	// taking it back is an undo like any other (cut_lane.go).
	lanes []cutLane
	// and the floor under the row count, or ✕ on an emptied bottom row would
	// be an edit Undo cannot take back (cutEditor.nRows)
	nRows int
}

func (ed *cutEditor) snapshot() cutState {
	return cutState{append([]cutSeg(nil), ed.segs...), append([]cutFx(nil), ed.fx...), ed.aspect,
		copyShift(ed.shift), copyRows(ed.rows), append([]cutLane(nil), ed.cutLanes...), ed.nRows}
}

func (ed *cutEditor) restore(st cutState) {
	ed.segs = st.segs
	ed.fx = st.fx
	ed.dropFx() // whatever was held may not exist in the restored list
	// the sources are moved back before anything is measured against them,
	// and only when they actually moved: relayout is not free, and every
	// ordinary undo would otherwise pay for a feature it did not use
	if !sameShift(ed.shift, st.shift) || !sameRows(ed.rows, st.rows) ||
		!sameCutLanes(ed.cutLanes, st.lanes) || ed.nRows != st.nRows {
		for b := range ed.shift {
			if _, ok := st.shift[b]; !ok {
				ed.slideSrc(b, -ed.shift[b])
			}
		}
		for b, d := range st.shift {
			ed.slideSrc(b, d-ed.shift[b])
		}
		// a copy in hand is a row NUMBER and a session second, and it is the
		// one thing here the snapshot does not hold: taken while the band had
		// one shape and pasted after it was put back into another, it would
		// splice footage off a camera nobody chose. Undo drops the selection
		// and the marks for the same reason -- what the hand was holding was
		// held against a cut that is no longer the cut.
		if !sameRows(ed.rows, st.rows) {
			ed.copyOn = false
			ed.syncInsertBtn()
		}
		ed.shift, ed.rows = copyShift(st.shift), copyRows(st.rows)
		ed.nRows = st.nRows
		// after the corrections, because the cut's own rows are rebuilt from
		// what cut.json says rather than moved, and where they land is the
		// correction the shift map has just been put back to
		ed.setLanes(st.lanes)
		sortLanes(ed.auds)
		ed.relayout()
	}
	ed.setAspect(st.aspect)
}

// pushUndo snapshots the cut before an edit. Every path that changes segs, fx
// or the aspect goes through here first, so Add, Remove, Suggest and every
// effect edit are all reversible -- pressing Add is a try, not a commitment.
func (ed *cutEditor) pushUndo() {
	ed.undo = append(ed.undo, ed.snapshot())
	if len(ed.undo) > undoDeep {
		ed.undo = ed.undo[len(ed.undo)-undoDeep:]
	}
	// a fresh edit forks history: what Undo took back no longer leads to this
	// cut, and a Redo that grafted it on anyway would interleave two histories
	ed.redo = nil
	ed.syncButtons()
}

func (ed *cutEditor) undoLast() {
	if len(ed.undo) == 0 {
		ed.a.setStatus("nothing to undo")
		return
	}
	ed.redo = append(ed.redo, ed.snapshot())
	ed.restore(ed.undo[len(ed.undo)-1])
	ed.undo = ed.undo[:len(ed.undo)-1]
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	ed.syncButtons()
	ed.a.setStatus(fmt.Sprintf("undone — %d segment(s) left", len(ed.segs)))
}

// redoLast is undoLast run backwards: the cut Undo stepped away from goes back
// on, and the step itself goes back on the undo pile -- appended raw, because
// pushUndo would clear the very stack this is walking.
func (ed *cutEditor) redoLast() {
	if len(ed.redo) == 0 {
		ed.a.setStatus("nothing to redo")
		return
	}
	ed.undo = append(ed.undo, ed.snapshot())
	ed.restore(ed.redo[len(ed.redo)-1])
	ed.redo = ed.redo[:len(ed.redo)-1]
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	ed.syncButtons()
	ed.a.setStatus(fmt.Sprintf("redone — %d segment(s)", len(ed.segs)))
}

// setBase marks the current cut as what Revert returns to: whatever was on disk
// when the page loaded, or whatever Suggest produced. Everything after that is
// the user's own delta.
func (ed *cutEditor) setBase() {
	ed.base = ed.snapshot()
	ed.syncButtons()
}

func sameCut(a, b []cutSeg) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameSeg(a[i], b[i]) {
			return false
		}
	}
	return true
}

// sameSeg is what == used to be, before a scene could carry a set of silenced
// lanes and stopped being a comparable struct. Everything scalar still compares
// scalar; only Quiet needs its own reading.
// Every field is named here by hand because Go will not compare a struct that
// holds a slice, so == is gone. That is a standing hazard -- a field added
// later and forgotten here would make Revert stop noticing it -- which is why
// TestEverySegmentFieldCountsAsAChange walks the type by reflection and fails
// on any field this function does not read.
func sameSeg(a, b cutSeg) bool {
	return a.S == b.S && a.E == b.E && a.Ins == b.Ins && a.Dur == b.Dur &&
		a.Rate == b.Rate && a.Ss == b.Ss && a.Mute == b.Mute &&
		a.Cam == b.Cam && a.Lane == b.Lane && a.Split == b.Split &&
		sameQuiet(a.Quiet, b.Quiet)
}

// sameQuiet compares the two as SETS, not as lists. Which lane the user
// silenced first is not part of what the cut sounds like, and Revert lighting
// up because two toggles were pressed in the other order would be a lie about
// there being an unsaved change.
func sameQuiet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, q := range a {
		if !laneQuiet(b, q) {
			return false
		}
	}
	return true
}

func sameState(a, b cutState) bool {
	if !sameCut(a.segs, b.segs) || a.aspect != b.aspect || len(a.fx) != len(b.fx) {
		return false
	}
	if !sameShift(a.shift, b.shift) || !sameCutLanes(a.lanes, b.lanes) {
		return false // a lane dragged into place, or put there, is worth Reverting
	}
	for i := range a.fx {
		if a.fx[i] != b.fx[i] {
			return false
		}
	}
	return true
}

func (ed *cutEditor) syncButtons() {
	if ed.undoBtn != nil {
		ed.undoBtn.SetSensitive(len(ed.undo) > 0)
	}
	if ed.redoBtn != nil {
		ed.redoBtn.SetSensitive(len(ed.redo) > 0)
	}
	if ed.revertBtn != nil {
		ed.revertBtn.SetSensitive(!sameState(ed.snapshot(), ed.base))
	}
	ed.syncInsertBtn()
	ed.syncPlayBtn()
}

// syncPlayBtn greys ▶✂ out when there is nothing it could honestly play: its
// preview is the finished video, an empty cut IS an empty video, and with no
// clips there are no gaps to skip either -- it would run the whole recording
// against its face's one promise. Plain ▶ never greys: the recording is always
// there to play. Synced from setCutOnly and from syncButtons, which every edit
// passes through, so adding the first clip wakes the button up again.
func (ed *cutEditor) syncPlayBtn() {
	if ed.cutPlayBtn == nil {
		return
	}
	ed.cutPlayBtn.SetSensitive(len(ed.segs) > 0)
}

// syncInsertBtn tells the Insert button which of its two jobs it is doing. Held
// card: it opens that card, so it says Edit. Anything else: it puts a new one in
// at the playhead.
func (ed *cutEditor) syncInsertBtn() {
	if ed == nil || ed.insBtn == nil {
		return
	}
	if ed.laneBtn != nil {
		// footage only: a copied SOUND has no picture to put on a row, and the
		// lane it stands in for is already chosen (cutSeg.Lane)
		ed.laneBtn.SetVisible(ed.copyOn && ed.copyAud == "")
		if ed.copyOn {
			ed.laneBtn.SetTooltipText(fmt.Sprintf("put the copied footage (%s – %s, %.1f s) "+
				"on a row of its own starting at the red line, instead of splicing it into "+
				"the cut. Nothing is cut to it yet: select on the new row and press ＋ Add, "+
				"the same way you would cut to a second camera",
				mmss(ed.copyFrom), mmss(ed.copyFrom+ed.copyLen), ed.copyLen))
		}
	}
	if s := ed.heldSeg(); s != nil && s.isInsert() {
		ed.insBtn.SetLabel("✎ Edit")
		ed.insBtn.SetTooltipText("change the held card — what it says, and whether it plays " +
			"over the footage (overwrite) or between it (insert)")
		return
	}
	if f := ed.heldFx(); f != nil {
		ed.insBtn.SetLabel("✎ Edit")
		ed.insBtn.SetTooltipText("change the held effect — " + f.fxLabel())
		return
	}
	if ed.copyOn {
		ed.insBtn.SetLabel("⧉ Paste")
		if ed.copyAud != "" {
			ed.insBtn.SetTooltipText(fmt.Sprintf("lay the copied sound (%.1f s of %s, %s – %s) "+
				"over the footage at the red line: the picture runs on and the video stays "+
				"exactly as long. Esc drops the copy",
				ed.copyLen, ed.copyAud, mmss(ed.copyFrom), mmss(ed.copyFrom+ed.copyLen)))
			return
		}
		ed.insBtn.SetTooltipText(fmt.Sprintf("splice the copied footage (%s – %s, %.1f s) into "+
			"the cut at the red line: the cut is opened there, those seconds play again, and "+
			"the video gets longer by them. Esc drops the copy",
			mmss(ed.copyFrom), mmss(ed.copyFrom+ed.copyLen), ed.copyLen))
		return
	}
	ed.insBtn.SetLabel("⧉ Insert")
	ed.insBtn.SetTooltipText("put a file in the cut at the playhead — a video sting, a still, " +
		"or an SVG that animates itself. A selected region gives it its length; " +
		"otherwise the file's own. With the selection drawn in a lane's own wave " +
		"it offers sounds instead, and lays one over those seconds without moving the " +
		"picture. A card (tier.svg, s.svg … in assets) or any SVG " +
		"with {{holes}} in it asks what to put on it first. Right-click a card on " +
		"the track to hold it, and this becomes Edit.")
}

// droppedSpans is the session time this cut throws away, as stretches: the
// holes between the kept clips, plus whatever hangs off either end of each
// recording. Only the cut preview's scrim uses it -- everywhere else "dropped" is
// simply the absence of green -- so it is built on demand rather than kept.
//
// Inserts are skipped rather than counted as keeping their span: a spliced card
// occupies no session time (S == E) and an overwriting one sits inside footage
// that is kept anyway, so neither one opens or closes a hole.
func (ed *cutEditor) droppedSpans() [][2]float64 {
	var out [][2]float64
	for _, sp := range ed.runs() {
		t := sp.t0
		for _, s := range ed.segs {
			if s.isInsert() || s.E <= t || s.S >= sp.t1 {
				continue
			}
			if s.S > t {
				out = append(out, [2]float64{t, math.Min(s.S, sp.t1)})
			}
			t = math.Max(t, s.E)
		}
		if t < sp.t1 {
			out = append(out, [2]float64{t, sp.t1})
		}
	}
	return out
}

// segAt returns the index of the kept scene covering t, or -1.
func (ed *cutEditor) segAt(t float64) int {
	for i, s := range ed.segs {
		if t >= s.S && t < s.E {
			return i
		}
	}
	return -1
}

func (ed *cutEditor) coalesce() {
	// this is where the segment list is rearranged wholesale -- sorted, merged,
	// renumbered -- so a held edge or a held clip, which are indexes into it, have
	// to let go
	ed.edgeOn, ed.segOn = false, false
	sort.Slice(ed.segs, func(i, j int) bool { return ed.segs[i].S < ed.segs[j].S })
	var out []cutSeg
	film := -1 // where the last stretch of footage went, which is what merges
	for _, s := range ed.segs {
		// An insert merges with nothing, in either direction. Merging is for two
		// selections of the same footage that turned out to touch; an insert is a
		// file, and swallowing one into the clip beside it -- or growing one over
		// the footage that follows -- would lose the file and keep the seconds.
		//
		// It is the last FOOTAGE that is merged into, not the last item: a
		// spliced card sorts between two clips without taking any session time
		// from them, so they still touch and are still one stretch of the
		// recording -- with a mark inside it saying where it is cut open.
		// ...and two scenes of DIFFERENT cameras never merge, however exactly
		// they touch. The seam between them is the cut from one camera to the
		// other -- the whole point of the second row -- and merging them would
		// throw the switch away and keep the seconds.
		// ...and a border | Split made is not one of those accidents. It was
		// drawn to give this stretch a life of its own, and merging it away
		// on the next edit anywhere in the cut would undo a press nobody
		// repeated. A drag that puts the two back together clears the flag
		// itself, which is the way back (mergeDropped).
		if !s.isInsert() && !s.Split && film >= 0 && s.Cam == out[film].Cam &&
			s.S <= out[film].E+mergeTol && allSpliced(out[film+1:]) {
			if s.E > out[film].E {
				out[film].E = s.E
			}
			continue
		}
		if !s.isInsert() {
			film = len(out)
		}
		out = append(out, s)
	}
	ed.segs = out
}

// allSpliced is whether everything here takes no session time -- which is to
// say, whether two clips either side of it are still next to each other.
func allSpliced(segs []cutSeg) bool {
	for _, s := range segs {
		if !s.spliced() {
			return false
		}
	}
	return true
}

// inserts are the cut's non-footage items, kept aside while the footage is
// replaced wholesale -- which is what a suggestion and an audit both do. They
// were placed by hand and no model was told they exist, so a run that came back
// without them has not decided against them, it never saw them.
func insertsOf(segs []cutSeg) []cutSeg {
	var out []cutSeg
	for _, s := range segs {
		if s.isInsert() {
			out = append(out, s)
		}
	}
	return out
}

// splitSpliced is the cut as a sequence of clips to render: every spliced
// insert cuts the footage it sits in, and the two halves come out either side of
// it. Nothing else changes, and a cut with no spliced insert in it comes back as
// it went in.
//
// This is deliberately not what the timeline stores. On the timeline the footage
// is one clip with a splice point marked inside it, which is what it looks like
// and what it edits like -- dragging the clip's end still means the end of the
// clip. The halves exist only where a sequence is required, which is every step
// after this one, and they all read the cut through produceSegs.
func splitSpliced(segs []cutSeg) []cutSeg {
	ordered := append([]cutSeg(nil), segs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].S != ordered[j].S {
			return ordered[i].S < ordered[j].S
		}
		// a splice at the very start of a clip goes before it, not after: the
		// card is what you meant to see first
		return ordered[i].spliced() && !ordered[j].spliced()
	})
	var out []cutSeg
	for _, s := range ordered {
		n := len(out)
		if !s.spliced() || n == 0 {
			out = append(out, s)
			continue
		}
		// the clip it lands in is the one before it, since the list is in
		// order -- and it only cuts anything if it lands strictly inside
		prev := out[n-1]
		if prev.isInsert() || s.S <= prev.S || s.S >= prev.E {
			out = append(out, s)
			continue
		}
		tail := prev
		out[n-1].E, tail.S = s.S, s.S
		out = append(out, s, tail)
	}
	return out
}

func filmedOf(segs []cutSeg) []cutSeg {
	var out []cutSeg
	for _, s := range segs {
		if !s.isInsert() {
			out = append(out, s)
		}
	}
	return out
}

func (ed *cutEditor) persist() {
	// keyed, because cutFile.Sound is read on load and never written: an old
	// project's whole-cut choice is migrated into the scenes once (migrateSound)
	// and the field goes out of the file on the very next save
	b, _ := json.MarshalIndent(cutFile{Segs: ed.segs, Aspect: ed.aspect, Fx: ed.fx,
		Shift: ed.shift, Rows: ed.rows, Lanes: ed.cutLanes, NRows: ed.nRows}, "", "  ")
	os.MkdirAll(filepath.Dir(ed.a.cutPath()), 0o755)
	if err := os.WriteFile(ed.a.cutPath(), append(b, '\n'), 0o644); err != nil {
		ed.a.logf("save cut: %v", err)
	}
	ed.updateTotal()
	ed.updateOut() // the file on disk just changed size
	// Narrate and Produce are gated on cut.json existing, and this is the only
	// place it comes into existence. Without this the tabs stayed grey after a
	// perfectly good cut and woke up only on a rescan or a restart, which looks
	// exactly like the cut not having worked.
	ed.a.updateGates()
	ed.syncButtons() // every edit changes whether there is a delta to revert
	// an edit can put a card under the playhead or take one away -- dropping an
	// insert, removing it, undoing either -- and the preview says which of those
	// happened before the line is moved again
	ed.showInsert()
	ed.redrawTracks()
}

// cutLen is how long the finished video is: every clip's own length, which for
// a spliced card is the card, for slowed footage the stretch it plays as, and
// for everything else the footage under it. Not the span of the timeline --
// the timeline is the session's clock, it is as long as the recording is, and
// no edit on this page makes it longer or shorter.
func (ed *cutEditor) cutLen() float64 {
	sum := 0.0
	for _, s := range ed.fxSegs() {
		sum += s.length() // a spliced card takes no session time and still runs
	}
	return sum
}

// fxSegs is the cut as the render will run it: the spliced cards cut out into
// their own clips, and every clip carrying the rate the effects over it come to
// (splitSpliced + applyFx, which is produceSegs' own pipe).
//
// Every number this page prints about TIME goes through here, because the
// question behind all of them is the same one -- how long is the video, and
// how far into it is this -- and a clip's session seconds stopped being that
// answer the moment speed effects existed. A ×2 over half a scene is half a
// scene the finished video does not contain, and a total that reads the
// segments straight says it does.
func (ed *cutEditor) fxSegs() []cutSeg { return applyFx(splitSpliced(ed.segs), ed.fx) }

// cutPos is where session time t falls on the finished video's clock, effects
// and all. The reading the ▶✂ preview is asked for.
func (ed *cutEditor) cutPos(t float64) float64 { return cutPos(ed.fxSegs(), t) }

// runLen is how long the session seconds t0..t1 run in the finished video --
// the same arithmetic as above for a stretch that is not a whole clip, which
// is what a selection and a clip in hand both are.
//
// A stop is nought in the spans and runs at ×1 here, exactly as the render
// treats it: the picture stands still and the footage under it plays on, so a
// stop costs the video no time (rateStep.applied, cut_fxstill.go). It is the
// speed-ups and slow-downs that move this number.
func (ed *cutEditor) runLen(t0, t1 float64) float64 {
	if t1 <= t0 {
		return 0
	}
	out, at := 0.0, t0
	for _, st := range rateSpans(ed.fx) {
		lo, hi := math.Max(st.t0, t0), math.Min(st.t1, t1)
		if hi <= lo {
			continue
		}
		out += math.Max(0, lo-at) // seconds no effect covers run at ×1
		out += (hi - lo) / st.applied()
		at = hi
	}
	return out + math.Max(0, t1-at)
}

// spanSecs is how a stretch's length is written in a status line: its own
// seconds, and what it comes to in the video when an effect makes those two
// different. Both, because both are being asked about -- the first is the
// footage you are pointing at, the second is what it costs the video -- and a
// cut with no effects in it reads exactly as it always did.
func (ed *cutEditor) spanSecs(t0, t1 float64) string {
	raw, run := t1-t0, ed.runLen(t0, t1)
	if math.Abs(raw-run) < 0.05 {
		return fmt.Sprintf("%.1f s", raw)
	}
	return fmt.Sprintf("%.1f s, %.1f s in the video", raw, run)
}

func (ed *cutEditor) updateTotal() {
	if ed.total == nil {
		return
	}
	sum, src := ed.cutLen(), 0.0
	for _, v := range ed.vids {
		src += v.dur
	}
	line := fmt.Sprintf("cut %s  ·  source %s  ·  %d segment(s)",
		mmss(sum), mmss(src), len(ed.segs))
	// counted separately because they are not segments of the session: two of
	// the "segments" being cards is the difference between a five-minute cut of
	// footage and a five-minute cut with a minute of graphics in it
	if n := len(insertsOf(ed.segs)); n > 0 {
		line += fmt.Sprintf(", %d inserted", n)
	}
	ed.total.SetMarkup("<small>" + line + "</small>")
}

// updateInputs says what this page is working from: the recordings on the
// tracks, and the session timeline that Suggest is sent.
//
// The timeline is the part worth spelling out. It is not "the videos" -- it is
// every line anyone said, cleaned, merged with the event log of every
// recording, and the whole of it goes into the request. That is a thing you
// would otherwise have to read the code to know, and it is the difference
// between a suggestion that can hear a joke and one that can only see.
func (ed *cutEditor) updateInputs() {
	if ed == nil || ed.inputs == nil {
		return
	}
	src := 0.0
	var names []string
	for _, v := range ed.vids {
		src += v.dur
		names = append(names, fmt.Sprintf("%s  %s", mmss(v.dur), v.base))
	}
	line := fmt.Sprintf("%d recording(s) · %s of footage", len(ed.vids), mmss(src))
	detail := strings.Join(names, "\n")
	if len(names) == 0 {
		line, detail = "nothing to cut — no source on Inputs is marked as footage", ""
	}
	// the separate recordings are not footage and are not part of the timeline's
	// geometry, but they are on the page, and a lane that starts in the middle
	// of the tracks and stops before the end is only explicable if this row says
	// why: each is placed by its own wall clock, and only the stretch that
	// overlaps the footage is drawn.
	if len(ed.auds) > 0 {
		sep := 0
		for _, au := range ed.auds {
			if !au.master {
				sep++
			}
		}
		line += fmt.Sprintf(" · %d separate recording(s) on %d lane(s)", sep, ed.audioLanes())
		detail += "\n\nEvery sound in the session, placed by its own clock — only the part running while the footage ran is drawn, and all of it is what the preview plays:"
		for _, au := range ed.auds {
			kind := "mono"
			if au.chans >= 2 {
				// a stereo file with one signal in it is said as such: it is why
				// it is drawn on one lane, and the row is where that is explained
				kind = "L/R"
				if ed.lanes(au) < 2 {
					kind = "L=R"
				}
			}
			what := fmt.Sprintf("from %s into the session", mmss(au.start))
			if au.master {
				what = "the footage's own track"
			}
			detail += fmt.Sprintf("\n%s  %s  %s, %s", mmss(au.dur), au.base, kind, what)
		}
	}

	rows := loadTSVRows(filepath.Join(ed.a.transcriptDir(), "session.tsv"))
	switch {
	case len(rows) == 0:
		line += " · no session timeline — run Describe"
	default:
		speech, events := 0, 0
		for _, r := range rows {
			if r.spk == "EVENT" {
				events++
			} else {
				speech++
			}
		}
		line += fmt.Sprintf(" · timeline %d lines (%d spoken, %d on screen) → all of it goes to Suggest",
			speech+events, speech, events)
		// the same string the request will carry, so the size is the real one
		detail += fmt.Sprintf("\n\nunderstand/transcript/session.txt — %d kB, sent whole with the cut prompt",
			(len(sessionText(rows, ed.a.narratorMic()))+512)/1024)
	}
	// the context box on Describe rides along with every request this page
	// makes, so this row -- which is the list of what Suggest is sent -- is
	// where it has to appear. Silent extra input is how a cut ends up obeying
	// something the user forgot they wrote.
	if c := ed.a.sessionCtx(); c != "" {
		line += " · session context"
		detail += "\n\nSession context (Describe), sent with Suggest and the audit:\n" + c
	}
	ed.inputs.SetText(line)
	ed.inputs.SetTooltipText(strings.TrimSpace(detail))
}

// updateOut says what is on disk, which is not what ed.total says: the total is
// the cut in the editor, and until it is persisted the two differ.
func (ed *cutEditor) updateOut() {
	if ed == nil || ed.out == nil {
		return
	}
	ed.out.SetText(summarizeOutputs(ed.a.cutDir()))
}

// ---- drawing ---------------------------------------------------------------

// plateText draws a label on its own dark ground, at the given baseline.
//
// The video name sits ON the thumbnails, and no single ink is readable there:
// white vanishes into a bright frame, black into a dark one. Inverting what is
// underneath (cairo's DIFFERENCE operator) sounds like the answer and is not --
// mid-gray inverts to mid-gray, and a gameplay frame is mostly mid-gray. The
// plate is what subtitles do, and it works on every frame.
// hatchBand paints the band that means "the footage stops here": a dark ground
// with yellow diagonals over it. Two things on the timeline say that -- the hole
// between two recordings, and the point where a spliced insert cuts a clip open
// -- and they say it with one picture, because to the footage they are the same
// event.
//
// The diagonals are dashed, at a fifth of the band's height. Drawn whole they
// were long unbroken ramps that read as a texture painted over the track rather
// than as marks on it; short strokes read as hatching, which is what it is meant
// to be. Everything is clipped to the band: the diagonals begin left of it so
// that the leftmost pixels are hatched too, and without the clip that overhang
// lands on the thumbnail beside it.
func hatchBand(cr *cairo.Context, x, w, top, h float64) {
	cr.SetSourceRGB(0.22, 0.2, 0.16)
	cr.Rectangle(x, top, w, h)
	cr.Fill()
	hatchStrokes(cr, x, w, top, h)
}

// hatchStrokes is the marks without the ground, for a band that is already
// painted something -- the splice marker is violet first, because it says two
// things at once, and hatching drawn under that violet would be tinted by it
// until it was no longer the same marks.
func hatchStrokes(cr *cairo.Context, x, w, top, h float64) {
	cr.Save()
	defer cr.Restore()
	cr.Rectangle(x, top, w, h)
	cr.Clip()
	cr.SetSourceRGB(0.45, 0.4, 0.3)
	cr.SetLineWidth(1)
	// the stroke runs corner to corner, so its length is the diagonal; a fifth
	// of that, with as much again of gap, is the dash
	cr.SetDash([]float64{h * math.Sqrt2 / 5, h * math.Sqrt2 / 5}, 0)
	for dx := x - h; dx < x+w; dx += 6 {
		cr.MoveTo(dx, top+h)
		cr.LineTo(dx+h, top)
		cr.Stroke()
	}
}

func plateText(cr *cairo.Context, x, y float64, s string) {
	e := cr.TextExtents(s)
	cr.SetSourceRGBA(0, 0, 0, 0.66)
	cr.Rectangle(x-3, y-11, e.Width+6, 14)
	cr.Fill()
	cr.SetSourceRGB(1, 1, 1)
	cr.MoveTo(x, y)
	cr.ShowText(s)
}

// drawTrack paints the track: the footage as it was shot, with what the cut
// keeps tinted green over it.
//
// There used to be a second band under this one showing the same thumbnails
// with the dropped stretches missing. It said nothing the green does not: the
// question at this page is which parts are kept, and one band answers it in one
// place instead of asking the eye to hold two rows of identical thumbnails
// against each other.
//
// The widget is the size of the window onto the timeline, never the size of the
// timeline: an hour at the top zoom is 432,000 px wide, which is thirteen times
// what a cairo surface can even be, and every redraw -- ten a second while the
// preview runs -- was walking all of it. So everything below is in timeline
// coordinates with the view scrolled under it (the Translate), and every loop is
// cut down to what is actually on screen first. The work per frame is then the
// same whether the session is a minute or an afternoon.
func (ed *cutEditor) drawTrack(cr *cairo.Context, w, h int) {
	th := float64(ed.thumbHt)
	top := ed.picTop()
	// how deep the whole stack of camera rows is. Everything that is about the
	// CUT and not about one camera is drawn across all of it: one green band
	// down every row, not a separate one per row that could be read as saying
	// the cameras were kept separately.
	bandH := ed.picBottom() - top
	// background
	cr.SetSourceRGB(0.13, 0.13, 0.13)
	cr.Rectangle(0, 0, float64(w), float64(h))
	cr.Fill()

	// what is on screen, in timeline px. The margin is for the things that
	// start left of the edge and reach into view: a thumbnail, a tick's label.
	const margin = 80
	vx0, vx1 := ed.viewX-margin, ed.viewX+float64(w)
	cr.Save()
	cr.Translate(-ed.viewX, 0)
	defer cr.Restore()

	// the hatched holes: every stretch nobody filmed, whatever its real length,
	// drawn as the one gap width. On the runs and not on the recordings, because
	// two overlapping files have no hole between them to draw
	for i, sp := range ed.spans {
		if i > 0 && sp.px >= vx0 && sp.px-gapPx <= vx1 {
			hatchBand(cr, sp.px-gapPx, gapPx, top, bandH)
		}
	}

	for _, v := range ed.vids {
		if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0-gapPx {
			continue // this recording is off screen entirely
		}
		lt := ed.laneTop(v.lane)        // this camera's row
		step := v.thumbStep(th, ed.pps) // thumbnails, only the ones in view
		first, last := v.frameRange(ed.pps, vx0, vx1, step)
		for i := first; i < last; i += step {
			t := v.sessionAt(float64(i) * v.interval)
			pb := ed.thumb(v.frames[i])
			if pb == nil {
				continue
			}
			x := ed.xOf(t)
			// never wider than the row it belongs to: a lane's last thumbnail
			// is a whole frame's worth of a file the row stops partway through
			w := math.Min(float64(pb.Width()), float64(step)*v.interval*ed.pps)
			w = math.Min(w, ed.xOf(v.start+v.dur)-x)
			if w <= 0 {
				continue
			}
			gdk.CairoSetSourcePixbuf(cr, pb, x, lt+2)
			cr.Rectangle(x, lt+2, w, th)
			cr.Fill()
		}

		// file boundary + name
		cr.SetSourceRGB(0.9, 0.7, 0.2)
		cr.SetLineWidth(2)
		cr.MoveTo(v.pxOrigin, lt)
		cr.LineTo(v.pxOrigin, lt+ed.laneH())
		cr.Stroke()
		cr.SetFontSize(10)
		name := v.base
		if v.off > 0 {
			// which seconds of the file this row is showing. A lane cut from a
			// recording is that recording's name over again, and this is the
			// only thing on the page that says which part of it (cut_lane.go)
			name += fmt.Sprintf(" from %s", mmss(v.off))
		}
		if d := ed.shiftOf(v.base); d != 0 {
			// a camera moved by hand looks exactly like a camera whose file
			// says it started there, and the difference is the whole of what
			// the right button did (cut_shift.go)
			name += fmt.Sprintf(" %+.2f s", d)
		}
		plateText(cr, v.pxOrigin+4, lt+12, name)

		if au := ed.pairAud(v.base); au != nil {
			// and the row's own sound directly under its pictures, edge to
			// edge: the pair is one piece of footage seen twice
			ed.drawPairStrip(cr, v, *au, lt+ed.laneH(), vx0, vx1)
		}
	}
	// which row the held scene is SHOWN from, said on the rows themselves
	// (cut_cam.go) -- and under it, whether it hears the camera it is shown
	// from, said on that camera's own strip (cut_hear.go). One column of
	// marks, one question per row.
	ed.drawCamBadges(cr, vx0, vx1)
	ed.drawHearBadges(cr, ed.hearBadgesSrc(), vx0, vx1)
	// and the switch for that sound in the whole cut, at the strip's left where
	// a recorded lane's sits on its name plate
	ed.drawPairSwitches(cr)

	// ruler
	stepS := tickStep(ed.pps)
	cr.SetFontSize(9)
	for _, sp := range ed.spans {
		if sp.px > vx1 || sp.px+sp.dur()*ed.pps < vx0 {
			continue
		}
		from := math.Max(sp.t0, sp.t0+(vx0-sp.px)/ed.pps)
		to := math.Min(sp.t1, sp.t0+(vx1-sp.px)/ed.pps)
		t0 := math.Ceil(from/stepS) * stepS
		for t := t0; t < to; t += stepS {
			x := ed.xOf(t)
			cr.SetSourceRGB(0.6, 0.6, 0.6)
			cr.MoveTo(x, float64(rulerH))
			cr.LineTo(x, float64(rulerH)-5)
			cr.Stroke()
			cr.MoveTo(x+2, float64(rulerH)-7)
			cr.ShowText(fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60))
		}
	}

	// in/out markers: solid lines with flag triangles, same visual weight as
	// the yellow file boundaries so they are actually findable
	if ed.hasIn {
		x := ed.xOf(ed.markIn)
		cr.SetSourceRGB(0.15, 0.85, 0.25)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+bandH)
		cr.Stroke()
		cr.MoveTo(x, top)
		cr.LineTo(x+9, top)
		cr.LineTo(x, top+9)
		cr.ClosePath()
		cr.Fill()
	}
	if ed.hasOut {
		x := ed.xOf(ed.markOut)
		cr.SetSourceRGB(0.92, 0.12, 0.12)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+bandH)
		cr.Stroke()
		cr.MoveTo(x, top)
		cr.LineTo(x-9, top)
		cr.LineTo(x, top+9)
		cr.ClosePath()
		cr.Fill()
	}

	// the state overlay: everything the cut keeps, tinted green. What is left
	// untinted is what the cut drops -- which is the whole of what the second
	// band used to say, said here against the footage it refers to.
	for _, s := range ed.segs {
		if s.isInsert() && !(s.audioIns() && !s.spliced()) {
			// violet, below: green means "this footage is kept". The one insert
			// that keeps its footage is a sound laid over a selection -- the
			// picture runs on under it -- so the tint stays for that one.
			continue
		}
		x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		// on the scene's OWN row and no other. The green is what will be shown,
		// and with two cameras up that is a claim about one of them -- a tint
		// down both rows would say the finished video shows two pictures at
		// once. One camera, one row, and this is the whole band again.
		st, lh := ed.segTop(s), ed.laneH()
		cr.SetSourceRGBA(0.2, 0.8, 0.3, 0.30)
		cr.Rectangle(x0, st, x1-x0, lh)
		cr.Fill()
		// hard green edges, boundary-marker style
		cr.SetSourceRGB(0.15, 0.85, 0.25)
		cr.SetLineWidth(2)
		for _, x := range []float64{x0, x1} {
			cr.MoveTo(x, st)
			cr.LineTo(x, st+lh)
			cr.Stroke()
		}
	}

	// While the preview is the cut (▶✂) the dropped stretches are dimmed rather
	// than merely left
	// untinted. In that mode they are not "footage the cut does not keep", they
	// are the seconds ▶ is about to jump over -- and a mode that changes what
	// plays has to be visible on the band that says what plays. The inserts
	// below draw over this, which is right: they are kept.
	if ed.cutOnly {
		cr.SetSourceRGBA(0.04, 0.04, 0.05, 0.62)
		for _, g := range ed.droppedSpans() {
			x0, x1 := ed.xOf(g[0]), ed.xOf(g[1])
			if x1 < vx0 || x0 > vx1 {
				continue
			}
			cr.Rectangle(x0, top, x1-x0, bandH)
		}
		cr.Fill()
	}

	// inserts, over the overlay. Violet rather than a
	// shade of the green: an insert is not footage that was kept, it is footage
	// that is not there, and the two must not be told apart by brightness. The
	// file's name is written into the band because that is the only thing on
	// this page that says WHICH card is at 12:30 -- there is no thumbnail under
	// it to recognize, the track behind it is whatever the insert covered.
	for _, s := range ed.segs {
		if !s.isInsert() || s.audioIns() {
			// a sound-only insert is marked in the audio lanes, where the thing
			// it changes lives; the picture band it leaves alone
			continue
		}
		// A spliced card costs the footage nothing, so it owns no session time
		// and has no span of the timeline to be drawn across. What it has is a
		// POINT where the footage is cut open and a length of its own, and
		// spliceSpan is that length drawn at this zoom, STARTING at the point --
		// the card begins where the red line was when it was placed, and the
		// marker grows rightward with the zoom the way the card runs.
		x0, x1 := ed.segSpan(s)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		cr.SetSourceRGBA(0.55, 0.35, 0.9, 0.55)
		cr.Rectangle(x0, top, x1-x0, bandH)
		cr.Fill()
		if s.spliced() {
			// hatched over the violet, so the marker says both things at once: a
			// card goes in here, and the footage stops for it
			hatchStrokes(cr, x0, x1-x0, top, bandH)
		}
		cr.SetSourceRGB(0.75, 0.6, 1)
		cr.SetLineWidth(2)
		for _, x := range []float64{x0, x1} {
			cr.MoveTo(x, top)
			cr.LineTo(x, top+bandH)
			cr.Stroke()
		}
		cr.SetFontSize(10)
		switch {
		case s.spliced():
			// The length comes with the name here and nowhere else: a spliced card
			// does not stretch between two borders you can read off the ruler.
			// Beside the marker while the marker is a marker, inside it once the
			// zoom has made it wide enough to write in -- text that starts at the
			// same place either way, so it does not appear to jump as you zoom.
			tx := x1 + 4
			if x1-x0 > 90 {
				tx = x0 + 4
			}
			markPlate(cr, tx, top+th-2, "card", fmt.Sprintf("%s  %.1fs", insName(s), s.Dur))
		case x1-x0 > 24:
			// the file, not the parameters: a filled-in tier board's parameters
			// are longer than the clip they would be written across
			markPlate(cr, x0+4, top+th-2, "card", insName(s))
		}
	}

	// A sound-only insert is marked on the SOUND: the recorders' band below
	// when the session has one (drawAudio, which also says why the picture
	// band leaves these alone), and every row's wave strip here always --
	// with the cameras' waves paired under their pictures, a session with no
	// separate recorder has no other band to say "these seconds were placed".
	heldSnd := ed.heldSeg()
	for i := range ed.segs {
		s := ed.segs[i]
		if !s.audioIns() {
			continue
		}
		x0, x1 := ed.segSpan(s)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		named := false
		for r := 0; r < max(1, ed.laneN); r++ {
			ph := ed.pairH(r)
			if ph <= 0 {
				continue
			}
			// named once, on the first strip that can carry it: the same
			// mark on three rows saying the same file three times is noise
			ed.sndInsMark(cr, s, x0, x1, ed.laneTop(r)+ed.laneH(), ph, heldSnd == &ed.segs[i], !named)
			named = true
		}
	}

	// slowed or frozen stretches, tinted rose over the footage itself: the
	// effect belongs to the frames under it, and the lane below carries its
	// handle. The same hue as its lane marker, so the two read as one thing.
	for _, f := range ed.fx {
		if f.Kind != "speed" {
			continue
		}
		x0, x1 := ed.fxSpanPx(f)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		cr.SetSourceRGBA(0.92, 0.42, 0.6, 0.18)
		cr.Rectangle(x0, top, x1-x0, bandH)
		cr.Fill()
	}

	// the ✕ that takes a whole row away, over everything else the picture band
	// draws: a control the inserts or the cut preview's dimming could paint
	// over would be a control that is there on some frames and not others.
	// The one that drops a scene is not here -- it is on the green bar in the
	// selection row (drawSelBand), which is drawn just below.
	ed.drawLaneKill(cr, vx0, vx1)
	// and an emptied row's ✕, which removes the space. The VIEW's left edge,
	// not vx0: that is the culling edge and sits a margin further left, which
	// is where this badge used to be painted -- off the side of the widget,
	// while the press for it worked at the edge you can see (rowKillAt).
	ed.drawRowKill(cr, ed.viewX)

	// the effects lane, under the picture band (cut_fx.go)
	ed.drawSelBand(cr, vx0, vx1)
	ed.drawFxLane(cr, vx0, vx1)
	ed.drawFxKill(cr, vx0, vx1)

	// the clip a double click has picked up, outlined in white. The edge marker
	// below says which BORDER is about to move; this says which whole clip is,
	// and they are the same gesture at two scales, so they are the same ink.
	if s := ed.heldSeg(); s != nil && !s.audioIns() {
		// ...unless the held clip is a sound: its marker is in the lanes, and
		// the outline goes where the marker is (drawAudio)
		x0, x1 := ed.segSpan(*s)
		st := ed.segTop(*s)
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.SetLineWidth(2)
		cr.Rectangle(x0+1, st+1, x1-x0-2, ed.laneH()-2)
		cr.Stroke()
	}

	// selection rubber band
	if ed.sel.active {
		a, b := ed.sel.t0, ed.sel.t1
		if b < a {
			a, b = b, a
		}
		x0, x1 := ed.xOf(a), ed.xOf(b)
		// on the row it was drawn on: the blue says which picture ＋ Add is
		// about to keep, so it has to be over that picture
		cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.45)
		cr.Rectangle(x0, ed.laneTop(ed.sel.lane), x1-x0, ed.laneH())
		cr.Fill()
	}

	// which sound is in hand, said on the wave itself: a selection drawn on a
	// row's own strip wears its second wash there, exactly as one drawn in a
	// separate recorder's lane wears it in the band below (drawAudio)
	if ed.sel.active && ed.selSnd() {
		for _, v := range ed.vids {
			if v.base != ed.sel.aud {
				continue
			}
			if au := ed.pairAud(v.base); au != nil {
				x0, x1 := ed.selSpanPx()
				sy, sh := ed.laneTop(v.lane)+ed.laneH(), float64(ed.lanes(*au))*waveLaneH
				cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.34)
				cr.Rectangle(x0, sy, x1-x0, sh)
				cr.Fill()
				cr.SetSourceRGB(0.62, 0.82, 1)
				for _, x := range []float64{x0, x1} {
					cr.Rectangle(x-1.5, sy, 3, sh)
				}
				cr.Fill()
			}
		}
	}

	// the row the preview is watching (camAt), outlined across the pair --
	// the strip is the same footage. Dashed, so it cannot be read as the held
	// clip's solid white; gone the moment ▶ takes the preview back to the cut.
	if m := ed.watchRow(); m >= 0 {
		lt := ed.laneTop(m)
		cr.SetSourceRGBA(0.95, 0.95, 1, 0.6)
		cr.SetLineWidth(1.5)
		cr.SetDash([]float64{5, 4}, 0)
		cr.Rectangle(ed.viewX+1, lt+0.75, float64(w)-2, ed.laneH()+ed.pairH(m)-1.5)
		cr.Stroke()
		cr.SetDash(nil, 0)
	}

	// the border under the pointer: a soft white bar with a halo behind it, over
	// the green one it belongs to. Not the held marker's ink -- no heads, and
	// half the strength -- because it is an offer rather than a state: this is
	// the border the next press would take, and it goes away when the pointer
	// does. See hoverEdge.
	if ed.edgeHovOn {
		x := ed.xOf(ed.edgeHovT)
		cr.SetSourceRGBA(1, 1, 1, 0.16)
		cr.SetLineWidth(9)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+bandH)
		cr.Stroke()
		cr.SetSourceRGBA(1, 1, 1, 0.6)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+bandH)
		cr.Stroke()
	}

	// the clip edge that is held: white, wider than the green border it sits on,
	// with a head each way to say that it moves. Drawn last, so nothing painted
	// over it can hide what is about to change under the next ‹f.
	if ed.edgeOn && ed.edgeSeg < len(ed.segs) {
		x := ed.xOf(ed.edgeTime())
		st, lh := ed.segTop(ed.segs[ed.edgeSeg]), ed.laneH()
		cr.SetSourceRGB(1, 1, 1)
		cr.SetLineWidth(3)
		cr.MoveTo(x, st)
		cr.LineTo(x, st+lh)
		cr.Stroke()
		for _, d := range []float64{-1, 1} {
			cr.MoveTo(x, st+lh/2-5)
			cr.LineTo(x+7*d, st+lh/2)
			cr.LineTo(x, st+lh/2+5)
			cr.ClosePath()
			cr.Fill()
		}
	}
}

func (ed *cutEditor) inCut(t float64) bool {
	for _, s := range ed.segs {
		if t >= s.S && t < s.E {
			return true
		}
	}
	return false
}

func tickStep(pps float64) float64 {
	for _, s := range []float64{1, 2, 5, 10, 30, 60, 120, 300, 600} {
		if s*pps >= 70 {
			return s
		}
	}
	return 1200
}

func (ed *cutEditor) thumb(path string) *gdkpixbuf.Pixbuf {
	if pb, ok := ed.thumbs[path]; ok {
		return pb
	}
	pb, err := gdkpixbuf.NewPixbufFromFileAtScale(path, -1, ed.thumbHt, true)
	if err != nil {
		pb = nil
	}
	ed.thumbs[path] = pb
	return pb
}

// The run bar drives the preview through these; see transport in pipeline.go.
func (ed *cutEditor) playing() bool { return ed.player != nil && ed.player.Playing() }
func (ed *cutEditor) cued() bool    { return ed.player != nil && ed.player.Cued() }

// playAs is both play buttons. Each one is ▶ for its own idea of what the
// preview IS -- ▶ the recording, ▶✂ the cut -- so each press delivers exactly
// what the face it landed on promises. Pressing the button whose preview is
// already running pauses it; pressing the other one switches the preview over,
// and if something was playing it plays on as the other thing rather than
// stopping -- switching is why you pressed a play button and not ⏸.
func (ed *cutEditor) playAs(cut bool) {
	if ed.cutOnly != cut {
		ed.setCutOnly(cut)
		if ed.playing() {
			return
		}
	}
	ed.toggle()
}

// setCutOnly switches what the preview IS, with everything that follows from
// that: the gap-skip guard, the dimming, the clock's meaning and the ▶✂ lamp.
// The two play buttons are the only hands on it.
func (ed *cutEditor) setCutOnly(cut bool) {
	if ed.cutOnly == cut {
		return
	}
	ed.cutOnly = cut
	ed.jumped = -1
	ed.syncPlayBtn() // an empty cut is nothing to play; see that function
	if cut {
		// the mode promises kept material, so it delivers some immediately
		// rather than at the next tick
		ed.cutOnlySnap()
		if len(ed.segs) == 0 {
			ed.a.setStatus("preview is the cut — and the cut is empty, so ▶✂ has " +
				"nothing to play until a clip is added")
		} else {
			ed.a.setStatus("preview is the cut — removed stretches are skipped and the " +
				"clock reads the finished video's own time")
		}
	} else {
		ed.a.setStatus("preview is the recording again — everything plays, cuts and all")
	}
	ed.showTime()
	ed.a.syncPlayIcons() // both ▶s redraw: whose preview this is just changed
	ed.redrawTracks()
}

// syncCutPlay draws the ▶✂ button: ⏸✂ while its preview is the one running,
// and a lit face for as long as the preview is the cut at all -- the lamp the
// old toggle button's pressed state used to be. Label states, not icon states,
// because no stock icon says "play, but the cut": the ✂ has to share the face.
func (ed *cutEditor) syncCutPlay() {
	if ed.cutPlayBtn == nil {
		return
	}
	if ed.playing() && ed.cutOnly {
		ed.cutPlayBtn.SetLabel("⏸✂")
		ed.cutPlayBtn.SetTooltipText("pause the cut preview")
	} else {
		ed.cutPlayBtn.SetLabel("▶✂")
		ed.cutPlayBtn.SetTooltipText("play the CUT instead of the recording: the removed " +
			"stretches are skipped, so this runs the finished video. The clock reads the " +
			"cut's own time while it does. Changes nothing that is saved.")
	}
	if ed.cutOnly {
		ed.cutPlayBtn.AddCSSClass("suggested-action")
	} else {
		ed.cutPlayBtn.RemoveCSSClass("suggested-action")
	}
}

func (ed *cutEditor) toggle() {
	// ▶ is grey in this state (syncPlayBtn), but the button is not the only way
	// in -- a click on the picture and the run bar land here too, and every way
	// in has to refuse for the same reason
	if ed.cutOnly && len(ed.segs) == 0 {
		ed.a.setStatus("the cut is empty — add a clip to play it, or press ▶ " +
			"to play the recording instead")
		return
	}
	if ed.player == nil {
		return
	}
	if !ed.playing() {
		// ▶ plays the CUT: a preview that was watching one row on a click's
		// orders stops watching it. The scenes name their own cameras from
		// here (camAt), and the branches below must load the scene's file,
		// not the watched one's.
		ed.monRow = 0
		ed.redrawTracks() // the dashed outline goes with it
	}
	// ⏸ then ▶ is one gesture -- "let me look at that" -- and it has to put the
	// picture back where it stopped it. Without this the second press was read
	// as a fresh start and took the line to whatever was held: pause halfway
	// through the clip you are working on, press ▶, and you are at its first
	// frame again, watching the part you had just watched.
	//
	// The line's own position is the test, not a flag: anything that moves it
	// while paused -- a click on a track, a frame step with nothing held -- is
	// the hand choosing a new place, and a press after that starts there. A
	// step that moves the HELD thing instead leaves the line alone, so it
	// resumes, which is what "step the clip and carry on" should do.
	resume := ed.resumingHere()
	if ed.playing() {
		ed.markPause() // this press is the ⏸
	} else {
		ed.resumeOn = false // and this one spends what it left
	}
	// Where ▶ starts, which is not always where the line is. Only on the way
	// into playing, whichever branch takes it: ⏸ has to stop where it is.
	if !ed.playing() {
		switch s := ed.heldSeg(); {
		// ▶✂ is asked a different question from ▶ -- not "show me the thing I
		// am editing" but "how does the FINISHED video run from here" -- and
		// here is the red line, wherever the hand last put it. Inside a clip
		// that is a second the cut keeps, so it plays from there; outside one
		// there is nothing to play, so the line moves to the next clip and
		// plays from that (cutOnlySnap). Holding a clip is how you edit it, not
		// how you choose where the video starts, so neither hold below moves
		// the line here: winding back to a boundary you were not asking about
		// is the same chore in reverse.
		case ed.cutOnly:
			ed.cutOnlySnap()
		// resuming: the line is where ⏸ left it, and that is where the
		// picture goes on from. Under ▶✂ above it too -- cutOnlySnap only
		// moves a line standing in a gap, which a paused one is not.
		case resume:
		// With a clip edge held, ▶ plays from the EDGE. It is the thing you are
		// working on and the only reason to press play while holding it is to
		// watch what you have just trimmed to; starting from wherever the
		// playhead was last left meant winding back to the boundary by hand
		// every time.
		case ed.edgeOn:
			ed.setPlayhead(ed.edgeTime())
		case s != nil:
			ed.setPlayhead(s.S) // a held clip plays from its own start, for the same reason
		}
	}
	// what this scene hears, settled before the transport moves rather than in
	// the showInsert below it: ▶ starts the recordings (syncMix), and a lane
	// this scene silences is one ▶ must not start at all
	ed.syncHush()
	ed.player.Toggle()
	// the black "no footage on this row" frame comes and goes with the
	// standstill (showInsert), and pausing is a standstill nothing else
	// re-settles: playback's own ticks stop with it
	ed.showInsert()
	ed.started = ed.started || ed.player.Playing()
	ed.a.updateRunControls()
}

// markPause remembers where the transport stopped, so the next ▶ can tell
// "carry on" from "start". Only ⏸ writes it; ⏹ throws it away (stop).
func (ed *cutEditor) markPause() { ed.resumeT, ed.resumeOn = ed.playhead, true }

// resumingHere is whether ▶ is the second half of a ⏸ ... ▶ pair. The line's
// own position is the test: it is where the pause left it, so anything that
// has moved it since is the hand asking to start somewhere else instead.
func (ed *cutEditor) resumingHere() bool {
	return ed.resumeOn && math.Abs(ed.playhead-ed.resumeT) < 1e-6
}

func (ed *cutEditor) stop() {
	if ed.player != nil {
		ed.player.Stop()
	}
	ed.started = false  // ⏹ hands ▶ back to the step's own job, suggesting
	ed.resumeOn = false // and there is nothing left to resume from
}

// ---- page ------------------------------------------------------------------

func (a *App) buildCut() gtk.Widgetter {
	ed := &cutEditor{a: a, pps: 4, thumbHt: 64, jumped: -1, rowHov: -1, fxKillHov: -1,
		bandKillHov: -1, thumbs: map[string]*gdkpixbuf.Pixbuf{}}
	a.ed = ed
	if p, err := NewPlayer(); err == nil {
		ed.player = p // the preview above the tracks; independent of Review's
		p.OnState = a.updateRunControls
		p.OnError = a.playerErr("the cut preview")
		p.OnLog = func(s string) { a.logf("%s", s) }
		glib.TimeoutAdd(playTick, ed.followPlayback)
	} else {
		a.logf("cut preview player: %v", err)
	}

	// Suggesting is this page's long job, and every other page's long job is the
	// run bar's ▶. It used to be a "Suggest cut" button here as well, which left
	// the page with two ways to start the same run and made ▶ mean Suggest only
	// while the cut happened to be empty. The button is gone; what is left is the
	// length it runs to, captioned with the button that now uses it.
	tgtTip := "target seconds for the FIRST suggested cut, which ▶ in the run bar asks for; " +
		"your own edits are never limited"
	tgtLbl := gtk.NewLabel("")
	tgtLbl.SetMarkup("<small>▶ target</small>")
	tgtLbl.AddCSSClass("dim-label")
	tgtLbl.SetTooltipText(tgtTip)
	ed.target = gtk.NewEntry()
	ed.target.SetText("300")
	ed.target.SetMaxWidthChars(4)
	ed.target.SetWidthChars(4) // it holds a number of seconds, not a sentence
	ed.target.SetInputPurpose(gtk.InputPurposeDigits)
	ed.target.SetTooltipText(tgtTip)
	secs := gtk.NewLabel("s")
	secs.AddCSSClass("dim-label")
	secs.SetTooltipText("seconds")
	// the number and its unit read as one control, so they sit closer than the
	// bar's spacing; the caption goes underneath, with the other small print
	tgtBox := gtk.NewBox(gtk.OrientationHorizontal, 2)
	tgtBox.Append(ed.target)
	tgtBox.Append(secs)
	ed.addBtn = gtk.NewButtonWithLabel("＋ Add")
	add := ed.addBtn
	add.AddCSSClass("suggested-action")
	add.ConnectClicked(func() { a.addSelClicked() })
	// the same selection, cut out of what it lies in rather than kept or
	// dropped: the third thing that can be done to a span (cut_split.go).
	ed.splitBtn = gtk.NewButtonWithLabel("| Split")
	ed.splitBtn.ConnectClicked(func() { a.splitSelRange() })
	// the same selection, dropped instead of kept. Beside Add because they are
	// one pair, and greyed by the same rule -- see cut_selrm.go for why a
	// remove is back on the bar at all.
	ed.remBtn = gtk.NewButtonWithLabel("－ Remove")
	ed.remBtn.ConnectClicked(func() { a.removeSelRange() })
	// Copy takes the selected seconds in hand rather than acting on the cut:
	// while a copy is held, Insert reads ⧉ Paste and splices those seconds in
	// again at the red line. Greyed until there is a selection, because a copy
	// IS the selection taken in hand, and with nothing selected the press
	// could only explain itself.
	ed.copyBtn = gtk.NewButtonWithLabel("⧉ Copy")
	ed.copyBtn.ConnectClicked(func() { a.copyClicked() })
	ed.syncSelBtns()
	// One button, two jobs, because they are the same job seen from either end:
	// with nothing held it puts a card in, and with a card held it opens that
	// card. A second button that is greyed out unless you happen to be holding an
	// insert would say the same thing and take up the bar saying it.
	ed.insBtn = gtk.NewButtonWithLabel("⧉ Insert")
	ins := ed.insBtn
	ins.ConnectClicked(func() { a.insertClicked() })
	// the second thing a copy of footage can be. Paste puts it back into the
	// cut in sequence; this puts it on a row of its own, beside the cameras, so
	// the green can choose between the two. It comes and goes with the copy --
	// there is nothing it could mean with nothing in hand.
	ed.laneBtn = gtk.NewButtonWithLabel("⇲ Lane")
	ed.laneBtn.ConnectClicked(func() { a.pasteLane() })
	ed.syncInsertBtn() // its label and tooltip depend on what is held
	// Undo and Revert are icons, not words. They were the two widest buttons in
	// the bar and they are both the kind of control you reach for by shape --
	// Undo has a keyboard shortcut people already know, and Revert is a rare
	// deliberate act, not something scanned for. The glyphs they used to carry
	// (↶ and ↺) were nearly the same picture; the theme's undo arrow and
	// revert-to-saved icon are not.
	ed.revertBtn = gtk.NewButtonFromIconName("document-revert-symbolic")
	ed.revertBtn.SetTooltipText("Revert edits — drop everything you added or removed by hand and go back to " +
		"the last suggestion — or, if you have not suggested yet, to the cut this page opened with")
	ed.revertBtn.SetSensitive(false)
	ed.revertBtn.ConnectClicked(func() { a.revertClicked() })
	ed.undoBtn = gtk.NewButtonFromIconName("edit-undo-symbolic")
	ed.undoBtn.SetTooltipText("Undo — take back the last Add, Remove or Suggest (Ctrl+Z)")
	ed.undoBtn.SetSensitive(false)
	ed.undoBtn.ConnectClicked(func() { ed.undoLast() })
	ed.redoBtn = gtk.NewButtonFromIconName("edit-redo-symbolic")
	ed.redoBtn.SetTooltipText("Redo — put back what Undo took (Ctrl+Shift+Z)")
	ed.redoBtn.SetSensitive(false)
	ed.redoBtn.ConnectClicked(func() { ed.redoLast() })
	// The playhead's time, printed. It sits with the transport keys because
	// those are the buttons that move it, and it is monospaced ("numeric") so
	// the digits do not dance under ‹f/f› -- a readout that reflows on every
	// frame is one you cannot read while stepping.
	ed.clock = gtk.NewLabel("")
	ed.clock.AddCSSClass("numeric")
	ed.clock.SetWidthChars(8) // "--:--.-" and "59:59.9" both fit; the bar never twitches
	ed.clock.AddCSSClass("dim-label")
	ed.clock.SetMarginStart(2)
	ed.clock.SetMarginEnd(2)
	ed.showTime() // opens as "--:--.-", not as a blank gap in the bar

	ed.total = gtk.NewLabel("")
	ed.total.AddCSSClass("dim-label")
	ed.total.SetHExpand(true)
	ed.total.SetXAlign(1)
	// a label with no ellipsis reports its whole text as a minimum, and this bar
	// is a plain box, so that minimum was a floor under the window itself:
	// measured, the bar could not be narrower than 1527px, of which this line
	// was 272. Ellipsized it still shows in full wherever there is room -- the
	// natural width does not move -- and the window is free to be narrower than
	// the sentence.
	ed.total.SetEllipsize(pango.EllipsizeEnd)

	// Two pairs that both step something up and down, so they must not look
	// alike: one zooms the timeline, the other sizes the thumbnails drawn on it.
	// They used to be a bare +/− and a pair of magnifiers, which is backwards --
	// a magnifier IS the zoom icon, and the thing being made bigger in the other
	// pair is a picture. So: the theme's zoom icons for the timeline, and a
	// picture with a sign for the thumbnails. Different nouns, not two spellings
	// of the same one.
	sized := func(sign, tip string, click func()) *gtk.Button {
		row := gtk.NewBox(gtk.OrientationHorizontal, 1)
		row.Append(gtk.NewImageFromIconName("image-x-generic-symbolic"))
		row.Append(gtk.NewLabel(sign))
		b := gtk.NewButton()
		b.SetChild(row)
		b.SetTooltipText(tip)
		b.ConnectClicked(click)
		return b
	}
	thumbMinus := sized("−", "smaller thumbnails on the tracks", func() { ed.setThumbH(ed.thumbHt * 3 / 4) })
	thumbPlus := sized("+", "larger thumbnails on the tracks", func() { ed.setThumbH(ed.thumbHt * 4 / 3) })

	zoomOut := gtk.NewButtonFromIconName("zoom-out-symbolic")
	zoomOut.SetTooltipText("zoom the timeline out — it stops where the whole session is on screen " +
		"(the scroll wheel does the same, around the cursor)")
	zoomOut.ConnectClicked(func() { ed.zoomStep(1 / 1.25) })
	zoomIn := gtk.NewButtonFromIconName("zoom-in-symbolic")
	zoomIn.SetTooltipText("zoom the timeline in, around the middle of what is on screen " +
		"(the scroll wheel does the same, around the cursor)")
	zoomIn.ConnectClicked(func() { ed.zoomStep(1.25) })

	// The camera and the clock (cut_fx.go). The dropdown is the shape of the
	// finished video; the three buttons put effects in. They sit with the
	// editing controls because that is what they are -- each one changes what
	// Produce makes, is saved in cut.json, and answers to Undo.
	ed.aspectDD = gtk.NewDropDownFromStrings(fxAspects)
	ed.aspectDD.SetTooltipText("the shape of the finished video — source is the footage's own, " +
		"9:16 is a vertical short. The whole frame fits inside it (bars either side) until " +
		"▭ View frames a region; the outline on the preview is what the finished video shows")
	ed.aspectDD.NotifyProperty("selected", func() {
		if ed.aspectMu {
			return // set by code (reload, undo), not a choice being made
		}
		if i := int(ed.aspectDD.Selected()); i >= 0 && i < len(fxAspects) {
			ed.aspectChanged(fxAspects[i])
		}
	})
	// The five effects behind one dropdown -- a menu of verbs, not a state:
	// picking one fires it and the control snaps back to its label, so the
	// notify below re-enters once with 0 and leaves.
	fxKinds := []string{"✚ Effect", "⊕ Zoom", "❝ Text", "▨ SVG", "⏩ Speed", "🔊 Volume"}
	fxDD := gtk.NewDropDownFromStrings(fxKinds)
	fxDD.SetTooltipText("put an effect in: ⊕ Zoom frames what the video shows — drag a box on " +
		"the preview and say how long; when its seconds are up the camera either pulls back " +
		"to where it was or stays on the region, which is how a widescreen recording is turned " +
		"into a vertical short. ❝ Text writes words over the picture for a few seconds in " +
		"a box you draw, ▨ SVG lays a drawing of yours over it the same way — it does not cut " +
		"the video, an insert is the one that does. ⏩ Speed puts a stretch on a clock of its " +
		"own — slowed to a quarter, " +
		"up to 100× to run through dead air, or ×0 to stop the picture on one frame while the " +
		"footage runs on underneath. On the preview " +
		"the box under the pointer can be dragged and its border resized; click its mark in " +
		"the lane below the track for its numbers. 🔊 Volume is the one that changes nothing " +
		"you can see: the seconds it covers are played louder or quieter than they were " +
		"recorded, anywhere from silent to ten times, which is how a passage nobody can " +
		"hear is rescued without touching the rest")
	fxDD.NotifyProperty("selected", func() {
		i := int(fxDD.Selected())
		if i <= 0 {
			return
		}
		fxDD.SetSelected(0)
		switch i {
		case 1:
			ed.armFx("zoom")
		case 2:
			ed.armFx("text")
		case 3:
			a.svgClicked()
		case 4:
			a.speedClicked()
		case 5:
			a.volumeClicked()
		}
	})

	// one button that is ▶ or ⏸ depending on the preview, like every other play
	// button in the app (syncPlayIcons in pipeline.go keeps it drawn)
	ed.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	ed.playBtn.SetTooltipText("play or pause the preview at the playhead")
	ed.playBtn.ConnectClicked(func() { ed.playAs(false) })
	// with something held -- a clip edge, a whole clip, an effect (click its
	// mark) -- these move that instead of the playhead. Said on every one of
	// them, because that is the state you are in when you look at them.
	prev5 := gtk.NewButtonWithLabel("‹‹f")
	prev5.SetTooltipText("back 5 frames (pauses) — or whatever is held, 5 frames")
	prev5.ConnectClicked(func() { ed.frameStep(-5) })
	prevF := gtk.NewButtonWithLabel("‹f")
	prevF.SetTooltipText("previous frame (pauses) — or whatever is held, one frame")
	prevF.ConnectClicked(func() { ed.frameStep(-1) })
	nextF := gtk.NewButtonWithLabel("f›")
	nextF.SetTooltipText("next frame (pauses) — or whatever is held, one frame")
	nextF.ConnectClicked(func() { ed.frameStep(+1) })
	next5 := gtk.NewButtonWithLabel("f››")
	next5.SetTooltipText("forward 5 frames (pauses) — or whatever is held, 5 frames")
	next5.ConnectClicked(func() { ed.frameStep(+5) })
	// The second ▶. There used to be a "✂ Cut only" toggle here: a MODE, which
	// ▶ then obeyed -- so playing the cut was two buttons in the right order,
	// and a ▶ that sometimes played the recording and sometimes the cut. Two
	// play buttons say it in one press each: ▶ the recording, every second of
	// it, which is what you want while deciding where the cuts go; ▶✂ the cut
	// -- gaps jumped, effects on, the clock on the cut's own time, what
	// Produce will make. Whichever ran last still colors the page (dimming,
	// clock), and the ▶✂ face stays lit while the preview is the cut.
	// Nothing about it is saved.
	ed.cutPlayBtn = gtk.NewButtonWithLabel("▶✂")
	ed.cutPlayBtn.ConnectClicked(func() { ed.playAs(true) })
	ed.syncCutPlay() // opens with its tooltip and face in the recording state

	// The selection in numbers. It used to sit under its own ⟦ in / out ⟧ / ✕
	// buttons, but the band made those a second way of doing what a drag
	// already does -- and a pair of marks set by button could exist with no
	// selection under them, which left Add refusing while the readout showed a
	// range. The numbers are the half worth keeping, so they moved in with the
	// buttons that CONSUME a selection instead (see the bar below).
	ed.marks = gtk.NewLabel("")
	ed.marks.AddCSSClass("numeric")
	ed.marks.AddCSSClass("dim-label")
	ed.marks.SetTooltipText("the selection, in session time")
	ed.showMarks() // opens as dashes, not as a blank sliver under the buttons

	formPane := ed.buildForm()

	// The bar in groups rather than as one row of twenty equal buttons. Twenty
	// things spaced identically is twenty things to read every time, and the
	// eight pixels between each of them added up to a bar that would not fit a
	// laptop screen. Buttons that do one job together are linked into a single
	// segmented control -- no gaps inside, so the group reads as one object and
	// the eye lands on four groups instead of twenty buttons.
	//
	// Left to right is also the order of the work: move the playhead, mark what
	// you found, change the cut. Then a rule, and past it the view controls,
	// which change what you SEE and never what is saved.
	linked := func(ws ...gtk.Widgetter) *gtk.Box {
		b := gtk.NewBox(gtk.OrientationHorizontal, 0)
		b.AddCSSClass("linked")
		for _, w := range ws {
			b.Append(w)
		}
		return b
	}
	rule := func() *gtk.Separator {
		s := gtk.NewSeparator(gtk.OrientationVertical)
		s.SetMarginTop(2)
		s.SetMarginBottom(2)
		return s
	}

	// A control and what it says, as one column: the buttons on top, their
	// numbers in small print underneath. The readout sits UNDER its control
	// rather than beside it because side by side each pair read as two bar
	// items, and the eye had to learn which number belonged to which buttons.
	col := func(top, under gtk.Widgetter) *gtk.Box {
		c := gtk.NewBox(gtk.OrientationVertical, 0)
		c.SetVAlign(gtk.AlignCenter)
		c.Append(top)
		c.Append(under)
		return c
	}

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	// the wheel over the bar steps frames, so a hand hovering the transport
	// never has to land on one exact button to scrub
	bar.AddController(ed.wheelFrames())
	bar.Append(col(linked(ed.playBtn, ed.cutPlayBtn, prev5, prevF, nextF, next5), ed.clock))
	// how loud the preview is, next to the two ▶s that use it -- the run bar
	// at the bottom of the window has one too, and both are the same number
	// (volumeCtl). Here as well as there because this is the page a cut is
	// listened to on, and reaching past the timeline to a slider on the status
	// bar is a long way to go to turn the game down
	bar.Append(volumeCtl())
	bar.Append(rule())
	bar.Append(col(tgtBox, tgtLbl))
	bar.Append(col(linked(add, ed.splitBtn, ed.remBtn, ed.copyBtn, ins, ed.laneBtn), ed.marks))
	bar.Append(ed.aspectDD)
	bar.Append(fxDD)
	bar.Append(linked(ed.undoBtn, ed.redoBtn, ed.revertBtn))
	// The two prompts this page sends -- the rules Suggest works to and the
	// audit that reads its answer back -- were a dropdown and an Edit button
	// here. They are on Prepare with all the others now (prepedit.go): a prompt
	// is written before the first run and then left alone, and this bar is
	// where the session's actual work happens.
	bar.Append(rule()) // past here nothing changes the cut
	// the totals are the small print under the view controls: the last column,
	// pushed to the right edge, its line growing leftwards into the free space
	viewRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	viewRow.SetHAlign(gtk.AlignEnd)
	viewRow.Append(linked(zoomOut, zoomIn))
	viewRow.Append(linked(thumbMinus, thumbPlus))
	totCol := col(viewRow, ed.total)
	totCol.SetHExpand(true)
	bar.Append(totCol)

	ed.srcArea = gtk.NewDrawingArea()
	ed.srcArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawTrack(cr, w, h)
	})
	ed.audArea = gtk.NewDrawingArea()
	ed.audArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawAudio(cr, w, h)
	})
	ed.audArea.SetVisible(false) // until reload finds a separate recording
	// the right button has no hover state to advertise itself with -- there is
	// no ghost of a recording under the pointer the way there is a highlighted
	// border -- so the one place it can be said is here
	ed.srcArea.SetTooltipText("right-drag a camera's row to move that camera along the " +
		"session clock until its sound lines up with another's, or the wave strip under " +
		"it to move that one recording — up or down onto another row too, when that row " +
		"has room; right-drag inside a selection to move the scenes in it instead")
	ed.audArea.SetTooltipText("right-drag a lane to move that recording along the session " +
		"clock — line its waveform up with the footage's own and the two clocks agree")
	ed.srcArea.SetHExpand(true)
	ed.audArea.SetHExpand(true)
	// the tracks are as wide as the page, so their width IS the view width
	ed.srcArea.ConnectResize(func(w, h int) {
		ed.viewW = float64(w)
		ed.syncScroll()
		// the fit-the-window zoom moves with the window: widen the page while
		// fully zoomed out and the timeline has to grow with it, or a scrollbar
		// comes back for the empty strip beside it
		if m := ed.minPps(); ed.pps < m {
			ed.pps = m
			glib.IdleAdd(ed.relayout) // never resize from inside an allocation
		}
	})

	// The lanes answer to the mouse exactly as the picture band does: the wheel
	// zooms, a click places the playhead, a drag selects, and a press near a
	// border picks that border up. They are a view of the same timeline, and a
	// band you can see a cut point in but not click on is a band that makes you
	// aim at the thumbnails instead.
	for _, area := range []*gtk.DrawingArea{ed.srcArea, ed.audArea} {
		area := area
		area.SetFocusable(true) // so Del/Ctrl+Z reach the page after a click
		// wheel zooms around the cursor; Shift+wheel (and a trackpad's sideways
		// swipe) pans, which used to be the scrolled window's job
		motion := gtk.NewEventControllerMotion()
		motion.ConnectMotion(func(x, y float64) { ed.lastX = x })
		area.AddController(motion)
		scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollBothAxes)
		scroll.ConnectScroll(func(dx, dy float64) bool {
			if scroll.CurrentEventState()&gdk.ShiftMask != 0 {
				dx, dy = dy, 0
			}
			if dx != 0 {
				ed.setOff(ed.viewX + dx*ed.viewW/8)
			}
			if dy != 0 {
				ed.zoomAt(ed.lastX, math.Pow(1.25, -dy))
			}
			return true
		})
		area.AddController(scroll)
		// The left button does two things, and which one is decided at the press
		// by where it lands: on a held edge it moves that edge, anywhere else it
		// is the selection it has always been. That is why the edge has to be
		// picked up first -- without a held edge to aim at, "drag the border"
		// and "drag a region" are the same gesture over the same pixels, and
		// every selection that started near a boundary would trim instead.
		drag := gtk.NewGestureDrag()
		var dragStartX, dragStartY float64
		var hadSel bool
		var selT0, selT1 float64
		var trimming, moving bool
		var selPart int    // which part of the selection band this drag has, if any
		var fxPart int     // ...and which part of the effect's band
		var grabAt float64 // where in the held clip the press landed
		drag.ConnectDragBegin(func(x, y float64) {
			area.GrabFocus()
			dragStartX, dragStartY = x, y
			// a press in the effects lane is about the effect under it: it
			// picks that effect up if it was not already in hand, and the
			// drag then slides it -- the same deal a held clip gets one band
			// up, except that an effect needs no separate right-click first.
			// A marker is a few px wide; making the hand say "this one" twice
			// before it may move it is a tax on the only thing you can do to
			// it here.
			// a press in the selection band is about the selection: its ends
			// move that end, its middle moves the whole of it, its ✕ throws it
			// away, and a press on the empty part of the row starts a new one
			// exactly as a press on the pictures does. Like the effects lane,
			// no separate right-click first: there is one object in this row,
			// and making the hand name it twice is a tax on the only thing
			// there is to do here.
			selPart = selNone
			if area == ed.srcArea && ed.hitSelBand(y) {
				if selPart = ed.selPartAt(x + ed.viewX); selPart == selKill {
					ed.killSel()
					selPart = selNone
					return
				}
				if selPart != selNone {
					ed.holdSel(selPart)
					a, _ := ed.selSpan()
					grabAt = ed.tAtView(x) - a
					return
				}
				// clear of the blue, the GREEN bar: it stands for a clip, so
				// its ends are that clip's borders and the drag trims them,
				// its middle is the clip and the drag slides it -- the same
				// trimming and moving the picture band's gestures drive, so
				// the clamps, snaps, preview and undo are one machinery.
				if i, part := ed.bandClipPartAt(x + ed.viewX); part != selNone {
					if part == selKill {
						ed.killSeg(i) // the page's one "drop that scene"
						return
					}
					ed.holdBandClip(i, part)
					if part == selWhole {
						moving = true
						grabAt = ed.tAtView(x) - ed.segs[i].S
					} else {
						trimming = true
					}
					return
				}
				ed.dropSel() // clear of it: this is a new selection
			}
			if area == ed.srcArea && ed.fxHitLane(y) {
				// the ✕ at a band's right end, asked before the band: the same
				// press would otherwise pick the effect up (cut_fxkill.go)
				if i := ed.fxKillAt(x+ed.viewX, y); i >= 0 {
					ed.killFx(i)
					return
				}
				if i := ed.fxIndexAt(x+ed.viewX, y); i >= 0 {
					if !ed.fxOn || ed.fxSel != i {
						ed.holdFx(i)
					}
					// a press on an end of a band that has ends changes that
					// end; anywhere else on it slides the whole thing
					fxPart = ed.fxPartAt(i, x+ed.viewX)
					ed.fxMoving = true
					grabAt = ed.tAtView(x) - ed.heldFx().T
				} else {
					ed.dropFx() // a press on the empty lane puts it down
				}
				return
			}
			// the cut itself: a press within a few px of a green border takes
			// that border and the drag trims it. The border is highlighted and
			// the pointer is a resize arrow before the press happens (see
			// hoverEdge), which is what makes one button enough here -- the
			// hand knows it is about to trim rather than to select, and does
			// not have to name the edge with a second button first.
			//
			// The ruler, the selection row and the effects lane are not part of
			// this: they are their own objects, and the branches above have
			// already returned for them.
			// the lane badges, in either area and before anything else that
			// could claim the same ground: they only exist while a scene is in
			// hand, and while one is, pressing one is the only thing that
			// press can have meant (cut_hear.go)
			// the whole-lane switch, on the band's name plates: it is at a
			// fixed x and is always there, where a scene's badge is in
			// timeline coordinates and comes and goes with the scene in
			// hand -- so where the two can claim the same press (a scene
			// beginning at the very left of the view) the permanent one
			// wins, and the scene's badge is a hair's scroll away
			if area == ed.audArea {
				if base := ed.laneSwitchAt(x, y); base != "" {
					ed.toggleLaneAll(base)
					return
				}
			}
			// the same switch for the sound filmed with the pictures, on the
			// paired strip under them: one per camera row, and asked here for
			// the reason above
			if area == ed.srcArea {
				if bases := ed.pairSwitchAt(x, y); len(bases) > 0 {
					ed.toggleLanesAll(bases, pairSwitchName(bases))
					return
				}
			}
			if base := ed.hearAt(x+ed.viewX, y, area == ed.srcArea); base != "" {
				ed.toggleHear(base)
				return
			}
			// the same column, on the picture rows: which camera the scene is
			// shown from (cut_cam.go). Asked with the sound's badges and for
			// the reason they are asked here -- while a scene is in hand,
			// pressing one of its marks is the only thing the press can mean
			if area == ed.srcArea {
				if r := ed.camBadgeAt(x+ed.viewX, y); r >= 0 {
					ed.setSegCam(r)
					return
				}
			}
			// the ✕ badges the picture band carries, asked before the borders
			// they can overlap: a press on one would otherwise be read as
			// taking hold of a clip edge to trim it. Dropping a SCENE is not
			// among them any more -- that ✕ is on the green bar in the
			// selection row, and it is answered with the rest of that row's
			// parts (bandClipPartAt, above).
			if area == ed.srcArea && ed.hitPics(y) {
				// a cut lane's own ✕ sits at the row's start (cut_lane.go)
				if name := ed.laneKillAt(x+ed.viewX, y); name != "" {
					ed.killLane(name)
					return
				}
				// an empty row's ✕ can share ground with nothing: the row
				// wearing it has no footage, so no lane badge either
				if r := ed.rowKillAt(x+ed.viewX, y); r >= 0 {
					ed.killRow(r)
					return
				}
			}
			if area != ed.srcArea || ed.hitPics(y) {
				if trimming = ed.pickAt(x+ed.viewX, false) == pickEdge; trimming {
					return // the drag is that border's
				}
			}
			if moving = ed.onHeldSeg(x + ed.viewX); moving {
				// the clip travels with the pointer from wherever it was taken
				// hold of, not by its start: a clip that jumped so that its start
				// was under the cursor would move before the hand did
				grabAt = ed.tAtView(x) - ed.heldSeg().S
				return
			}
			ed.dropEdge() // any other left click puts a held edge or clip down
			ed.dropSeg()
			ed.dropSel()
			hadSel, selT0, selT1 = ed.sel.active, ed.sel.t0, ed.sel.t1
			ed.sel.t0 = ed.tAtView(x)
			ed.sel.t1 = ed.sel.t0
			ed.sel.active = true
			// a selection drawn in a lane is a selection of THAT sound, and
			// the press is the only place it can be said: from here on the
			// selection is a span of session time like any other, and which
			// band drew it is not something the span remembers by itself.
			ed.sel.aud = ""
			if area == ed.audArea {
				ed.sel.aud = ed.audAtY(y)
			}
			// ...and a selection drawn on the pictures is a selection of the
			// CAMERA it was drawn on, said in the same place and for the same
			// reason. A press in the thin space between two rows keeps the row
			// the last one used rather than jumping to the first: it is a miss,
			// not a change of mind.
			if area == ed.srcArea {
				if l := ed.laneAt(y); l >= 0 {
					ed.sel.lane = l
				} else if l := ed.pairAt(y); l >= 0 {
					// drawn on the wave strip under a row: that row's camera,
					// and the selection is of that footage's SOUND -- the
					// strip is the lane the recording used to have below
					ed.sel.lane = l
					ed.sel.aud = ed.pairAudAt(x+ed.viewX, y)
				}
			}
			ed.syncSelBtns()
		})
		drag.ConnectDragUpdate(func(ox, oy float64) {
			if trimming {
				ed.moveEdgeTo(ed.tAtView(dragStartX+ox), true)
				ed.showEdge(true) // the picture comes with it
				return
			}
			if moving {
				ed.moveSegTo(ed.tAtView(dragStartX+ox)-grabAt, true)
				ed.showSeg(true)
				return
			}
			if ed.fxMoving {
				if fxPart == fxStart || fxPart == fxEnd {
					ed.resizeFxTo(fxPart == fxEnd, ed.tAtView(dragStartX+ox))
				} else if f := ed.heldFx(); f != nil {
					// the whole band slides, so both its ends are offered to
					// the cuts and the other effects (snapFxSpan)
					t0, t1 := f.fxSpan()
					ed.moveFxTo(ed.snapFxSpan(ed.tAtView(dragStartX+ox)-grabAt, t1-t0), true)
				}
				ed.showFx(true) // the picture comes with it, as with a clip
				return
			}
			switch selPart {
			case selWhole:
				ed.moveSelTo(ed.tAtView(dragStartX+ox) - grabAt)
				return
			case selStart, selEnd:
				ed.resizeSelTo(selPart == selEnd, ed.tAtView(dragStartX+ox))
				return
			}
			ed.sel.t1 = ed.tAtView(dragStartX + ox)
			ed.syncSelMarks() // the readout under Add follows the drag live
			ed.syncSelBtns()
		})
		drag.ConnectDragEnd(func(ox, oy float64) {
			_, _, _ = hadSel, selT0, selT1
			if trimming {
				trimming = false
				// a border trimmed out until it meets the next clip closes the
				// gap between them, and two kept stretches with nothing
				// between them are one stretch: the same join a clip dragged
				// against its neighbour gets (cut_split.go). This is the
				// commoner way to ask for it -- the gap is closed by extending
				// what is kept, where sliding a clip moves the footage.
				merged := ed.edgeDirty && ed.mergeTouching(ed.edgeSeg)
				if ed.edgeDirty {
					ed.persist() // the drag is over: this is the cut that goes on disk
					if !merged {
						// the picture lands exactly where the edge did,
						// throttling or no throttling, so what you trimmed to
						// is what is on screen and the next ‹f is judged
						// against it. Only when something actually moved: a
						// press that merely picked the border up is a choice,
						// and a choice does not move the red line (pickAt).
						ed.showEdge(false)
					}
				}
				if merged {
					return // the border is gone, and the join said so
				}
				ed.edgeStatus()
				return
			}
			if moving {
				moving = false
				// dragged up against the clip beside it, the two are one clip
				// again: the drop is the join (cut_split.go). Asked before the
				// write, so what goes on disk is the merged cut, and it says
				// its own sentence -- there is no held clip left to report on.
				merged := ed.segDirty && ed.mergeDropped()
				if ed.segDirty {
					ed.persist()
					ed.segDirty = false
					// the picture lands where the clip did, so what you moved
					// it to is what is on screen. Only when it actually MOVED,
					// the rule the held edge above already keeps: the second
					// press of a double click lands on a clip that is already
					// in hand and ends a drag that went nowhere, and putting
					// the line on the clip's start there is the page yanking
					// the picture away from the frame that was clicked.
					ed.showSeg(false)
				}
				if merged {
					return
				}
				ed.segStatus()
				return
			}
			if ed.fxMoving {
				ed.fxMoving, fxPart = false, fxWhole
				moved := ed.fxDirty
				if ed.fxDirty {
					ed.persist()
					ed.fxDirty = false
				}
				ed.fxStatus()
				// a press that took an effect and put it straight back down is
				// a CLICK on it, and a click on an effect asks for its numbers.
				// They used to be behind a double click, which is a thing you
				// have to be told about -- and a bar you can click, drag and
				// resize but not open reads as an effect with no settings.
				//
				// Only when it did not move: a form is opened on the effect as
				// it was at the press, and saving it looks that effect up by
				// those numbers (updateFx). After a drag they are last second's
				// numbers, and the save would find nothing to write to.
				//
				// On an idle, not here: the dialog must not open in the middle
				// of the gesture it is answering.
				if !moved && math.Abs(ox) < 5 && math.Abs(oy) < 5 {
					glib.IdleAdd(func() { ed.a.editFx() })
				}
				return
			}
			if selPart != selNone {
				part := selPart
				selPart = selNone
				ed.holdSel(part) // the status line, with the numbers as they now are
				return
			}
			if math.Abs(ox) >= 5 || math.Abs(oy) >= 5 {
				return // a real drag: the new selection stands
			}
			// a press without movement is a CLICK: cue the playhead. The
			// selection dies with it, readout included -- a reading that
			// outlived its band would show a range Add then refuses to act
			// on. Only the marks: clearMarks would also drop a held effect,
			// which a click on the footage deliberately leaves in hand.
			ed.sel.active = false
			ed.hasIn, ed.hasOut = false, false
			ed.showMarks()
			ed.syncSelBtns()
			// a click on a row is also the answer to "which camera is the
			// preview showing": that one, even where a kept scene shows
			// another (camAt). monRow rather than sel.lane, because the
			// selection's row follows every drag and watching is a choice
			// only a click makes. A click elsewhere -- the band, the ruler,
			// the recorders below -- moves the line and changes no minds.
			// Not while playing: ▶ promised the cut, and followPlayback
			// re-cues from camAt every tick, so a watch started here would
			// change what PLAYS, sound and all, not just what is shown.
			if area == ed.srcArea && !ed.playing() && len(ed.vids) > 0 {
				if l := ed.laneAt(dragStartY); l >= 0 {
					ed.monRow = l + 1
				} else if l := ed.pairAt(dragStartY); l >= 0 {
					ed.monRow = l + 1
				}
			}
			ed.setPlayhead(ed.tAtView(dragStartX))
			ed.monStatus()
			// ...and a click ON THE GREEN takes that scene in hand, which is
			// what the same click on the green bar in the band already does.
			// It is one object drawn in two rows, and it answered to one click
			// in one of them and two in the other; the drawn thing is the
			// bigger target and the one the hand goes to first.
			//
			// Last, so the scene's own account of itself is the line that
			// stands. Clear of the green nothing is taken and the drop above
			// (dropSeg, at the press) is what the click meant.
			if area == ed.srcArea && ed.hitPics(dragStartY) {
				if px := dragStartX + ed.viewX; ed.segOnGreen(px, dragStartY) >= 0 {
					ed.grabSeg(px) // the same scene: segOnGreen asked segAtPx for it
				}
			}
		})
		area.AddController(drag)

		// The right button is the TIMELINE's, not the cut's. Every other
		// gesture on this page is measured against where the recordings sit,
		// and none of them can move a recording -- which leaves no way at all
		// to correct the one thing a file name cannot get right, the seconds
		// (cut_shift.go). Here it is: drag a row and that camera slides along
		// the clock until its waveform lines up with the one below it, drag a
		// lane and that recording does.
		//
		// Inside the selection it means the other thing: the kept scenes slide
		// and the footage stays. Same button, because it is the same idea --
		// something is in the wrong place in time and the hand is putting it
		// right -- and which of the two it is is exactly whether the press
		// landed inside the green you drew to say "this bit".
		slide := gtk.NewGestureDrag()
		slide.SetButton(gdk.BUTTON_SECONDARY)
		var slideSrcs []string           // what this drag moves; empty = the green
		var slideFrom map[string]float64 // their corrections at the press
		var slideSegs []cutSeg           // ...or the cut at the press
		var slideWhat string
		var slideD float64                   // the gesture so far, in seconds
		var slideEdges, slideTargs []float64 // what moves, and what it lands on
		var slideY0 float64                  // where the press landed, for the row under the pointer
		var slideRows bool                   // this drag may change rows (it moves sources on the picture band)
		var slideOn, slideTimeOn bool
		slide.ConnectDragBegin(func(x, y float64) {
			area.GrabFocus()
			slideSrcs, slideFrom, slideSegs = nil, nil, nil
			slideD, slideOn, slideTimeOn = 0, false, false
			slideY0 = y
			a0, a1 := ed.selSpan()
			t := ed.tAtView(x)
			switch {
			case area == ed.audArea:
				// a lane has no green of its own to move, and one recording
				// out of step with the rest is the whole reason this exists
				slideSrcs = []string{ed.audAtY(y)}
				slideWhat = "the recording"
			case area == ed.srcArea && ed.pairAt(y) >= 0:
				// on the wave strip: the one recording under the pointer, not
				// the whole row -- the per-recording drag the old audio band
				// had lives here now. Its pictures come with it: one base,
				// one correction (cut_shift.go).
				slideSrcs = []string{ed.pairAudAt(x+ed.viewX, y)}
				slideWhat = slideSrcs[0]
			case ed.sel.active && ed.sel.aud == "" && t >= a0 && t < a1 &&
				(ed.hitPics(y) || ed.hitSelBand(y)):
				slideSegs = append([]cutSeg(nil), ed.segs...)
				slideWhat = "the selected scenes"
			default:
				l := ed.laneAt(y)
				if l < 0 {
					l = ed.sel.lane // the hair between two rows is a miss, not a choice
				}
				slideSrcs = ed.laneSrcs(l)
				slideWhat = fmt.Sprintf("camera %d", l+1)
			}
			if len(slideSrcs) == 1 && slideSrcs[0] == "" {
				slideSrcs = nil // an empty lane row: nothing under the pointer
			}
			slideFrom = copyShift(ed.shift)
			slideEdges, slideTargs = ed.slideSnapSet(slideSrcs, slideSegs != nil)
			// only sources on the picture band have a row to move to: a
			// separate recording's lane is the recorder's, not a row, and the
			// green names rows rather than sitting on one
			slideRows = slideSrcs != nil && area == ed.srcArea
		})
		// The drag is read in pixels over the zoom, not through tAtView: an
		// unfilmed stretch is drawn as one fixed hatch however many minutes it
		// stands for, so two x a hair apart across one can be a quarter of an
		// hour apart in time. Correcting a clock by "however long that gap
		// happens to be" is not a correction anyone asked for.
		slide.ConnectDragUpdate(func(ox, oy float64) {
			if ed.pps <= 0 || (slideSrcs == nil && slideSegs == nil) {
				return
			}
			d := ox / ed.pps
			// ...but it does snap: within a few pixels of an edge of the
			// dragged material meeting a still one -- the selection's border
			// above it, another recording's start, a scene's edge -- the drag
			// lands exactly on it (slideSnap). Same reach at every zoom.
			d = slideSnap(d, slideEdges, slideTargs, snapPx/math.Max(ed.pps, 0.001))
			// The same drag moves between rows: the pointer standing on
			// another row that has room is the ask (moveRow). Worked out
			// before the gates below because a purely vertical drag is a drag
			// too, and has to open the gesture -- but never the TIME gate,
			// which stays sideways-only: with the hand moving straight up the
			// snap would otherwise be free to yank the part sideways onto
			// whatever alignment happens to be in reach.
			to := -1
			if slideRows {
				if r := ed.rowAt(slideY0 + oy); ed.rowFits(slideSrcs, r) {
					to = r
				}
			}
			slideTimeOn = slideTimeOn || math.Abs(ox) >= 3
			if !slideTimeOn && to < 0 {
				return // a right click, not yet a drag
			}
			if !slideOn {
				ed.pushUndo() // the whole gesture is one step back, rows and seconds both
				slideOn = true
			}
			if slideTimeOn {
				slideD = d
				if slideSrcs != nil {
					ed.shiftTo(slideSrcs, slideFrom, d)
				} else if ed.slideGreen(slideSegs, d) {
					ed.redrawTracks()
				}
				ed.a.setStatus(shiftLabel(slideWhat, d))
			}
			if to >= 0 && ed.moveRow(slideSrcs, to) {
				ed.a.setStatus(fmt.Sprintf("%s moved to row %d — its kept scenes came along", slideWhat, to+1))
			}
		})
		slide.ConnectDragEnd(func(ox, oy float64) {
			if !slideOn {
				return
			}
			slideOn = false
			ed.persist()
			// the frame under the red line is different footage now, or the
			// same footage at a different second, and a preview still showing
			// the old one is the page disagreeing with itself
			ed.setPlayhead(ed.playhead)
			ed.a.setStatus(shiftLabel(slideWhat, slideD) +
				" — right-drag a row to line its sound up with another camera's, " +
				"a lane to move one recording, or inside a selection to move the scenes in it")
		})
		area.AddController(slide)

		// Picking up a whole clip is the second click of a double click. It used
		// to be the right button, and the right button is the timeline's now:
		// every other thing on this page is taken hold of by hovering it and
		// pressing, and a border you had to name with a different button first
		// was the one exception -- you could see the edge under the pointer and
		// still not be able to grab it.
		//
		// A clip cannot follow the border onto the single press, because over
		// the cut that press is already how you put the red line somewhere, and
		// a gesture that both navigates and picks things up is a gesture you can
		// use for neither. So: click to go there, click again to take the clip
		// that is there. What each press means is pickAt, where the order of the
		// questions is written down with its reasons.
		//
		// The selection row is not part of this: up there the left button does
		// the whole job. Nor is the effects lane: a click there already holds
		// the effect AND opens its numbers (see the drag's end), so the second
		// click of a double one has nothing left to mean and the branch below
		// is only there to keep the press off the playhead.
		pick := gtk.NewGestureClick()
		pick.SetButton(gdk.BUTTON_PRIMARY)
		pick.ConnectPressed(func(n int, x, y float64) {
			if n < 2 {
				return
			}
			area.GrabFocus()
			if area == ed.srcArea && ed.fxHitLane(y) {
				return // the lane's own gesture, and it is on the single press
			}
			if area == ed.srcArea && !ed.hitPics(y) {
				return
			}
			ed.pickAt(x+ed.viewX, true)
		})
		area.AddController(pick)

		// Hovering says what a press would take hold of, and says it in the band
		// itself as well as in the pointer: the effect markers sit shoulder to
		// shoulder and the narrowest one wins (see fxIndexAt), and a clip border
		// is two px of green among a lot of other green, so "the one under the
		// pointer" is not always the one the eye would have guessed.
		// Highlighting the answer removes the guess -- and on the borders it is
		// what lets a single press mean trim without ever surprising anyone.
		hover := gtk.NewEventControllerMotion()
		if area == ed.srcArea {
			hover.ConnectMotion(func(x, y float64) { ed.hoverTracks(x, y) })
			hover.ConnectLeave(func() { ed.hoverTracks(-1, -1) })
		} else {
			hover.ConnectMotion(func(x, y float64) { ed.hoverLanes(x, y) })
			hover.ConnectLeave(func() { ed.hoverLanes(-1, -1) })
		}
		area.AddController(hover)
	}

	// The scrollbar is ours rather than a scrolled window's, because a scrolled
	// window would want a child as wide as the whole timeline (see drawTrack).
	// Hidden when it cannot move: a bar at the zoom floor is a bar that says
	// there is more session off to the right when there is not.
	ed.hadj = gtk.NewAdjustment(0, 0, 0, 1, 1, 0)
	ed.hadj.ConnectValueChanged(func() {
		ed.viewX = ed.hadj.Value()
		ed.redrawTracks()
	})
	ed.hbar = gtk.NewScrollbar(gtk.OrientationHorizontal, ed.hadj)
	ed.hbar.SetVisible(false)

	band := gtk.NewBox(gtk.OrientationVertical, 4)
	band.Append(ed.srcArea)
	band.Append(ed.audArea) // the recorders' band: the sound nobody filmed
	tracks := gtk.NewBox(gtk.OrientationVertical, 4)
	tracks.Append(ed.lineOver(band)) // the red line, on a layer of its own
	tracks.Append(ed.hbar)
	tracks.SetVExpand(true)
	tracks.SetVAlign(gtk.AlignStart) // the tracks are their own height; the rest is air

	bottom := gtk.NewBox(gtk.OrientationVertical, 8)
	bottom.SetMarginTop(6)
	bottom.SetMarginStart(12)
	bottom.SetMarginEnd(12)
	bottom.SetMarginBottom(8)
	bottom.Append(bar)
	bottom.Append(tracks)

	// What this step wrote and a way into the folder. ed.total above says what
	// the editor holds; this says what is actually saved, which is the thing
	// the next step reads. The group rides the shared bottom bar (outStack in
	// main.go) rather than the page: every step answers this same question,
	// so it is asked in one place.
	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("cut/ — the cut, as cut.json")
	openOut.ConnectClicked(func() { a.openFolder(a.cutDir()) })
	ed.out = gtk.NewLabel("")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.Append(openOut)
	outRow.Append(ed.out)
	a.outStack.AddNamed(outRow, "cut")
	ed.updateOut()

	// Ctrl+Z and Del on the page. Bubble phase on purpose: the notes box and
	// the target entry see the key first and keep their own editing behaviour.
	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		switch {
		case keyval == gdk.KEY_z && state&gdk.ControlMask != 0:
			ed.undoLast()
		case (keyval == gdk.KEY_Z || keyval == gdk.KEY_y) && state&gdk.ControlMask != 0:
			ed.redoLast()
		case keyval == gdk.KEY_Delete || keyval == gdk.KEY_BackSpace:
			a.removeSelClicked()
		// ← and → are the frame buttons for the hand that is already on the
		// mouse, and they exist ONLY while an edge or a clip is held: unheld
		// they are the focus keys GTK expects them to be. Bubble phase still
		// gives the notes box and the lists their own scrolling first.
		case (ed.edgeOn || ed.segOn || ed.fxOn) && (keyval == gdk.KEY_Left || keyval == gdk.KEY_Right):
			n := 1
			if state&gdk.ShiftMask != 0 {
				n = 5
			}
			if keyval == gdk.KEY_Left {
				n = -n
			}
			ed.frameStep(n)
		case (ed.edgeOn || ed.segOn || ed.fxOn || ed.selOn || ed.copyOn || ed.fxArm != "") && keyval == gdk.KEY_Escape:
			ed.dropEdge()
			ed.dropSeg()
			ed.dropFx()
			ed.dropSel()
			ed.copyOn = false
			ed.syncInsertBtn()
			ed.fxArm = ""
			ed.syncFxCursor()
			ed.syncPreviewZoom() // an armed view/zoom had the live layer down
		default:
			return false
		}
		return true
	})
	bottom.AddController(keys)

	// Video and forms side by side on top, timeline across the full width
	// below. The picture is 16:9 and a form is a column of labelled rows, so
	// they want opposite shapes -- stacked, the column ate the height the tracks
	// needed and the space beside the video stayed empty. The tracks are the one
	// thing that wants the whole width, so they get it.
	top := gtk.NewPaned(gtk.OrientationHorizontal)
	top.SetEndChild(formPane)
	top.SetShrinkEndChild(false)
	if ed.player != nil {
		ed.player.Picture.SetVExpand(true)
		ed.player.Picture.SetSizeRequest(-1, 160)
		// clicking the video itself also toggles; the ▶/⏸ button lives in the bar
		click := gtk.NewGestureClick()
		click.ConnectReleased(func(n int, x, y float64) { ed.toggle() })
		ed.player.Picture.AddController(click)
		// a frame + breathing room, so the video is not glued to its neighbors.
		// Between the two, the framing overlay: the rectangle that says what a
		// vertical (or zoomed) cut of this picture will show (cut_fxview.go).
		vframe := videoFrame(ed.buildFxOverlay())
		vframe.SetMarginTop(10)
		vframe.SetMarginStart(12)
		vframe.SetMarginEnd(12)
		vframe.SetMarginBottom(6)
		top.SetStartChild(vframe)
	} else {
		top.SetStartChild(gtk.NewBox(gtk.OrientationVertical, 0)) // no preview: the forms have the row
	}
	top.SetPosition(660)

	// What this page reads, at the top, where Inputs and Describe put theirs.
	// The question it answers is the one asked just before pressing Suggest --
	// is everything in here, and does the model get to hear as well as see --
	// and it was answerable only by opening session.txt.
	ed.inputs = gtk.NewLabel("")
	ed.inputs.SetXAlign(0)
	ed.inputs.SetHExpand(true)
	ed.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(ed.inputs)
	ed.updateInputs()

	// which half of the page matters depends on whether you are cutting or
	// tuning what Suggest is told, so the divider is the user's
	pane := gtk.NewPaned(gtk.OrientationVertical)
	pane.SetStartChild(top)
	pane.SetEndChild(bottom)
	pane.SetPosition(380)
	pane.SetVExpand(true)
	// the tracks are never squeezed away. A paned shrinks both children below
	// their minimum by default, and with the log open on a short window the
	// 380 px above left the bottom half a few pixels of thumbnail at the edge
	// of the screen -- which reads as "the timeline is gone", not as "the
	// timeline is small". The picture above has a floor of its own (160 px)
	// and gives way first.
	pane.SetShrinkEndChild(false)
	pane.SetResizeStartChild(true)
	pane.SetResizeEndChild(false)

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(pane)
	// and the empty timeline is laid out from the start: the ruler, the
	// and an empty row are a page with no cut yet, which is a real state, and
	// the band has no height until relayout gives it one (see clearTracks)
	ed.relayout()
	return page
}

// zoomStep is what the + and − in the bar do: the wheel's own step, but around
// the middle of what is on screen rather than around a cursor that is up on the
// button and not over the timeline at all. Zooming about the view's center is
// also what keeps a click-click-click on + heading somewhere: whatever you
// centered stays centered.
func (ed *cutEditor) zoomStep(factor float64) { ed.zoomAt(ed.viewW/2, factor) }

// zoomAt zooms about a point of the VIEW (a cursor position, or its middle),
// keeping whatever is under that point under it afterwards.
func (ed *cutEditor) zoomAt(viewX, factor float64) {
	t := ed.tAtView(viewX)
	ed.pps = math.Max(ed.minPps(), math.Min(120, ed.pps*factor))
	ed.relayout()
	ed.setOff(ed.xOf(t) - viewX)
}

// minPps is the zoom at which the whole session fits across the window, and
// therefore the floor: below it the timeline would be smaller than the space it
// has and scrolling would move nothing.
//
// The holes between the filmed runs are drawn at a fixed width and do not
// shrink with the zoom, so they come off the width the footage may use.
// Dividing the window by the duration alone -- which is what this did -- left
// the fully zoomed-out timeline wider than its window by every hole in it, so
// the scrollbar stayed and it still slid, which reads as a timeline hiding
// something off to the right when there is nothing out there at all.
func (ed *cutEditor) minPps() float64 {
	return fitPps(ed.viewW, ed.filmedDur(), len(ed.runs()))
}

// fitPps is that floor without a widget in the way: the zoom at which dur
// seconds spread over n filmed runs come to exactly view pixels, gaps and the
// rounding in relayout included.
func fitPps(view, dur float64, n int) float64 {
	if view <= 0 || dur <= 0 {
		return 0 // no allocation yet, or nothing loaded: no width to fit into
	}
	gaps := float64(max(0, n-1)) * gapPx
	return math.Max(0, (view-gaps-1)/dur) // -1: relayout rounds the width up
}

// sessEnd is the far end of the session: the moment the last recording stops.
// Past it is time nobody filmed, so nothing on the timeline may be dragged out
// there. A clip and an edge are already held to their own recording; an effect
// is held to this, which is the same rule one recording wider.
func (ed *cutEditor) sessEnd() float64 {
	// the LATEST end, not the last recording's: sorted by start, the file that
	// begins last is not necessarily the one that stops last -- a camera that
	// rolled the whole session outlasts the one switched on halfway through it
	end := 0.0
	for _, v := range ed.vids {
		end = math.Max(end, v.start+v.dur)
	}
	return end
}

// filmedDur is how much of the session got filmed at all: the runs added up,
// NOT the recordings added up. Two cameras rolling through the same minute are
// one minute of timeline between them, and a zoom fitted to the sum of the
// files would fit the tracks into half the window.
func (ed *cutEditor) filmedDur() float64 {
	d := 0.0
	for _, s := range ed.runs() {
		d += s.dur()
	}
	return d
}

// updateCutInfo (re)loads the editor when its inputs exist. It is the ONLY
// thing that fills this page -- buildCut makes an empty one -- so anything
// that changes what Prepare wrote has to end up here, or the tracks go on
// showing a session that is over. refreshCut is how the runs say so.
func (a *App) updateCutInfo() {
	if a.ed == nil {
		return
	}
	a.ed.updateOut()    // true even with no timeline to load: the folder is the folder
	a.ed.updateInputs() // and so is what is missing, which is the useful part here
	a.ed.stale = false  // whatever the tracks show after this, it is what is on disk
	if !a.canCut() {
		a.ed.clearTracks()
		return
	}
	if err := a.ed.reload(); err != nil {
		a.logf("cut editor: %v", err)
		a.ed.clearTracks() // a half-built timeline is worse than an empty one
	}
}

// refreshCut brings the Cut page up to date with what a run just wrote: now if
// that is the page on screen, and otherwise on the way in.
//
// Describe is what made this necessary. It writes the session timeline the Cut
// page is gated on, the tab unlocks the moment it lands -- and nothing rebuilt
// the tracks, so the page you were finally allowed to open was the empty box it
// had been built as. It filled in on the next restart, which is what made it
// look like the run had not worked rather than like the page had not looked.
func (a *App) refreshCut() {
	if a.ed == nil {
		return
	}
	a.ed.stale = true
	if a.stack == nil || a.stack.VisibleChildName() != "cut" {
		return // it will catch up on the way in
	}
	// on screen, so it has to catch up now -- but not once per caller. Opening a
	// project says "the sources changed" three times on its way through
	// applyProject, and a rebuild is three ffprobes per recording. The idle pass
	// folds them into the one that matters, the last.
	if a.ed.pending {
		return
	}
	a.ed.pending = true
	glib.IdleAdd(func() {
		a.ed.pending = false
		if a.ed.stale {
			a.updateCutInfo()
		}
	})
}

// clearTracks empties the timeline. For the project that was swapped out from
// under the page: without it, opening another project whose folder holds no
// session of its own leaves the previous one's recordings drawn on the tracks,
// which is the most convincing wrong thing this page can show.
//
// It lays the empty timeline out too, rather than returning early when there
// is nothing to clear. The band has no height of its own -- fitSrc gives it
// one, and only relayout calls fitSrc -- so an editor that was never laid out
// is a page with no tracks on it at all, not a page with empty tracks. That
// was what a project with frames but no cut showed: the ruler and
// the empty row are what "no cut yet" looks like, and they need the relayout
// as much as a full cut does.
func (ed *cutEditor) clearTracks() {
	ed.vids, ed.segs, ed.undo, ed.redo, ed.base = nil, nil, nil, nil, cutState{}
	ed.fx, ed.fxOn, ed.fxArm = nil, false, ""
	ed.setAspect("")
	ed.sel.active = false
	ed.hasPlay = false
	ed.clearMarks()
	ed.syncButtons()
	ed.relayout() // which redraws every band and re-counts the total
}

func (ed *cutEditor) clearMarks() {
	ed.hasIn, ed.hasOut = false, false
	ed.showMarks()
	// an edge, a clip or an effect held over an Undo or a Revert points into
	// the old cut
	ed.edgeOn, ed.segOn, ed.fxOn = false, false, false
	ed.syncInsertBtn()
	ed.syncSelBtns()
}

func (a *App) addSelClicked() {
	ed := a.ed
	if !ed.sel.active || len(ed.vids) == 0 {
		a.setStatus("drag a region on a track first")
		return
	}
	// Add keeps FOOTAGE, and a sound-scoped selection is not about footage. The
	// button is greyed for this; the guard is for every other way in.
	if ed.sel.aud != "" {
		a.setStatus(fmt.Sprintf("＋ Add keeps footage, and the selection is %s's sound — "+
			"drag on the pictures instead — a selection is of what it was drawn on", ed.sel.aud))
		return
	}
	// a selection lying in the gap between two recordings, or one shorter than
	// a scene, adds nothing at all. Saying "added" over a cut that did not move
	// -- and leaving an undo step that undoes nothing -- is the one status line
	// that cannot be trusted afterwards, so measure first and say what happened.
	if len(ed.rangePieces(ed.sel.t0, ed.sel.t1)) == 0 {
		a.setStatus(fmt.Sprintf("nothing to add: %.2f s of footage selected — a scene "+
			"is %.0f s or more, and the gaps between recordings hold none",
			math.Abs(ed.sel.t1-ed.sel.t0), minSegLn))
		return
	}
	ed.pushUndo()
	stole := ed.addRange(ed.sel.t0, ed.sel.t1)
	ed.sel.active = false
	ed.clearMarks()
	// with two cameras up, "added" is only half of what happened: the seconds
	// came off whatever camera had them, and a switch nobody was told about is
	// a switch that reads as footage going missing
	switch {
	case stole && ed.laneN > 1:
		a.setStatus(fmt.Sprintf("added on %s, and taken off the other camera — "+
			"↶ Undo (Ctrl+Z) takes it back", ed.camName(ed.sel.lane)))
	case ed.laneN > 1:
		a.setStatus(fmt.Sprintf("added on %s — ↶ Undo (Ctrl+Z) takes it back",
			ed.camName(ed.sel.lane)))
	default:
		a.setStatus("added — ↶ Undo (Ctrl+Z) takes it back")
	}
}

// copyClicked takes the selected stretch of footage in hand. Nothing happens
// to the cut: the selection is measured, remembered, and the Insert button
// turns into ⧉ Paste. The selection itself stays on the band -- taking a copy
// is reading, not editing.
func (a *App) copyClicked() {
	ed := a.ed
	if !ed.sel.active {
		a.setStatus("select a stretch of the pictures or of a lane first — " +
			"⧉ Copy takes the selection in hand")
		return
	}
	t0 := math.Min(ed.sel.t0, ed.sel.t1)
	ln := math.Abs(ed.sel.t1 - ed.sel.t0)
	if ln < minSegLn {
		a.setStatus(fmt.Sprintf("the selection is %.2f s — under %.0f s there is nothing worth copying", ln, minSegLn))
		return
	}
	ed.copyFrom, ed.copyLen, ed.copyOn = t0, ln, true
	// what was drawn on is what is copied: a lane's sound from a lane, footage
	// -- picture and what was filmed with it -- from the pictures. Silencing a
	// pasted stretch is the pasted clip's own question, asked of it by its form
	// (soundOpen), rather than a scope set before the copy was taken.
	ed.copyAud, ed.copyCam = ed.sel.aud, ed.sel.lane
	ed.syncInsertBtn()
	if ed.copyAud != "" {
		a.setStatus(fmt.Sprintf("copied %.1f s of sound from %s (%s – %s) — click the timeline "+
			"where it goes, then ⧉ Paste lays it over the footage there. Esc drops the copy",
			ln, ed.copyAud, mmss(t0), mmss(t0+ln)))
		return
	}
	a.setStatus(fmt.Sprintf("copied %s – %s (%.1f s) — click the timeline where it goes, "+
		"then press ⧉ Paste. Esc drops the copy", mmss(t0), mmss(t0+ln), ln))
}

// pasteCopy splices the copied footage into the cut at the red line, as a copy
// segment (see copyScheme): the cut is opened at that point, the copied seconds
// play again, and the footage carries on from the very next frame. Pasting
// consumes the copy -- the button goes back to ⧉ Insert -- because a copy that
// stayed in hand would leave the file chooser unreachable behind a label that
// never changes back; the selection is still on the band, and ⧉ Copy takes it
// again for another paste of the same seconds.
func (a *App) pasteCopy() {
	ed := a.ed
	if !ed.hasPlay {
		a.setStatus("click the timeline where the copy goes first")
		return
	}
	if ed.copyAud != "" {
		a.pasteSound()
		return
	}
	was := ed.cutLen()
	ed.addSplice(fmt.Sprintf("%s%.3f", copyScheme, ed.copyFrom), ed.playhead, ed.copyLen,
		false, ed.copyCam)
	ed.copyOn = false
	ed.syncInsertBtn()
	a.setStatus(fmt.Sprintf("pasted %.1f s of footage from %s at %s — the cut is now %s "+
		"(was %s). Right-click it to play it silent — ↶ Undo takes it back",
		ed.copyLen, mmss(ed.copyFrom), mmss(ed.playhead), mmss(ed.cutLen()), mmss(was)))
}

// pasteLane is the other place a copy can go: not back into the cut in
// sequence, but onto a row of its own beside the cameras, so the green can
// choose between the two (cut_lane.go).
//
// It is the same seconds of the same file either way. What differs is what the
// timeline then says about them: spliced, they play after the footage they were
// taken from and the video is longer by them; on a lane they play INSTEAD of
// whatever else was rolling, and the video is exactly as long as it was. A
// second angle on a session shot with one camera is this, and nothing else on
// this page can say it.
//
// Nothing is cut to the new row. A lane that arrived already green would have
// made the choice the lane exists to offer.
func (a *App) pasteLane() {
	ed := a.ed
	if !ed.copyOn || ed.copyAud != "" {
		return // the button is only on the bar while footage is in hand
	}
	if !ed.hasPlay {
		a.setStatus("click the timeline where the new lane starts first")
		return
	}
	v := pickVideoOn(ed.vids, ed.copyCam, ed.copyFrom)
	if v == nil {
		a.setStatus(fmt.Sprintf("nothing is rolling at %s any more — the copy was taken "+
			"from a recording this page has since moved or dropped", mmss(ed.copyFrom)))
		return
	}
	// the FILE second, which is what a lane is a window on: the copy was taken
	// at a session second, and the two differ by wherever that recording sits
	name := ed.addLane(v.path, v.at(ed.copyFrom), ed.playhead, ed.copyLen)
	if name == "" {
		a.setStatus("that copy is too short to be a lane of its own")
		return
	}
	ed.copyOn = false
	ed.syncInsertBtn()
	ed.sel.active = false
	ed.clearMarks()
	a.setStatus(fmt.Sprintf("%.1f s of footage from %s is now the %s lane, starting at %s "+
		"— select on that row and press ＋ Add to cut to it; its ✕ takes the row away "+
		"again, and ↶ Undo takes this back", ed.copyLen, mmss(ed.copyFrom), name,
		mmss(ed.playhead)))
	ed.redrawTracks()
}

// pasteSound lays the copied sound over the footage at the red line. Sound
// alone, so the picture is left exactly as it was: those seconds keep their
// frames, the video does not get longer by a single one, and the only thing
// that changes is what is heard. That is the other half of ⧉ Paste -- a copy
// of footage is spliced in and lengthens the video, a copy of sound is laid
// over it and does not -- and which of the two this is was settled by the band
// the selection was drawn on, not asked again here.
func (a *App) pasteSound() {
	ed := a.ed
	au := ed.audByBase(ed.copyAud)
	if au == nil {
		a.setStatus(fmt.Sprintf("%s is not in the session any more — the copied sound has "+
			"nowhere to come from", ed.copyAud))
		return
	}
	// where in the FILE those seconds are. A selection that began before the
	// recording did starts the sound where the LANE does -- the file's own
	// beginning for a recording, the window's for a cut lane: there is nothing
	// earlier on that lane to play, and refusing the paste over a second of
	// lead-in nobody selected on purpose would be the worse answer.
	ss := au.at(math.Max(ed.copyFrom, au.start))
	at := ed.playhead
	// the copy stays in hand when it had nowhere to go: the paste did not
	// fail so much as miss, and the answer to missing is to move the red line
	// and press again, not to go and copy the same seconds a second time
	n := ed.addSound(a.relToRoot(au.path), at, ed.copyLen, ss, ed.copyAud)
	if n == 0 {
		a.setStatus(fmt.Sprintf("the cut keeps no footage at %s — a sound is laid OVER the "+
			"picture, so there has to be a picture there; move the red line into a green "+
			"stretch and press ⧉ Paste again", mmss(at)))
		return
	}
	ed.copyOn = false
	ed.syncInsertBtn()
	over := "the footage"
	if n > 1 {
		// it crossed a hole in the cut, and saying so is the difference between
		// a puzzling second marker in the lanes and an expected one
		over = fmt.Sprintf("%d stretches of footage", n)
	}
	a.setStatus(fmt.Sprintf("laid %.1f s of %s over %s at %s — the picture runs on "+
		"under it and the cut is still %s — ↶ Undo takes it back",
		ed.copyLen, ed.copyAud, over, mmss(at), mmss(ed.cutLen())))
}

// audByBase is the recording with this base name, or nil when the session no
// longer has it -- which a copy taken before a reload can find.
func (ed *cutEditor) audByBase(base string) *tlAudio {
	for i := range ed.auds {
		if ed.auds[i].base == base {
			return &ed.auds[i]
		}
	}
	return nil
}

// syncSelBtns tells the buttons that act on the selection what the selection
// now is. Called from every place one is made, resized, pointed at the other
// band, or cleared.
//
// ⧉ Copy is greyed when there is nothing worth taking: the button is a verb on
// the selection, and its being lit is the page saying a selection is there to
// be copied.
//
// ＋ Add and － Remove are greyed while the selection is a sound's. They choose
// which FOOTAGE the cut keeps, and footage here is picture and the sound filmed
// with it in one piece: there is no way to keep the sound and drop the picture,
// so on a selection drawn in a lane they have nothing they could honestly do.
// Greyed rather than left quietly cutting the picture, because the wave it was
// drawn on has just said this selection is about sound, and a button acting on
// the other thing would make that a lie.
//
// Its tooltip is set here rather than where it is built, because a button whose
// sensitivity changes has two things to say and only one of them is true at a
// time.
func (ed *cutEditor) syncSelBtns() {
	if ed == nil {
		return
	}
	snd := ed.sel.active && ed.sel.aud != ""
	if ed.copyBtn != nil {
		ed.copyBtn.SetSensitive(ed.sel.active && math.Abs(ed.sel.t1-ed.sel.t0) >= minSegLn)
	}
	if ed.addBtn != nil {
		ed.addBtn.SetSensitive(!snd)
		tip := "keep the selected region (Undo takes it back)"
		if snd {
			tip = "＋ Add keeps footage, and this selection is " + ed.sel.aud +
				"'s sound — drag on the pictures instead — a selection is of what it was drawn on"
		}
		ed.addBtn.SetTooltipText(tip)
	}
	if ed.splitBtn != nil {
		ed.splitBtn.SetSensitive(!snd)
		tip := "cut the selected region free: a border at each end, nothing removed, " +
			"so those seconds become a scene of their own. With nothing selected it " +
			"cuts once, at the red line (Undo takes it back)"
		if snd {
			tip = "| Split cuts footage, and this selection is " + ed.sel.aud +
				"'s sound — drag on the pictures instead — a selection is of what it was drawn on"
		}
		ed.splitBtn.SetTooltipText(tip)
	}
	if ed.remBtn != nil {
		ed.remBtn.SetSensitive(!snd)
		tip := "drop the selected region — through the middle of a scene it " +
			"leaves two, one either side (Undo takes it back)"
		if snd {
			tip = "－ Remove drops footage, and this selection is " + ed.sel.aud +
				"'s sound — drag on the pictures instead — a selection is of what it was drawn on"
		}
		ed.remBtn.SetTooltipText(tip)
	}
}

// insertClicked drops a file into the cut at the playhead: a video sting ("a
// few moments later"), a still, a diagram, an animated tier list. What it is for
// is the things a session does not contain -- the ranking at the end of a rating
// video is the case this was built for, and no camera was pointed at it.
//
// The playhead, not the selection: a selection says which footage to keep or
// drop, and an insert replaces footage rather than choosing it. Where a region
// IS selected its length is taken as the insert's, which is how to say "four
// seconds of this, here" without editing an edge afterwards.
func (a *App) insertClicked() {
	ed := a.ed
	// the same button opens a held card instead of choosing a new file. Holding
	// one is a statement about what you are working on, and "insert another card
	// at the playhead" is not what anyone means while holding one.
	if s := ed.heldSeg(); s != nil && s.isInsert() {
		a.editInsert()
		return
	}
	// and a held effect the same way: while one is held, the button is its Edit
	if ed.heldFx() != nil {
		a.editFx()
		return
	}
	// a copy in hand is what the button places: Paste is Insert with the file
	// already chosen
	if ed.copyOn {
		a.pasteCopy()
		return
	}
	if !ed.hasPlay && !ed.sel.active {
		a.setStatus("click the timeline where the insert goes first")
		return
	}
	at, want := ed.playhead, 0.0
	// the selection is read HERE and not in the callback, beside the seconds
	// and for the same reason: the chooser is a window the hand can reach
	// around, and the file that comes back has to be placed the way the
	// selection read when the button was pressed.
	lane := ""
	if ed.sel.active {
		at = math.Min(ed.sel.t0, ed.sel.t1)
		want = math.Abs(ed.sel.t1 - ed.sel.t0)
		lane = ed.sel.aud
	}

	// what the chooser admits follows what the selection was drawn on. A
	// selection in a lane is about sound, and offering it a tier card there
	// would be offering to put a picture where the hand pointed at a waveform.
	// Footage, and no selection at all, are offered everything -- what an
	// insert does to the sound is the form's question now, asked of the file
	// that actually comes back (soundOpen).
	title, name, exts := "Insert a clip, image, animation or sound",
		"Video, image, SVG or audio", insExts
	if ed.sel.active && ed.selSnd() {
		title, name, exts = "Insert a sound over the selected seconds", "Audio", audExts
	}
	d := gtk.NewFileDialog()
	d.SetTitle(title)
	d.SetInitialFolder(gio.NewFileForPath(a.insertDir()))
	filt := gtk.NewFileFilter()
	filt.SetName(name)
	for _, e := range exts {
		filt.AddSuffix(e)
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(filt.Object)
	d.SetFilters(filters)
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.OpenFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		path := f.Path()
		// Which mode a file arrives in follows the gesture that placed it, and
		// the button is called Insert: a card dropped at the playhead is put
		// BETWEEN the footage, so the video gets longer by it and nothing
		// filmed is lost. A card placed over a SELECTION is the other one --
		// marking seconds and then putting a card there is a sentence that
		// says what those seconds are for -- and it is the selection that gave
		// it its length, so the two answers stay together.
		m := insMode{dur: want, splice: want < minSegLn, lane: lane}
		if m.dur < minSegLn {
			m.dur = a.insertLength(path)
		}
		// what the tick opens on: a file with no sound of its own replaces no
		// sound, so the session carries on under it -- which is the rule the
		// page has always followed ("an insert replaces what it brings, and
		// nothing else"). A file that brought sound arrives bringing it.
		m.mute = !insHasSound(path)
		m.askMute = ed.soundOpen(path, at, m.dur, m)
		// a card is a picture with holes in it, and the holes are the whole
		// point of one: ask before placing it rather than dropping an empty
		// board on the timeline and leaving the filling to a path typed by hand.
		// A file with no holes is asked about too -- how it sits in the cut and
		// how long it runs are questions about a video sting as much as about a
		// card, and a sting placed without being asked is a sting that can only
		// ever overwrite.
		fields, _ := insFields(path)
		a.askInsertParams("Insert", path, fields, m, func(q svgQuery, m insMode) {
			a.placeInsert(path+q.suffix(), at, m)
		})
	})
}

// What the insert chooser admits. audExts is the sound half on its own,
// because a selection drawn in a lane is offered only those. Both lists are
// the extensions insKind sorts by, so what the chooser lets in is exactly what
// the render knows how to ask ffmpeg for.
var audExts = []string{"mp3", "wav", "ogg", "oga", "flac", "m4a", "aac", "opus"}

// picExts is the picture half, offered on its own to a selection scoped to the
// picture alone.
var picExts = []string{"mp4", "mkv", "mov", "webm", "avi", "m4v",
	"png", "jpg", "jpeg", "webp", "bmp", "gif", "svg"}

var insExts = append(append([]string{}, picExts...), audExts...)

// insMode is how an insert sits in the cut: over the footage or between it, and
// for how long. The two are asked for together because they are one decision --
// a card that costs no footage has nothing to take its length from, so the
// seconds have to be said rather than dragged.
type insMode struct {
	splice bool
	dur    float64
	// whether it brings its own sound. The dialog's one tick, in whichever of
	// its two readings the mode is in (askInsertParams): silent when the
	// footage is cut open for it, the session carrying on underneath when it
	// is laid over. See cutSeg.Mute, which is the field it becomes.
	mute bool
	// which recording a sound is being put in place of: the lane the selection
	// was drawn in, read before the chooser opened. Not a dialog question --
	// the hand said it by pointing at that waveform. See cutSeg.Lane.
	lane string
	// a third way for it to sit in the cut, and the only one that adds a ROW
	// rather than a scene: the file goes on a band of its own and the cut
	// reaches it with the green, like a second camera nobody filmed with
	// (cut_lane.go). Video only -- a row is footage, and a still on one would
	// be a card wearing a camera's clothes.
	asLane bool
	// whether mute is a live question for this insert at all, which is what
	// decides if the dialog shows the tick: a picture insert that either
	// brings a sound of its own or lands over seconds that have one. Both
	// answers are then honest readings and only the hand knows which was
	// meant. See cutEditor.soundOpen, which is the whole of the condition.
	askMute bool
}

// placeInsert puts a chosen file in the cut. The path may carry a card's
// parameters, which are kept with it: the file is made relative to the project
// so it survives a move, and the parameters are not a path and are not touched.
func (a *App) placeInsert(ins string, at float64, m insMode) {
	if m.dur < minSegLn {
		m.dur = a.insertLength(ins)
	}
	file, q := insSplit(ins)
	rel := a.relToRoot(file) + q.suffix()
	was := a.ed.cutLen()
	how := "over the footage — drag its edges to retime it"
	switch {
	case m.asLane:
		// a row, not a scene. Nothing is added to the cut here on purpose: the
		// point of a lane is that the green chooses between it and the cameras
		// beside it, and a lane that arrived already green would have taken
		// that choice (cut_lane.go)
		name := a.ed.addLane(file, 0, at, m.dur)
		if name == "" {
			a.setStatus(fmt.Sprintf("%s is too short to be a lane of its own",
				filepath.Base(file)))
			return
		}
		how = fmt.Sprintf("on a lane of its own (%s) — select on that row and press ＋ Add "+
			"to cut to it; its ✕ takes the row away again", name)
	case m.splice:
		a.ed.addSplice(rel, at, m.dur, m.mute, a.ed.sel.lane)
		how = "between the footage, which is cut open for it"
		if m.mute {
			how = "between the footage, which is cut open for it, and silent — " +
				"the selection was scoped to the picture alone"
		}
	case insKind(file) == "audio":
		// a sound goes in through its own door, because it is the one insert
		// that must leave the picture exactly as it found it (layOverSound)
		n := a.ed.addSound(rel, at, m.dur, 0, m.lane)
		if n == 0 {
			a.setStatus(fmt.Sprintf("the cut keeps no footage at %s — %s is a sound, and a "+
				"sound is laid OVER the picture; move the red line into a green stretch "+
				"and insert it again", mmss(at), filepath.Base(file)))
			return
		}
		how = "over the footage, which keeps its frames — drag its edges to retime it"
		if n > 1 {
			how = fmt.Sprintf("over %d stretches of footage, which keep their frames", n)
		}
	default:
		a.ed.addInsert(rel, at, m.dur, m.mute)
		if m.mute {
			how = "over the picture only, and what is heard under it runs on — " +
				"drag its edges to retime it"
		}
	}
	a.ed.sel.active = false
	a.ed.clearMarks()
	// The length of the finished video, said here because this is the one edit
	// whose effect on it cannot be read off the timeline: the timeline is the
	// session's clock and stays exactly as long as the recording, while a card
	// spliced into it makes the VIDEO longer by its own seconds.
	a.setStatus(fmt.Sprintf("%s inserted at %s for %.1f s, %s — the cut is now %s (was %s) "+
		"— ↶ Undo takes it back", filepath.Base(file), mmss(at), m.dur, how,
		mmss(a.ed.cutLen()), mmss(was)))
}

// editInsert opens the held card: what is written on it, whether it plays over
// the footage or between it, and how long it runs. It is the same dialog that
// places one, because those are the same three questions -- and it opens even
// for a card with nothing written on it, since a video sting still has a mode
// and a length.
func (a *App) editInsert() {
	ed := a.ed
	held := ed.heldSeg()
	if held == nil || !held.isInsert() {
		return
	}
	was := *held
	before := ed.cutLen()
	file, q := insSplit(was.Ins)
	path := a.fromRoot(file) + q.suffix()
	if was.isCopy() {
		path = was.Ins // not a file: the dialog asks only its mode and seconds
	}
	fields, _ := insFields(path) // no fields is a dialog of mode and seconds
	em := insMode{splice: was.spliced(), dur: was.length(), mute: was.Mute, lane: was.Lane}
	em.askMute = ed.soundOpen(path, was.S, em.dur, em)
	a.askInsertParams("Save", path, fields, em,
		func(q svgQuery, m insMode) {
			// the card is found again rather than remembered: the dialog does
			// not hold the timeline still, and coalesce renumbers
			i := ed.indexOfSeg(was)
			if i < 0 {
				a.setStatus("that card is no longer in the cut")
				return
			}
			ed.applyInsert(i, file+q.suffix(), m)
			how := "over the footage"
			if m.splice {
				how = "between the footage, which is cut open for it"
			}
			a.setStatus(fmt.Sprintf("%s — %.1f s, %s — the cut is now %s (was %s)",
				insBase(was.Ins), m.dur, how, mmss(ed.cutLen()), mmss(before)))
		})
}

// insertDir is where the insert chooser opens: an assets folder beside the
// project, since a card reused across sessions lives with the project rather
// than with any recording. Opening the chooser is also when the built-in cards
// are put there -- there is no other moment where a card is what the user is
// after, and a folder that opens empty teaches that there is nothing to insert.
func (a *App) insertDir() string {
	dir := filepath.Join(a.root, "assets")
	if a.root == "" || !exists(a.root) {
		return a.outDir
	}
	wrote, err := writeSVGCards(dir)
	if err != nil {
		a.logf(">>> assets: %v", err)
		if !exists(dir) {
			return a.outDir
		}
	}
	if len(wrote) > 0 {
		a.logf(">>> wrote the built-in cards to %s: %s", dir, strings.Join(wrote, ", "))
	}
	return dir
}

// askInsertParams fills a card in before it is placed. One entry per parameter,
// and the card says what those are: a tier board asks for its six tiers by name
// and for what has just landed on one of them, and an SVG somebody else wrote
// asks for whatever it declares.
func (a *App) askInsertParams(verb, path string, fields []svgField, m insMode, ok func(svgQuery, insMode)) {
	form := a.cutForm()
	if form == nil {
		return // no page, so no column and no button that could have been pressed
	}
	// a file with nothing to fill in still has the two questions below it, and
	// telling someone about key=value for a video sting is an answer to a
	// question they did not ask
	subText := "What is on the card. It is kept with the insert as " +
		"name.svg?key=value, so the same file serves every session."
	if len(fields) == 0 {
		subText = "How this sits in the cut, and how long it runs."
	}
	sub := gtk.NewLabel(subText)
	sub.SetXAlign(0)
	sub.SetWrap(true)
	sub.AddCSSClass("dim-label")

	grid := gtk.NewGrid()
	grid.SetRowSpacing(6)
	grid.SetColumnSpacing(10)

	var entries []*gtk.Entry
	var done func()

	// the card as the dialog stands: every entry's text under the key it was
	// asked for. An empty row is still a row -- an empty D tier is a statement --
	// but an empty caption is nothing at all and is left unsaid.
	cur := func() svgQuery {
		var q svgQuery
		for i, f := range fields {
			if v := strings.TrimSpace(entries[i].Text()); v != "" || f.Keep {
				q = append(q, svgParam{f.Key, v})
			}
		}
		return q
	}
	// How the card sits in the cut, which is a question about the FOOTAGE and so
	// belongs beside what the card says rather than in a menu somewhere: over it,
	// which is what a card has always done here and costs the seconds it runs, or
	// between it, which cuts the clip open at that point and makes the video
	// longer by exactly the card.
	//
	// Two lines rather than one box with a tick in it. A tick is an option, and
	// which of these two a card is is not an option -- it is what the card DOES
	// to the footage, and the mode nobody looked at is the one that quietly ate
	// eight seconds of the session out of a button labelled Insert.
	between := gtk.NewCheckButtonWithLabel(
		"Insert BETWEEN the footage — the video gets longer by the card, nothing filmed is lost")
	over := gtk.NewCheckButtonWithLabel(
		"Play OVER the footage — the card replaces those seconds (the same as Remove)")
	own := gtk.NewCheckButtonWithLabel(
		"Put it on a LANE of its own — a row of the band to cut to, and nothing is cut yet")
	over.SetGroup(between) // one group is a set of radio buttons
	own.SetGroup(between)
	// only footage can be a row. A row is a recording as far as everything
	// downstream is concerned -- the render cuts stretches of it, the preview
	// seeks in it -- and a card is not a recording, it is a picture the cut
	// puts over one.
	own.SetVisible(insKind(path) == "video")
	between.SetActive(m.splice)
	over.SetActive(!m.splice)
	between.SetTooltipText("The footage is cut at this point, the card plays, and the footage " +
		"carries on with the very next frame. The finished video is longer by exactly the card, " +
		"and its sound is silent under the card and resumes where it stopped.")
	over.SetTooltipText("The card is on screen instead of those seconds of session, which are " +
		"gone from the cut exactly as Remove would take them. The video is no longer than it was.")
	own.SetTooltipText("The file becomes a new row of the picture band, starting at the red " +
		"line, as though a camera nobody set up had been rolling there. Nothing is added to " +
		"the cut by this: select on the new row and press ＋ Add to cut to it, the same way " +
		"you would cut between two cameras. Its ✕ takes the row away again.")
	// what this insert does to the sound. One flag on the segment (cutSeg.Mute)
	// and one tick here, and the MODE decides which of its two readings is
	// being asked about -- the same split the flag itself has, so the tick
	// says the sentence the mode makes true rather than a general one that is
	// true in neither. This is where the question lives now: it used to be the
	// selection's scope, which is gone.
	//
	// A tick and not a pair of lines, because unlike over-versus-between this
	// genuinely is a preference: both answers are ordinary, and neither eats a
	// stretch of the session.
	keep := gtk.NewCheckButtonWithLabel("")
	keep.SetActive(m.mute)
	keep.SetVisible(m.askMute)
	// nothing is underneath a row: a lane brings its own picture and its own
	// sound and covers nothing until the cut says so
	syncKeep := func() {
		keep.SetSensitive(!own.Active())
		if between.Active() {
			keep.SetLabel("Play it SILENT — the insert's own sound is not used")
			keep.SetTooltipText("The footage is cut open for this insert, so there is " +
				"nothing underneath it to hear. Ticked, it runs silent; unticked, it " +
				"brings whatever sound it has of its own.")
			return
		}
		keep.SetLabel("Keep the sound running under it — only the picture is replaced")
		keep.SetTooltipText("Ticked, the picture is replaced and everything that was " +
			"audible in those seconds — the capture's own track, every separate " +
			"recording, and this file's own sound if it has one — is decided in " +
			"favour of the session: what was playing carries on underneath. " +
			"Unticked, the insert brings its own sound, or silence if it has none.")
	}
	between.ConnectToggled(syncKeep)
	over.ConnectToggled(syncKeep)
	own.ConnectToggled(syncKeep)
	syncKeep()

	secs := gtk.NewEntry()
	secs.SetText(strings.TrimSuffix(fmt.Sprintf("%.1f", m.dur), ".0"))
	secs.SetMaxWidthChars(5)
	secs.SetWidthChars(5)
	secs.SetInputPurpose(gtk.InputPurposeNumber)
	secs.SetTooltipText("how long the card runs. An inserted card has no edges on the " +
		"timeline to drag, so this is where its length is said.")
	secLbl := gtk.NewLabel("Seconds")
	secLbl.SetXAlign(1)
	secBox := gtk.NewBox(gtk.OrientationHorizontal, 6)
	secBox.Append(secLbl)
	secBox.Append(secs)
	secBox.SetHAlign(gtk.AlignStart)

	// what the two controls say now, with a length that is never zero: a card of
	// no seconds is not a shorter card, it is one nobody ever sees
	mode := func() insMode {
		// lane rides through untouched: which recording a sound insert stands
		// in for is said by the lane the selection was drawn in, before the
		// chooser opened, and nothing in this window asks it again. mute is
		// the tick above, in whichever of its two readings the mode is in.
		out := insMode{splice: between.Active(), asLane: own.Active(), dur: m.dur,
			mute: m.mute, lane: m.lane, askMute: m.askMute}
		if v, err := strconv.ParseFloat(strings.TrimSpace(secs.Text()), 64); err == nil && v >= minSegLn {
			out.dur = v
		}
		if m.askMute && !out.asLane {
			out.mute = keep.Active()
		}
		return out
	}
	done = func() {
		q, md := cur(), mode()
		form.hideForm()
		ok(q, md)
	}
	build := func(fs []svgField) {
		fields, entries = fs, make([]*gtk.Entry, len(fs))
		for i, f := range fs {
			lbl := gtk.NewLabel(f.Label)
			lbl.SetXAlign(1)
			e := gtk.NewEntry()
			e.SetText(f.Val)
			e.SetHExpand(true)
			if f.Hint != "" {
				e.SetPlaceholderText(f.Hint)
				lbl.SetTooltipText(f.Hint)
			}
			entries[i] = e
			grid.Attach(lbl, 0, i, 1, 1)
			grid.Attach(e, 1, i, 1, 1)
			if f.Logo {
				pick := gtk.NewButtonWithLabel("Logo…")
				pick.SetTooltipText("add image files to this tier — a chip is a name, " +
					"a logo, or Name|logo.png for both")
				pick.ConnectClicked(func() { a.pickLogos(&a.win.Window, e) })
				grid.Attach(pick, 2, i, 1, 1)
			}
			e.ConnectActivate(done)
		}
	}
	build(fields)

	secs.ConnectActivate(done)
	insert := gtk.NewButtonWithLabel(verb)
	insert.AddCSSClass("suggested-action")
	insert.ConnectClicked(func() { done() })
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { form.hideForm() })

	// pinned under the column's scroller, not at the foot of the form: six
	// questions are taller than the panel, and the button that answers them
	// had scrolled off the bottom of it
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(cancel)
	btns.Append(insert)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(sub)
	box.Append(grid)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	box.Append(between)
	box.Append(over)
	box.Append(own)
	box.Append(keep)
	box.Append(secBox)
	form.showFormFoot(verb+" "+filepath.Base(path), box, btns, nil)
	if len(entries) > 0 {
		entries[0].GrabFocus()
	}
}

// pickLogos adds image files to a list of items. They go in the way a path is
// kept everywhere else in a project -- relative to it, so the project still
// works after it is moved -- and as bare paths, which is a chip that is only its
// logo. Type a name and a bar in front of one to have both.
func (a *App) pickLogos(parent *gtk.Window, e *gtk.Entry) {
	d := gtk.NewFileDialog()
	d.SetTitle("Logos for this tier")
	d.SetInitialFolder(gio.NewFileForPath(a.insertDir()))
	filt := gtk.NewFileFilter()
	filt.SetName("Images")
	for _, ext := range []string{"png", "jpg", "jpeg", "webp", "gif", "bmp", "svg"} {
		filt.AddSuffix(ext)
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(filt.Object)
	d.SetFilters(filters)
	d.OpenMultiple(context.Background(), parent, func(res gio.AsyncResulter) {
		list, err := d.OpenMultipleFinish(res)
		if err != nil || list == nil {
			return // dismissed
		}
		items := splitLabels(e.Text())
		for i := uint(0); i < list.NItems(); i++ {
			obj := list.Item(i)
			if obj == nil {
				continue
			}
			f := &gio.File{Object: obj}
			if p := f.Path(); p != "" {
				items = append(items, a.relToRoot(p))
			}
		}
		e.SetText(strings.Join(items, ", "))
	})
}

// insertLength is how long a file wants to be on screen: a video's own length,
// an animation's own length, and a fixed few seconds for a still, which has no
// opinion. Only a default -- the edges are draggable like any other clip's.
func (a *App) insertLength(path string) float64 {
	switch insKind(path) {
	case "video", "audio":
		file, _ := insSplit(path)
		if d, err := ffprobeDur(file); err == nil && d > 0 {
			return d
		}
	case "svg":
		// the card as it will be rendered, parameters and all: a board of eight
		// tiers takes longer to arrive than a board of three, and the length
		// offered here has to be the length of the card actually inserted
		b, _, err := insSVG(path)
		if err != nil {
			break
		}
		if svgHasCSSAnimation(b) && !svgAnimated(b) {
			a.logf(">>> %s: a CSS animation with no @keyframes in the file — drawn as a still",
				insBase(path))
		}
		if root, err := parseSVG(b); err == nil {
			if d := svgDuration(root); d > 0 {
				return d
			}
		}
	}
	return insDefault
}

// revertClicked throws away the hand-made delta and nothing else. Undoing ten
// Adds one at a time is not a workflow, but neither is nuking a suggestion you
// wanted to keep: this returns to the checkpoint -- the last suggestion, or the
// cut the page opened with -- and is itself one ↶ Undo away from coming back.
func (a *App) revertClicked() {
	ed := a.ed
	if sameState(ed.snapshot(), ed.base) {
		a.setStatus("nothing to revert — the cut is as it was")
		return
	}
	was := len(ed.segs)
	ed.pushUndo()
	ed.restore(cutState{append([]cutSeg(nil), ed.base.segs...),
		append([]cutFx(nil), ed.base.fx...), ed.base.aspect,
		copyShift(ed.base.shift), copyRows(ed.base.rows),
		append([]cutLane(nil), ed.base.lanes...), ed.base.nRows})
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	switch {
	case len(ed.base.segs) == 0:
		a.setStatus(fmt.Sprintf("reverted — your %d hand-made segment(s) are gone, "+
			"the cut is empty again (↶ Undo brings them back)", was))
	default:
		a.setStatus(fmt.Sprintf("reverted to the %d segment(s) of the last suggestion "+
			"(↶ Undo brings your edits back)", len(ed.base.segs)))
	}
}

// removeSelClicked drops the selected stretch or, when nothing is selected, the
// single scene under the playhead: clicking a green scene and pressing Remove
// should work without having to rubber-band it first. It never fails silently --
// a button that does nothing and says nothing reads as a missing button.
func (a *App) removeSelClicked() {
	ed := a.ed
	switch i := -1; {
	case ed.heldFx() != nil:
		// same rule as a held clip: what is held is what you are working on
		ed.removeHeldFx()
	case ed.heldSeg() != nil:
		// what is held is what you are working on, and a spliced card cannot be
		// removed any other way: it has no span to select and it is under the
		// playhead at one instant only
		s := *ed.heldSeg()
		ed.pushUndo()
		rest := make([]cutSeg, 0, len(ed.segs))
		rest = append(rest, ed.segs[:ed.segSel]...)
		ed.segs = append(rest, ed.segs[ed.segSel+1:]...)
		ed.dropSeg()
		ed.persist()
		what := fmt.Sprintf("the scene at %s", mmss(s.S))
		if s.isInsert() {
			what = insBase(s.Ins)
		}
		a.setStatus(fmt.Sprintf("removed %s (%.0f s) — ↶ Undo takes it back", what, s.length()))
	case ed.sel.active && ed.sel.aud != "":
		// the selection is a sound's, and ⌦ drops FOOTAGE. Falling through to
		// "the scene under the playhead" would be worse than refusing: it
		// would remove something nobody pointed at.
		a.setStatus(fmt.Sprintf("⌦ drops footage, and the selection is %s's sound "+
			"— drag on the pictures instead — a selection is of what it was drawn on",
			ed.sel.aud))
	case ed.sel.active:
		before := len(ed.segs)
		ed.pushUndo()
		ed.removeRange(ed.sel.t0, ed.sel.t1)
		ed.sel.active = false
		ed.clearMarks()
		a.setStatus(fmt.Sprintf("removed — %d segment(s), was %d", len(ed.segs), before))
	case ed.hasPlay:
		if i = ed.segAt(ed.playhead); i < 0 {
			a.setStatus("the playhead is not on a kept scene — click a green one, or drag a region")
			return
		}
		s := ed.segs[i]
		ed.pushUndo()
		ed.segs = append(ed.segs[:i], ed.segs[i+1:]...)
		ed.persist()
		a.setStatus(fmt.Sprintf("removed the scene at %s (%.0f s) — ↶ Undo takes it back",
			mmss(s.S), s.E-s.S))
	default:
		a.setStatus("nothing selected — click a kept scene, or drag a region on a track")
	}
}
