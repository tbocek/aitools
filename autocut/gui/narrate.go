package main

// Narrate / Voice. Who speaks, on top (narrate_voice.go), and what they
// say, below: the lines in a vertical list -- a clip may carry several, each
// "[emotion @seconds] words" in one editable box -- next to a preview player
// with a seek slider. Clicking a row jumps the video there (play state
// preserved). ▶ on the run bar is the INITIAL fill: it writes the narration
// once (and again only if the cut moved) and speaks it; after that the lines
// are edited by hand -- text, delivery, placement, add and remove -- and the
// speak button voices whatever changed.
//
// The preview plays the CUT: it skips what Cut removed instead of running on
// into it, and when it reaches a line that has not been spoken yet it holds the
// picture, speaks the line, and carries on. Waiting is the point -- a preview
// that runs a clip mute is a preview of a video nobody is going to make.
//
// Emotions are taken from how the moment was actually spoken -- the generator
// sees the original lines and the events -- and then heightened; the user has
// the last word per entry.
//
// The narration is mixed OVER the clip's own audio rather than replacing it
// (Produce ducks the original to a fifth and leaves it there), which is the fact
// the prompt is written around: whatever was said in the clip is still audible,
// so narration that quotes it, or says it again in other words, is heard twice.
// The prompt used to ask for exactly that -- "reuse quotable lines VERBATIM" --
// and the result was a narrator reading the transcript back over the people
// saying it.
//
// Voice on the fly (narrate_tts.go): audio.cpp's own audiocpp_server keeps
// IndexTTS2 loaded, so per-line synthesis skips the model reload. This is the
// same HTTP API the audio.cpp WebUI proxies to, called the same way (the
// reference goes up first and voice_ref names where the server put it, see
// serverFile). Autocut uses whatever server is listening
// (AUDIOCPP_SERVER or AUTOCUT_TTS_URL, default 127.0.0.1:8765) and never starts
// one itself.
// Synthesized lines are cached by hash of (voice, text, emotion). The pitch
// knob sits with the voice at the top of the page: it moves the reference wav
// before it is cloned, so it changes who is speaking rather than transposing
// what was spoken.
//
// narrate/narration.json      entries
// narrate/voice_ref_base.wav  the reference as chosen or cut
// narrate/voice_ref.wav       ...shifted by the reference pitch: the server's input
// narrate/tts/<hash>.wav      synthesis cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	ttsPort = 8765
	// how hard the emotion is blended into the voice (IndexTTS2 emo_alpha).
	// 0.6 read "angry" as mildly put out; at 0.85 it actually shouts. Part of
	// the synthesis cache key (ttsWav): a changed blend must re-speak, or half
	// the narration stays at the old intensity.
	emoAlpha = "0.85"
	// how far ahead of a line a row's ▶ -- and a click on the row -- drops the
	// preview, so the moment the line was placed on can be watched arriving
	// instead of judged from a standing start. leadIn gives these seconds back
	// when they turn out to belong to the line above.
	narrRunIn = 3.0
)

// One paragraph or bullet per line, unwrapped: see describeSystem.
//
// Short on purpose, and numbered. This ran on a 27B model that had been given
// eight hundred words of nuance and answered with captions -- terse, formal,
// referring to things it had been told not to name. A small model reads a long
// rule list as a list of prohibitions and writes the safest thing that breaks
// none of them, which is nothing anybody would watch. Rules it can hold, and
// one worked example of the voice, do more than any amount of explaining: the
// example below is the single most load-bearing part of this prompt.
//
// It is commentary, not memoir, and that is the whole of the second rewrite.
// Told it had been there and that this had happened to it, the model wrote a
// man remembering his own body -- "I'm already spinning", "my hands are not
// listening", "I keep saying up" -- six entries out of nine opening with I'm,
// and every clip a summary of what it had cost him. Watch what this actually
// competes with on YouTube and the voice is doing the opposite: it is watching
// the picture WITH you, in the present.
//
// The third rewrite is the amount. Asked for a verdict on every beat and given
// a budget of 2.5 words a second -- a speaking rate -- the model filled every
// clip wall to wall, which is a voice that never stops and, worse, always
// sounds the same: a paragraph starting at 0.3 s and running to the end,
// whatever the clip was doing. The pause is what makes a voice-over sound like
// somebody watching. encodeClip lays the line at the head of the clip and
// leaves the rest to the game, so the pauses are simply the words nobody spent:
// short lines, some clips with no line at all, and the gaps come out different
// lengths on their own, because the clips are different lengths and only some
// of them are worth talking over. Hence narrBudget, the empty text "" rule, and
// the worked example of one.
//
// And the ratio is taught by the examples, not the rules: told "several clips
// should get one" and shown two examples of which one was "", the model
// returned five silent clips out of nine -- it imitated the fifty-fifty split
// it was shown. So the rule says one or two per video, and the "" example is
// one of three.
//
// "at" is the other half of matching the video. The writer places each line on
// the second it is about; the render starts it there (encodeClip's adelay) and
// so does the preview (entryAt). That is what lets a line about the chest wait
// for the chest, and the sign-off close the video instead of playing 30 s
// before the end of the last clip.
//
// And the worked example is INVENTED, deliberately. It used to be built from
// this repo's own test session -- "Open up, FBI.", the chest, the gorillas --
// which made the model's behavior on that very footage unreadable: it quoted
// the line while it was in the prompt and swerved around it once the rules
// changed, paraphrasing the example's vocabulary ("a big lid... we all crowd
// it") to avoid repeating text the prompt had already used. An example the
// input can never collide with is the only way a rule about NARRATOR lines is
// tested by the session rather than by the example.
const narrSystem = `You are the voice-over on a video of one session -- a game, a build, a lesson, a drive. What it is, is in the user context and in the EVENT lines: read it first and talk about what is happening on screen and what we are doing about it.` + narrCraft

// narrCraft is the craft: the premise, how a line is written and placed, how a
// pause is made, and the worked examples. It was appended to two openings --
// one for a session, one for a showcase, picked by the Style dropdown -- and
// the openings differed by one paragraph, what every line is ABOUT. There is
// one now, and what this session is comes from the user context like every
// other fact about it (prompts.go).
const narrCraft = `

You are the only voice in the video. The clips keep their own sound -- the game, the room -- but nothing anybody said is played, so every spoken line in a clip's block is material nobody will ever hear unless you use it.

Your voice: present tense, contractions, short sentences, and whoever the user context says you are. With nothing said about it, be the person behind the camera -- off-hand, saying "we" and "look at this". Most lines are a full thought, what is happening and what we are doing about it, but two words are a whole line when they are the right two: "Well. Great." after a disaster beats a sentence explaining it.

For each clip, in order:
1. Say what was said, better. Take the spoken lines over that clip and give them in your voice: the same meaning and the same facts, sharper and shorter. A line that reads like broken speech-to-text is one to say properly, never to quote as it stands. Where nothing was said, write from the EVENT lines instead.
2. Add nothing nobody said and the pictures do not show -- no name, no number, no outcome you were not given. Say less rather than fill.
3. Less is more. The clip's word count is a ceiling across all its entries, not a target, and most clips should come in far under it. Most clips get a line, a short one; a clip that carries itself gets text "" -- still an entry, no words in it -- but that is one or two clips in the video, not half of them.
4. Place each line at the offset it happens: react to the vault after we have seen the vault.
5. A pause is an entry, not punctuation. The voice runs straight through a comma, a dash and a full stop, so a beat cannot be written into a line -- it is made by ending the line and starting another. Two entries on the same clip, in time order, each with its own "at" and its own emotion: the second's "at" is the first's start, plus its spoken length -- about two and a half words a second, so ten words is four seconds -- plus the silence you want. A second and a half before a punchline is what makes it land, and it is the only way to get one.

Every line has an emotion, and it moves: the setup calm, the reaction surprised, the verdict flat. Weight it where the reading has to be exact.

Never report your own body or your feelings -- no "I'm spinning", no "my hands". You are behind the camera talking about what is in front of it. Start in the middle: never "In this clip", and never open two clips the same way. The last clip ends the video: a sign-off if the user context wants one, otherwise the last thing worth saying, with its "at" near that clip's end so the video ends when you stop talking.

Three clip blocks and the lines they should get:
  [+2s] EVENT: Four players push on a vault door that does not move.
  [+9s] NARRATOR: Housekeeping. You ordered towels?
  [+14s] SPEAKER_01: my controller just died
  -> at 10 [calm]: "Nobody here has the key, of course."
  -> at 13 [happy]: "Housekeeping! You ordered towels?"
  -> at 17 [surprised=0.6]: "And now his controller's dead."

  [+3s] EVENT: The group crowds into a small shop, climbing over the counter.
  [+19s] EVENT: A player knocks the till off the counter and the shop empties.
  -> at 4 [happy]: "Whooo, it's a bit crowded in here."
  -> at 20 [calm]: "Aaand the shop is closed."
  -> at 23 [proud=0.5]: "Great job, everyone."

  [+1s] EVENT: The player runs along a rooftop and drops into an alley.
  [+8s] EVENT: He lands badly and slides into a wall.
  -> ""

The first clip is what a pause looks like: one thought per entry, three seconds apart, rather than one line with dots in it.

Answer in narrate's shape.`

// narrNoMicNote rides on the narrate prompt when the finished video DOES play
// what people said out loud, which is the one case the prompt's premise is
// wrong about: it is written for a session whose voices are not in the video
// -- split off and silenced, or recorded on a microphone the render never
// plays -- and there "say what was said, better" is the whole job. Where the
// viewer can hear them, the same instruction has the narrator read the
// transcript back over the people saying it.
//
// Which case it is, is not a setting: it is what the scenes hear (speechHeard).
// This used to fire on "the session has no separate narrator recording", which
// is a different question and got the answer right only by accident -- a
// session with a narrator mic AND the footage's own voices kept was told the
// voices were nobody's to repeat.
//
// Appended rather than a style of its own, so a reworded prompt keeps working.
const narrNoMicNote = `THIS VIDEO PLAYS WHAT PEOPLE SAID OUT LOUD. The lines marked SPEAKER are heard by the viewer in the speakers' own voices, so never say one back: set it up before it lands, or react after it. The NARRATOR lines are still yours -- nobody hears those unless you use them -- and so is every clip the speakers left alone.`

// narrCaptionsAddendum rides on the narrate prompt when the project's voice is
// "no audio": the same writer, writing lines nobody will ever speak. Appended
// rather than shipped as a style so a user-edited narrate prompt keeps
// working -- these rules override only what speaking meant.
const narrCaptionsAddendum = `THIS VIDEO HAS NO VOICE-OVER. Nobody speaks your lines: they are burned into the picture as captions and the viewer READS them. Everything above still holds -- the taste, the timing, the restraint -- with these changes:
- Write for the eye, not the ear. Shorter still: a caption is read in the corner of the attention, and a line the viewer has to study is a line over footage they are missing.
- "emotion" means nothing with no voice. Leave it "".
- Give every entry with text a "pos": where the caption sits on the picture. "bottom" is the default and almost always right; "top" when the action or the game's own UI lives at the bottom of the frame; "center" only for a line that IS the moment, like a title card.
So each entry is {"start":<sec>,"end":<sec>,"at":<sec>,"text":"...","emotion":"","pos":"bottom|top|center"}.`

type narrEntry struct {
	S float64 `json:"s"`
	E float64 `json:"e"`
	// At is where inside the clip the line starts, in seconds from the clip's
	// start -- the writer places it on the moment the line is about, so a line
	// about the chest waits for the chest and the sign-off sits at the end of
	// the last clip rather than 30 s before it. Zero is the head of the clip,
	// which is also what every entry written before the field existed means.
	At      float64 `json:"at,omitempty"`
	Text    string  `json:"text"`
	Emotion string  `json:"emotion"`
	// Pos is where the line sits on the picture when Produce burns it in as a
	// caption: "top", "center", or "" for the bottom, where subtitles live
	// unless the action does. Written by the captions-only narration, editable
	// as a tag word ("[top] look up here"), and meaningless to the TTS.
	Pos string `json:"pos,omitempty"`
	// Roll is how many times this line has been asked for a different take.
	// The engine draws a random seed per request unless it is given one, so
	// two runs of the same words came back as two different performances and a
	// take you liked could not be kept. The seed is derived from the line
	// instead (ttsKey), which makes it stable -- and this counter is the one
	// way to move it, when the delivery is nearly right and you just want
	// another draw. It salts the cache key too, or the re-roll would be served
	// the take it was pressed to replace.
	Roll int `json:"roll,omitempty"`
}

// setNarrOff records the Narration tick and lets every page that cares redraw:
// Produce hides the game volume and the subtitles when there is no voice for
// them to be about.
func (a *App) setNarrOff(off bool) {
	if a.narrOff == off {
		return
	}
	a.narrOff = off
	a.syncNarrOff()
	a.syncNarrPage()
	a.saveProjectNow()
}

// applyNarrOff is a project's answer arriving: the box follows it, and so does
// Produce. Called from applyProject, which may run before either page exists.
func (a *App) applyNarrOff(off bool) {
	a.narrOff = off
	if a.narr != nil && a.narr.onBox != nil {
		a.narr.onBox.SetActive(!off)
	}
	a.syncNarrOff()
	a.syncNarrPage()
}

// syncNarrPage greys the Narrate page when this video has no narration. Safe
// before the page exists: a project's answer arrives first.
func (a *App) syncNarrPage() {
	n := a.narr
	if n == nil {
		return
	}
	for _, w := range n.body {
		if s, ok := w.(interface{ SetSensitive(bool) }); ok {
			s.SetSensitive(!a.narrOff)
		}
	}
}

type narrator struct {
	// the Narration tick at the top of the page: whether this video has one,
	// and everything the tick greys out -- the lines, the preview and the
	// voice picker. Not the tick itself: it is how the page comes back.
	onBox *gtk.CheckButton
	body  []gtk.Widgetter

	a       *App
	entries []narrEntry
	// the clips whose last line you deleted: "this one plays its own audio"
	// said once, in the file, instead of as a blank row (see silentFor)
	silent []cutSeg

	player         *Player   // video preview
	voice          *Player   // narration audio rides along on this one
	fx             *fxScreen // the effects over that preview; the Cut page's own layers
	playSeg        int       // entry currently voiced, -1 none
	jumped         int       // clip we last skipped a gap to, -1 none
	playVideoStart float64   // session start of the video loaded in the preview
	pos            float64   // last known playhead, in session time (tick + cue)

	// the seek slider under the video. sliding guards the loop: the tick sets
	// the value and the value-changed handler seeks, and without the flag the
	// two feed each other. seekWant/seekArmed debounce a drag into one seek per
	// 120 ms instead of a pipeline reload per pixel.
	// The slider runs on the CUT's clock, not the session's: its range is the
	// kept seconds end to end, so what the edit removed takes up no room on the
	// bar at all. Everything else on this page (the row times, the playhead,
	// Cut) speaks session time, so the two are converted at the edge --
	// cutPos on the way out, cutAt on the way in.
	slider    *gtk.Scale
	timeLbl   *gtk.Label
	playBtn   *gtk.Button
	sliding   bool
	seekWant  float64
	seekArmed bool

	durCache map[string]float64 // wav path -> seconds, so ⚠ never probes twice
	durProbe map[string]bool    // wav paths an ffprobe is out measuring right now
	synthing bool               // a playback-triggered synthesis is in flight
	held     bool               // the preview is paused by us, not by the user
	// the user has started the preview and not stopped it, which is what makes
	// the run bar the preview's transport. A clip merely CUED -- by clicking a
	// line to see where it is -- must not take ▶ away from the step's own run.
	started bool
	// wavs the server refused: stalling on them again would pause at every clip
	synthFail map[string]bool
	list      *gtk.ListBox
	// the scroller the list sits in, kept so a rebuild can put the reader back
	// where they were (rebuildRows)
	listScroll *gtk.ScrolledWindow
	rows       []*narrRow
	// what typing owes the disk. The line boxes and the clock fields write
	// the whole narration file and re-count the output folder, which is a
	// write and a directory walk per keystroke unless they are collected
	// (saveSoon, flushSave).
	saveQ debounce

	building bool // guards feedback loops while (re)building rows
	rebuildQ bool // a rebuild is waiting for the idle (queueRebuild)

	// speaking is the row whose line is on n.voice, -1 none: it is what draws
	// that row's ⏸, and it is set whoever started the sound. solo answers the
	// other question -- which row started it from its OWN ▶ -- and it lasts
	// only as long as that line does: with the picture, the tick drops it once
	// the line has been spoken and the preview carries on down the cut, because
	// the cut is what this page previews and an audition is a seek into it.
	// Without the picture (soloPic false) it lasts until the wav ends, and the
	// tick must not pause the voice under it.
	speaking int
	solo     int
	// the row the ⏸ is currently drawn on, so the tick can notice when it
	// should move without redrawing every row's icon ten times a second
	liveRow int
	// the audition has the picture with it. False when nothing on disk covers
	// that clip: then the line is spoken over a still frame, which is all there
	// is, and the tick is not the one driving it.
	soloPic bool

	// the two rows every other step has: what goes into the narration run, and
	// what this step has written. Narrate had neither, which made it the one
	// page where you could not tell a narration written against the current cut
	// from one written against a cut you have since changed.
	inputs *gtk.Label
	out    *gtk.Label
}

