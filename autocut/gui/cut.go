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
// step3/cut.json {"segs":[{"s":..,"e":..}]}   session-time seconds, sorted

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
	selBandH = 16
	gapPx    = 26  // display width of a between-recordings hole
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
const suggestSystem = `You choose the moments for a highlight video of a gaming session, cut for YouTube. Someone who was not there should be able to watch it from start to finish and enjoy it.

You get the whole session as one timeline, [mm:ss] then who, then the line. The minutes keep counting past 59, so [72:30] is 4350 seconds.
  [12:04] EVENT: what was on screen then, and whether it was hectic or calm
  [12:04] SPEAKER_01: something said out loud, which the video plays
  [12:04] NARRATOR: something said on the narrator's own microphone. The video does not play it, but the voice-over will say it, so a good NARRATOR line is worth cutting for like any other.

Return strict JSON, nothing else:
{"segments":[{"start":<sec>,"end":<sec>}]}

Timing.

- start and end are session seconds: mm*60+ss from the stamps.
- 6 to 20 segments, chronological, never overlapping. 8 to 45 seconds each as a rule, but a beat that the session notes ask for, or that needs its whole build up and payoff, runs as long as it needs. A story cut off in the middle is worse than a long segment.
- The total should land within about a tenth of the target length you are given. Well short or well over is rejected and you are asked again.
- Only times the timeline actually shows. Never invent one, and never round to a moment nothing happens at.
- Only stretches with footage. A span with no EVENT lines has nothing to show, and a segment there is thrown away, which leaves the video short.

What you have been told about this session.

- The request may open with a block headed ABOUT THIS SESSION: notes on what happened, who did what, what to look out for. They are written by someone who was there, they know things the timeline only hints at, and they outrank every general rule below.
- Whatever the notes name is a segment. Work out where it happens from the words spoken around it and from the EVENT lines, and take it. If the notes name four things, four of your segments are those things and the rest fill in around them.
- Take the whole thing, not the moment it is first mentioned. Something the notes single out usually runs setup, then the thing itself, then the reaction, and those can be minutes apart: someone is warned not to open the chest long before anyone opens it. Start where it is set up and end after the last reaction to it. Cutting at the first mention hands the viewer a setup with no payoff, which is the one way to make an important moment worse than leaving it out.
- Footage still decides. If no recording has EVENT lines over a named moment, it happened off camera: take the nearest stretch that IS on camera, where it is talked about, rather than a stretch with nothing to show.
- Being in the notes does not put it in the video. Never invent a moment because the notes led you to expect one.

What goes in.

- Open with an introduction. The first segment establishes what this session is: wherever the speakers say what they are doing, where they are, or what they are after. It is allowed to be quiet. Its job is to stop the viewer being lost, not to impress.
- Then vary the pace on purpose. The EVENT lines tell you which moments are hectic and which are calm. All peaks is as tiring to watch as no peaks, so set a loud stretch against a quiet one, and let the good fast sequences run long enough to breathe rather than clipping every one to the minimum.
- Keep the funny lines. A joke, a good insult, a scream, someone confidently wrong, someone breaking down laughing: this is why people watch other people play, and it beats a technically impressive moment nobody reacted to. When the joke lands in the speech and the picture is unremarkable, take it anyway.
- Take the action peaks too: wins, disasters, near misses, reveals, and anything that pays off something set up earlier in the session. A callback is worth more than a bigger explosion.
- Spread the picks over the whole session instead of mining one dense stretch, and finish on something that feels like an ending: the outro if there is one, otherwise the last win or the last good line.

Where to cut.

- Do not cut into a sentence. Use the stamps of the lines either side to start a beat before the first word you want and to end after the reaction to it, so no clip opens or closes mid-word.
- Give a joke its setup. A punchline with the line that set it up cut off is not funny, and neither is a reaction to something the viewer did not see.
- End on the payoff, never on the setup. Where the picture shows something being opened, decided, fought or discovered, the segment ends after the outcome and after what was said about it, however long that takes. Ending a beat just before the thing everyone was waiting for is the worst cut you can make.
- A segment that is only silence, or only walking around, is not a highlight however much the pacing wants a calm one. Calm means quieter action or a good quiet line, not nothing.`

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
const ratingSystem = `You cut a rating video: a session where people play, watch or try several things and end by ranking them. Someone who was not there should finish the video knowing what was rated, what each one was like, and where each one landed.

You get the whole session as one timeline, [mm:ss] then who, then the line. The minutes keep counting past 59, so [72:30] is 4350 seconds.
  [12:04] EVENT: what was on screen then, and whether it was hectic or calm
  [12:04] SPEAKER_01: something said out loud, which the video plays
  [12:04] NARRATOR: something said on the narrator's own microphone. The video does not play it, but the voice-over will say it, so a good NARRATOR line is worth cutting for like any other.

Return strict JSON, nothing else:
{"segments":[{"start":<sec>,"end":<sec>}]}

Timing.

- start and end are session seconds: mm*60+ss from the stamps.
- Chronological and never overlapping. This is a hard constraint of the format, not a preference: the segments are played back in the order you give them, so you cannot gather all the items into a montage at the front. Cover them where they happen.
- 8 to 45 seconds each as a rule. A verdict or a reveal that needs its whole build up and payoff runs as long as it needs.
- The total should land within about a tenth of the target length you are given. Well short or well over is rejected and you are asked again.
- Only times the timeline actually shows, and only stretches with footage. A span with no EVENT lines has nothing to show, and a segment there is thrown away, which leaves the video short.

Work out what is being rated first.

- Before choosing anything, read the timeline through and list the items: the maps, levels, weapons, characters, songs, restaurants -- whatever this session scores. Names come from the speech; the EVENT lines tell you when each one is on screen.
- The request may open with a block headed ABOUT THIS SESSION, written by someone who was there. If it names the items or the scoring, that is the list, and it outranks anything you infer.
- Then find the ranking. It is almost always near the end: the tier list, the countdown, the "so the winner is". Find where it starts and where the last item is placed.

The shape, in this order.

- 1. What this is. Open on the stretch where the speakers say what they are doing and how the scoring works -- the rules, the scale, what they are looking for. Without it the rest is a list of opinions about nothing.
- 2. The line-up. Where the session shows the items before playing them -- a menu, a map list, a browse, someone reading the names out -- take it. It is the one place the viewer sees the whole field at once, and it is usually cheap: one segment, sometimes two.
- 3. Every item, once each, in the order they come up. This is the body of the video and the part that matters most. For each item take the stretch that shows what it is actually like and what they made of it: the moment that decided their opinion, plus the reaction or the score being said out loud. One good segment per item beats three from the item that went best.
- 4. The verdict. Take the ranking whole, and never cut it short. If it runs long, take it as several consecutive segments rather than trimming it to one -- a ranking that stops at third place is the one ending that makes the whole video pointless. The final segment is where the top item is named.

Coverage beats highlights.

- An item with no segment is a hole the ending falls through: the ranking names it, and the viewer has never seen it. Cover every item first, and only then spend what is left of the target length on the best of them.
- Where the target length will not stretch to all of them, give every item something short rather than some of them something generous.
- Where there is room to spare, spend it on the items the group argued about, changed their mind on, or disagreed over -- disagreement is what makes a rating worth watching -- and on whatever the ranking calls out at the end.
- Keep the funny lines while you are covering. A joke, a scream, someone confidently wrong: when a good line lands on the item you were going to cover anyway, that is the stretch to take.

Where to cut.

- Do not cut into a sentence. Use the stamps of the lines either side to start a beat before the first word you want and to end after the reaction to it.
- End on the verdict, not on the play. A segment about an item ends after someone says what they think of it, not the moment the action stops.
- Where an item is played long before it is judged, the segment covering it is the one with the judgement in it. Reaching back for the earlier action instead leaves the viewer with a clip and no point.
- Never invent a moment, a name or a score. If the timeline does not show where an item was rated, take the nearest stretch where it is discussed instead.`