type narrRow struct {
	text  *gtk.TextView
	speak *gtk.Button // ▶/⏸ for this one line
	stamp func()      // redraws this row's end time and its ⚠ (see restamp)
}

// One box per line, delivery and words together: the box shows
// "[excited @65] Weee, ziplining!" and lineParts splits it back apart before
// anything is saved or spoken. The split exists because the TTS server takes
// the emotion as its own request field -- whatever is left in the text gets
// pronounced, so a tag that stayed inline would be read out as the word
// "excited". The model's JSON keeps the same fields for the same reason; only
// the editing view merges them.
//
// @N is the line's placement, seconds from the clip's start -- the same "at"
// the writer sets, editable where the timing is judged. It is optional both
// ways: a tag without one leaves the placement as it was (an edit to the words
// must not silently move the line to the clip's head), and hasAt says whether
// one was written at all.
var lineAtRe = regexp.MustCompile(`\s*@([0-9]+(?:\.[0-9]+)?)\s*$`)

// posTag says whether a tag word names a caption placement, and which one it
// stores as. The spelled-out default comes back as the empty string it is
// saved as -- "[bottom]" is an answer, not an emotion.
func posTag(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "top":
		return "top", true
	case "center", "centre", "middle":
		return "center", true
	case "bottom":
		return "", true
	}
	return "", false
}

func lineParts(s string) (emotion string, at float64, hasAt bool, text string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			tag := strings.TrimSpace(s[1:i])
			if m := lineAtRe.FindStringSubmatch(tag); m != nil {
				at, _ = strconv.ParseFloat(m[1], 64)
				hasAt = true
				tag = strings.TrimSpace(tag[:len(tag)-len(m[0])])
			}
			return tag, at, hasAt, strings.TrimSpace(s[i+1:])
		}
	}
	return "", 0, false, s
}

// lineText is the inverse: what the box shows for an entry. The placement is
// NOT rendered back -- the row's time field owns it now, and two editable
// spellings of one number is how they drift apart. @N typed into the box still
// works (lineParts), it just moves the line and then lives in the time field.
func lineText(e narrEntry) string {
	if strings.TrimSpace(e.Text) == "" {
		return "" // a silent clip is an empty box; an emotion alone says nothing
	}
	tag := e.Emotion
	if tag == "" {
		tag = e.Pos // a caption has no delivery; its placement is the tag
	}
	if tag == "" {
		return e.Text
	}
	return "[" + tag + "] " + e.Text
}

func (a *App) narrPath() string { return filepath.Join(a.narrateDir(), "narration.json") }

// keepPrevNarration copies the narration aside before a run overwrites it, and
// hands back where it put it.
//
// ▶ rewrites every line now, which is what makes a narration re-doable and also
// what makes it losable: an evening of hand-edited lines is one button press
// from being a fresh draft. A copy costs a few kilobytes and turns that press
// from irreversible into inconvenient. One generation deep on purpose -- the
// question it answers is "I did not mean that", asked immediately.
//
// Nothing here fails the run: no narration yet is the ordinary first press, and
// a copy that cannot be made is worth a quiet nothing rather than a stop.
func keepPrevNarration(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err // no narration yet: nothing to keep, and not a problem
	}
	prev := strings.TrimSuffix(path, ".json") + ".prev.json"
	if err := os.WriteFile(prev, b, 0o644); err != nil {
		return "", err
	}
	return prev, nil
}

// ---- persistence ------------------------------------------------------------

func (n *narrator) load() {
	n.entries, n.silent = nil, nil
	if b, err := os.ReadFile(n.a.narrPath()); err == nil {
		var f struct {
			Entries []narrEntry
			Silent  []cutSeg
		}
		if json.Unmarshal(b, &f) == nil {
			n.entries, n.silent = f.Entries, f.Silent
		}
	}
	// in placement order, whatever the file says. Every reader downstream walks
	// the list in order -- staleFor decides from it whether ▶ must rewrite the
	// narration, entryAt gives a line the window up to the next one -- and the
	// file can be written mid-edit, between a line moving clips and the rows
	// being rebuilt around it.
	n.sortEntries()
}

// saveSoon is save once the typing stops. Everything that edits a line as it is
// typed goes through here; everything that is a decision -- a row added, a take
// picked, a line silenced -- still calls save outright, because a decision you
// watched happen must be on disk before you can doubt it.
func (n *narrator) saveSoon() { n.saveQ.call(n.save) }

// flushSave settles what typing owes before something else looks at the file.
// Nil-safe, so the page that is not built yet costs its callers no guard.
func (n *narrator) flushSave() {
	if n != nil {
		n.saveQ.flush()
	}
}

func (n *narrator) save() {
	n.silent = pruneSilent(n.silent, n.entries)
	b, _ := json.MarshalIndent(struct {
		Entries []narrEntry `json:"entries"`
		Silent  []cutSeg    `json:"silent,omitempty"`
	}{n.entries, n.silent}, "", "  ")
	os.MkdirAll(filepath.Dir(n.a.narrPath()), 0o755)
	if err := os.WriteFile(n.a.narrPath(), append(b, '\n'), 0o644); err != nil {
		n.a.logf("save narration: %v", err)
	}
	n.updateOut()
}

// pullRows copies the editable widgets back into the model.
func (n *narrator) pullRows() {
	if n.building {
		return
	}
	for i, r := range n.rows {
		if i >= len(n.entries) {
			break
		}
		buf := r.text.Buffer()
		emo, at, hasAt, text := lineParts(buf.Text(buf.StartIter(), buf.EndIter(), false))
		n.entries[i].Text = text
		if text == "" {
			continue // an emptied box silences the line; its old emotion stays
		}
		if p, ok := posTag(emo); ok {
			// the tag word is a placement, not a delivery: "[top]" moves the
			// caption and leaves nothing for the TTS to act
			n.entries[i].Pos = p
			n.entries[i].Emotion = ""
		} else {
			n.entries[i].Emotion = emo
		}
		if hasAt {
			e := &n.entries[i]
			e.At = math.Min(math.Max(0, at), math.Max(0, e.E-e.S-1))
		}
	}
}

// ---- page ------------------------------------------------------------------

func (a *App) buildNarrate() gtk.Widgetter {
	n := &narrator{a: a, playSeg: -1, jumped: -1, speaking: -1, solo: -1, liveRow: -1,
		synthFail: map[string]bool{}, durCache: map[string]float64{},
		durProbe: map[string]bool{}}
	a.narr = n
	if p, err := NewPlayer(); err == nil {
		n.player = p
		// the picture is what "playing" means here; the narration track follows
		// it, so only this one draws the run bar's button -- and the transport's
		p.OnState = func() {
			a.updateRunControls()
			if n.playBtn != nil {
				setPlayIcon(n.playBtn, p.playing,
					"play the cut with its narration", "pause")
			}
		}
		p.OnError = a.playerErr("the narrate preview")
		p.OnLog = func(s string) { a.logf("%s", s) }
	}
	if p, err := NewPlayer(); err == nil {
		n.voice = p
		// not the run bar's business -- this one speaks single lines -- but the
		// line's own button has to go back to ▶ when the line ends
		p.OnState = a.syncPlayIcons
		p.OnError = a.playerErr("the narration line")
	}

	n.list = gtk.NewListBox()
	n.list.SetSelectionMode(gtk.SelectionSingle)
	n.list.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil || n.building {
			return
		}
		i := row.Index()
		if i >= 0 && i < len(n.entries) {
			// to the LINE, not to its clip: the line may sit a minute in, and
			// what a click wants judged is the line landing on its moment --
			// a few seconds of run-in shows the moment arrive. leadIn rather
			// than the arithmetic, and the same leadIn the row's ▶ uses:
			// picking a line is picking a line, and two run-ins for the one act
			// would only be two numbers to learn.
			//
			// This is the ONLY place the preview jumps of its own accord. The
			// tick that follows playback selects rows too, and would land here
			// on every clip -- selectRow holds n.building across it for exactly
			// that reason, so a seek here is always a user picking a line.
			n.seekTo(n.leadIn(i))
		}
	})
	left := gtk.NewScrolledWindow()
	n.listScroll = left
	left.SetChild(n.list)
	left.SetVExpand(true)
	// The rows wrap; they do not scroll sideways. Left on the default the
	// column scrolled horizontally instead, which is not a narrower column at
	// all: the rows keep the width their longest line wants, the text boxes
	// stay exactly as wide as they were, and a scrollbar appears under them --
	// so making the window smaller hid the words rather than re-wrapping them.
	// Every other scrolling column in the app says this (cut_form.go,
	// publish.go); this one had been the exception.
	left.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	// a floor, not the width: the divider opens this column at 790, which is
	// wide enough that a narration line wraps into about three. As a minimum
	// 780 was the widest thing in the app, and every other page inherited it --
	// the cost of asking for a comfortable size instead of setting one.
	left.SetSizeRequest(360, -1)

	// right side: video + controls + the prompt, top to bottom of the page
	vframe := videoFrame(nil)
	vframe.SetTooltipText("click the picture to play the cut with its narration, " +
		"and again to pause — ⏹ below hands ▶ back to writing and speaking the narration")
	if n.player != nil {
		n.player.Picture.SetVExpand(true)
		// the picture goes in wrapped: what this page is for is judging a line
		// against the moment it lands on, and the moment is the FINISHED frame
		// -- cropped, zoomed, frozen where a stop freezes it, with its titles
		// on (narrate_fxview.go)
		vframe.SetChild(n.buildNarrFx())
		click := gtk.NewGestureClick()
		// held goes with it: a pause the user asked for is theirs to undo, and a
		// synthesis finishing must not start the video back up behind them
		click.ConnectReleased(func(cnt int, x, y float64) { n.toggle() })
		n.player.Picture.AddController(click)
		glib.TimeoutAdd(100, n.followPlayback)
	}
	// Top and bottom only: the column's own margins (on right, below) give the
	// video the same left and right edge as the button and the prompt under it.
	// It had none on the left at all, so the picture ran into the divider and
	// the frame around it stopped being a frame on that side.
	vframe.SetMarginTop(10)
	vframe.SetMarginBottom(6)

	// The transport, subtitle-editor style: play/pause, a seek slider over the
	// cut, the playhead's clock, and the two verbs of manual editing -- a line
	// at the playhead, and voicing what changed. The run bar's ▶ stays the
	// initial fill (write once, speak everything); these are for the editing
	// that follows it.
	// ±3 s either side of play, in session time and on the cut: a jump that
	// lands in a stretch the cut removed carries on to the neighbouring clip,
	// the same rule the slider and the wheel follow
	back := gtk.NewButtonFromIconName("media-seek-backward-symbolic")
	back.SetTooltipText("back 3 seconds")
	back.ConnectClicked(func() { n.seekTo(snapToCut(n.clips(), n.pos, n.pos-3, cutEdge)) })
	n.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	n.playBtn.SetTooltipText("play the cut with its narration")
	n.playBtn.ConnectClicked(func() { n.toggle() })
	fwd := gtk.NewButtonFromIconName("media-seek-forward-symbolic")
	fwd.SetTooltipText("forward 3 seconds")
	fwd.ConnectClicked(func() { n.seekTo(snapToCut(n.clips(), n.pos, n.pos+3, cutEdge)) })
	n.slider = gtk.NewScale(gtk.OrientationHorizontal, nil)
	n.slider.SetHExpand(true)
	n.slider.SetDrawValue(false)
	n.slider.SetTooltipText("the cut, end to end — what the edit removed is not on this bar, so every " +
		"point on it is video you keep. Drag to seek; paused, the wheel steps a frame at a time")
	// The wheel over the slider steps frames, and only while the picture is
	// stopped: scrolling a playing preview fights the tick, which is writing the
	// slider's value ten times a second, and the seeks land behind where the
	// video has already got to. Capture phase and a true return take the wheel
	// off GtkRange, whose own handling would jump the value by a page.
	wheel := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical |
		gtk.EventControllerScrollDiscrete)
	wheel.SetPropagationPhase(gtk.PhaseCapture)
	wheel.ConnectScroll(func(dx, dy float64) bool {
		if n.player == nil || n.player.Playing() {
			return true // swallowed: a scrub during playback is not a scrub
		}
		if dy != 0 {
			n.frameStep(int(math.Round(dy))) // wheel down runs forward, as everywhere
		}
		return true
	})
	n.slider.AddController(wheel)
	n.slider.ConnectValueChanged(func() {
		if n.sliding {
			return
		}
		// The handle is somewhere on the CUT, so there is nothing to snap: the
		// removed stretches are not on the bar to be dropped into. It only has
		// to be read back into session time, which is what the player and every
		// other number on this page are in.
		//
		// The bar used to span the gaps as well, which made most of it dead
		// space on a real session -- thirty minutes of source for five of cut --
		// and every drag through it a fight between the handle and a snap
		// dragging it back to the nearest clip edge.
		n.seekWant = cutAt(n.clips(), n.slider.Value())
		// one seek per 120 ms, not one per pixel of the drag: a seek that
		// lands in a different recording reloads the pipeline
		if n.seekArmed {
			return
		}
		n.seekArmed = true
		glib.TimeoutAdd(120, func() bool {
			n.seekArmed = false
			n.seekTo(n.seekWant)
			return false
		})
	})
	n.timeLbl = gtk.NewLabel("00:00")
	n.timeLbl.AddCSSClass("dim-label")
	// ＋ is the same ＋ every line's row carries, and means the same thing: a
	// new line here. It sits with the transport because "here" is the playhead
	// -- you pause where the video has nothing to say, and add there -- and it
	// had a row of its own under this one, a whole line of the column spent on
	// one button, pushing the voice picker down.
	//
	// It is also the only way in ABOVE a line: a row's ＋ adds below itself, so
	// the first line of a clip is reached by pausing where the new one should
	// speak. That is the better move anyway -- a line is placed against what is
	// on screen, and here you are looking at it.
	addBtn := gtk.NewButtonFromIconName("list-add-symbolic")
	addBtn.SetTooltipText("start a new narration line at this second — it runs until the clip's " +
		"next line, or the clip's end. Pause where the video has nothing to say and press this.")
	addBtn.ConnectClicked(func() { a.addLineClicked() })

	transport := gtk.NewBox(gtk.OrientationHorizontal, 6)
	transport.Append(back)
	transport.Append(n.playBtn)
	transport.Append(fwd)
	transport.Append(addBtn)
	transport.Append(n.slider)
	transport.Append(n.timeLbl)
	// and how loud it is, beside the button that plays it: this page is where
	// a narration line is judged against the sound under it, which is a
	// judgement made with a hand on the volume
	transport.Append(volumeCtl())

	preview := gtk.NewBox(gtk.OrientationVertical, 8)
	preview.Append(vframe)
	preview.Append(transport)
	// the picture takes this column's spare height. It used to go to the voice
	// picker, which was a scrolling list of voices and could use it; the picker
	// is two rows now, and height given to it is height taken off the video.
	preview.SetVExpand(true)

	// The prompt was a box filling the bottom half of this column, then a
	// dropdown above the video, and it is on Prepare with all the others now
	// (prepedit.go). What took its place is the voice picker -- which used to
	// sit on top of the narration lines and take a third of their column's
	// height for a choice made once.
	//
	// That swap is the point of the page's shape: the lines are what you work
	// on, they are sentences, and sentences want width. With the picker out of
	// their column they have the whole of it, and it is under the video where it
	// is read alongside the sample it plays.
	voice := a.buildVoicePicker()

	// Whether this video is narrated at all, at the top of the column that is
	// about the narration. Some videos want none -- the speakers carry them,
	// or the words go on screen instead -- and there was no way to say so: the
	// nearest was the captions voice, which still writes lines and still asks
	// Produce to carry them. Off means off, and Produce loses what only a
	// narration needs (produce.go).
	n.onBox = gtk.NewCheckButtonWithLabel("Narration")
	n.onBox.SetActive(!a.narrOff)
	n.onBox.SetTooltipText("Whether this video has a narration. Unticked, ▶ writes none, " +
		"the lines already written are left alone, and Produce drops the game-volume " +
		"slider and the subtitle choices -- all three exist to carry a voice-over.")
	n.onBox.ConnectToggled(func() { a.setNarrOff(!n.onBox.Active()) })
	head := gtk.NewBox(gtk.OrientationHorizontal, 6)
	head.SetHAlign(gtk.AlignEnd)
	head.Append(n.onBox)

	right := gtk.NewBox(gtk.OrientationVertical, 6)
	right.SetMarginStart(12)
	right.SetMarginEnd(12)
	right.SetMarginBottom(8)
	right.Append(head)
	right.Append(preview)
	right.Append(voice)
	// everything on this page except the tick itself: with no narration there
	// is nothing here to do, and a page of live controls over a video that has
	// none is a page that invites the work the tick just said not to do. The
	// tick stays alive, because turning it back on is the one thing still
	// worth pressing.
	n.body = []gtk.Widgetter{left, preview, voice}

	// ...and the narration lines run the full height beside it, with the whole
	// of the left column to wrap in
	split := gtk.NewPaned(gtk.OrientationHorizontal)
	split.SetStartChild(left)
	split.SetEndChild(right)
	split.SetPosition(790)
	split.SetVExpand(true)
	split.SetShrinkEndChild(false)

	// What this step reads, at the top -- the row every other step has and
	// this one did not. What it has written goes to the shared bottom bar
	// (outStack in main.go), like every step's.
	n.inputs = gtk.NewLabel("")
	n.inputs.SetXAlign(0)
	n.inputs.SetHExpand(true)
	n.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(n.inputs)

	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("narrate/ — narration.json, the voice reference and the synthesis cache")
	openOut.ConnectClicked(func() { a.openFolder(a.narrateDir()) })
	n.out = gtk.NewLabel("")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.Append(openOut)
	outRow.Append(n.out)
	a.outStack.AddNamed(outRow, "narrate")

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(split)

	n.load()
	n.rebuildRows()
	n.updateInputs()
	n.updateOut()
	return page
}

// textBoxHeight is how tall a row's box has to be to hold that many lines of
// words: the lines themselves, the view's own margins (2 above, 2 below), the
// frame's 1px border, and two pixels of slack. The error is not symmetric -- a
// box two pixels too tall is invisible, a box two pixels too short is a
// scrollbar -- so the slack rounds up.
func textBoxHeight(lineH, lines int) int { return lines*lineH + 8 }

// queueRebuild is rebuildRows for a caller that is inside a row's own signal
// handler -- which is to say, inside a widget the rebuild is about to destroy.
//
// GTK is still standing on that widget when our handler returns: it goes on
// walking the entry's handler list, finishing the focus change it was
// delivering, and -- for the text view in the row -- holding iterators into a
// buffer the rebuild has just thrown away. Tearing the list down underneath
// that is a use-after-free, and it is what a committed time edit crashed on:
// "any mutation that affects indexable buffer contents will invalidate all
// outstanding iterators", then SIGSEGV inside the main loop.
//
// So the rebuild waits for the loop to finish delivering the event and runs on
// the next idle, when nothing is standing on the old widgets any more. It is
// coalesced because one edit raises several of these: Enter commits, and the
// focus then leaves the box it just committed.
func (n *narrator) queueRebuild() {
	if n.list == nil || n.rebuildQ {
		return // headless (tests), or one is already on its way
	}
	n.rebuildQ = true
	glib.IdleAdd(func() {
		n.rebuildQ = false
		n.rebuildRows()
	})
}

func (n *narrator) rebuildRows() {
	if n.list == nil {
		return // headless (tests): the entries are the model, the rows a view of it
	}
	// Where the reader was. A rebuild throws every row away and builds it
	// again, and a fresh list is scrolled to the top -- so editing line twelve
	// and committing it put line one on screen and line twelve somewhere below
	// the fold, which is what "it jumps back to the start" was.
	//
	// The OFFSET is restored, not the focus. A rebuild is usually raised by the
	// focus leaving a box (queueRebuild), so grabbing it back would pull it off
	// whatever it was moving to -- and the one path that does want the focus
	// asks for it afterwards by name (focusLine).
	at, sel := n.listOffset(), -1
	if r := n.list.SelectedRow(); r != nil {
		sel = r.Index()
	}
	n.building = true
	for {
		row := n.list.RowAtIndex(0)
		if row == nil {
			break
		}
		n.list.Remove(row)
	}
	n.rows = nil
	n.speaking, n.solo = -1, -1 // the buttons that knew about it are gone
	n.sortEntries()             // a hand-edited time may have reordered the list
	// A line's height in pixels, measured rather than guessed -- font size and
	// text scaling differ per machine, and the box is sized in lines.
	//
	// Measured in the BOX's font, which is monospace and not the list's. Asking
	// the list was asking the wrong widget: monospace is a few pixels taller per
	// line on most themes, so the room set aside for three lines held three
	// lines minus about ten pixels, and a full box came up with a scrollbar that
	// could travel that far and no further. A scrollbar for ten pixels is worse
	// than no scrollbar at all -- it says there is more to read when there is
	// not.
	lineH := 20
	probe := gtk.NewTextView()
	probe.SetMonospace(true)
	if lay := probe.CreatePangoLayout("Ay"); lay != nil {
		if _, h := lay.PixelSize(); h > 0 {
			lineH = h
		}
	}
	oneLine, threeLines := textBoxHeight(lineH, 1), textBoxHeight(lineH, 3)
	for i := range n.entries {
		e := &n.entries[i]
		i := i

		head := gtk.NewBox(gtk.OrientationHorizontal, 6)
		// the line's start, on the session clock, editable: the "at" is
		// derived from it (clamped to the line's clip), so timing is corrected
		// where it is judged -- against the video -- not by arithmetic on
		// offsets. The list re-sorts when the edit is done (activate/rebuild),
		// not per keystroke, or the box would jump out from under the cursor.
		when := gtk.NewEntry()
		// A clock is eight characters wide and never more, so say so twice: an
		// entry with no max-width-chars asks for a natural width far past what
		// it will ever hold, and took most of the row with "10:25.0" in it --
		// pushing the end-time label off the edge.
		when.SetWidthChars(8)
		when.SetMaxWidthChars(8)
		when.SetHExpand(false)
		when.SetText(fmtClock(e.S + e.At))
		when.SetTooltipText("when this line's audio starts (mm:ss.s on the session clock) — after the dash is " +
			"when it stops speaking. A time inside another clip moves the line there; one outside the cut is " +
			"refused and the box goes back to where the line really is")
		tl := gtk.NewLabel("")
		tl.AddCSSClass("dim-label")
		// ...and never a floor under the window, the way the Inputs line is
		// not one. Now that the column no longer scrolls sideways, a label's
		// minimum width IS the row's, and this one holds sentences: "⚠ this
		// clip's lines run 3.4 s past it — the render will have them moved
		// earlier and sped up" is half a screen that nothing could shrink
		// past. It is a summary of what the row below already says.
		tl.SetEllipsize(pango.EllipsizeEnd)
		// an empty box is now an answer and not a failure -- the narration is
		// asked to leave clips alone -- so the row says which it is, or a page
		// with three blank lines on it reads as a run that half worked
		stamp := func() {
			warn, s := false, ""
			if strings.TrimSpace(e.Text) == "" {
				s = "(no line — this clip plays on its own audio)"
			} else if n.a.captionsOnly() {
				// nothing below applies with no voice: no wav, no spoken
				// length, no overrun to warn about. The line stays on screen
				// until the next one, so there is no end time to print either.
				s = "(caption — the viewer reads it; never spoken)"
			} else {
				// the end is when the AUDIO stops: start plus the spoken
				// length, measured off the wav when it exists and estimated
				// (~, from how fast the spoken lines came out) until it does.
				// The gap to the next line
				// is just the game playing -- it belongs to nobody's row.
				dur := n.speechDur(*e)
				s = "– " + fmtClock(e.S+e.At+dur)
				if !exists(n.a.ttsWav(*e)) {
					s += " (~)" // estimated: the line has not been spoken yet
				}
				// the corner the mix can only paper over: more speech than the
				// slot holds gets slid earlier and then sped up. Say so here,
				// while the words are still being chosen.
				if win := n.lineWindow(i); dur > win {
					edge := "the clip ends"
					if n.lineEnd(i) < e.E-0.05 {
						edge = "the next line"
					}
					s += fmt.Sprintf("  ⚠ ~%.0f s of speech, %.0f s before %s", dur, win, edge)
					warn = true
				}
				// ...and the clip's whole schedule, on the row it hangs off.
				// The line above is about one line's room; this is about all
				// of them stacked up, which is the thing the render moves
				// (clipOverrun).
				if n.clipTop(i) {
					if over := n.clipOverrun(i); over > 0.05 {
						how := "moved earlier"
						if over > n.clipSlack(i) {
							how = "moved earlier and sped up"
						}
						s += fmt.Sprintf("  ⚠ this clip's lines run %.1f s past it — the render will have them %s",
							over, how)
						warn = true
					}
				}
			}
			if warn {
				tl.AddCSSClass("error")
			} else {
				tl.RemoveCSSClass("error")
			}
			tl.SetText(s)
		}
		stamp()
		tl.SetHExpand(true)
		tl.SetXAlign(0)
		when.ConnectChanged(func() {
			if n.building {
				return
			}
			// only what a clock can contain gets to stay: a stray letter is
			// removed as it is typed, not complained about after
			if c := cleanClock(when.Text()); c != when.Text() {
				p := when.Position()
				when.SetText(c) // re-enters this handler; the clean text passes
				when.SetPosition(p - 1)
				return
			}
			t, ok := parseClock(when.Text())
			if !ok {
				when.AddCSSClass("error") // half-typed reads as such, not as applied
				return
			}
			when.RemoveCSSClass("error")
			// live, while typing, the line only moves INSIDE its own clip: a
			// half-typed "1" on its way to "12:42" must not re-home the line to
			// the clip at one second and back again on the next keystroke.
			// Landing somewhere else is a decision, and decisions are committed.
			e.At = math.Min(math.Max(0, t-e.S), math.Max(0, e.E-e.S-1))
			n.saveSoon()
			n.restamp(i)
		})
		// commit is what a finished edit means: the typed time is placed for
		// real -- another clip included -- and then WRITTEN BACK, so the box
		// shows where the line is rather than where it was asked to go. That
		// write-back is the fix for a row reading "12:42 – 11:07.5".
		commit := func() bool {
			if n.building {
				return false
			}
			t, ok := parseClock(when.Text())
			if !ok {
				// a half-typed time abandoned snaps back to the last applied
				// one -- an "error" box left behind would read as a placement
				// that somehow took
				when.RemoveCSSClass("error")
				when.SetText(fmtClock(e.S + e.At))
				return false
			}
			moved := n.moveLine(i, t)
			when.SetText(fmtClock(e.S + e.At))
			n.restamp(i)
			return moved
		}
		// Enter commits the edit: the list re-sorts around the new time
		when.ConnectActivate(func() {
			commit()
			if !n.building {
				n.queueRebuild()
			}
		})
		wf := gtk.NewEventControllerFocus()
		wf.ConnectLeave(func() {
			// a line that changed clips has moved in the list as well, so the
			// order has to be redone; one nudged within its clip has not, and
			// rebuilding under a click that is on its way to another widget
			// would pull that widget out from under it
			if commit() {
				n.queueRebuild()
			}
		})
		when.AddController(wf)
		// ▶ per line rather than a 🔊: it is a play button and, while its line is
		// sounding, the ⏸ for it (syncSpeakIcons draws that)
		speak := gtk.NewButtonFromIconName("media-playback-start-symbolic")
		if strings.TrimSpace(e.Text) == "" {
			// no line to lead into: this ▶ plays the clip itself (syncSpeakIcons
			// keeps the wording right as the words come and go)
			speak.SetTooltipText("play this clip — it has no line, so you hear the game")
		} else {
			speak.SetTooltipText("play this line — from a few seconds ahead of it where those seconds are its own, " +
				"from the line itself where the line above is still speaking; the preview then carries on down the cut")
		}
		speak.ConnectClicked(func() { n.a.speakEntry(i) })
		roll := gtk.NewButtonFromIconName("view-refresh-symbolic")
		roll.SetTooltipText("re-roll: speak this line again as a different take — same words, same delivery, new draw")
		roll.ConnectClicked(func() { n.a.rerollEntry(i) })
		// One ＋, and it adds BELOW. A second one for the other direction was a
		// third button on a row that already carries five, to reach a place the
		// transport's own ＋ reaches better: that one adds at the playhead, so
		// the way to put a line above the first of a clip is to pause where it
		// should speak and press the ＋ next to ▶ -- which is also how you place
		// every other line, against the picture rather than against a row.
		add := gtk.NewButtonWithLabel("＋")
		add.SetTooltipText("add a line below this one, starting where its audio ends — " +
			"for one above it, pause there and use the ＋ beside the play button")
		add.ConnectClicked(func() {
			n.pullRows()
			n.focusLine(n.addLineAfter(i))
		})
		del := gtk.NewButtonFromIconName("user-trash-symbolic")
		del.SetTooltipText("remove this line — the video plays its own audio here instead")
		del.ConnectClicked(func() { n.deleteLine(i) })
		head.Append(when)
		head.Append(tl)
		head.Append(speak)
		head.Append(roll)
		head.Append(add)
		head.Append(del)

		text := gtk.NewTextView()
		text.SetWrapMode(gtk.WrapWord)
		text.SetMonospace(true) // every editable box in the app is this font
		text.SetTopMargin(2)
		text.SetBottomMargin(2) // the last line needs its descender, same as the first
		text.SetLeftMargin(6)
		text.SetTooltipText(`"[emotion] words" — the emotion is sent to the TTS as the delivery, never spoken; the field on the left is when the line starts.
Words are read by a judge: "angry", "surprised, happy". Add weights to skip it and set the mix exactly: "[angry=1]", "[happy=0.8, surprised=0.4]", "[excited=1]".
The eight it mixes: happy, angry, sad, afraid, disgusted, melancholic, surprised, calm — plus named mixes of them (excited, awed, alarmed, frustrated, tender, ... see the ⓘ).`)
		text.Buffer().SetText(lineText(*e))
		tScroll := gtk.NewScrolledWindow()
		tScroll.SetChild(text)
		tScroll.SetPropagateNaturalHeight(true)
		// the words wrap, so nothing ever runs off the side: a horizontal
		// scrollbar could only take height away from the lines
		tScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
		// The box is as tall as what is in it, between one line and three: with
		// propagate-natural-height set, the scrolled window asks for the view's
		// own height and these two clamp it. A one-line row is one line tall --
		// a page of them used to be a page of mostly empty boxes -- and the
		// fourth line is where the scrollbar starts, with three lines of context
		// above it rather than a few pixels of travel.
		tScroll.SetMinContentHeight(oneLine)
		tScroll.SetMaxContentHeight(threeLines)
		tScroll.AddCSSClass("frame")

		change := func() {
			if !n.building {
				n.pullRows()
				n.saveSoon() // a file write per keystroke, otherwise
				n.restamp(i) // pullRows has just put what is in the box into e
			}
		}
		text.Buffer().ConnectChanged(change)

		box := gtk.NewBox(gtk.OrientationVertical, 4)
		box.SetMarginTop(6)
		box.SetMarginBottom(6)
		box.SetMarginStart(8)
		box.SetMarginEnd(8)
		box.Append(head)
		box.Append(tScroll)
		n.list.Append(box)
		n.rows = append(n.rows, &narrRow{text: text, speak: speak, stamp: stamp})
	}
	n.building = false
	n.restoreOffset(at, sel)
}