// shortsStyleName is how the Shorts wording is picked and stored; the style
// clamp in suggestClicked reads the same name, so the two cannot drift apart.
const shortsStyleName = "YouTube Shorts"

// shortsSystem is the cut for a YouTube Short: one subject, 20 to 30 seconds,
// watched on a phone mid-scroll. suggestSystem builds a video someone watches
// from start to finish; a Short is scrolled INTO, so it opens mid-action,
// stays on its one subject, and is gone before it needs a second one. The
// subject comes from the editor -- the ABOUT THIS SESSION notes -- and this
// prompt's job is everything else: finding where that subject happens,
// cutting it hard, and marking the few effects that make a phone read it.
//
// This is the one style whose cut gets effects -- but they are no longer in
// this reply. They used to be, and it could never line up: the audit rewrote
// the segments AFTER the effects were chosen, so the zooms sat where the cut
// used to be. suggestFx places them now, in a third call made once the audit
// has settled the segments. (suggestCut still parses an fx list if a reply
// carries one -- a project's edited copy of this prompt may still ask for
// them -- and clampFxToSegs holds whatever arrives to the final cut.)
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const shortsSystem = `You cut a YouTube Short from a gaming session: one vertical clip of 20 to 30 seconds, watched on a phone mid-scroll. The viewer made no promise to stay -- the first two seconds have to already be the good part, or they are gone.

You get the whole session as one timeline, [mm:ss] then who, then the line. The minutes keep counting past 59, so [72:30] is 4350 seconds.
  [12:04] EVENT: what was on screen then, and whether it was hectic or calm
  [12:04] SPEAKER_01: something said out loud, which the video plays
  [12:04] NARRATOR: something said on the narrator's own microphone. The video does not play it, but the voice-over will say it.

Return strict JSON, nothing else:
{"segments":[{"start":<sec>,"end":<sec>}]}

The subject.

- The request opens with a block headed ABOUT THIS SESSION: what the editor wants this Short to be about. That is the subject. Find where it happens on the timeline from the words spoken around it and from the EVENT lines, and build the whole Short on it.
- One subject, told whole. A Short that shows three unrelated good moments is three Shorts, all ruined. The last line of setup, the thing itself, the best reaction -- then out.
- If the notes name nothing, take the single best moment of the session on your own judgement: the loudest reaction, the biggest surprise, the funniest line -- one beat whose build-up and payoff fit inside half a minute.

Timing.

- start and end are session seconds: mm*60+ss from the stamps.
- 1 to 3 segments, chronological, never overlapping. One segment when the moment stands alone; two or three when the setup or the reaction lives elsewhere in the session and the jump between them is what sells it.
- The total should land within about a tenth of the target length you are given. Well short or well over is rejected and you are asked again.
- Only times the timeline actually shows, and only stretches with footage. A span with no EVENT lines has nothing to show.
- Open mid-action. No introduction and no scene-setting: the hook IS the clip. If the viewer needs one fact to get it, a caption added in a later pass can carry that fact -- do not spend footage on it.

Where to cut.

- Do not cut into a sentence, but cut HARD: a Short has no room for the breath a long video takes either side of a moment. Start on the last line of setup that still makes the payoff land, and end on the reaction's peak, not its tail.
- End on the payoff. The last second decides the rewatch, and the rewatch is what the format rewards.`

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
	// this insert brings no sound of its own -- what a selection scoped to the
	// picture alone puts in the cut (▲▲ on the scope strip). The one flag reads
	// two ways, and which one is decided by the mode rather than by a second
	// flag: SPLICED, the cut is open and there is nothing else in the slot, so
	// the insert plays silent; OVERWRITING, the footage is still underneath and
	// keeps being heard, so only the picture is replaced. Both are the same
	// sentence -- this insert contributes no audio -- and an ordinary cut.json
	// is unchanged by it.
	Mute bool `json:"mute,omitempty"`
	// for a sound laid over the footage: which recording it was put in place
	// OF. Empty is the answer a selection scoped to picture-and-sound gives --
	// the file stands in for everything audible, which is what overwriting the
	// sound has always meant. Named (▼), it stands in for that one recording
	// and the rest keep playing under it. Meaningless on anything but a sound
	// insert, and an ordinary cut.json is unchanged by it.
	Lane string `json:"lane,omitempty"`
}

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
}

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

	// the tracks are built from what Preprocessing wrote, and that is a
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
		// scopes rather than two, because a selection can now say "these
		// seconds of video, and leave what is heard alone" -- see cut_scope.go
		// for why that stopped being the same sentence as "these seconds".
		// Meaningless while aud names a recording, and cleared with it.
		pic bool
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
	// ...and whether it is the picture ALONE, which pastes silent. Kept here
	// for the same reason and read at exactly the same moment.
	copyPic bool
	// the selection band's own hold, the same shape as the edge's and the
	// effect's: whether the band is in hand, and whether the pointer is over
	// it. Its row is cut_selband.go.
	selOn     bool
	selHov    bool
	bandHov   bool // the pointer is over the green bar's clip, ends included
	killHovOn bool // ...and over a kept scene's ✕ (cut_segkill.go)
	killHov   int
	selCur    string // the cursor name the source area last asked for
	audCur    string // ...and the lanes
	scopeCur  string // ...and the seam between them; see setCursor
	thumbHt   int    // thumbnail height; the 🔍 buttons change it
	srcHt     int    // the height the source area was last asked for; see fitSrc
	playhead  float64
	hasPlay   bool
	player    *Player
	playVideo *tlVideo // which recording the preview is playing
	// the preview has been started and not stopped, which is what makes the run
	// bar its transport. Same rule as narrate and produce: a recording merely
	// LOADED -- which clicking the timeline does, just to show the frame there
	// -- must not take ▶ away from suggesting, which is what ▶ means here.
	started bool

	markIn, markOut float64 // editor-style in/out points, session time
	hasIn, hasOut   bool

	// the clip edge the right button has picked up: which segment, which side of
	// it, and whether this hold has moved anything yet. The undo snapshot is
	// taken on the first move, so picking an edge up and putting it back down is
	// not an edit. edgeOn rather than an index-or-minus-one because a zero value
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

	// the whole clip the right button has picked up, held the same way and for
	// the same reason: an edge is what you grab near a border, and a clip is what
	// you grab anywhere else on one. A left drag then slides it, keeping its
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
	audArea      *gtk.DrawingArea // the waveform lanes; hidden when there are none
	// the seam between them: ▲ the footage / ▼ one recording's sound, which is
	// what the selection is OF. See cut_scope.go.
	scopeArea *gtk.DrawingArea
	scopeHov  int
	total     *gtk.Label
	clock     *gtk.Label // the red line's time in numbers, beside the transport
	marks     *gtk.Label // the two marks in numbers, under the buttons that set them

	target *gtk.Entry
	inputs *gtk.Label // what this page reads, and what Suggest is sent
	out    *gtk.Label // what step3/ holds, the same line every other page shows

	// the form column beside the video (cut_form.go): its heading, the words
	// it shows when it is empty, the form in it and who to tell when that form
	// is taken out. formBox nil means no page has been built, which is what
	// every headless test is.
	formBox   *gtk.Box
	formHead  *gtk.Box
	formTitle *gtk.Label
	formIdle  *gtk.Label
	formCur   gtk.Widgetter
	formGone  func()

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

	undo []cutState // one snapshot per edit; every edit is reversible
	redo []cutState // what Undo took back, so Redo can put it in again
	base cutState   // the cut at the last checkpoint; Revert returns to this

	// the camera and the clock (cut_fx.go): the cut's aspect ratio ("" is
	// the source's own), the effects, and which one the right button holds.
	// Held the same way a clip is -- one thing held at a time, so taking hold
	// of an effect drops a held clip or edge and the other way round.
	aspect string
	fx     []cutFx
	// the preview plays the CUT rather than the recording: the stretches the
	// edit removed are skipped instead of played through, so ▶ shows what the
	// finished video will run. A view mode -- nothing here is saved, and the
	// track still draws every second of the session underneath.
	cutOnly bool
	cutBtn  *gtk.ToggleButton
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
	// this hold has moved its effect; the undo snapshot is taken on the first
	// move, so lifting an effect and putting it back is not an edit
	fxDirty bool
	// an effect is being dragged along its lane right now. The line follows a
	// dragged effect, and syncFxHold must not read that as the line walking
	// away from it -- see there.
	fxMoving bool
	// the last playhead read off the player, and the wall clock when it was
	// read. The camera runs on these rather than on the playhead itself; see
	// livePlayhead.
	posT  float64
	posAt time.Time
	// the highest time livePlayhead has answered since the clock was last
	// re-based: while the stream plays, the live clock never runs backward
	liveMax float64
	// what the next drag on the video draws: "zoom", "text" or "svg" while
	// one of the effect buttons is armed, "" when the video is just a picture.
	fxArm string
	// the drawing an armed "svg" is waiting to place -- chosen before the
	// drag, because a box means nothing until you know what goes in it
	fxSrc string
	// the drawings already rasterized for the preview, by file (fxsvg.go)
	svgs   map[string]*fxSVG
	fxArea *gtk.DrawingArea // the overlay on the preview (cut_fxview.go)
	// the pointer's current shape over that overlay, remembered so the motion
	// handler only touches the cursor when it actually changes
	fxCursor string
	// the live-zoom layer over the preview: the same video again, transformed
	// so the camera's window fills the output box while the stream plays
	fxZoom    *gtk.Fixed
	fxZoomPic *gtk.Picture
	// the stop effect's still over the preview, on its own Fixed so the
	// camera can move over it, and the frame it is showing (cut_fxstill.go)
	fxStillBox *gtk.Fixed
	fxStillPic *gtk.Picture
	fstill     *fxStill
	aspectDD   *gtk.DropDown // the toolbar's aspect choice
	aspectMu   bool          // the dropdown is being set by code, not by hand

	undoBtn, redoBtn, revertBtn *gtk.Button
	playBtn                     *gtk.Button // ▶/⏸ for the preview; drawn by syncPlayIcons
	insBtn                      *gtk.Button // ⧉ Insert, or ✎ Edit while a card is held
	copyBtn                     *gtk.Button // ⧉ Copy, greyed until there is a selection to take
	// ＋ Add: the footage's verb, greyed while the selection is a sound's (see
	// syncSelBtns). － Remove used to stand beside it; it is the ✕ on each kept
	// scene now (cut_segkill.go), and ⌦ for the things that have no ✕.
	addBtn *gtk.Button
}