// listOffset is how far down the narration list the reader has scrolled, or 0
// where there is no scroller to ask (a test's page).
func (n *narrator) listOffset() float64 {
	if n.listScroll == nil {
		return 0
	}
	if adj := n.listScroll.VAdjustment(); adj != nil {
		return adj.Value()
	}
	return 0
}

// restoreOffset puts the list back where it was after a rebuild, and re-selects
// the row that was selected.
//
// On the idle rather than now: the rows have just been appended and have no
// height yet, so the adjustment has no range to hold the value and setting it
// here would clamp it to nought -- which is the very jump this exists to stop.
// Clamped to what the new list can actually show, because the rebuild may have
// left fewer rows than the offset was measured against (a line deleted, another
// project loaded).
//
// The selection goes back through selectRow, which holds n.building across it:
// picking a row seeks the preview, and a row that was already picked being put
// back is not a hand asking for anything.
func (n *narrator) restoreOffset(at float64, sel int) {
	if n.listScroll == nil || (at <= 0 && sel < 0) {
		return
	}
	glib.IdleAdd(func() {
		if n.listScroll == nil {
			return
		}
		if adj := n.listScroll.VAdjustment(); adj != nil && at > 0 {
			adj.SetValue(math.Max(0, math.Min(at, adj.Upper()-adj.PageSize())))
		}
		if sel >= 0 && sel < len(n.rows) {
			n.selectRow(sel)
		}
	})
}

// restamp redraws the times on a row and on the row above it, and the second
// half is the point. A line's slot runs to where the NEXT line on the clip
// starts (lineEnd, lineWindow), so a typed time -- or a box emptied, which
// hands the seconds back to the clip -- changes two rows and only the edited
// one was ever redrawn. The row above went on printing an end that had moved
// and a ⚠ measured against a slot that was no longer that size, until some
// unrelated rebuild happened to correct it.
func (n *narrator) restamp(i int) {
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(n.rows) && n.rows[j].stamp != nil {
			n.rows[j].stamp()
		}
	}
}

// seekTo cues the preview at a session time, preserving play state.
func (n *narrator) seekTo(t float64) { n.cue(t, n.player != nil && n.player.playing) }

// cue is seekTo with the play state named rather than carried over, for the
// one caller that has nothing to carry: a preview started from a cold page,
// where the file has to be loaded and played in one go. Handing PlaySegment
// play=false and calling Toggle after it would race the preroll -- the seek
// only lands when the bus says AsyncDone, and pendPlay is how it is told.
func (n *narrator) cue(t float64, play bool) {
	ed := n.a.ed
	if ed == nil || n.player == nil {
		return
	}
	// cutVideoAt, not videoAt: this page previews the FINISHED VIDEO, so the
	// picture is the one the scene at t names. videoAt starts from the row the
	// Cut page was last asked to watch, which is a thing you do while editing
	// there -- and it followed the preview here, so a click on the second
	// camera's thumbnails in Cut made Narrate play the second camera for the
	// whole session, whatever the cut says. Nil in a gap; there is no finished
	// video there, and followPlayback skips them anyway.
	v := ed.cutVideoAt(t)
	if v == nil {
		return
	}
	n.setPlayhead(t)
	n.playSeg = -1 // re-trigger the voice on the next tick
	// Already in this file: seek inside it. Reloading the uri tears the pipeline
	// down and builds it again for a jump of a few seconds, and playback only
	// picks up when the fresh preroll reports back on the bus -- a seek just
	// keeps playing, which is what skipping a gap should feel like.
	if n.player.loaded == v.path {
		// before the seek, never after: a rate only takes hold at a seek, and
		// this is the seek
		n.player.SetRate(fxPreviewRateAt(ed.fx, t))
		n.player.SeekTo(t - v.start)
		// the seek cleared ended, so this starts at the new position rather
		// than replaying whatever the stream stopped on
		if play && !n.player.playing {
			n.player.Toggle()
		}
		return
	}
	// which recordings are heard under THIS footage: the render mixes them
	// (clipMixes) and so must the page that judges whether a line fits between
	// them -- a scene shown from one camera and heard from another's
	// microphone was silent here until this
	n.player.SetMix(ed.mixUnder(v))
	n.player.SetRate(fxPreviewRateAt(ed.fx, t))
	n.player.PlaySegment(v.path, t-v.start, -1, play)
	n.playVideoStart = v.start
	n.syncFxSound() // the mix is new, so its hush is owed again
}

// setPlayhead records where the preview is and shows it on the slider, without
// the slider's own handler mistaking the update for a drag. The blue row comes
// with it: every way the preview moves -- the tick, a seek, a drag, ⏪ and ⏩ --
// arrives here, so this is the one place that keeps the list pointing at what
// is on screen.
func (n *narrator) setPlayhead(t float64) {
	n.pos = t
	// the effects follow the line, always: this is the one funnel every mover
	// of the playhead goes through, so hanging them here is what makes a seek
	// into a zoom show the zoom and a seek into a stop show the frozen frame
	n.syncFx(t)
	n.syncFxSound()
	// ...but only while the picture is rolling. Paused, the blue row is the
	// user's: they clicked it to work on it, and a click seeks five seconds
	// ahead of the line so the moment can be watched arriving -- following the
	// playhead there would move the selection off the row that was just picked.
	if n.player != nil && n.player.playing {
		n.selectRow(n.nearestEntry(t))
	}
	if n.slider == nil || n.sliding {
		return
	}
	segs := n.clips()
	n.sliding = true
	n.slider.SetValue(cutPos(segs, t)) // the bar is the cut's clock, this is the session's
	n.sliding = false
	if n.timeLbl != nil {
		// both clocks, because the page needs both: the session time is what the
		// rows, the Cut page and every warning are written in, and the cut time
		// is how far into the finished video this is -- which is the question a
		// bar measuring the cut invites, and the one nothing here answered
		n.timeLbl.SetText(fmt.Sprintf("%02d:%02d · %s/%s", int(t)/60, int(t)%60,
			mmss(cutPos(segs, t)), mmss(cutLen(segs))))
	}
}

// syncFxSound puts the preview's sound where the finished video's is, which
// is the other half of "the play must respect the cut": the lanes the scene at
// the playhead hears, the loudness the volume effects give it, and silence
// where a stop asked for its seconds to be taken out. The picture's half is
// narrate_fxview.go.
//
// A narration line is written against both. There is no room for a line under
// a shout the render is about to boost, and a lane the scene silences is room
// the finished video WILL have -- so a page that plays every lane at the
// recording's own level is a page where every gap looks too small.
//
// None of the three needs a seek, so all three settle on the playhead itself
// (setPlayhead) and are as true while scrubbing as while playing.
// gameGain is the level the footage and the recordings under it play at, at
// session second t: the cut's own say over those seconds (a volume effect),
// times the game's level wherever the render is going to duck it.
//
// The render's rule is per CLIP, not per line: a clip with anything written on
// it has its WHOLE bed at GameVol, start to end, rather than a duck that
// follows the words (encodeClip). This page has to sound the same way, because
// the one judgement it exists for -- is there room here for this line -- was
// being made against a mix the finished video never has. The game played at
// full level under every line, so every gap sounded narrower than it was going
// to be, and lines were written short for room that was already there.
func (n *narrator) gameGain(t float64) float64 {
	g := 1.0
	if n.a != nil && n.a.ed != nil {
		g = fxGainAt(n.a.ed.fx, t)
	}
	if n.clipSpeaks(t) {
		g *= n.a.gameVol()
	}
	return g
}

// clipSpeaks is whether the clip at session second t has anything written on
// it. The render's own question, asked the render's way -- the whole clip, not
// the seconds a line covers -- so that the level does not step up and down
// inside a clip here while the video holds it flat.
func (n *narrator) clipSpeaks(t float64) bool {
	for _, e := range n.entries {
		if t >= e.S && t < e.E && strings.TrimSpace(e.Text) != "" {
			return true
		}
	}
	return false
}

func (n *narrator) syncFxSound() {
	ed, p := n.a.ed, n.player
	if ed == nil || p == nil {
		return
	}
	p.SetFxGain(n.gameGain(n.pos))
	s := n.heardScene(n.pos)
	// Two silences, and this page owes both. fxHush is a speed effect that asked
	// for its seconds to be taken out of the sound; cardHush is a card laid
	// over the footage, which takes those seconds' audio with the picture
	// unless it was put there for the picture alone (keepsSoundUnder).
	//
	// The preview cannot load a card -- it is not one of the session's
	// recordings, so cutVideoAt answers with whatever is on that lane and cue
	// leaves the previous file running. The picture being wrong is a known
	// hole; the SOUND being wrong is not survivable here, because the whole of
	// this page is judging a line against the audio it has to fit between.
	p.SetMuted(fxHush(ed.fx, n.pos) || cardHush(overInsert(ed.segs, n.pos)))
	base, until := "", 0.0
	if v := ed.cutVideoAt(n.pos); v != nil {
		base = v.base
		// the answer holds to the end of this clip, or to the next clip's
		// start, in the file's own seconds: a lane started now stops there
		// by itself (auxAudio.stopAt), the way the Cut page's does
		if s != nil {
			until = v.at(s.E)
		} else if _, next := gapAt(ed.segs, n.pos); next >= 0 {
			until = v.at(ed.segs[next].S)
		}
	} else if p.loaded != "" {
		base = baseName(p.loaded) // past a clip's end: the file still running
	}
	own, quiet := hushOf(s, base)
	p.Hush(own, quiet, until)
}

// overInsert is the card the cut lays over the footage at t. Spliced cards are
// deliberately not here: those own no session time at all (S == E, see
// cutSeg.spliced) -- they are a point the footage is cut open at -- so a
// playhead running down the session never sits inside one.
func overInsert(segs []cutSeg, t float64) *cutSeg {
	for i := range segs {
		if s := &segs[i]; s.isInsert() && !s.spliced() && t >= s.S && t < s.E {
			return s
		}
	}
	return nil
}

// needsReload is the tick's one question about the file under the picture: is
// the preview on the recording the CUT names at t. Split out of followPlayback
// because the tick is GStreamer and this is arithmetic, and because the case it
// was missing is invisible from inside the tick -- two scenes that touch, taken
// on different lanes. Nothing moves, no gap is jumped, and the answer changes.
func needsReload(segs []cutSeg, vids []tlVideo, loaded string, t float64) bool {
	v := cutVideoOn(segs, vids, t)
	return v != nil && loaded != v.path
}

// heardScene is the clip whose answer about the lanes the preview is under,
// which is not always the clip the playhead is inside.
//
// The Cut page's rule is that a second belonging to no scene is heard whole:
// there, scrubbing through material the edit removed should sound like
// something. This page is the opposite. It previews the FINISHED video, where
// those seconds do not exist -- and it spends real time outside a clip on
// purpose: the tick holds past a clip's end while a line finishes speaking (the
// render grows the clip for exactly that, see followPlayback), and skipping a
// gap costs a seek and a preroll. With no scene to ask, every lane came back at
// full level in those seconds, so a recording's own voice -- silenced in every
// scene of the cut, and replaced by the narration -- spoke again underneath the
// line that replaced it.
//
// So: the clip being played OUT of, and before the first one the clip being
// played INTO. Either way it is the clip whose material is actually sounding.
func (n *narrator) heardScene(t float64) *cutSeg {
	ed := n.a.ed
	if ed == nil {
		return nil
	}
	if i := ed.segAt(t); i >= 0 {
		return &ed.segs[i]
	}
	prev, next := -1, -1
	for i := range ed.segs {
		if ed.segs[i].E <= t && (prev < 0 || ed.segs[i].E > ed.segs[prev].E) {
			prev = i
		}
		if ed.segs[i].S > t && (next < 0 || ed.segs[i].S < ed.segs[next].S) {
			next = i
		}
	}
	switch {
	case prev >= 0:
		return &ed.segs[prev]
	case next >= 0:
		return &ed.segs[next]
	}
	return nil
}

// syncPlayRate is the one that does: a speed effect only takes hold at a seek,
// so crossing into or out of one while the picture runs has to seek to where
// the stream already is. The same bargain Cut makes -- a small hitch at each
// boundary, in exchange for a preview that is actually the speed it claims.
func (n *narrator) syncPlayRate() {
	if n.player != nil && n.a.ed != nil {
		n.player.SetRateNow(fxPreviewRateAt(n.a.ed.fx, n.pos))
	}
}

// frameStep nudges the preview by whole frames, the way Cut's playhead
// moves. It is what the wheel over the slider does: ⏪ and ⏩ jump three seconds
// to find a moment, and this lands on it -- "the chest is on screen HERE" is a
// frame, and a line's placement is only as good as the frame it was judged
// against.
//
// Frames, not seconds, so the source's own rate decides the step: a 60 fps
// capture nudges half as far as a 30 fps one, which is what a frame means.
// Inside the cut only. The slider shows the cut and its gaps snap over, so a
// step that would land in removed material lands on the near edge of the
// neighbouring clip instead -- the same rule the preview follows when it plays
// through one.
func (n *narrator) frameStep(frames int) {
	segs := n.clips()
	if n.a.ed == nil || n.player == nil || len(segs) == 0 || frames == 0 {
		return
	}
	fps := 30.0 // no video under the playhead: a plausible rate beats no step
	if v := n.a.ed.cutVideoAt(n.pos); v != nil && v.fps > 0 {
		fps = v.fps
	}
	n.seekTo(frameTarget(segs, n.pos, fps, frames))
}

// frameTarget is where that step lands: the arithmetic, with the cut's gaps
// closed up. Split out because it is the part that can be wrong.
func frameTarget(segs []cutSeg, pos, fps float64, frames int) float64 {
	return snapToCut(segs, pos, pos+float64(frames)/fps, 1/fps)
}

// cutEdge is how far inside a clip a scrub lands when it snaps back onto its
// tail. The clip's end is already the gap after it, so landing exactly there
// would be landing on the thing being snapped away from.
const cutEdge = 0.05

// snapToCut keeps a scrub on the cut. A time inside a clip is itself; a time in
// the gap between two clips is footage Cut removed -- the one thing the
// finished video will never contain -- so it lands on the near edge of the clip
// the move was HEADING for: forward, the next clip's first frame; back, the
// tail of the clip behind. Off either end of the cut, its first or last frame.
//
// Direction is the whole point. The preview's own gap-skip only ever runs
// forward, which is right while the video plays and wrong for a drag: dragging
// the handle left through a gap used to spit the playhead out at the far side,
// past everything the drag was trying to reach.
func snapToCut(segs []cutSeg, from, to, edge float64) float64 {
	if len(segs) == 0 {
		return to
	}
	cur, next := gapAt(segs, to)
	if cur >= 0 {
		return to
	}
	last := segs[len(segs)-1].E - edge
	if to < from { // going back
		switch {
		case next > 0:
			return segs[next-1].E - edge // the tail of the clip behind
		case next == 0:
			return segs[0].S // in the run-up to the cut: nothing is behind it
		default:
			return last // back from past the end
		}
	}
	if next >= 0 {
		return segs[next].S
	}
	return last
}

// cutLen, cutPos and cutAt are the session clock and the cut's own clock, and
// the conversion between them. The cut's clock counts only what is kept: it is
// the clock of the video that will actually be produced, which is what a seek
// bar under a preview of that video should measure.
//
// This is what "the slider jumps over what was removed" means in the end -- not
// a handle that snaps out of the gaps it can be dropped into, but a bar the gaps
// are not on. A session of half an hour cut down to five minutes had five sixths
// of its slider standing for footage nobody will ever see, and dragging across
// it was a fight with the snap.
func cutLen(segs []cutSeg) float64 {
	tot := 0.0
	for _, s := range segs {
		tot += math.Max(0, s.length())
	}
	return tot
}

// cutPos is a session time on the cut's clock: how much kept material comes
// before it. A time in a gap -- the preview passes through them, briefly, on its
// way to the next clip -- reads as the boundary it is about to reach, so the
// handle waits at the join instead of jumping ahead of the picture.
func cutPos(segs []cutSeg, t float64) float64 {
	acc := 0.0
	for _, s := range segs {
		if t <= s.S {
			return acc
		}
		if t < s.E {
			off := t - s.S
			if s.Rate > 0 {
				off /= s.Rate // slowed footage: a session second is 1/rate cut seconds
			}
			return acc + off
		}
		acc += math.Max(0, s.length())
	}
	return acc
}

// cutAt is the way back: a place on the cut's clock as a session time. Every
// value the slider can hold maps into a clip, which is the point of the whole
// exercise -- there is no such thing as dropping the handle on removed footage.
func cutAt(segs []cutSeg, x float64) float64 {
	if len(segs) == 0 {
		return x
	}
	acc := 0.0
	for _, s := range segs {
		d := math.Max(0, s.length())
		if x < acc+d {
			if s.Dur > 0 {
				// a card runs for d, but it happens AT a point of the session:
				// there is no session time inside it to land on, only the
				// moment the footage is cut open for it
				return s.S
			}
			if s.Rate > 0 {
				// slowed footage: the bar's seconds cover 1/rate of the session's
				return s.S + math.Max(0, x-acc)*s.Rate
			}
			return s.S + math.Max(0, x-acc)
		}
		acc += d
	}
	// the far end of the bar is the cut's last frame, not the instant after it:
	// e.E is already the gap that follows, the same edge snapToCut backs off
	last := segs[len(segs)-1]
	return math.Max(last.S, last.E-cutEdge)
}

// syncSlider sizes the slider to the cut. Called wherever the cut or the
// narration may have changed shape: page build, rescan, after a run.
func (n *narrator) syncSlider() {
	if n.slider == nil {
		return
	}
	segs := n.clips()
	if len(segs) == 0 {
		return
	}
	n.sliding = true
	// zero-width would make the handle unmovable and the value meaningless; a
	// cut of nothing is not a state the page can preview anyway
	n.slider.SetRange(0, math.Max(0.001, cutLen(segs)))
	n.slider.SetValue(cutPos(segs, n.pos))
	n.sliding = false
}

// The run bar drives the preview through these; see transport in pipeline.go.
// Both players move together: the picture leads and the narration rides along.
func (n *narrator) playing() bool { return n.player != nil && n.player.Playing() }
func (n *narrator) cued() bool    { return n.player != nil && n.player.Cued() }

func (n *narrator) toggle() {
	if n.player == nil {
		return
	}
	// held goes with it: a pause the user asked for is theirs to undo, and a
	// synthesis finishing must not start the video back up behind them
	n.held = false
	// the preview and a single line share one audio player, so whichever starts
	// second takes it: a line auditioned on its own ends here rather than
	// running on underneath the picture
	n.claimVoice()
	// nothing loaded: the preview has never been pointed at anything, so start
	// it at the top of the cut. Toggling an empty pipeline would report itself
	// as playing while the frame stayed black.
	if n.player.loaded == "" {
		segs := n.clips()
		if len(segs) == 0 {
			n.a.setStatus("nothing to preview yet — cut some clips first")
			return
		}
		n.cue(segs[0].S, true)
		if n.player.loaded == "" {
			n.a.setStatus("no recording covers the start of the cut")
			return
		}
		n.started = true
		n.a.updateRunControls()
		return
	}
	n.player.Toggle()
	n.started = n.started || n.player.Playing()
	n.a.updateRunControls()
	if n.player.Playing() {
		// forget which line was voiced, or the tick would wait for the picture
		// to reach the NEXT one before speaking again -- a resume in the middle
		// of a line would play it mute. The voice is dropped with it: the wav
		// in there may be stale (the line could have been edited while paused),
		// and the tick reloads the right one at the right offset either way.
		n.playSeg = -1
		if n.voice != nil && (n.voice.playing || n.voice.Cued()) {
			n.voice.Stop()
		}
		return
	}
	if n.voice != nil {
		n.voice.Pause()
	}
}

// claimVoice ends an audition and hands both players back to the preview. While
// solo is set the tick leaves that audio alone (followPlayback), so it has to be
// cleared by whoever takes over -- otherwise a line still sounding over a still
// frame would go on playing under whatever started next.
func (n *narrator) claimVoice() {
	if n.solo < 0 {
		return
	}
	n.solo = -1
	if n.voice != nil {
		n.voice.Stop()
	}
	n.speaking = -1
	n.syncSpeakIcons()
}

func (n *narrator) stop() {
	if n.player != nil {
		n.player.Stop()
	}
	if n.voice != nil {
		n.voice.Stop()
	}
	n.playSeg, n.jumped, n.held = -1, -1, false
	n.speaking, n.solo = -1, -1
	n.started = false // ⏹ hands the run bar back to the step itself
}

// ---- playback with voice ----------------------------------------------------

// clips is what the preview follows: the cut from Cut, or the narration
// entries when that page has not been built this session. They are the same list --
// narration is written one entry per clip -- but the cut exists first, and the
// preview has to respect it before a word has been written.
func (n *narrator) clips() []cutSeg {
	if n.a.ed != nil && len(n.a.ed.segs) > 0 {
		return n.a.ed.segs
	}
	segs := make([]cutSeg, len(n.entries))
	for i, e := range n.entries {
		segs[i] = cutSeg{S: e.S, E: e.E}
	}
	return segs
}

// gapAt places a session time against the cut: the clip covering t, and the
// first clip starting after it. cur < 0 with next >= 0 is a hole the edit
// removed; both < 0 is past the end of the cut.
func gapAt(segs []cutSeg, t float64) (cur, next int) {
	next = -1
	for i, s := range segs {
		if t >= s.S && t < s.E {
			return i, -1
		}
		if s.S > t {
			return -1, i
		}
	}
	return -1, next
}

// entryAt finds the narration line covering a session time. It is looked up by
// time rather than by clip index so that a cut edited after narrating speaks
// the right line, or none, instead of an off-by-one one.
//
// An entry with no words in it is not a line: it is a clip the narration left
// alone, and the preview runs it the way the render will, on its own audio. It
// has to be skipped HERE rather than further down, or the tick finds no wav for
// it, stops the picture and sends the empty string to the TTS server.
//
// The line starts at S+At, not at S: before that the clip is playing but the
// line has not arrived yet, which is -1 exactly like a clip with no line --
// the render leaves that stretch to the game, and so does the preview. A
// clip's second line ends the first one's window: the entries are sorted, so
// the next entry on the same clip is simply the next in the list.
// leadIn is where a row's ▶ -- or a click on the row -- drops the preview: a
// few seconds ahead of the line, so the moment it was written for can be
// watched arriving.
//
// Never back inside the line above it. A clip carries as many lines as the
// writer put on it, and three seconds ahead of the second line on a clip is the
// first one still mid-sentence: entryAt hands those seconds to that earlier line
// -- it owns the clip until this one starts -- so the tick found it under the
// playhead and resumed its wav from wherever the seek had landed in it. Press
// ▶ on line 5, hear the back half of line 4. A run-in is worth having where the
// seconds belong to nobody; where they belong to another line, starting on the
// line itself is what the button says it does.
//
// entryAt is asked rather than the times compared here, so this cannot drift
// from the rule that actually decides who speaks. Asked about a line that is
// first on its clip it answers about this one, or about nobody, and either way
// the run-in stands: the seconds before the first line of a clip are the clip
// arriving, which is exactly what there is to watch.
func (n *narrator) leadIn(i int) float64 {
	e := n.entries[i]
	t := math.Max(e.S, e.S+e.At-narrRunIn) // not before the clip: no video there
	if n.entryAt(t) >= 0 {
		return e.S + e.At // a line is speaking there; start on this one instead
	}
	return t
}

func (n *narrator) entryAt(t float64) int {
	for i, e := range n.entries {
		if strings.TrimSpace(e.Text) != "" && t >= e.S+e.At && t < n.lineEnd(i) {
			return i
		}
	}
	return -1
}

// nearestEntry is which row the picture is on. entryAt answers a narrower
// question -- whose wav should be sounding at t -- and answers -1 for most of
// the running time: before a line arrives, after it has finished, and through
// every clip the narration deliberately left alone. Those are exactly the
// moments you are watching for something to fix, so the blue row used to sit on
// whatever spoke last while the video ran somewhere else entirely.
//
// So this one always answers. A line owns the stretch from where it starts to
// where the next line on the same clip does, or to the clip's end; t inside
// that stretch is that row, distance zero. Outside every stretch -- in the gap
// between two clips, or past the end of the cut -- it is the row whose stretch
// is nearest, which is the one you would reach for. Silent markers count: a
// clip with no line is still a row, and "where am I" has an answer there too.
func (n *narrator) nearestEntry(t float64) int {
	best, bestD := -1, math.Inf(1)
	for i, e := range n.entries {
		s, end := e.S+e.At, n.lineEnd(i)
		d := 0.0
		switch {
		case t < s:
			d = s - t
		case t > end:
			d = t - end
		}
		// ties go to the later row: two lines on one clip meet at a shared
		// instant -- where the first one's stretch ends is where the second's
		// begins -- and at that instant the one about to be heard is the answer,
		// the same way entryAt hands the second its own window there.
		if d <= bestD {
			best, bestD = i, d
		}
	}
	return best
}

// voiceBusy reports that a narration line is sounding right now -- the state
// the preview must not cut mid-word.
func (n *narrator) voiceBusy() bool {
	return n.voice != nil && n.voice.playing && n.speaking >= 0
}

// selectRow moves the blue row to the line the playback is on, so the list
// follows the sound. Selecting a row by hand seeks the preview -- that is what
// clicking one is for -- so this programmatic move rides the building flag,
// the same guard every rebuild-driven signal uses. No focus grab: the user may
// be typing in another row's box, and the highlight must not steal the cursor.
func (n *narrator) selectRow(i int) {
	if n.list == nil || i < 0 {
		return
	}
	row := n.list.RowAtIndex(i)
	if row == nil || row == n.list.SelectedRow() {
		return
	}
	was := n.building
	n.building = true
	n.list.SelectRow(row)
	n.building = was
}

// addLineAt inserts a line at a session time, the way a subtitle editor's
// "add cue at playhead" does. The line belongs to the cut segment under t --
// the cut is Cut's and stays respected: between clips there is nothing to
// narrate, so the button reports that instead of inventing an interval. A clip
// whose one entry is the empty "leave it alone" marker is not given a second
// row; that entry IS the clip's line the moment it gets a placement and words.
// Returns the index of the row to edit, or -1 with the reason set as status.
func (n *narrator) addLineAt(t float64) int {
	segs := n.clips()
	si := -1
	for i, s := range segs {
		if t >= s.S && t < s.E {
			si = i
			break
		}
	}
	if si < 0 {
		n.a.setStatus("the playhead is between clips — the cut has nothing to narrate here")
		return -1
	}
	s := segs[si]
	at := math.Min(math.Max(0, t-s.S), math.Max(0, s.length()-1))
	// the clip's own entries, if any
	lo, hi := -1, -1
	for i, e := range n.entries {
		if math.Abs(e.S-s.S) <= 0.05 && math.Abs(e.E-s.E) <= 0.05 {
			if lo < 0 {
				lo = i
			}
			hi = i
		}
	}
	// a second can only hold one line. Where one already starts, the + is a
	// jump to it, not a duplicate; where one is still SPEAKING, there is no
	// room and the honest answer is where the room begins.
	for i := lo; lo >= 0 && i <= hi; i++ {
		e := n.entries[i]
		if math.Abs(e.At-at) < 1 {
			n.a.setStatus("a line already starts here — edit it, or move the playhead")
			return i
		}
		if end := e.At + n.speechDur(e); strings.TrimSpace(e.Text) != "" &&
			at > e.At && at < end+0.3 {
			n.a.setStatus(fmt.Sprintf("a line is speaking here until %s — add after it",
				fmtClock(s.S+end)))
			return -1
		}
	}
	if lo >= 0 && lo == hi && strings.TrimSpace(n.entries[lo].Text) == "" {
		n.entries[lo].At = at // the silent marker becomes the line
		n.save()
		n.rebuildRows()
		return lo
	}
	// insert in placement order among the clip's entries; a clip with none yet
	// gets its first, placed after whichever earlier clip's entries exist
	ins := len(n.entries)
	if lo >= 0 {
		ins = hi + 1
		for i := lo; i <= hi; i++ {
			if at < n.entries[i].At {
				ins = i
				break
			}
		}
	} else {
		for i, e := range n.entries {
			if e.S > s.S+0.05 {
				ins = i
				break
			}
		}
	}
	e := narrEntry{S: s.S, E: s.E, At: at}
	n.entries = append(n.entries[:ins], append([]narrEntry{e}, n.entries[ins:]...)...)
	n.save()
	n.rebuildRows()
	return ins
}

// addLineAfter is the row's own +: a line starting where this one's audio ends
// plus a beat, which is the first second the playhead rule would allow anyway.
func (n *narrator) addLineAfter(i int) int {
	if i < 0 || i >= len(n.entries) {
		return -1
	}
	e := n.entries[i]
	at := e.At + 1.2 // a marker's "audio" is nothing: right after its start
	if strings.TrimSpace(e.Text) != "" {
		at = e.At + n.speechDur(e) + 0.5
	}
	if at >= e.E-e.S-1 {
		n.a.setStatus("no room after this line — the clip ends first")
		return -1
	}
	return n.addLineAt(e.S + at)
}

// moveLine puts line i where the row's time field says, and is the whole answer
// to a typed time. The field used to do this itself, in one line, by clamping
// into the line's OWN clip -- so a time in the next clip moved the line to one
// second before the end of this one, and the box went on showing what was typed.
// The row then read "12:42 – 11:07.5": an end before its start, two numbers
// disagreeing about the same line, and nothing on screen saying which was real.
//
// A time in another clip is not a mistake, it is a move. The entry carries its
// clip's bounds, and the spoken wav is keyed by words and delivery only (see
// ttsKey), so re-homing it costs nothing and does what was asked. A time in a
// gap, or past the cut, has no clip to land in: there the clamp is right, but it
// has to be said out loud rather than left as a silent disagreement.
//
// Returns true when the line changed clips, which is a big enough move that the
// caller re-sorts the list around it.
func (n *narrator) moveLine(i int, t float64) bool {
	if i < 0 || i >= len(n.entries) {
		return false
	}
	e := &n.entries[i]
	segs := n.clips()
	ci, _ := gapAt(segs, t)
	if ci < 0 {
		// keep it where it is, and name the clip it may live in -- "that time is
		// not in this line's clip" is the one thing the old silent clamp never
		// managed to say
		e.At = math.Min(math.Max(0, t-e.S), math.Max(0, e.E-e.S-1))
		n.save()
		n.a.setStatus(fmt.Sprintf("%s is outside the cut — this line stays in its clip (%s–%s), at %s",
			fmtClock(t), fmtClock(e.S), fmtClock(e.E), fmtClock(e.S+e.At)))
		return false
	}
	s := segs[ci]
	from := cutSeg{S: e.S, E: e.E} // the clip it may be leaving, for the note below
	moved := math.Abs(s.S-e.S) > 0.05 || math.Abs(s.E-e.E) > 0.05
	e.S, e.E = s.S, s.E
	// a line still may not start in the last second of its clip: there is no
	// room to speak, and everything downstream (lineWindow, the ⚠, the render's
	// spill) is written against that rule
	e.At = math.Min(math.Max(0, t-s.S), math.Max(0, s.length()-1))
	at := e.At // read before the caller's rebuild can move the entry
	// The clip the line just left is now empty, and that has to be recorded or
	// staleFor reads the hole as a clip the cut moved and the next ▶ rewrites
	// narration nobody asked it to touch. It used to be recorded as a blank
	// entry, which meant a blank ROW -- a text box's worth of page saying
	// nothing. It is a line of JSON now (silentFor).
	//
	// Only when the line was the last one there, and only for a clip that is
	// really in the cut: a line sitting on video the cut no longer has (an
	// orphan, see refitEntries) leaves nothing behind.
	if moved && clipIndex(segs, from) >= 0 && !clipHasEntry(n.entries, from, i) {
		n.silent = markSilent(n.silent, from)
	}
	n.save()
	if moved {
		n.a.setStatus(fmt.Sprintf("moved this line to the clip at %s — it now starts at %s",
			fmtClock(s.S), fmtClock(s.S+at)))
	}
	return moved
}