// ---- data ------------------------------------------------------------------

// mmss is how this page says a duration. It was three identical closures in
// three functions before something outside them needed it too.
func mmss(t float64) string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }

func (a *App) cutDir() string  { return filepath.Join(a.outDir, "step3") }
func (a *App) cutPath() string { return filepath.Join(a.cutDir(), "cut.json") }

// reload rebuilds the timeline from the current selection + step outputs.
func (ed *cutEditor) reload() error {
	a := ed.a
	vids, auds := a.snapSources()
	if len(vids) == 0 {
		return fmt.Errorf("nothing to cut — no source on the Preprocessing step is marked as footage")
	}
	// same zero convention as session.tsv: min start over ALL sources
	zero := math.MaxFloat64
	type st struct {
		path  string
		start float64
	}
	var all []st
	for _, p := range append(append([]string{}, vids...), auds...) {
		s, err := sourceStart(p)
		if err != nil {
			return fmt.Errorf("cannot place %s in time: %w", baseName(p), err)
		}
		all = append(all, st{p, s})
		zero = math.Min(zero, s)
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
	ed.auds = masterLanes(ed.vids)
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
	ed.setAspect("")
	ed.syncButtons()
	if b, err := os.ReadFile(a.cutPath()); err == nil {
		var c struct {
			Segs   []cutSeg
			Aspect string
			Fx     []cutFx
		}
		if json.Unmarshal(b, &c) == nil {
			ed.segs = c.Segs
			ed.fx = migrateFx(c.Fx)
			ed.setAspect(c.Aspect)
		}
	}
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
			rows = loadSeg4(filepath.Join(a.outDir, "step1", base, "transcript.tsv"))
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

	// The envelopes, in the background and one goroutine each: decoding an hour
	// of audio takes seconds, and a page that waited for them would be a tab
	// that does not open. A lane whose envelope has not landed yet draws its
	// ground and no wave, and the redraw when it does is the whole of the
	// arrival -- there is nothing to recompute, because the audio is not part of
	// the timeline's geometry.
	if ed.waves == nil {
		ed.waves = map[string]*waveform{}
	}
	for _, au := range ed.auds {
		if _, ok := ed.waves[au.base]; ok {
			continue
		}
		au := au
		go func() {
			wf, err := loadWave(a.waveCache(), au.path, au.chans)
			glib.IdleAdd(func() {
				if err != nil {
					a.logf("no waveform for %s: %v", au.base, err)
					return
				}
				ed.waves[au.base] = wf
				ed.fitAudio() // it may have come back on fewer lanes than the probe promised
				ed.redrawTracks()
			})
		}()
	}

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

func (ed *cutEditor) relayout() {
	x := 0.0
	for i := range ed.vids {
		if i > 0 {
			x += gapPx
		}
		ed.vids[i].pxOrigin = x
		x += ed.vids[i].dur * ed.pps
	}
	ed.totalW = x
	if ed.srcArea != nil {
		// height only: the width is whatever the page gives us. The +8 is the
		// picture band's own breathing room; the lane below it is where the
		// camera and clock effects live (cut_fx.go).
		ed.fitSrc()
		ed.fitAudio()
		ed.fitScope() // the seam sits under the pictures, lanes or no lanes
		ed.syncScroll()
		ed.redrawTracks()
	}
	ed.updateTotal()
}

// picTop is where the picture band starts: under the ruler's clock and under
// the selection band that now sits below it.
func (ed *cutEditor) picTop() float64 { return float64(rulerH + selBandH) }

// hitPics is whether a y of the source area is on the picture band -- the
// thumbnails and the green over them. The two rows above it and the effects
// lane below it are their own objects with their own rules, so "is this press
// about the cut itself" is a question worth having one answer to.
func (ed *cutEditor) hitPics(y float64) bool {
	return y >= ed.picTop() && y < ed.picTop()+float64(ed.thumbHt)+4
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
	h := int(ed.picTop()) + ed.thumbHt + 8 + int(ed.fxLaneHeight())
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
	if v := ed.videoAt(t); v != nil && ed.player != nil {
		wasPlaying := ed.player.playing
		// before the seek, never after: a rate only takes hold at a seek, and
		// this is the seek. Setting it afterwards would need a second one.
		ed.player.SetRate(fxRateAt(ed.fx, t))
		if ed.playVideo == v {
			ed.player.SeekTo(t - v.start) // same file: cheap in-place seek
		} else {
			ed.playVideo = v
			// which recordings are under THIS piece of footage, and by how far
			// their clocks differ from its own -- both change with the file, so
			// they are settled before the file is
			ed.player.SetMix(ed.mixUnder(v))
			ed.player.PlaySegment(v.path, t-v.start, -1, wasPlaying)
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
func (ed *cutEditor) mixUnder(v *tlVideo) []mixTrack {
	var out []mixTrack
	for _, au := range ed.auds {
		if au.master || au.path == v.path {
			continue
		}
		if au.start+au.dur <= v.start || au.start >= v.start+v.dur {
			continue
		}
		out = append(out, mixTrack{path: au.path, delta: v.start - au.start, dur: au.dur})
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
	// Under ✂ Cut only the readout is the cut's own clock: how far into the
	// FINISHED video this is, which is the question that mode is asked. Same
	// format either way, so switching modes cannot shove the bar sideways --
	// the tooltip is what says which of the two clocks is being read.
	// ...unless the cut is empty: then its clock has no reading -- every second
	// of the session maps to 0:00.0 -- and a toolbar stuck on zero while the
	// red line moves answers nothing. The session clock is the one thing that
	// can still say where the line is, so it stays until there is a cut to read.
	t, tip := ed.playhead, ed.playheadTip()
	if ed.cutOnly && len(ed.segs) > 0 {
		t = cutPos(ed.segs, ed.playhead)
		tip = fmt.Sprintf("%s into the cut, of %s — the finished video's own clock "+
			"(✂ Cut only). Session time here is %s.",
			mmss(t), mmss(cutLen(ed.segs)), playheadClock(ed.playhead, ed.hasPlay))
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
		where = fmt.Sprintf("%s at %s", filepath.Base(v.path), fmtClock(ed.playhead-v.start))
		if v.fps > 0 {
			where += fmt.Sprintf(", frame %d", int(math.Round((ed.playhead-v.start)*v.fps)))
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
	local := math.Max(0, math.Min(v.dur, ed.playhead-v.start+float64(n)/v.fps))
	ed.playhead = v.start + local
	ed.reLive(ed.playhead) // a hand on the line: the live clock comes with it
	// the rate before the seek, never after -- it only takes hold at one, and
	// this is the seek. The same bargain setPlayhead makes.
	ed.player.SetRate(fxRateAt(ed.fx, ed.playhead))
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

// livePlayhead is the playhead with the time since it was last read added back
// on: where the picture is NOW, rather than where it was at the last tick.
//
// The page reads the player's clock every playTick milliseconds, which is the
// right rate for everything it drives except one thing. A camera glide is a
// continuous move, and sampling it ten times a second shows it as ten jumps --
// a one second transition arriving in ten steps. The render has no such
// problem: zoompan evaluates the same piecewise-linear path per frame, so what
// is choppy in the preview is smooth in the file. This closes the gap the
// other way, so that what you approve is what you get.
//
// The extrapolation is capped at one tick. A stall then shows as the picture
// standing still -- which it is -- instead of the camera sailing on and
// snapping back when the real position arrives.
//
// And it never runs backward while the stream plays. At a high rate the
// pipeline can fall behind the extrapolation -- ×4 is four seconds of footage
// decoded every second -- and each fresh position read then lands BEHIND the
// value already handed out. Passed on raw, that sawtooth reached everything
// drawn on this clock ten times a second: a title flicking on and off across
// its own edges, its fade jittering, the camera juddering mid-glide. The
// clamp holds the last answer until the real clock passes it again, and is
// re-based wherever the line is placed by hand (setPlayhead) or the stream
// stops being the clock at all.
//
// It is a clamp and not a ratchet, which is the difference between hiding
// jitter and hiding a seek. The mark may never stand more than one whole tick
// past the last position actually read: the sawtooth it exists to hide is a
// fraction of a tick, so it stays hidden, while a jump backward is seconds and
// cannot be. Unbounded, the mark was monotonic for the life of the page -- jump
// back over a title and the preview went on drawing it, at the alpha of a
// moment the line had already left, until playback climbed all the way to where
// it had been.
func (ed *cutEditor) livePlayhead() float64 {
	if ed.player == nil || !ed.player.playing || ed.posAt.IsZero() {
		ed.liveMax = ed.playhead // nothing to smooth; re-arm on the line itself
		return ed.playhead
	}
	d := time.Since(ed.posAt).Seconds()
	if d < 0 {
		d = 0
	}
	span := float64(playTick) / 1000
	if d > span {
		d = span
	}
	if v := ed.posT + d*ed.player.Rate(); v > ed.liveMax {
		ed.liveMax = v
	}
	if hi := ed.posT + span*ed.player.Rate(); ed.liveMax > hi {
		ed.liveMax = hi // a whole tick of headroom, and not one second more
	}
	return ed.liveMax
}

// reLive re-arms the live clock on t. All three of its parts move together or
// none of them do: the position the extrapolation runs from, the wall clock
// that position was read at, and the high-water mark that keeps the clock from
// running backward.
//
// Re-basing the mark alone was not enough, and the way it failed is the reason
// this exists. A seek does not stop playback when it lands in the file already
// open, so the very next read -- the overlay's, sixty times a second, long
// before the next 100ms tick rewrites the position -- extrapolated from the
// position the line had BEFORE the jump, found it higher than the freshly
// lowered mark, and set the mark back to it. From there the clock was stuck in
// the future: every effect between the line and where it had been was drawn as
// though the jump had never happened.
func (ed *cutEditor) reLive(t float64) {
	ed.liveMax, ed.posT, ed.posAt = t, t, time.Now()
}

// followPlayback keeps the red line on the player's clock while it runs;
// on pause the queries stop and the line simply stays put.
// syncPlayRate puts the preview on the clock the footage under the line runs
// on, so a slowed stretch is slow to watch and not just rose-tinted on the
// track. Called as the line moves under playback -- a rate that changes with
// nowhere to seek to has to be seeked to where the stream already is, which is
// the one thing SetRate deliberately will not do for itself.
//
// Every seek here is a flushing one, so each boundary costs a small hitch. An
// instant-rate-change seek would not, but support for it varies by element and
// a preview that silently keeps the old rate is worse than one that stutters
// for a frame at the edge of an effect.
func (ed *cutEditor) syncPlayRate() {
	if ed.player == nil || !ed.player.SetRate(fxRateAt(ed.fx, ed.playhead)) {
		return
	}
	if ed.player.playing {
		if pos, ok := ed.player.Position(); ok {
			ed.player.SeekTo(pos)
		}
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
		ed.playhead = ed.playVideo.start + pos
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
		ed.syncPlayRate()   // the line has crossed into or out of a speed effect
		ed.revealPlayhead() // playback runs the line off the view; recenter and follow
		ed.redrawTracks()
	}
	return true // keep the timer alive
}

func (ed *cutEditor) xOf(t float64) float64 {
	for _, v := range ed.vids {
		if t <= v.start+v.dur {
			if t < v.start {
				return v.pxOrigin
			}
			return v.pxOrigin + (t-v.start)*ed.pps
		}
	}
	return ed.totalW
}

func (ed *cutEditor) tAt(x float64) float64 {
	if len(ed.vids) == 0 {
		return 0 // nothing loaded: every x on an empty track is time zero
	}
	for i, v := range ed.vids {
		end := v.pxOrigin + v.dur*ed.pps
		if x < v.pxOrigin {
			if i == 0 {
				return v.start
			}
			return v.start // inside a gap: clamp to the next video's start
		}
		if x <= end {
			return v.start + (x-v.pxOrigin)/ed.pps
		}
	}
	last := ed.vids[len(ed.vids)-1]
	return last.start + last.dur
}

// tAtView is the same for an x on the widget, which is a window onto the
// timeline scrolled viewX px along it.
func (ed *cutEditor) tAtView(x float64) float64 { return ed.tAt(x + ed.viewX) }

// frameRange is which of a recording's frames drawTrack has to paint for the
// px window x0..x1: a half-open range walked in strides of step.
//
// The first index is snapped DOWN to a multiple of the stride, which is the
// point of doing this here rather than inline. Every frame is a candidate but
// only every step'th is drawn, so an unsnapped start would pick a different
// set of frames for every scroll position and the thumbnails would visibly
// reshuffle as the timeline moved under them.
func (v *tlVideo) frameRange(pps, x0, x1 float64, step int) (first, last int) {
	perFrame := pps * v.interval // px of timeline per frame
	first = max(0, int((x0-v.pxOrigin)/perFrame))
	first -= first % step
	last = min(len(v.frames), int((x1-v.pxOrigin)/perFrame)+1)
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

// videoAt is pickVideo over the timeline this editor is showing.
func (ed *cutEditor) videoAt(t float64) *tlVideo { return pickVideo(ed.vids, t) }

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
		i0 := int((t - snapTol - v.start) / v.interval)
		i1 := int((t + snapTol - v.start) / v.interval)
		for i := max(1, i0); i <= min(len(sc)-1, i1); i++ {
			if sc[i] > 2*mean && sc[i] >= sc[i-1] {
				try(v.start+float64(i)*v.interval, math.Min(1, sc[i]/(4*mean)))
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
	for _, v := range ed.vids {
		s := math.Max(t0, v.start)
		e := math.Min(t1, v.start+v.dur)
		if e-s >= minSegLn {
			out = append(out, cutSeg{S: s, E: e})
		}
	}
	return out
}

func (ed *cutEditor) addRange(t0, t1 float64) {
	ed.segs = append(ed.segs, ed.rangePieces(t0, t1)...)
	ed.coalesce()
	ed.persist()
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
	ed.layOver(cutSeg{S: t, E: t + dur, Ins: path, Mute: mute})
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
// it (step5, case s.audioIns()). Over footage the cut keeps, that is invisible
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
		// stands in for the same recording, because the scope was named once
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
func (ed *cutEditor) addSplice(path string, t, dur float64, mute bool) {
	if dur < minSegLn {
		dur = insDefault
	}
	ed.pushUndo()
	ed.segs = append(ed.segs, cutSeg{S: t, E: t, Ins: path, Dur: dur, Mute: mute})
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

// insName is what an insert is called on the track and in the status line: the
// file's name, or, for a copy, the footage it plays again -- a copy has no file
// to name, and the seconds are how the eye finds the original.
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
// the sound from the recording instead of from the file (step5, the isInsert
// case, which puts it in prodClip.snd exactly where an audio insert's file
// would go).
//
// playsSilent: the cut WAS opened for it, so there is nothing underneath to
// keep and no sound anywhere in the slot. clipInput reports the input as having
// no audio and the silence comes from anullsrc, the same way a held frame's
// does.
func (s cutSeg) keepsSoundUnder() bool { return s.isInsert() && s.Mute && !s.spliced() }
func (s cutSeg) playsSilent() bool     { return s.isInsert() && s.Mute && s.spliced() }

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
	// the dialog asks this one only when the scope left it open (insMode.
	// askMute); otherwise m.mute came back exactly as the card handed it over
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
		if s.S < t0 && t0-s.S >= minSegLn {
			out = append(out, cutSeg{S: s.S, E: t0})
		}
		if s.E > t1 && s.E-t1 >= minSegLn {
			out = append(out, cutSeg{S: t1, E: s.E})
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
// borders are the handles for that, the right button picks one up, and until it
// is put down the frame buttons move it a frame at a time instead of the
// playhead -- the same gesture, aimed at the thing you just said you were
// working on.
//
// Picking up and moving are two buttons, deliberately. The right button ONLY
// selects: it says which edge you mean and then lets go, so the choice survives
// while your hand does something else, and no right-hand twitch can trim a clip
// by a second. Moving it is then ‹f and f› for a frame at a time, or a left drag
// on the white bar for a sweep -- and both of them put the picture on the frame
// the edge is now at, because an edge is judged by what it cuts on and not by
// where it reads on a ruler.

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
	ed.a.setStatus(fmt.Sprintf("clip %d: %s – %s (%.1f s)", ed.edgeSeg+1,
		fmtClock(s.S), fmtClock(s.E), s.E-s.S))
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
// The same two-button gesture as an edge, aimed one level up. The right button
// presses near a border pick that border up; the right button pressed anywhere
// ELSE on a clip picks up the whole clip, and a left drag then slides it with its
// length intact. That is the edit the page had no spelling for: "this scene, but
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
	ed.a.setStatus(fmt.Sprintf("clip %d: %s – %s (%.1f s)", ed.segSel+1,
		fmtClock(s.S), fmtClock(s.E), s.length()))
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
	if ed.scopeArea != nil {
		ed.scopeArea.QueueDraw()
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
// and the aspect. One snapshot, not three -- an edit that changed the camera
// and an edit that changed the cut undo the same way, and an undo that
// restored the segments but left the effects would un-mix two lists the user
// edited as one page.
type cutState struct {
	segs   []cutSeg
	fx     []cutFx
	aspect string
}

func (ed *cutEditor) snapshot() cutState {
	return cutState{append([]cutSeg(nil), ed.segs...), append([]cutFx(nil), ed.fx...), ed.aspect}
}

func (ed *cutEditor) restore(st cutState) {
	ed.segs = st.segs
	ed.fx = st.fx
	ed.dropFx() // whatever was held may not exist in the restored list
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
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameState(a, b cutState) bool {
	if !sameCut(a.segs, b.segs) || a.aspect != b.aspect || len(a.fx) != len(b.fx) {
		return false
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

// syncPlayBtn greys ▶ out when there is nothing it could honestly play: under
// ✂ Cut only the preview is the finished video, an empty cut IS an empty
// video, and with no clips there are no gaps to skip either -- the preview
// would run the whole recording against the mode's one promise. Synced from
// the mode's toggle and from syncButtons, which every edit passes through, so
// adding the first clip wakes the button up again.
func (ed *cutEditor) syncPlayBtn() {
	if ed.playBtn == nil {
		return
	}
	ed.playBtn.SetSensitive(!ed.cutOnly || len(ed.segs) > 0)
}

// syncInsertBtn tells the Insert button which of its two jobs it is doing. Held
// card: it opens that card, so it says Edit. Anything else: it puts a new one in
// at the playhead.
func (ed *cutEditor) syncInsertBtn() {
	if ed == nil || ed.insBtn == nil {
		return
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
		"otherwise the file's own. With the selection on ▼ (the strip above the lanes) " +
		"it offers sounds instead, and lays one over those seconds without moving the " +
		"picture. A card (tier.svg, s.svg … in assets) or any SVG " +
		"with {{holes}} in it asks what to put on it first. Right-click a card on " +
		"the track to hold it, and this becomes Edit.")
}

// droppedSpans is the session time this cut throws away, as stretches: the
// holes between the kept clips, plus whatever hangs off either end of each
// recording. Only the ✂ Cut only scrim uses it -- everywhere else "dropped" is
// simply the absence of green -- so it is built on demand rather than kept.
//
// Inserts are skipped rather than counted as keeping their span: a spliced card
// occupies no session time (S == E) and an overwriting one sits inside footage
// that is kept anyway, so neither one opens or closes a hole.
func (ed *cutEditor) droppedSpans() [][2]float64 {
	var out [][2]float64
	for _, v := range ed.vids {
		end := v.start + v.dur
		t := v.start
		for _, s := range ed.segs {
			if s.isInsert() || s.E <= t || s.S >= end {
				continue
			}
			if s.S > t {
				out = append(out, [2]float64{t, math.Min(s.S, end)})
			}
			t = math.Max(t, s.E)
		}
		if t < end {
			out = append(out, [2]float64{t, end})
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
		if !s.isInsert() && film >= 0 && s.S <= out[film].E+0.25 && allSpliced(out[film+1:]) {
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
	b, _ := json.MarshalIndent(struct {
		Segs   []cutSeg `json:"segs"`
		Aspect string   `json:"aspect,omitempty"`
		Fx     []cutFx  `json:"fx,omitempty"`
	}{ed.segs, ed.aspect, ed.fx}, "", "  ")
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
	// through the same pipe the render reads (splitSpliced + applyFx), so the
	// number under the tracks is the length of the video Produce would make
	for _, s := range applyFx(splitSpliced(ed.segs), ed.fx) {
		sum += s.length() // a spliced card takes no session time and still runs
	}
	return sum
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
		detail += fmt.Sprintf("\n\nstep2/transcript/session.txt — %d kB, sent whole with the cut prompt",
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

	for vi, v := range ed.vids {
		if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0-gapPx {
			continue // this recording is off screen entirely
		}
		// hatched hole before this video
		if vi > 0 {
			hatchBand(cr, v.pxOrigin-gapPx, gapPx, top, th+4)
		}
		// thumbnails, only the ones in view
		step := max(1, int(th*1.78/(ed.pps*v.interval)+0.5))
		first, last := v.frameRange(ed.pps, vx0, vx1, step)
		for i := first; i < last; i += step {
			t := v.start + float64(i)*v.interval
			pb := ed.thumb(v.frames[i])
			if pb == nil {
				continue
			}
			x := ed.xOf(t)
			w := math.Min(float64(pb.Width()), float64(step)*v.interval*ed.pps)
			gdk.CairoSetSourcePixbuf(cr, pb, x, top+2)
			cr.Rectangle(x, top+2, w, th)
			cr.Fill()
		}

		// file boundary + name
		cr.SetSourceRGB(0.9, 0.7, 0.2)
		cr.SetLineWidth(2)
		cr.MoveTo(v.pxOrigin, top)
		cr.LineTo(v.pxOrigin, top+th+4)
		cr.Stroke()
		cr.SetFontSize(10)
		plateText(cr, v.pxOrigin+4, top+12, v.base)
	}

	// ruler
	stepS := tickStep(ed.pps)
	cr.SetFontSize(9)
	for _, v := range ed.vids {
		if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0 {
			continue
		}
		from := math.Max(v.start, v.start+(vx0-v.pxOrigin)/ed.pps)
		to := math.Min(v.start+v.dur, v.start+(vx1-v.pxOrigin)/ed.pps)
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
		cr.LineTo(x, top+th+4)
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
		cr.LineTo(x, top+th+4)
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
		cr.SetSourceRGBA(0.2, 0.8, 0.3, 0.30)
		cr.Rectangle(x0, top, x1-x0, th+4)
		cr.Fill()
		// hard green edges, boundary-marker style
		cr.SetSourceRGB(0.15, 0.85, 0.25)
		cr.SetLineWidth(2)
		for _, x := range []float64{x0, x1} {
			cr.MoveTo(x, top)
			cr.LineTo(x, top+th+4)
			cr.Stroke()
		}
	}

	// Under ✂ Cut only the dropped stretches are dimmed rather than merely left
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
			cr.Rectangle(x0, top, x1-x0, th+4)
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
		cr.Rectangle(x0, top, x1-x0, th+4)
		cr.Fill()
		if s.spliced() {
			// hatched over the violet, so the marker says both things at once: a
			// card goes in here, and the footage stops for it
			hatchStrokes(cr, x0, x1-x0, top, th+4)
		}
		cr.SetSourceRGB(0.75, 0.6, 1)
		cr.SetLineWidth(2)
		for _, x := range []float64{x0, x1} {
			cr.MoveTo(x, top)
			cr.LineTo(x, top+th+4)
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
		cr.Rectangle(x0, top, x1-x0, th+4)
		cr.Fill()
	}

	// the ✕ on every kept scene, over everything else the picture band draws:
	// a control the inserts or the ✂ Cut dimming could paint over would be a
	// control that is there on some frames and not others (cut_segkill.go)
	ed.drawSegKill(cr, vx0, vx1)

	// the effects lane, under the picture band (cut_fx.go)
	ed.drawSelBand(cr, vx0, vx1)
	ed.drawFxLane(cr, vx0, vx1)

	// the clip a double click has picked up, outlined in white. The edge marker
	// below says which BORDER is about to move; this says which whole clip is,
	// and they are the same gesture at two scales, so they are the same ink.
	if s := ed.heldSeg(); s != nil && !s.audioIns() {
		// ...unless the held clip is a sound: its marker is in the lanes, and
		// the outline goes where the marker is (drawAudio)
		x0, x1 := ed.segSpan(*s)
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.SetLineWidth(2)
		cr.Rectangle(x0+1, top+1, x1-x0-2, th+2)
		cr.Stroke()
	}

	// selection rubber band
	if ed.sel.active {
		a, b := ed.sel.t0, ed.sel.t1
		if b < a {
			a, b = b, a
		}
		x0, x1 := ed.xOf(a), ed.xOf(b)
		cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.45)
		cr.Rectangle(x0, top, x1-x0, th+4)
		cr.Fill()
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
		cr.LineTo(x, top+th+4)
		cr.Stroke()
		cr.SetSourceRGBA(1, 1, 1, 0.6)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+th+4)
		cr.Stroke()
	}

	// the clip edge that is held: white, wider than the green border it sits on,
	// with a head each way to say that it moves. Drawn last, so nothing painted
	// over it can hide what is about to change under the next ‹f.
	if ed.edgeOn && ed.edgeSeg < len(ed.segs) {
		x := ed.xOf(ed.edgeTime())
		cr.SetSourceRGB(1, 1, 1)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+th+4)
		cr.Stroke()
		for _, d := range []float64{-1, 1} {
			cr.MoveTo(x, top+th/2-5)
			cr.LineTo(x+7*d, top+th/2)
			cr.LineTo(x, top+th/2+5)
			cr.ClosePath()
			cr.Fill()
		}
	}

	// the red select point / playhead
	if ed.hasPlay {
		x := ed.xOf(ed.playhead)
		cr.SetSourceRGB(0.9, 0.15, 0.15)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, float64(h))
		cr.Stroke()
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

func (ed *cutEditor) toggle() {
	// ▶ is grey in this state (syncPlayBtn), but the button is not the only way
	// in -- a click on the picture and the run bar land here too, and every way
	// in has to refuse for the same reason
	if ed.cutOnly && len(ed.segs) == 0 {
		ed.a.setStatus("the cut is empty — add a clip to play it, or turn ✂ Cut only " +
			"off to play the recording")
		return
	}
	if ed.player == nil {
		return
	}
	// With a clip edge held, ▶ plays from the EDGE. It is the thing you are
	// working on and the only reason to press play while holding it is to watch
	// what you have just trimmed to; starting from wherever the playhead was
	// last left meant winding back to the boundary by hand every time. Only on
	// the way into playing -- ⏸ has to stop where it is, not jump.
	if ed.edgeOn && !ed.playing() {
		ed.setPlayhead(ed.edgeTime())
	} else if s := ed.heldSeg(); s != nil && !ed.playing() {
		ed.setPlayhead(s.S) // a held clip plays from its own start, for the same reason
	}
	if !ed.playing() {
		// last, because the two branches above may themselves have put the line
		// in a gap -- an edge's own time is the first frame of the stretch after
		// it, which under cut-only is exactly what is not to be played
		ed.cutOnlySnap()
	}
	ed.player.Toggle()
	ed.started = ed.started || ed.player.Playing()
	ed.a.updateRunControls()
}

func (ed *cutEditor) stop() {
	if ed.player != nil {
		ed.player.Stop()
	}
	ed.started = false // ⏹ hands ▶ back to the step's own job, suggesting
}

// ---- page ------------------------------------------------------------------

func (a *App) buildCut() gtk.Widgetter {
	ed := &cutEditor{a: a, pps: 4, thumbHt: 64, jumped: -1,
		thumbs: map[string]*gdkpixbuf.Pixbuf{}}
	a.ed = ed
	if p, err := NewPlayer(); err == nil {
		ed.player = p // the preview above the tracks; independent of Review's
		p.OnState = a.updateRunControls
		p.OnError = a.playerErr("the cut preview")
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
	// The four effects behind one dropdown -- a menu of verbs, not a state:
	// picking one fires it and the control snaps back to its label, so the
	// notify below re-enters once with 0 and leaves.
	fxKinds := []string{"✚ Effect", "⊕ Zoom", "❝ Text", "▨ SVG", "⏩ Speed"}
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
		"the box under the pointer can be dragged and its border resized; right-click it for " +
		"its numbers, or use the marks in the lane below the track")
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
		}
	})

	// one button that is ▶ or ⏸ depending on the preview, like every other play
	// button in the app (syncPlayIcons in pipeline.go keeps it drawn)
	ed.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	ed.playBtn.SetTooltipText("play or pause the preview at the playhead")
	ed.playBtn.ConnectClicked(ed.toggle)
	// with something held -- a clip edge, a whole clip, an effect (right-click
	// one) -- these move that instead of the playhead. Said on every one of
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
	// The one control on this bar that changes what ▶ MEANS. Off, the preview
	// is the recording: every second of it, removed stretches included, which
	// is what you want while deciding where the cuts go. On, it is the cut --
	// the gaps are jumped, the effects are on the picture, and what runs is
	// what Produce will make. Nothing about it is saved.
	ed.cutBtn = gtk.NewToggleButton()
	ed.cutBtn.SetChild(gtk.NewLabel("✂ Cut only"))
	ed.cutBtn.SetTooltipText("play the CUT instead of the recording: the removed stretches " +
		"are skipped, so ▶ runs the finished video. The clock switches to the cut's " +
		"own time with it. Changes nothing that is saved.")
	ed.cutBtn.ConnectToggled(func() {
		ed.cutOnly = ed.cutBtn.Active()
		ed.jumped = -1
		ed.syncPlayBtn() // an empty cut is nothing to play; see that function
		if ed.cutOnly {
			// the mode promises kept material, so it delivers one immediately
			// rather than at the next tick
			ed.cutOnlySnap()
			if len(ed.segs) == 0 {
				ed.a.setStatus("preview is the cut — and the cut is empty, so ▶ has " +
					"nothing to play until a clip is added")
			} else {
				ed.a.setStatus("preview is the cut — removed stretches are skipped and the " +
					"clock reads the finished video's own time")
			}
		} else {
			ed.a.setStatus("preview is the recording again — everything plays, cuts and all")
		}
		ed.showTime()
		ed.redrawTracks()
	})

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

	// The three prompts this page sends, in the order it sends them, behind one
	// dropdown in the bar. What Suggest is told is the rules plus what this
	// session was; the audit reads that suggestion back; the effects call is
	// made last and only by the Shorts style, once the audit has settled the
	// segments. All three are read once and then left alone for the rest of a
	// project, which is a poor trade for the half of the top row they used to
	// hold permanently -- so they open in the column that half became
	// (cut_form.go), one at a time, on request.
	promptRow := a.promptBar(ed,
		promptSlot{"cut", "Cut",
			"The rules, plus what this session was and what matters in it"},
		promptSlot{"audit", "Audit",
			"How the suggestion is read back: what counts as ending too early, and how readily a segment is dropped"},
		promptSlot{"effects", "Effects",
			"Only for the YouTube Shorts style: how the finished cut is decorated — zooms, speeds and captions, placed inside the final segments"})
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
	bar.Append(col(linked(ed.playBtn, prev5, prevF, nextF, next5), ed.clock))
	bar.Append(ed.cutBtn)
	bar.Append(rule())
	bar.Append(col(tgtBox, tgtLbl))
	bar.Append(col(linked(add, ed.copyBtn, ins), ed.marks))
	bar.Append(ed.aspectDD)
	bar.Append(fxDD)
	bar.Append(linked(ed.undoBtn, ed.redoBtn, ed.revertBtn))
	// still on the side of the rule where things change the cut: what the
	// prompts say is what Suggest sends, and the ✎ in the menu is the only
	// standing sign that this project has changed one
	bar.Append(promptRow)
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
	ed.scopeArea = ed.newScopeArea()
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
			// the ✕ in a kept scene's corner, asked before the borders: it is
			// drawn hard against the scene's right edge, so the same press
			// would otherwise be read as taking hold of that border to trim it
			if area == ed.srcArea && ed.hitPics(y) {
				if i := ed.segKillAt(x+ed.viewX, y); i >= 0 {
					ed.killSeg(i)
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
			_ = dragStartY
			_, _, _ = hadSel, selT0, selT1
			if trimming {
				trimming = false
				if ed.edgeDirty {
					ed.persist() // the drag is over: this is the cut that goes on disk
					// the picture lands exactly where the edge did, throttling or
					// no throttling, so what you trimmed to is what is on screen
					// and the next ‹f is judged against it. Only when something
					// actually moved: a press that merely picked the border up is
					// a choice, and a choice does not move the red line (pickAt).
					ed.showEdge(false)
				}
				ed.edgeStatus()
				return
			}
			if moving {
				moving = false
				if ed.segDirty {
					ed.persist()
					ed.segDirty = false
				}
				ed.showSeg(false)
				ed.segStatus()
				return
			}
			if ed.fxMoving {
				ed.fxMoving, fxPart = false, fxWhole
				if ed.fxDirty {
					ed.persist()
					ed.fxDirty = false
				}
				ed.fxStatus()
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
			ed.setPlayhead(ed.tAtView(dragStartX))
		})
		area.AddController(drag)

		// Picking up a whole clip is the second click of a double click. It used
		// to be the right button, and the right button is gone: every other
		// thing on this page is taken hold of by hovering it and pressing, and a
		// border you had to name with a different button first was the one
		// exception -- you could see the edge under the pointer and still not be
		// able to grab it.
		//
		// A clip cannot follow the border onto the single press, because over
		// the cut that press is already how you put the red line somewhere, and
		// a gesture that both navigates and picks things up is a gesture you can
		// use for neither. So: click to go there, click again to take the clip
		// that is there. What each press means is pickAt, where the order of the
		// questions is written down with its reasons.
		//
		// The selection row is not part of this: up there the left button does
		// the whole job. In the effects lane the second click means ✎ Edit --
		// holding, sliding and resizing are all on the single press, so the
		// double click is free, and "click it again to open its numbers" is
		// what every file manager has taught the hand to expect.
		pick := gtk.NewGestureClick()
		pick.SetButton(gdk.BUTTON_PRIMARY)
		pick.ConnectPressed(func(n int, x, y float64) {
			if n < 2 {
				return
			}
			area.GrabFocus()
			if area == ed.srcArea && ed.fxHitLane(y) {
				if i := ed.fxIndexAt(x+ed.viewX, y); i >= 0 {
					ed.holdFx(i)
					// on an idle, not inline: the dialog must not open in the
					// middle of the press it is answering, or the release
					// never reaches the drag gesture underneath
					glib.IdleAdd(func() { ed.a.editFx() })
				}
				return
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

	tracks := gtk.NewBox(gtk.OrientationVertical, 4)
	tracks.Append(ed.srcArea)
	tracks.Append(ed.scopeArea) // the seam, and the handle that names it
	tracks.Append(ed.audArea)   // under the footage: the footage is the master
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

	// Same line steps 1 and 2 end on, in the same place: what this step wrote,
	// and a way into the folder. ed.total above says what the editor holds;
	// this says what is actually saved, which is the thing the next step reads.
	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("step3/ — the cut, as cut.json")
	openOut.ConnectClicked(func() { a.openFolder(a.cutDir()) })
	ed.out = gtk.NewLabel("")
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.SetHAlign(gtk.AlignEnd)
	outRow.Append(outLbl)
	outRow.Append(openOut)
	outRow.Append(ed.out)
	bottom.Append(outRow)
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
		// the arrows are the frame buttons for the hand that is already on the
		// mouse, and they exist ONLY while an edge or a clip is held: unheld
		// they are the focus keys GTK expects them to be
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

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(pane)
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
// The gaps between recordings are drawn at a fixed width and do not shrink with
// the zoom, so they come off the width the footage may use. Dividing the window
// by the duration alone -- which is what this did -- left the fully zoomed-out
// timeline wider than its window by every gap in it, so the scrollbar stayed
// and it still slid, which reads as a timeline hiding something off to the
// right when there is nothing out there at all.
func (ed *cutEditor) minPps() float64 {
	return fitPps(ed.viewW, ed.totalDur(), len(ed.vids))
}

// fitPps is that floor without a widget in the way: the zoom at which dur
// seconds spread over n recordings come to exactly view pixels, gaps and the
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
	if len(ed.vids) == 0 {
		return 0
	}
	v := ed.vids[len(ed.vids)-1]
	return v.start + v.dur
}

func (ed *cutEditor) totalDur() float64 {
	d := 0.0
	for _, v := range ed.vids {
		d += v.dur
	}
	return d
}

// updateCutInfo (re)loads the editor when its inputs exist. It is the ONLY
// thing that fills this page -- buildCut makes an empty one -- so anything
// that changes what Preprocessing wrote has to end up here, or the tracks go on
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
func (ed *cutEditor) clearTracks() {
	if len(ed.vids) == 0 && len(ed.segs) == 0 {
		return
	}
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
			"press ▲ on the strip above the lanes to point it at the picture", ed.sel.aud))
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
	ed.addRange(ed.sel.t0, ed.sel.t1)
	ed.sel.active = false
	ed.clearMarks()
	a.setStatus("added — ↶ Undo (Ctrl+Z) takes it back")
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
	ed.copyAud, ed.copyPic = ed.sel.aud, ed.selPic()
	ed.syncInsertBtn()
	if ed.copyAud != "" {
		a.setStatus(fmt.Sprintf("copied %.1f s of sound from %s (%s – %s) — click the timeline "+
			"where it goes, then ⧉ Paste lays it over the footage there. Esc drops the copy",
			ln, ed.copyAud, mmss(t0), mmss(t0+ln)))
		return
	}
	if ed.copyPic {
		a.setStatus(fmt.Sprintf("copied the picture of %s – %s (%.1f s), without the sound "+
			"filmed with it — click the timeline where it goes, then ⧉ Paste splices those "+
			"frames in silent. Esc drops the copy", mmss(t0), mmss(t0+ln), ln))
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
	ed.addSplice(fmt.Sprintf("%s%.3f", copyScheme, ed.copyFrom), ed.playhead, ed.copyLen, ed.copyPic)
	ed.copyOn = false
	ed.syncInsertBtn()
	what := "footage"
	if ed.copyPic {
		// a paste that came out silent and said nothing about it would read as
		// a fault in the render rather than as the scope the copy was taken at
		what = "silent picture"
	}
	a.setStatus(fmt.Sprintf("pasted %.1f s of %s from %s at %s — the cut is now %s (was %s) "+
		"— ↶ Undo takes it back", ed.copyLen, what, mmss(ed.copyFrom), mmss(ed.playhead),
		mmss(ed.cutLen()), mmss(was)))
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
	// recording did starts the sound at the file's own beginning: there is
	// nothing earlier to play, and refusing the paste over a second of lead-in
	// nobody selected on purpose would be the worse answer.
	ss := math.Max(0, ed.copyFrom-au.start)
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
// ＋ Add is greyed while the selection is a sound's. It chooses which FOOTAGE
// the cut keeps, and footage here is picture and the sound filmed with it in
// one piece: there is no way to keep the sound and drop the picture, so on a ▼
// selection it has nothing it could honestly do. Greyed rather than left
// quietly cutting the picture, because the strip above the lanes has just said
// this selection is about sound, and a button acting on the other thing would
// make that a lie.
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
				"'s sound — press ▲ on the strip above the lanes to point it at the picture"
		}
		ed.addBtn.SetTooltipText(tip)
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
	// the scope is read HERE and not in the callback, beside the seconds and for
	// the same reason: the chooser is a window the hand can reach around, and
	// the file that comes back has to be placed the way the selection read when
	// the button was pressed.
	mute, lane := ed.sel.active && ed.selPic(), ""
	if ed.sel.active {
		at = math.Min(ed.sel.t0, ed.sel.t1)
		want = math.Abs(ed.sel.t1 - ed.sel.t0)
		lane = ed.sel.aud
	}

	// what the chooser admits follows the scope. A selection in a lane is about
	// sound, and offering it a tier card there would be offering to put a
	// picture where the hand pointed at a waveform; a selection scoped to the
	// picture alone is the mirror, and a sound is the one thing that cannot be
	// laid over frames while leaving the sound under them alone. Footage in one
	// piece, and no selection at all, are offered everything.
	title, name, exts := "Insert a clip, image, animation or sound",
		"Video, image, SVG or audio", insExts
	switch {
	case ed.sel.active && ed.selSnd():
		title, name, exts = "Insert a sound over the selected seconds", "Audio", audExts
	case mute:
		title, name, exts = "Insert a clip or image over the selected picture",
			"Video, image or SVG", picExts
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
		m := insMode{dur: want, splice: want < minSegLn, mute: mute, lane: lane}
		if m.dur < minSegLn {
			m.dur = a.insertLength(path)
		}
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
	// whether it brings its own sound. Not asked in the dialog: it is the
	// selection's scope, decided before the chooser opened (▲▲ picture alone),
	// and asking it twice would let the two answers disagree. See cutSeg.Mute
	// for the two things it means, which mode already says which of.
	mute bool
	// which recording a sound is being put in place of, read from the same
	// scope and for the same reason (▼). See cutSeg.Lane.
	lane string
	// the one case the scope does NOT settle mute, so the dialog has to ask:
	// picture-and-sound (▲▼), over footage that has something to hear, with a
	// file that brings no sound of its own. "Replace both" is what ▲▼ means,
	// but there is nothing to replace the sound WITH -- silence and carry-on
	// are both honest readings and only the hand knows which was meant. See
	// cutEditor.soundOpen, which is the whole of the condition.
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
	case m.splice:
		a.ed.addSplice(rel, at, m.dur, m.mute)
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
		a.logf(">>> put %s in %s — the built-in cards. They are ordinary files: edit them, "+
			"or keep them as they are and fill them in when you insert one.",
			strings.Join(wrote, ", "), dir)
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
	over.SetGroup(between) // one group of two is a pair of radio buttons
	between.SetActive(m.splice)
	over.SetActive(!m.splice)
	between.SetTooltipText("The footage is cut at this point, the card plays, and the footage " +
		"carries on with the very next frame. The finished video is longer by exactly the card, " +
		"and its sound is silent under the card and resumes where it stopped.")
	over.SetTooltipText("The card is on screen instead of those seconds of session, which are " +
		"gone from the cut exactly as Remove would take them. The video is no longer than it was.")
	// the one question the scope could not settle, asked here and only here:
	// this file has no sound and there IS something to hear under the seconds
	// it is about. Default kept rather than silent, for the same reason the
	// rule reads "an insert replaces what it brings, and nothing else" -- a
	// file with no sound replaces no sound. A tick and not a pair of lines,
	// because unlike over-versus-between this genuinely is a preference: both
	// answers are ordinary, and neither eats a stretch of the session.
	keep := gtk.NewCheckButtonWithLabel(
		"Keep the sound running under it — only the picture is replaced")
	keep.SetActive(true)
	keep.SetTooltipText("This file brings no sound of its own. Ticked, the picture is " +
		"replaced and everything that was audible in those seconds — the capture's own " +
		"track and every separate recording — carries on underneath. Unticked, the " +
		"seconds go quiet.")
	keep.SetVisible(m.askMute)
	// nothing is underneath a card the footage was cut open for, so there is
	// nothing for it to keep: the question is dead the moment BETWEEN is picked
	syncKeep := func() { keep.SetSensitive(!between.Active()) }
	between.ConnectToggled(syncKeep)
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
		// mute and lane are the SCOPE's answers and ride through untouched --
		// they were settled before the chooser opened and nothing in this
		// window asks about them again. The one exception is the tick above,
		// which exists precisely because the scope had no answer to give.
		out := insMode{splice: between.Active(), dur: m.dur,
			mute: m.mute, lane: m.lane, askMute: m.askMute}
		if v, err := strconv.ParseFloat(strings.TrimSpace(secs.Text()), 64); err == nil && v >= minSegLn {
			out.dur = v
		}
		if m.askMute && !out.splice {
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

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	btns.Append(cancel)
	btns.Append(insert)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(sub)
	box.Append(grid)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	box.Append(between)
	box.Append(over)
	box.Append(keep)
	box.Append(secBox)
	box.Append(btns)
	form.showForm(verb+" "+filepath.Base(path), box, nil)
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
			a.logf(">>> %s asks for a CSS animation whose @keyframes are not in the file — "+
				"it will be a still. Both are read: @keyframes and SMIL (<animate>, "+
				"<animateTransform>).", insBase(path))
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
		append([]cutFx(nil), ed.base.fx...), ed.base.aspect})
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
			"— press ▲ on the strip above the lanes to point it at the picture",
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