// onClip, clipIndex and clipHasEntry are "the same clip" asked three ways. The
// bounds are compared with a twentieth of a second of slack, the tolerance the
// whole page uses for a line sitting on a clip: the numbers make the round trip
// through JSON and through the cut editor's own arithmetic, and a line is on the
// clip it was written for or it is not.
func onClip(s cutSeg, S, E float64) bool {
	return math.Abs(s.S-S) <= 0.05 && math.Abs(s.E-E) <= 0.05
}

func clipIndex(segs []cutSeg, s cutSeg) int {
	for i, c := range segs {
		if onClip(c, s.S, s.E) {
			return i
		}
	}
	return -1
}

// silentFor, markSilent and pruneSilent are the record of a clip you emptied on
// purpose.
//
// It exists because staleFor reads COVERAGE -- every clip in the cut needs a
// line, or the narration was written for a different cut and ▶ must write it
// again -- and a clip you deleted the last line of has none. Without the record
// the next ▶ would helpfully fill it back in, which is the opposite of what the
// trash was pressed for; with it, "this clip plays its own audio" is a decision
// the file keeps saying, and it says it in one line of JSON instead of a blank
// row taking up a third of the page.
//
// Bounds, not an index: if the clip itself moves in Cut, the record stops
// matching and the clip counts as uncovered again. That is right -- the cut
// changed under the decision, and that is exactly what staleness is.
func silentFor(silent []cutSeg, s cutSeg) bool {
	for _, q := range silent {
		if onClip(s, q.S, q.E) {
			return true
		}
	}
	return false
}

func markSilent(silent []cutSeg, s cutSeg) []cutSeg {
	if silentFor(silent, s) {
		return silent
	}
	return append(silent, cutSeg{S: s.S, E: s.E})
}

// pruneSilent drops the records for clips that have a line again, so a clip
// re-narrated by hand is not carrying a note saying it is quiet.
func pruneSilent(silent []cutSeg, entries []narrEntry) []cutSeg {
	out := silent[:0]
	for _, q := range silent {
		if !clipHasEntry(entries, q, -1) {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clipHasEntry is "does this clip still have a line on it", skipping one row --
// the one being moved or deleted, which is on its way off the clip.
func clipHasEntry(entries []narrEntry, s cutSeg, skip int) bool {
	for j, o := range entries {
		if j != skip && onClip(s, o.S, o.E) {
			return true
		}
	}
	return false
}

// focusLine puts a row under the user's fingers: selected, cursor in the box.
func (n *narrator) focusLine(i int) {
	if i < 0 {
		return
	}
	n.selectRow(i)
	if i < len(n.rows) {
		n.rows[i].text.GrabFocus()
	}
}

// addLineClicked is the button behind addLineAt: pause, place, select, and put
// the cursor in the new box so the next thing typed is the line's words.
func (a *App) addLineClicked() {
	n := a.narr
	if n == nil {
		return
	}
	if n.player != nil && n.player.playing {
		n.player.Pause() // a line is placed on a moment, not on a moving target
		if n.voice != nil {
			n.voice.Pause()
		}
	}
	n.pullRows()
	i := n.addLineAt(n.pos)
	n.focusLine(i)
}

// deleteLine removes a row -- all of it, including the last one on a clip.
//
// It used to turn that last one into the empty "this clip plays its own audio"
// marker instead, so the trash on such a row cleared the words and left the row
// sitting there, and pressing it again did nothing at all. A button that does
// not remove the thing it is pointed at is worse than no button. What the
// marker was protecting is staleFor, which read a clip with no entry as a clip
// that had MOVED and made the next run rewrite narration nobody touched; that
// is fixed where it belongs, in staleFor.
//
// The clip then has no row on the page. That is what removing its only line
// means, and the status says how to get one back.
func (n *narrator) deleteLine(i int) {
	if i < 0 || i >= len(n.entries) {
		return
	}
	// the indices the playback state holds are about to shift; the picture may
	// keep rolling, but a voice mid-line might be the very line being deleted
	if n.voice != nil && n.voice.playing {
		n.voice.Pause()
	}
	n.playSeg, n.speaking, n.solo = -1, -1, -1
	e := n.entries[i]
	clip := cutSeg{S: e.S, E: e.E}
	n.entries = append(n.entries[:i], n.entries[i+1:]...)
	if clipHasEntry(n.entries, clip, -1) {
		n.a.setStatus("line removed")
	} else {
		n.silent = markSilent(n.silent, clip)
		n.a.setStatus(fmt.Sprintf("line removed — the clip at %s plays its own audio now; "+
			"put a line back with ＋ on the transport", fmtClock(e.S)))
	}
	n.save()
	n.rebuildRows()
	n.updateOut()
}

// fmtClock and parseClock are the row's time field: session seconds as
// "mm:ss.d" out, and back in as "mm:ss", "mm:ss.5", "h:mm:ss" or plain
// seconds -- whatever the user reaches for.
func fmtClock(t float64) string {
	if t < 0 {
		t = 0
	}
	return fmt.Sprintf("%02d:%04.1f", int(t)/60, t-float64(int(t)/60*60))
}

// cleanClock strips everything a clock cannot contain, so the field corrects
// itself as it is typed instead of complaining afterwards.
func cleanClock(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == ':' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseClock(s string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0, false
	}
	t := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		t = t*60 + v
	}
	return t, true
}

// sortEntries keeps the list in playing order -- by clip, then by placement --
// which everything downstream leans on: entryAt bounds a line's window by the
// NEXT entry, lineWindow reads its sibling at i+1, and the rows read top to
// bottom the way the video plays. A hand-edited time can break the order; a
// rebuild restores it.
func (n *narrator) sortEntries() {
	sort.SliceStable(n.entries, func(a, b int) bool {
		if n.entries[a].S != n.entries[b].S {
			return n.entries[a].S < n.entries[b].S
		}
		return n.entries[a].At < n.entries[b].At
	})
}

// lineEnd is where line i's slot closes on the session clock: the same clip's
// next line, or the clip's end. It is what the row shows after the dash --
// unlike lineWindow it leaves the render's growth out, because the growth is
// the mix's spill room, not time the user placed anything in.
// nextSpoken is the line whose arrival is where line i's time runs out: the
// next one on the same clip, if it has words. Three places worked this out for
// themselves and one of them compared the two starts for exact equality --
// which is not the twentieth of a second of slack the rest of the page uses for
// "the same clip" (see onClip), so entryAt could put a line's end somewhere
// lineEnd did not and ▶ would hand the seconds to a different row than the ⚠
// was measuring. One spelling, so they cannot disagree.
func (n *narrator) nextSpoken(i int) int {
	if i+1 < len(n.entries) && math.Abs(n.entries[i+1].S-n.entries[i].S) <= 0.05 &&
		strings.TrimSpace(n.entries[i+1].Text) != "" {
		return i + 1
	}
	return -1
}

func (n *narrator) lineEnd(i int) float64 {
	if j := n.nextSpoken(i); j >= 0 {
		return n.entries[i].S + n.entries[j].At
	}
	return n.entries[i].E
}

// clipTop is whether row i is the first line written for its clip -- the row
// the whole clip's schedule hangs off, and the one a warning about the clip
// belongs on.
func (n *narrator) clipTop(i int) bool {
	return i == 0 || math.Abs(n.entries[i].S-n.entries[i-1].S) > 0.05
}

// clipLines is a clip's rows in the shape the render packs them: every line
// written for the same stretch, at the moment it was placed and as long as it
// takes to say. Wordless rows are left out for the same reason the render
// leaves them out -- there is nothing to speak, so there is nothing to fit.
func (n *narrator) clipLines(top int) []prodLine {
	var out []prodLine
	for i := top; i < len(n.entries) && (i == top || !n.clipTop(i)); i++ {
		if e := n.entries[i]; strings.TrimSpace(e.Text) != "" {
			out = append(out, prodLine{at: e.At, dur: n.speechDur(e)})
		}
	}
	return out
}

// clipOverrun is how far a clip's narration runs past the end of the clip after
// the render has done everything it can that costs nothing: stacked the lines
// up with a breath between them (packLines) and grown the slot as far as it is
// allowed to (maxExtend). Zero when it fits.
//
// This is the render's own arithmetic, asked here, because the per-line ⚠ next
// to it answers a different question -- "has THIS line room before the next one
// arrives" -- and by construction cannot see the one the render complains
// about. An overrun is CUMULATIVE: a first line three seconds too long pushes
// every line under it three seconds later, each of which may have all the room
// in the world of its own, and it is the last one that falls off the end. That
// came back as seven lines of "the narration does not fit where it was placed"
// after twenty minutes of encoding, about words that could have been changed in
// ten seconds on this page.
//
// The footage bound the render also applies is deliberately not here: whether a
// recording actually HAS four more seconds past the clip is a fact about the
// file, and guessing at it would turn a warning that is right into one that is
// sometimes right.
func (n *narrator) clipOverrun(top int) float64 {
	lines := n.clipLines(top)
	if len(lines) == 0 {
		return 0
	}
	e := n.entries[top]
	return math.Max(0, narrRun(lines, 1)-(e.E-e.S)-maxExtend)
}

// clipSlack is how much earlier the render may slide a clip's whole schedule
// before it has to start speeding the words up instead: the first line's
// placement, down to the lead-in, and no further.
func (n *narrator) clipSlack(top int) float64 {
	lines := n.clipLines(top)
	if len(lines) == 0 {
		return 0
	}
	packLines(lines, 1)
	return math.Max(0, lines[0].delay-narrLead)
}

// lineWindow is how long line i may speak: until the same clip's next line
// starts, or -- for a clip's last line -- to the clip's end plus the growth
// the render allows it (maxExtend). It is the number the ⚠ compares against.
func (n *narrator) lineWindow(i int) float64 {
	e := n.entries[i]
	if j := n.nextSpoken(i); j >= 0 {
		return n.entries[j].At - e.At
	}
	return (e.E - e.S) - e.At + maxExtend
}

// speechRate is how much text this TTS gets through in a second, counted in
// characters. Words are the wrong unit: "a second a word" -- what this used to
// guess -- read a ten-word line as ten seconds when it speaks in three, which
// is not a near miss but a ⚠ on a line that fits fine. Characters take word
// length and punctuation with them, and 15 a second is the 2.5 words a second
// narrBudget already hands out, so the estimate and the budget finally agree.
const speechRate = 15.0

// rateBounds is how far a measured rate is allowed to move the estimate. A
// wav that failed halfway, or one line of "Go!" padded with silence, would
// otherwise teach the page a speaking rate nobody speaks at.
const (
	rateMin = 8.0
	rateMax = 28.0
)

// measured is the take's real length, or 0 if it has not been measured yet.
// Probed once per wav and then remembered -- and probed off the GTK thread,
// because this is asked on every rebuild and on every playback tick, and an
// ffprobe is a spawned process: a page that waited for one per fresh take
// stood still for as long as the answers took, which read as the whole UI
// hitching. The estimate stands in for the take until the answer lands
// (speechDur), and the landing rebuilds the rows so the ⚠ and the printed
// ends are re-measured against the real length.
func (n *narrator) measured(e narrEntry) float64 {
	wav := n.a.ttsWav(e)
	if d, ok := n.durCache[wav]; ok {
		return d
	}
	// headless (tests) has no idle loop for the answer to land on; a probe
	// already in flight needs no second one; a take not yet spoken has
	// nothing to measure. A probe that failed is none of these: it stays
	// unmarked and is simply tried again when next asked, exactly as the
	// synchronous version retried -- the wav may still have been being
	// written when the first probe read it.
	if n.durCache == nil || n.durProbe == nil || n.durProbe[wav] || !exists(wav) {
		return 0
	}
	n.durProbe[wav] = true
	go func() {
		d, err := ffprobeDur(wav)
		glib.IdleAdd(func() {
			delete(n.durProbe, wav)
			if err != nil {
				return
			}
			n.durCache[wav] = d
			n.queueRebuild() // the (~) rows re-measure against the real length
		})
	}()
	return 0
}

// spokenRate is speechRate corrected by this narration's own takes. Every
// voice, language and emotion speaks at its own pace, and the lines already in
// the cache measure it exactly -- so once a few are spoken, the lines that are
// not stop being estimated against a stranger's rate.
func (n *narrator) spokenRate() float64 {
	chars, secs := 0, 0.0
	for _, e := range n.entries {
		t := strings.TrimSpace(e.Text)
		if t == "" {
			continue
		}
		if d := n.measured(e); d > 0 {
			chars += len([]rune(t))
			secs += d
		}
	}
	if secs < 3 || chars < 60 { // too little spoken to learn anything from
		return speechRate
	}
	return math.Min(rateMax, math.Max(rateMin, float64(chars)/secs))
}

// speechDur is how long the line takes to say: the synthesized wav when there
// is one, and the text divided by the rate until then.
func (n *narrator) speechDur(e narrEntry) float64 {
	if strings.TrimSpace(e.Text) == "" {
		return 0
	}
	if d := n.measured(e); d > 0 {
		return d
	}
	return float64(len([]rune(strings.TrimSpace(e.Text)))) / n.spokenRate()
}

// updateInputs is the row this page now opens with, the same question Inputs,
// Describe and Cut answer at the top of theirs: what is the narration run
// actually sent? Every one of these comes from somewhere else -- the clips from
// Cut, the words from Describe's transcript, the context box from Describe.
// A narration written against a cut you have since changed reads exactly
// like one written against the current cut, and this line is the difference.
func (n *narrator) updateInputs() {
	if n == nil || n.inputs == nil {
		return
	}
	n.syncSlider() // called exactly when the cut may have changed shape
	a := n.a
	segs := a.produceSegs()
	var total float64
	for _, s := range segs {
		total += s.length()
	}
	line := fmt.Sprintf("%d clip(s) · %s to narrate", len(segs), mmss(total))
	detail := fmt.Sprintf("cut/cut.json — %d clips, %s of video to write for", len(segs), mmss(total))
	if len(segs) == 0 {
		line, detail = "no cut yet — build one on the Cut step", ""
	}
	// what refit could not fix: clips the narration has no line for, or lines
	// whose clip is gone. Said here rather than only when ▶ is pressed -- the
	// page's whole job is that a narration written for another cut reads exactly
	// like one written for this one.
	if why := n.staleFor(segs); why != "" && len(n.entries) > 0 {
		line += " · ⚠ " + why
		detail += "\n\n⚠ " + why + " — ▶ writes the narration again"
	}
	if rows := loadTSVRows(filepath.Join(a.transcriptDir(), "session.tsv")); len(rows) > 0 {
		line += fmt.Sprintf(" · timeline %d lines", len(rows))
		detail += fmt.Sprintf("\n\nprepare/transcript/session.tsv — %d lines; the ones falling inside a clip (±4 s) go with that clip", len(rows))
	} else {
		line += " · no session timeline — run Describe"
	}
	if c := a.sessionCtx(); c != "" {
		line += " · session context"
		detail += "\n\nSession context (Describe), sent with the narration:\n" + c
	}
	// the voice is an input too -- not to the writing, to the speaking -- and it
	// is the one nothing else on the page states in words
	if vp := a.voicePick; vp != nil {
		if v, ok := vp.current(); ok {
			st := 0.0
			if vp.pitch != nil {
				st = vp.pitch.Value()
			}
			line += " · voice: " + v.name
			detail += fmt.Sprintf("\n\nSpoken by %s at %+.1f semitones (narrate/voice_ref.wav)", v.name, st)
		}
	}
	n.inputs.SetText(line)
	n.inputs.SetTooltipText(detail)
}

// updateOut is the line every other step ends on: what is on disk, as opposed
// to what is in the widgets. On this page they diverge more than anywhere else
// -- a line edited and not yet spoken is in neither the json nor the cache.
func (n *narrator) updateOut() {
	if n == nil || n.out == nil {
		return
	}
	n.out.SetText(summarizeOutputs(n.a.narrateDir()))
}

// followPlayback rides the preview clock: it skips what the edit removed, and
// keeps the narration audio alongside the picture.
func (n *narrator) followPlayback() bool {
	// the ⏸ moves with the playhead, so it is redrawn on the tick like the blue
	// row -- but only when it has somewhere else to be, or every row's icon
	// would be rewritten ten times a second
	if n.livePlayRow() != n.liveRow {
		n.syncSpeakIcons()
	}
	if n.player == nil || !n.player.playing {
		// The picture stopped, so the narration riding along with it stops too --
		// but ONLY the narration that was riding along with it. A line played
		// from its own ▶ where no recording covers the clip is on the same player
		// and belongs to nobody but the row that started it: it plays with the
		// picture deliberately still, and this tick was pausing it within 100 ms,
		// which read as a line that speaks a word and gives up.
		if n.voice != nil && n.voice.playing && n.solo < 0 {
			n.voice.Pause()
		}
		return true
	}
	pos, ok := n.player.Position()
	if !ok {
		return true
	}
	t := n.playVideoStart + pos
	n.setPlayhead(t)
	// Once the picture is rolling, the CUT is the master and playback simply
	// runs it in order. A row's ▶ is a seek and nothing more: it drops the
	// preview a few seconds ahead of that line so you can watch the line land,
	// and from the moment it has landed there is nothing left that is special
	// about it.
	//
	// This used to be an "audition mode" that owned the transport: at the end of
	// the auditioned CLIP it hopped to the clip of the next line, or stopped
	// dead if there was none. So a line played a second before its clip ended
	// threw the video somewhere else the moment it finished speaking, and what
	// you were watching was never the cut you are here to check. The audition
	// is over when its line has been spoken; the run carries on.
	if n.solo >= 0 && n.soloPic && n.solo < len(n.entries) {
		if e := n.entries[n.solo]; t >= e.S+e.At+n.speechDur(e) {
			n.solo, n.soloPic = -1, false
			n.syncSpeakIcons() // the ⏸ belongs to whichever line is sounding now
			n.a.updateRunControls()
		}
	}
	segs := n.clips()
	cur, next := gapAt(segs, t)
	// the run preview holds a clip-boundary jump the same way: the line is
	// still talking, and the render grew this clip so it could finish
	if cur < 0 && n.voiceBusy() && n.speaking < len(n.entries) &&
		t < n.entries[n.speaking].E+maxExtend {
		return true
	}
	// The preview plays the CUT, not the source: the stretch between two clips
	// is material the edit removed, so skip it instead of playing through it.
	// Re-entry is guarded twice over -- seekTo drops player.playing until the
	// new position prerolls, which stops the tick above from arriving
	// meanwhile, and jumped covers the case where the seek cannot happen at all
	// (no video covers that session time).
	if cur < 0 && len(segs) > 0 {
		switch {
		case next < 0: // past the last clip: the cut is over
			n.player.Pause()
			if n.voice != nil {
				n.voice.Pause()
			}
			n.playSeg, n.jumped, n.held = -1, -1, false
		case next != n.jumped:
			n.jumped = next
			n.seekTo(segs[next].S)
		}
		return true
	}
	n.jumped = -1
	// The cut can hand the picture to another camera with NO gap to jump: two
	// scenes that touch, the second taken on a different lane, which is most of
	// what stealing a scene for the other camera does. Only a gap re-cued, so
	// nothing here noticed, and the preview went on playing the first camera's
	// file straight through the second's clip.
	//
	// With the file came its SOUND -- at the wrong scene's levels, and past a
	// lane the second scene silences, because syncFxSound reads its base off
	// the cut (cutVideoAt) while the pipeline was still on the other recording.
	// So the one thing this page is for -- writing a line to fit the gaps in
	// the finished video's audio -- was judged against audio the finished video
	// does not have.
	//
	// cue is the whole fix: same path, it is a seek and nothing more; a new
	// one reloads and brings the mix, the rate and the hush with it.
	if ed := n.a.ed; ed != nil && needsReload(ed.segs, ed.vids, n.player.loaded, t) {
		n.seekTo(t)
		return true
	}
	n.syncPlayRate() // the line has crossed into or out of a speed effect

	ei := n.entryAt(t)
	if ei == n.playSeg {
		return true
	}
	// captions only: there is no voice to lay over the clip and nothing to
	// synthesize, so the preview simply plays the cut
	if ei < 0 || n.voice == nil || n.a.captionsOnly() {
		n.playSeg = ei
		return true
	}
	e := n.entries[ei]
	wav := n.a.ttsWav(e)
	switch {
	case exists(wav):
		n.playSeg = ei
		// the row whose words are being spoken shows the ⏸ for them wherever the
		// sound came from -- pressing ▶ on a line and watching it stay a ▶ while
		// the line plays is the button lying about what it just did
		n.speaking = ei
		n.voice.PlaySegment(wav, t-(e.S+e.At), -1, true)
		n.selectRow(ei)
		n.syncSpeakIcons()
	case n.synthFail[wav]:
		n.playSeg = ei // the server already refused this line; run the clip mute
		// ...but say so every time it comes around: a sticky silent failure
		// reads as "the TTS stopped working", not as a line that failed once
		n.a.setStatus(fmt.Sprintf("line %d failed to synthesize — see log; its ▶ retries", ei+1))
	default:
		n.holdForSynth(ei) // playSeg stays put: the voice starts when we resume
	}
	return true
}

// holdForSynth stops the preview on a line that has not been spoken yet, speaks
// it, and picks the video up where it stopped. Running the clip mute and
// catching the voice on some later pass would preview a cut nobody is going to
// watch; a few seconds of waiting is the honest version.
func (n *narrator) holdForSynth(i int) {
	n.player.Pause()
	if n.voice != nil {
		// Stop, not Pause: whatever wav is in there is a line that no longer
		// exists (the edit changed the cache key, which is why we are here).
		// Left merely paused, any later resume could replay a syllable of the
		// old voice before the tick catches up.
		n.voice.Stop()
	}
	n.playSeg = -1 // the resume must re-decide the voice, never assume it
	n.held = true
	if n.synthing {
		return // already on it; this tick only caught us still paused
	}
	n.pullRows() // speak what is in the box, not what was last saved
	if i >= len(n.entries) {
		return
	}
	e := n.entries[i]
	wav := n.a.ttsWav(e)
	n.a.setStatus(fmt.Sprintf("synthesizing line %d — the video waits for it"+
		" (the first line after a cold start also loads the model)", i+1))
	n.synthWait(i, e, wav)
}

// synthWait does the waiting part off the GUI thread: the tick that got us here
// only knows the line is missing, and the server call that fills it in would
// otherwise stutter the picture from inside the tick.
func (n *narrator) synthWait(i int, e narrEntry, wav string) {
	n.synthing = true
	n.syncSpeakIcons() // the wait is part of the audition; its row says so
	n.a.snapSources()
	go func() {
		err := n.a.synthesize(e)
		glib.IdleAdd(func() {
			n.synthing = false
			n.syncSpeakIcons()
			if err != nil {
				n.synthFail[wav] = true
				n.a.logf("synthesis failed: %v", err)
				n.a.setStatus(fmt.Sprintf("line %d failed -- see log; playing on without it", i+1))
			} else {
				n.a.setStatus(fmt.Sprintf("line %d ready", i+1))
			}
			// only if the pause was ours: a pause the user asked for stays.
			// The resume goes back to the LINE'S start, not to wherever the
			// picture froze: the hold can land mid-line (an edited line is
			// re-synthesized where its old audio used to be), and resuming
			// there would start the new wav at a stale offset -- past its end
			// if the new line is shorter, which is a video with no voice.
			if n.held && n.player != nil && !n.player.playing {
				n.held = false
				if err == nil {
					n.cue(math.Max(e.S, e.S+e.At), true)
				} else {
					n.player.Toggle() // no new line to land on; just carry on
				}
			}
		})
	}()
}

// sessionZero is the session's own second nought on the wall clock: the
// earliest moment any source names. It always answers -- a session where no
// file names a moment starts at 0:00 -- so callers subtract it without asking
// whether the sources could be placed at all.
func (a *App) sessionZero() float64 {
	vids, auds := a.snappedSources()
	_, zero := srcClock(append(append([]string{}, vids...), auds...))
	return zero
}

// ---- generation -------------------------------------------------------------

// staleFor is why the narration would have to be written again, or "" when it
// already answers this cut. The times are the test because writeNarration takes
// them from the cut verbatim -- an entry's start IS its clip's start -- so a
// mismatch means the cut moved underneath, which is the one thing that makes a
// narration wrong rather than merely different.
//
// Note what is NOT here: text you edited. A line you rewrote is the line you
// want, and a ▶ that threw it away and asked the model again would be a button
// nobody dares press twice.
func (n *narrator) staleFor(segs []cutSeg) string {
	if len(n.entries) == 0 {
		return "there is no narration yet"
	}
	// a clip may carry several entries, so the test is coverage, not count:
	// every entry sits exactly on a clip, and every clip either has one or is
	// on the silent list -- emptied by hand, which is an answer and not a hole
	ei := 0
	for i := range segs {
		on := 0
		for ei < len(n.entries) && onClip(segs[i], n.entries[ei].S, n.entries[ei].E) {
			on++
			ei++
		}
		if on == 0 && !silentFor(n.silent, segs[i]) {
			return fmt.Sprintf("clip %d has no narration — it is new, or the cut moved under it", i+1)
		}
	}
	if ei != len(n.entries) {
		return "the narration has lines for clips the cut no longer has"
	}
	return ""
}

// refitEntries moves the lines onto the cut as it is NOW, and does it without
// asking the model anything. Cut is a page you go back to, and a clip dragged
// wider there used to leave the narration on the old geometry: the row still
// printed the old end, the ⚠ still measured the words against the old slot, and
// the only thing that made the page agree with the cut was ▶ -- which rewrites
// every line you have edited by hand. The words are the expensive half and they
// are still good; it is the times under them that moved. So the times follow
// the cut and the words are left exactly as they are.
//
// A line keeps its place against the VIDEO rather than its offset into the
// clip: a clip that grew at the front would otherwise drag every line in it
// later by however far the front moved, which is the one thing the edit did not
// touch. Past the clip's edge it is clamped like any other placement.
//
// orphan counts the lines whose clip is gone altogether. Those are left alone
// on purpose -- deleting words somebody wrote is not a side effect a tab
// change gets to have -- and staleFor reports them, which is ▶'s job.
func refitEntries(segs []cutSeg, entries []narrEntry) (moved, orphan int) {
	for i := range entries {
		e := &entries[i]
		j := clipFor(segs, *e)
		if j < 0 {
			orphan++
			continue
		}
		if math.Abs(segs[j].S-e.S) <= 0.05 && math.Abs(segs[j].E-e.E) <= 0.05 {
			continue
		}
		at := e.S + e.At - segs[j].S
		e.S, e.E = segs[j].S, segs[j].E
		e.At = math.Min(math.Max(0, at), math.Max(0, e.E-e.S-1))
		moved++
	}
	return moved, orphan
}

// clipFor is the clip a line belongs to once the cut has moved: the one it
// shares the most video with. Overlap rather than "the clip holding its start",
// because a clip trimmed at the front leaves that start outside the very clip
// the rest of the line plainly sits in.
func clipFor(segs []cutSeg, e narrEntry) int {
	if e.E <= e.S {
		// a line written on a card has no span to share: its clip is the
		// zero-span seg still sitting at the same moment, if any
		for i, s := range segs {
			if s.E <= s.S && math.Abs(s.S-e.S) <= 0.05 {
				return i
			}
		}
		return -1
	}
	best, most := -1, 0.0
	for i, s := range segs {
		if o := math.Min(e.E, s.E) - math.Max(e.S, s.S); o > most {
			best, most = i, o
		}
	}
	return best
}

// refit is refitEntries on the page: called when Narrate is opened, which is
// the only moment the cut can have changed under it. The boxes are pulled in
// first because the rebuild reads the entries and would drop an edit still
// sitting in one, and the rebuild itself only happens when something moved.
func (n *narrator) refit() {
	segs := n.a.produceSegs()
	if len(segs) == 0 || len(n.entries) == 0 {
		return
	}
	n.pullRows()
	moved, orphan := refitEntries(segs, n.entries)
	if moved == 0 {
		return
	}
	n.sortEntries()
	n.save()
	n.rebuildRows()
	if orphan > 0 {
		n.a.setStatus(fmt.Sprintf("the cut moved — %d line(s) followed their clips, %d sit on video the cut no longer has", moved, orphan))
		return
	}
	n.a.setStatus(fmt.Sprintf("the cut moved — %d line(s) followed their clips", moved))
}

// unspoken counts the lines with no wav in the cache for the CURRENT voice --
// which is what makes switching voice, nudging the pitch or editing a line all
// show up as work for ▶ to do, without any of them being tracked as such.
func (n *narrator) unspoken() int {
	if n.a.captionsOnly() {
		return 0 // no voice: a written line is a finished line
	}
	miss := 0
	for _, e := range n.entries {
		if strings.TrimSpace(e.Text) != "" && !exists(n.a.ttsWav(e)) {
			miss++
		}
	}
	return miss
}

// narrateRun is ▶ on this page: everything between the cut and every line
// spoken, in one press.
//
// It was two buttons, and the split was the tool's bookkeeping rather than the
// user's: "Generate narration" beside the video, "Synthesize all" on the run
// bar, and no way to tell from either which one you owed. Worse, they were
// ordered -- generate, then speak -- so the second was pressed after the first
// every single time, and pressing it alone on a changed cut spoke lines written
// for clips that no longer existed.
//
// Now the page says what it needs and ▶ does it: write the narration, then
// speak whatever is not already in the cache. Both stages are one run, so ⏸ and
// ⏹ cover the pair and the progress bar runs from one end of the step to the
// other.
//
// ▶ WRITES EVERY TIME. It used to write only when staleFor said the narration
// could not be right, so that a hand-edited line was never thrown away by a
// button press -- and the cost of that was that a narration you wanted redone
// could not be redone at all: every clip already had a line, or a deliberate
// blank, so ▶ said "the narration matches the cut" and did nothing. There was
// no other way to ask for a rewrite.
//
// So the restraint is gone and the safety net is a file: keepPrevNarration
// copies narration.json aside before the model's answer replaces it, and the
// log says where. staleFor is still read -- it names WHY in the log and drives
// the ⚠ on the inputs row -- it just no longer decides.
func (a *App) narrateRun() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	n := a.narr
	if n == nil {
		return
	}
	segs := a.produceSegs()
	if len(segs) == 0 {
		a.setStatus("no cut yet — build one on the Cut step first")
		return
	}
	if a.narrOff {
		a.setStatus("this video has no narration — tick Narration at the top of this page to write one")
		return
	}
	n.pullRows() // an edit still sitting in a text box is part of this run
	// why is the reason for the log, not the decision: ▶ writes either way
	why := n.staleFor(segs)
	if why == "" {
		why = "rewriting every line"
	}
	if !exists(filepath.Join(a.transcriptDir(), "session.tsv")) {
		a.setStatus("run Transcript first — no session timeline")
		return
	}
	// the lines to speak are the ones the model is about to write, below: ▶
	// always writes first, so there is nothing here worth copying out
	var speak []narrEntry
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	// this run's bar is this run's: the two tracks are summed, so a fraction
	// left behind by the previous step would be added to every reading here
	a.qReset()
	// True only while the model is writing. The pulse used to stop when the run
	// did, which is the wrong end of it: the writing is one call with nothing to
	// measure, but the speaking that follows it is n lines and counts them, and
	// a bar still pulsing over that reading turned a real "speaking 4/9" into a
	// block sliding back and forth. It is read and written on the GUI thread
	// only -- the timeout below and the idle callback that clears it.
	writing := true
	// what the bar calls the two halves. Writing is one call and cannot be
	// queued -- the model decides how many lines there are -- so it is a job
	// with no tasks under it; the speaking below queues one task per line.
	a.qJob(trackSTT, "narration", 1, 2)
	a.logf(">>> narrate: %s — writing %d clip(s), one LLM call, then speaking them",
		why, len(segs))
	a.prog(trackSTT, 0, "thinking about it")
	glib.TimeoutAdd(150, func() bool {
		if !a.running || !writing {
			return false
		}
		// the reply is streamed, so the moment the first clip closes there is
		// something real to show and the pulse has to get out of its way --
		// Pulse and SetFraction are the same needle
		a.progMu.Lock()
		counted := a.progParts[trackSTT] > 0
		a.progMu.Unlock()
		if counted {
			return false
		}
		a.progress.Pulse()
		return true
	})

	// the writing is always the run's first half now, so the speaking is
	// always the second
	speakBase, speakSpan := narrWriteShare, 1-narrWriteShare

	go func() {
		{
			a.logCtx("narration")
			entries, err := a.writeNarration(segs)
			if err != nil {
				a.narrateDone(err, "narration")
				return
			}
			speak = entries
			// the written lines have to be on the page before they are spoken:
			// the run bar's ⏹ mid-synthesis must not leave the text nowhere
			// whatever the streamed count reached, the writing is now finished:
			// the bar starts the speaking half from a full half, not from
			// wherever a clip that closed oddly left it
			a.qDone(trackSTT, narrWriteShare)
			done := make(chan struct{})
			glib.IdleAdd(func() {
				writing = false // hands the bar to the count below
				// the old lines are on their way out; keep them where a hand
				// that wanted one back can reach it (see narrateRun's header)
				if prev, err := keepPrevNarration(a.narrPath()); err == nil && prev != "" {
					a.logf("    the narration it replaced is kept at %s", prev)
				}
				n.entries, n.silent = entries, nil
				n.save()
				n.rebuildRows()
				a.logf(">>> narration written for %d clips", len(entries))
				a.setStatus("narration written — speaking it")
				close(done)
			})
			<-done
		}
		if a.captionsOnly() {
			// no voice: a written line is a finished line. The speaking job
			// still opens and closes so the bar reaches the end it promised.
			a.qJob(trackSTT, "speaking", 2, 2)
			a.logfIdle("    narrate: captions only — %d line(s) written, none spoken", len(speak))
			a.narrateDone(nil, "narration")
			return
		}
		var failed error
		// the speaking half, which CAN be queued: one task per line, cached or
		// not. It is always the run's second job, because the writing above is
		// always the first.
		a.qJob(trackSTT, "speaking", 2, 2)
		a.qPush(trackSTT, len(speak), "line")
		spoke, cached := 0, 0
		for i, e := range speak {
			// ▶ started this and is the ⏸ for it, so the pause has to be real:
			// between lines, like every other run's (checkpoint in pipeline.go)
			if err := a.checkpoint(); err != nil {
				failed = err
				break
			}
			// before the cache check, not after: a line already spoken is a line
			// this run is done with, and skipping the reading for it left the bar
			// sitting at whatever the last synthesized line put there while the
			// loop ran through the rest of a cached narration.
			a.qTake(trackSTT)
			a.prog(trackSTT, speakBase+speakSpan*float64(i)/float64(len(speak)), "")
			if strings.TrimSpace(e.Text) == "" || exists(a.ttsWav(e)) {
				cached++
				continue
			}
			if err := a.synthesize(e); err != nil {
				failed = err
				break
			}
			spoke++
		}
		if failed == nil {
			a.logfIdle("    narrate: %d line(s) spoken, %d already in the cache", spoke, cached)
		}
		a.narrateDone(failed, "synthesis")
	}()
}

// narrateDone ends the run from whichever stage stopped it, on the GUI thread.
func (a *App) narrateDone(err error, stage string) {
	glib.IdleAdd(func() {
		a.running = false
		a.updateRunControls()
		if n := a.narr; n != nil {
			n.updateInputs()
			n.updateOut()
		}
		if err != nil {
			if !errors.Is(err, errStopped) {
				a.logf("%s FAILED: %v", stage, err)
				a.progress.SetText(stage + " failed — see log")
				return
			}
			a.progress.SetText(stage + " stopped")
			return
		}
		a.progress.SetFraction(1)
		if a.captionsOnly() {
			a.progress.SetText("narration ready — captions only, nothing spoken")
			return
		}
		a.progress.SetText("narration ready and spoken — ▶ the preview hears it in place")
	})
}

// writeNarration writes every clip's line in one call: they refer to each
// other, so the lines only fit together if they are written together.
// clipBriefs is everything the writer is told about the clips: for each one its
// length, its word budget, and the session timeline over it -- what was said
// and what was on screen, in order.
//
// Every line is stamped with where inside the clip it falls, because "when" is
// half of what makes a narration line wrong. A clip whose first forty seconds
// are a fall and a ghost, and whose last two are a pickaxe finally coming out,
// reads as a clip about digging if the order and the offsets are stripped off,
// and the line written for it then talks about digging over forty seconds of
// something else.
// narratorMic names the one recording in the session the finished video never
// plays: the microphone the narration's own voice is cloned from.
//
// Everything else in the timeline is heard. encodeClip takes each clip's sound
// from the footage it was cut from and ducks it under the narration, so the
// game and everyone talking through it are still there. What is not there is
// the narrator: he is the one person whose own words reach the viewer only when
// the TTS says them, which is why those lines -- and only those -- are the
// narration's to quote.
//
// Blank when the voice is cut from footage (a session that is one capture and
// nothing else): then the narrator is on the capture like everybody else.
func (a *App) narratorMic() string {
	slot := narratorSlot(a.voiceID())
	if slot == 0 {
		slot = 1 // a stock voice still narrates for whoever holds slot 1
	}
	p := a.narratorSource(slot)
	if p == "" {
		return ""
	}
	vids, _ := a.snappedSources()
	for _, v := range vids {
		if v == p {
			return ""
		}
	}
	return baseName(p)
}

// narrBudget is how many words a clip is allowed. It used to be 2.5 a second,
// which is a speaking rate: every clip came back filled from end to end, and a
// voice that never stops has no timing -- the pause is where the joke lands and
// where the game gets to be heard. The first cut went to a fifth of that, which
// over-shot the other way: six-word lines that tease and never follow through
// ("One glowing person wants three things." -- and what, do we find them?). At
// under a third there is room for the thought AND its punchline, the words
// still run out early in every clip, and because the cap binds past 40 s a
// two-minute clip does not get two minutes of talking either.
func narrBudget(dur float64) int {
	n := int(dur * 0.75)
	switch {
	case n < 8:
		return 8 // even a five-second clip can take a short one
	case n > 30:
		return 30
	}
	return n
}

// clipBriefs writes each clip's block. narr is narratorMic: lines off that
// recording are the narrator's own and get his name, every other line is one
// the video plays out loud. Blank means nobody is exempt, which is the
// assumption that cannot embarrass the narration.
func clipBriefs(segs []cutSeg, rows []tsvRow, fx []cutFx, narr string) string {
	return clipBriefsWith(segs, rows, fx, narr, func(i int, s cutSeg) string {
		return fmt.Sprintf("CLIP %d: %.1f–%.1f (%.0f s, at most %d words -- fewer is better, none is fine)",
			i+1, s.S, s.E, s.length(), narrBudget(s.length()))
	})
}

// clipBriefsWith is the same blocks under headings of the caller's own. The
// narration's carry the word budget; the upload text's carry where the clip
// falls in the finished video, which is the only clock its chapter list may
// use. A heading is read as an instruction as much as a label -- "at most 30
// words" over a clip in the upload brief is a limit a small model will obey on
// the description -- so each job writes its own.
func clipBriefsWith(segs []cutSeg, rows []tsvRow, fx []cutFx, narr string, head func(i int, s cutSeg) string) string {
	var b strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&b, "\n%s\n", head(i, s))
		// An insert has no footage under it and no transcript over it, so the
		// lines around it would be a description of whatever it covered -- which
		// is the one thing the viewer will not be looking at. Say what it is
		// instead: a card that says "a few moments later" wants no narration, and
		// a ranking graphic wants the ranking read out, and the file's name is
		// the only thing here that tells them apart.
		if s.isInsert() {
			// deliberately the whole thing, parameters and all: "tier.svg" says
			// nothing about what is on the card and "tier.svg?S=Dust II,Mirage"
			// says all of it, which is exactly what a narrator needs to read
			fmt.Fprintf(&b, "  (not footage: a graphic or clip inserted here, %q. "+
				"Narrate what is ON it, or say nothing -- there is nothing from the "+
				"session to describe, and inventing one would caption the wrong picture)\n",
				filepath.Base(s.Ins))
			continue
		}
		n := 0
		for _, r := range rows {
			if r.e <= s.S-4 || r.s >= s.E+4 {
				continue
			}
			// seconds from the clip's own start, so the narration can follow the
			// clip instead of arriving at it; a line just outside the clip goes
			// negative or past the end, which is exactly what it is
			fmt.Fprintf(&b, "  [%+.0fs] %s: %s\n", r.s-s.S, tlLabel(r, narr), r.text)
			n++
		}
		if n == 0 {
			b.WriteString("  (nothing recorded over this clip -- say something general, and invent nothing)\n")
		}
		// and the moments somebody marked. A label effect changes nothing in
		// the video -- it exists only to be read here: it is the editor's own
		// word for a moment ("the reveal", "boss fight"), the one thing in
		// this brief that no transcript row can contain, at the second they
		// say it happens. In the same column as the lines, so a note in the
		// user context can say what to do when one arrives.
		for _, f := range fx {
			t0, t1 := f.fxSpan()
			if f.Kind != "label" || strings.TrimSpace(f.Text) == "" || t1 <= s.S || t0 >= s.E {
				continue
			}
			fmt.Fprintf(&b, "  [%+.0fs] MARKED: %s\n", t0-s.S, strings.TrimSpace(f.Text))
		}
	}
	return b.String()
}

// speechHeard is whether the finished video plays anything anybody said out
// loud. It is the one question the narration's premise turns on: the prompt is
// written for a video whose only voice is the narration, and that is true
// exactly when no spoken line survives into it.
//
// A line survives when the scene covering it keeps the lane it was recorded on
// -- the same reading the render makes clip by clip (clipMixes, and laneQuiet
// for the footage's own track). The narrator's own microphone never survives:
// the render does not play it at all, which is what makes those lines the
// narration's material rather than its competition.
//
// A session with the voices split off and the voice lane silenced therefore
// answers false, which is the everyday case here and the one the prompt is
// written for; a straight capture with its own sound kept answers true.
func (a *App) speechHeard(segs []cutSeg, rows []tsvRow) bool {
	narr := a.narratorMic()
	for _, r := range rows {
		if r.spk == "EVENT" || (narr != "" && r.src == narr) {
			continue
		}
		for i := range segs {
			s := &segs[i]
			if s.isInsert() || r.e <= s.S || r.s >= s.E {
				continue
			}
			if s.hears(r.src) {
				return true
			}
		}
	}
	return false
}

// narrWriteShare is how much of the run bar the writing owns when there is
// writing to do. The speaking takes the rest, so the one bar goes forward from
// the first clip written to the last line spoken.
const narrWriteShare = 0.5

// narrEntriesDone counts the clips finished in a reply that is still arriving:
// the objects inside "entries" whose braces have closed. See jsonItemsDone,
// which is this with the key as an argument -- the Cut page counts the same way
// over "segments" and "checks".
func narrEntriesDone(s string) int {
	n, _ := jsonItemsDone(s, "entries")
	return n
}

func (a *App) writeNarration(segs []cutSeg) ([]narrEntry, error) {
	rows := a.sessionRows()
	// the box on the page is the whole system message: what used to be a
	// separate context field is part of it now (see buildNarrate)
	system := a.sysPrompt("narrate")
	if a.speechHeard(segs, rows) {
		system += "\n\n" + narrNoMicNote
	}
	if a.captionsOnly() {
		system += "\n\n" + narrCaptionsAddendum
	}
	user := a.ctxBlockFor("narrate") + "THE CLIPS AND WHAT IS KNOWN ABOUT EACH:" +
		clipBriefs(segs, rows, a.produceCut().Fx, a.narratorMic())
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	// the web, for a line about a thing the clip's block only names
	tools, ffx := a.webToolsFor("narrate")
	// the bar, while the one long call runs: clips counted as they close. Only
	// ever forward -- a retry starts the count again, and a bar that fell back
	// to 1/9 would read as work being undone rather than redone.
	best := 0
	onText := func(s string) {
		if d := narrEntriesDone(s); d > best {
			best = d
			a.prog(trackSTT, narrWriteShare*math.Min(1, float64(best)/float64(len(segs))),
				"writing %d/%d clips", best, len(segs))
		}
	}
	// thinking, until an attempt comes back with nothing: see thinkAgain
	think := true
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		reply, err := a.llmChatRetryTools("narrate", msgs, think, tools, a.webRunner("narrate", ffx), onText)
		if err != nil {
			return nil, err
		}
		clean := strings.TrimSpace(reply)
		if i := strings.Index(clean, "{"); i >= 0 {
			clean = clean[i:]
		}
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
		var out struct {
			Entries []rawEntry `json:"entries"`
		}
		problem := noAnswer(reply)
		if problem != "" {
			// nothing to parse: say so rather than reporting the parser's
			// bafflement at an empty string (llm.go)
		} else if err := json.Unmarshal([]byte(clean), &out); err != nil {
			// a reply the token ceiling chopped in half wants a shorter
			// answer, not a more careful one (cutOff, llm.go)
			if problem = cutOff(reply, err); problem == "" {
				problem = "not valid JSON: " + err.Error()
			}
		} else {
			entries, p := bindEntries(segs, out.Entries)
			if p == "" {
				return entries, nil
			}
			problem = p
		}
		a.logfIdle(">>> narration attempt %d rejected: %s", try+1, problem)
		if next := thinkAgain(think, reply); next != think {
			think = next
			a.logfIdle(">>> narrate: the model spent the whole call thinking and wrote " +
				"nothing — asking again with thinking off")
		}
		msgs = retryTurn(msgs, reply, problem)
	}
	return nil, fmt.Errorf("no valid narration after 3 attempts")
}

// rawEntry is one entry as the model wrote it, before it is trusted.
type rawEntry struct {
	Start, End, At     float64
	Text, Emotion, Pos string
}

// bindEntries fits a reply onto the cut. A clip may get several entries -- its
// lines, in order, each with its own placement -- so the count no longer
// identifies anything: an entry names its clip by echoing the start and end
// the brief printed, and those numbers are used ONLY to identify it. What is
// stored is the cut's own times, verbatim, which is what keeps staleFor's
// "has the cut moved" test exact.
//
// An empty line is a clip the narration deliberately leaves alone, and the
// prompt asks for those: the render then plays that clip on its own audio.
// What is not an answer is every clip empty, which is a model that has refused
// the job rather than one exercising taste. A clip with no entry at all is
// rejected the same way -- silence has to be said.
func bindEntries(segs []cutSeg, raw []rawEntry) ([]narrEntry, string) {
	var entries []narrEntry
	said, ci, last := 0, 0, -1
	for _, r := range raw {
		// scan forward: replies come in clip order, and a clip's own entries
		// stay together. Scanning from the last match (not from zero) is what
		// rejects entries out of order instead of silently reordering them.
		j := -1
		for k := ci; k < len(segs); k++ {
			if math.Abs(segs[k].S-r.Start) < 0.5 && math.Abs(segs[k].E-r.End) < 0.5 {
				j = k
				break
			}
		}
		if j < 0 {
			return nil, fmt.Sprintf("an entry says %g-%g, which matches no clip (or is out of order)", r.Start, r.End)
		}
		if j > last+1 {
			// the scan skipped over a clip to find this one: that clip got nothing
			return nil, fmt.Sprintf("clip %d (%.1f-%.1f) got no entry", last+2, segs[last+1].S, segs[last+1].E)
		}
		ci = j
		if strings.TrimSpace(r.Text) != "" {
			said++
		}
		last = j
		// the placement is clamped here so everything downstream can trust
		// it: never negative, never in the clip's final second
		at := math.Min(math.Max(0, r.At), math.Max(0, segs[j].length()-1))
		// the placement keeps only the three words Produce can burn; anything
		// else the model invents is the bottom, same as saying nothing
		pos, _ := posTag(r.Pos)
		entries = append(entries, narrEntry{
			S: segs[j].S, E: segs[j].E, At: at, Text: strings.TrimSpace(r.Text),
			Emotion: r.Emotion, Pos: pos})
	}
	if last != len(segs)-1 {
		return nil, fmt.Sprintf("clip %d (%.1f-%.1f) got no entry", last+2, segs[last+1].S, segs[last+1].E)
	}
	if said == 0 {
		return nil, "every clip came back with no line at all"
	}
	// two lines in one clip play in time order whatever order they arrived in
	sort.SliceStable(entries, func(a, b int) bool {
		if entries[a].S != entries[b].S {
			return entries[a].S < entries[b].S
		}
		return entries[a].At < entries[b].At
	})
	return entries, ""
}
