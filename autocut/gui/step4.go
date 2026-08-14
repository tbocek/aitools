package main

// Step 4: Narrate / Voice. Who speaks, on top (step4_voice.go), and what they
// say, below: one entry per cut segment in a vertical list -- time range,
// editable text, editable emotion -- next to a preview player. Clicking an
// entry jumps the video there (play state preserved); ✎ leaves a standing note
// on a clip ("mention the countdown", "shorter"), honored the next time the
// narration is written.
//
// The preview plays the CUT: it skips what step 3 removed instead of running on
// into it, and when it reaches a line that has not been spoken yet it holds the
// picture, speaks the line, and carries on. Waiting is the point -- a preview
// that runs a clip mute is a preview of a video nobody is going to make.
//
// Emotions are taken from how the moment was actually spoken -- the generator
// sees the original lines and the events -- and then heightened; the user has
// the last word per entry.
//
// The narration is mixed OVER the clip's own audio rather than replacing it
// (step 5 ducks the original to a fifth and leaves it there), which is the fact
// the prompt is written around: whatever was said in the clip is still audible,
// so narration that quotes it, or says it again in other words, is heard twice.
// The prompt used to ask for exactly that -- "reuse quotable lines VERBATIM" --
// and the result was a narrator reading the transcript back over the people
// saying it.
//
// Voice on the fly: audio.cpp's own audiocpp_server keeps IndexTTS2 loaded, so
// per-line synthesis skips the model reload. This is the same HTTP API the
// audio.cpp WebUI proxies to, called the same way (voice_ref by path, since
// both sides share a filesystem). Autocut uses whatever server is listening
// (AUDIOCPP_SERVER or AUTOCUT_TTS_URL, default 127.0.0.1:8765) and never starts
// one itself.
// Synthesized lines are cached by hash of (voice, text, emotion). The pitch
// knob sits with the voice at the top of the page: it moves the reference wav
// before it is cloned, so it changes who is speaking rather than transposing
// what was spoken.
//
// step4/narration.json      entries
// step4/voice_ref_base.wav  the reference as chosen or cut
// step4/voice_ref.wav       ...shifted by the reference pitch: the server's input
// step4/tts/<hash>.wav      synthesis cache

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	ttsPort  = 8765
	emoAlpha = "0.6"
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
const narrSystem = `You are the narrator of a YouTube gaming video. Clips have been cut out of a longer session and you write the voice-over for each one. You were there; this happened to you.

Your voice: first person, present tense, contractions, short sentences. Funny, self-aware, happy to be the idiot in the story. You are talking to one person watching, not to an audience.

Each clip's block lists what happened over it, in order. The offset is seconds from that clip's start:
  [+12s] ON SCREEN: what the picture showed
  [+12s] HEARD SPEAKER_00: a line the video itself plays out loud
  [+12s] SAID SPEAKER_00 (not in the video's audio): a line only you can deliver

Rules:
1. One entry per clip, with that clip's exact start and end.
2. Never repeat a HEARD line: the viewer hears it from the person who said it. Set it up before it, or react after it.
3. Do quote a "(not in the video's audio)" line, in your own voice, as you saying it. Those are your best moments and nobody hears them unless you say them.
4. Do not describe the picture. Say what you were trying to do, what it cost you, what you thought of it.
5. Use only what this clip's block says happened. Invent no names, places or outcomes.
6. Stay under the clip's word budget. Well under is good. Silence is part of this.
7. Start in the middle. Never open with "So", "Alright", "Okay", "In this clip".
8. Where a block gives you nothing, say something short and general, and invent nothing.
9. If the request opens with ABOUT THIS SESSION, its names, spellings and facts beat anything else.

Example of a clip block and the line it should get:
  [+2s] ON SCREEN: A gorilla clings to a branch above a group around a wooden chest.
  [+9s] SAID SPEAKER_00 (not in the video's audio): Open up, FBI.
  [+14s] HEARD SPEAKER_01: I left my black light in the basement.
  -> "Nobody can get this chest open, so obviously I try knocking. Open up, FBI. Turns out chests don't respect the badge."

emotion is how the TTS should read the line: "excited", "deadpan", "panicking, laughing", "quiet, storytelling".

Return strict JSON, nothing else:
{"entries":[{"start":<sec>,"end":<sec>,"text":"...","emotion":"..."}]}`

type narrEntry struct {
	S       float64 `json:"s"`
	E       float64 `json:"e"`
	Text    string  `json:"text"`
	Emotion string  `json:"emotion"`
	// A standing note for this clip -- "mention the countdown", "shorter". It
	// is not applied on its own: ✎ used to rewrite this one entry against the
	// model, which drifted the line away from its neighbours (they are written
	// together, referring to each other, and a solo rewrite sees none of that).
	// The note is kept and goes into the clip's block the next time the whole
	// narration is written.
	Instr string `json:"instr,omitempty"`
}

type narrator struct {
	a       *App
	entries []narrEntry

	player         *Player // video preview
	voice          *Player // narration audio rides along on this one
	playSeg        int     // entry currently voiced, -1 none
	jumped         int     // clip we last skipped a gap to, -1 none
	playVideoStart float64 // session start of the video loaded in the preview
	synthing       bool    // a playback-triggered synthesis is in flight
	held           bool    // the preview is paused by us, not by the user
	// the user has started the preview and not stopped it, which is what makes
	// the run bar the preview's transport. A clip merely CUED -- by clicking a
	// line to see where it is -- must not take ▶ away from the step's own run.
	started bool
	// wavs the server refused: stalling on them again would pause at every clip
	synthFail map[string]bool
	list      *gtk.ListBox
	rows      []*narrRow
	building  bool // guards feedback loops while (re)building rows

	// speaking is the row whose line is on n.voice, -1 none: it is what draws
	// that row's ⏸, and it is set whoever started the sound. solo answers the
	// other question -- which row started it from its OWN ▶ -- and while it is
	// set the audition owns both players: the tick must not pause the voice
	// under it, and must stop the picture at the end of that one clip.
	speaking int
	solo     int
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

	// a ✎ note has been written since the narration was, so the next ▶ owes the
	// user a rewrite. In the file rather than in memory: a note left at the end
	// of an evening is exactly the one that has to survive closing the app.
	rewrite bool
}

type narrRow struct {
	emotion *gtk.Entry
	text    *gtk.TextView
	speak   *gtk.Button // ▶/⏸ for this one line
}

func (a *App) narrPath() string { return filepath.Join(a.narrateDir(), "narration.json") }

// ---- persistence ------------------------------------------------------------

func (n *narrator) load() {
	n.entries, n.rewrite = nil, false
	if b, err := os.ReadFile(n.a.narrPath()); err == nil {
		var f struct {
			Entries []narrEntry
			Rewrite bool
		}
		if json.Unmarshal(b, &f) == nil {
			n.entries, n.rewrite = f.Entries, f.Rewrite
		}
	}
}

func (n *narrator) save() {
	b, _ := json.MarshalIndent(struct {
		Entries []narrEntry `json:"entries"`
		Rewrite bool        `json:"rewrite,omitempty"`
	}{n.entries, n.rewrite}, "", "  ")
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
		n.entries[i].Text = strings.TrimSpace(buf.Text(buf.StartIter(), buf.EndIter(), false))
		n.entries[i].Emotion = r.emotion.Text()
	}
}

// ---- page ------------------------------------------------------------------

func (a *App) buildStep4() gtk.Widgetter {
	n := &narrator{a: a, playSeg: -1, jumped: -1, speaking: -1, solo: -1, synthFail: map[string]bool{}}
	a.narr5 = n
	if p, err := NewPlayer(); err == nil {
		n.player = p
		// the picture is what "playing" means here; the narration track follows
		// it, so only this one draws the run bar's button
		p.OnState = a.updateRunControls
		p.OnError = a.playerErr("the narrate preview")
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
			n.seekTo(n.entries[i].S)
		}
	})
	left := gtk.NewScrolledWindow()
	left.SetChild(n.list)
	left.SetVExpand(true)
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
		vframe.SetChild(n.player.Picture)
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

	// No buttons at all now. There were three ways to start this step -- the
	// picture's own ▶, "Generate narration" here, "Synthesize all" on the run
	// bar -- for what is one job in two halves, and which half a press bought
	// you was something you had to know. The run bar's ▶ does both (narrateRun),
	// and the picture is still its own play/pause.
	preview := gtk.NewBox(gtk.OrientationVertical, 8)
	preview.Append(vframe)

	// One box, not two. The context box that used to sit above this was
	// concatenated onto the prompt just before the request went out -- so what
	// the model was told existed nowhere on screen, and which half a line
	// belonged in was a coin toss. The box IS the prompt; a project written
	// before this has its context folded in on load (migrateHints).
	words := gtk.NewBox(gtk.OrientationVertical, 8)
	words.Append(a.promptEditor("narrate", "Narration prompt",
		"The rules, plus what this session was and what matters in it"))
	// one scrollbar for the writing half; the prompt gets its full height from
	// this viewport, so the whole thing is there to read and edit. Its own
	// gutter rather than an overlay, for the reason step 3 gives: an overlay
	// slider is drawn over the framed box's border, which is where the column
	// stops looking like the same box as the one on the page beside it.
	wordScroll := gtk.NewScrolledWindow()
	wordScroll.SetChild(words)
	wordScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	wordScroll.SetOverlayScrolling(false)

	// the preview and the prompt both want the column, and which one matters
	// depends on whether you are watching or rewriting -- so it is a divider
	right := gtk.NewPaned(gtk.OrientationVertical)
	right.SetMarginStart(12)
	right.SetMarginEnd(12)
	right.SetMarginBottom(8)
	right.SetStartChild(preview)
	right.SetEndChild(wordScroll)
	right.SetPosition(420)

	// The voice on top of the words, in the words' own column, so the two share
	// a left and a right edge: they are one question -- what is said and who
	// says it -- and a picker spanning the whole page said instead that it also
	// governed the video beside them. It does not, and the third of the height
	// it took across the top was a third the video and the prompt did not get.
	// They were two sidebar steps with the voice first, which put the one choice
	// you cannot judge yet -- how a clone sounds reading THIS narration -- before
	// the narration existed; the divider is there for when the picker needs more
	// than its opening third.
	spoken := gtk.NewPaned(gtk.OrientationVertical)
	spoken.SetStartChild(a.buildVoicePicker())
	spoken.SetEndChild(left)
	spoken.SetResizeStartChild(false) // extra height goes to the narration
	spoken.SetShrinkStartChild(false)
	spoken.SetPosition(300)

	// ...and the video with its prompt runs the full height beside them
	split := gtk.NewPaned(gtk.OrientationHorizontal)
	split.SetStartChild(spoken)
	split.SetEndChild(right)
	split.SetPosition(790)
	split.SetVExpand(true)

	// What this step reads, at the top, and what it has written, at the bottom
	// -- the two rows every other step has and this one did not.
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
	openOut.SetTooltipText("step4/ — narration.json, the voice reference and the synthesis cache")
	openOut.ConnectClicked(func() { a.openFolder(a.narrateDir()) })
	n.out = gtk.NewLabel("")
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.SetHAlign(gtk.AlignEnd)
	outRow.SetMarginEnd(12)
	outRow.SetMarginBottom(6)
	outRow.Append(outLbl)
	outRow.Append(openOut)
	outRow.Append(n.out)

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(split)
	page.Append(outRow)

	n.load()
	n.rebuildRows()
	n.updateInputs()
	n.updateOut()
	return page
}

func (n *narrator) rebuildRows() {
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
	mmss := func(t float64) string { return fmt.Sprintf("%02d:%02d", int(t)/60, int(t)%60) }
	// three lines, measured in the list's own font instead of guessed in pixels
	// -- font size and text scaling differ per machine, and three lines is the
	// whole point of the pane width
	lineH := 20
	if lay := n.list.CreatePangoLayout("Ay"); lay != nil {
		if _, h := lay.PixelSize(); h > 0 {
			lineH = h
		}
	}
	threeLines := 3*lineH + 4 // + the text view's top margin and its frame
	for i := range n.entries {
		e := &n.entries[i]
		i := i

		head := gtk.NewBox(gtk.OrientationHorizontal, 6)
		tl := gtk.NewLabel(fmt.Sprintf("%s–%s", mmss(e.S), mmss(e.E)))
		tl.AddCSSClass("dim-label")
		emotion := gtk.NewEntry()
		emotion.SetText(e.Emotion)
		emotion.SetHExpand(true)
		emotion.SetPlaceholderText("emotion for the TTS delivery")
		// ▶ per line rather than a 🔊: it is a play button and, while its line is
		// sounding, the ⏸ for it (syncSpeakIcons draws that)
		speak := gtk.NewButtonFromIconName("media-playback-start-symbolic")
		speak.SetTooltipText("play this clip with its line spoken over it")
		speak.ConnectClicked(func() { n.a.speakEntry(i) })
		instruct := gtk.NewButtonWithLabel("✎")
		instruct.SetTooltipText("a note for this clip — kept, and honored the next time " +
			"the narration is written — the next ▶ does that")
		instruct.ConnectClicked(func() { n.a.instructDialog(i) })
		head.Append(tl)
		head.Append(emotion)
		head.Append(speak)
		head.Append(instruct)

		text := gtk.NewTextView()
		text.SetWrapMode(gtk.WrapWord)
		text.SetMonospace(true) // every editable box in the app is this font
		text.SetTopMargin(2)
		text.SetLeftMargin(6)
		text.Buffer().SetText(e.Text)
		tScroll := gtk.NewScrolledWindow()
		tScroll.SetChild(text)
		tScroll.SetPropagateNaturalHeight(true)
		tScroll.SetMinContentHeight(threeLines)     // a short line still gets the box
		tScroll.SetMaxContentHeight(threeLines * 2) // a long one grows, then scrolls
		tScroll.AddCSSClass("frame")

		change := func() {
			if !n.building {
				n.pullRows()
				n.save()
			}
		}
		emotion.ConnectChanged(change)
		text.Buffer().ConnectChanged(change)

		box := gtk.NewBox(gtk.OrientationVertical, 4)
		box.SetMarginTop(6)
		box.SetMarginBottom(6)
		box.SetMarginStart(8)
		box.SetMarginEnd(8)
		box.Append(head)
		box.Append(tScroll)
		// a note that is about to be sent must be readable without opening the
		// dialog that set it -- the same reason the hint boxes went away
		if e.Instr != "" {
			note := gtk.NewLabel("✎ " + e.Instr)
			note.SetXAlign(0)
			note.SetWrap(true)
			note.AddCSSClass("dim-label")
			note.SetTooltipText("goes into this clip's block the next time the narration is written")
			box.Append(note)
		}
		n.list.Append(box)
		n.rows = append(n.rows, &narrRow{emotion: emotion, text: text, speak: speak})
	}
	n.building = false
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
	v := ed.videoAt(t)
	if v == nil {
		return
	}
	n.playSeg = -1 // re-trigger the voice on the next tick
	// Already in this file: seek inside it. Reloading the uri tears the pipeline
	// down and builds it again for a jump of a few seconds, and playback only
	// picks up when the fresh preroll reports back on the bus -- a seek just
	// keeps playing, which is what skipping a gap should feel like.
	if n.player.loaded == v.path {
		n.player.SeekTo(t - v.start)
		// the seek cleared ended, so this starts at the new position rather
		// than replaying whatever the stream stopped on
		if play && !n.player.playing {
			n.player.Toggle()
		}
		return
	}
	n.player.PlaySegment(v.path, t-v.start, -1, play)
	n.playVideoStart = v.start
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
		// of a line would play it mute
		n.playSeg = -1
		return
	}
	if n.voice != nil {
		n.voice.Pause()
	}
}

// claimVoice ends an audition and hands both players back to the preview. While
// solo is set the tick leaves that audio alone and stops the picture at the end
// of the one clip (followPlayback), so it has to be cleared by whoever takes
// over -- otherwise the preview would stop at the auditioned clip's end.
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

// clips is what the preview follows: the cut from step 3, or the narration
// entries when step 3 has not been built this session. They are the same list --
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
func (n *narrator) entryAt(t float64) int {
	for i, e := range n.entries {
		if t >= e.S && t < e.E {
			return i
		}
	}
	return -1
}

// noteFor is the standing note written against a clip, for the pass that writes
// every line again. Matched by overlap and not by index, for the same reason
// entryAt is: the cut moves under the notes. A clip nudged a second either way
// is the same clip and keeps what was said about it; one that replaced it
// overlaps nothing and starts with no note. Most overlap wins, so a clip split
// in two takes the note into the half it mostly was.
func (n *narrator) noteFor(s cutSeg) string {
	best, most := "", 0.0
	for _, e := range n.entries {
		if e.Instr == "" {
			continue
		}
		if ov := math.Min(e.E, s.E) - math.Max(e.S, s.S); ov > most {
			best, most = e.Instr, ov
		}
	}
	return best
}

// updateInputs is the row this page now opens with, the same question Inputs,
// Describe and Cut answer at the top of theirs: what is the narration run
// actually sent? Every one of these comes from somewhere else -- the clips from
// Cut, the words from Describe's transcript, the context box from Describe --
// and one of them (the ✎ notes) is invisible on this page until you open a
// clip. A narration written against a cut you have since changed reads exactly
// like one written against the current cut, and this line is the difference.
func (n *narrator) updateInputs() {
	if n == nil || n.inputs == nil {
		return
	}
	a := n.a
	segs := a.produceSegs()
	var total float64
	for _, s := range segs {
		total += s.E - s.S
	}
	line := fmt.Sprintf("%d clip(s) · %s to narrate", len(segs), mmss(total))
	detail := fmt.Sprintf("step3/cut.json — %d clips, %s of video to write for", len(segs), mmss(total))
	if len(segs) == 0 {
		line, detail = "no cut yet — build one on the Cut step", ""
	}
	notes := 0
	for _, s := range segs {
		if n.noteFor(s) != "" {
			notes++
		}
	}
	if notes > 0 {
		line += fmt.Sprintf(" · %d ✎ note(s)", notes)
		detail += fmt.Sprintf("\n\n%d clip(s) carry a standing note (✎), sent with the clip they were written for", notes)
	}
	if rows := loadTSVRows(filepath.Join(a.transcriptDir(), "session.tsv")); len(rows) > 0 {
		line += fmt.Sprintf(" · timeline %d lines", len(rows))
		detail += fmt.Sprintf("\n\nstep2/transcript/session.tsv — %d lines; the ones falling inside a clip (±4 s) go with that clip", len(rows))
	} else {
		line += " · no session timeline — run Describe"
	}
	if c := a.sessionCtx(); c != "" {
		line += " · session context"
		detail += "\n\nSession context (Describe), sent with the narration:\n" + c
	}
	// the voice is an input too -- not to the writing, to the speaking -- and it
	// is the one nothing else on the page states in words
	if vp := a.voice5; vp != nil {
		if v, ok := vp.current(); ok {
			st := 0.0
			if vp.pitch != nil {
				st = vp.pitch.Value()
			}
			line += " · voice: " + v.name
			detail += fmt.Sprintf("\n\nSpoken by %s at %+.1f semitones (step4/voice_ref.wav)", v.name, st)
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
	// An audition is one clip and stops at the end of it. Running on into the
	// rest of the cut would make a line's ▶ the same button as the run bar's,
	// and there would be no way to hear one clip and only that one.
	if n.solo >= 0 && n.solo < len(n.entries) && t >= n.entries[n.solo].E {
		n.player.Pause()
		if n.voice != nil {
			n.voice.Pause()
		}
		n.solo, n.playSeg, n.held = -1, -1, false
		n.a.updateRunControls()
		return true
	}
	segs := n.clips()
	cur, next := gapAt(segs, t)
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

	ei := n.entryAt(t)
	if ei == n.playSeg {
		return true
	}
	if ei < 0 || n.voice == nil {
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
		n.voice.PlaySegment(wav, t-e.S, -1, true)
		n.syncSpeakIcons()
	case n.synthFail[wav]:
		n.playSeg = ei // the server already refused this line; run the clip mute
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
		n.voice.Pause()
	}
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
			// only if the pause was ours: a pause the user asked for stays
			if n.held && n.player != nil && !n.player.playing {
				n.held = false
				n.player.Toggle()
			}
		})
	}()
}

func (a *App) sessionZero() float64 {
	zero := math.MaxFloat64
	vids, auds := a.snappedSources()
	for _, p := range append(append([]string{}, vids...), auds...) {
		if s, err := sourceStart(p); err == nil {
			zero = math.Min(zero, s)
		}
	}
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
	switch {
	case len(n.entries) == 0:
		return "there is no narration yet"
	case n.rewrite:
		return "a ✎ note is waiting to be applied"
	case len(n.entries) != len(segs):
		return fmt.Sprintf("the cut has %d clip(s) and the narration %d", len(segs), len(n.entries))
	}
	for i := range segs {
		if math.Abs(segs[i].S-n.entries[i].S) > 0.05 || math.Abs(segs[i].E-n.entries[i].E) > 0.05 {
			return fmt.Sprintf("clip %d has moved since the narration was written", i+1)
		}
	}
	return ""
}

// unspoken counts the lines with no wav in the cache for the CURRENT voice --
// which is what makes switching voice, nudging the pitch or editing a line all
// show up as work for ▶ to do, without any of them being tracked as such.
func (n *narrator) unspoken() int {
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
// Now the page says what it needs and ▶ does it: write the narration if it is
// missing or no longer matches the cut (staleFor), then speak whatever is not
// already in the cache. Both stages are one run, so ⏸ and ⏹ cover the pair and
// the progress bar runs from one end of the step to the other.
func (a *App) narrateRun() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	n := a.narr5
	if n == nil {
		return
	}
	segs := a.produceSegs()
	if len(segs) == 0 {
		a.setStatus("no cut yet — build one on the Cut step first")
		return
	}
	n.pullRows() // an edit still sitting in a text box is part of this run
	why := n.staleFor(segs)
	if why != "" && !exists(filepath.Join(a.transcriptDir(), "session.tsv")) {
		a.setStatus("run Transcript first — no session timeline")
		return
	}
	// the standing notes travel with the clips they were written for, and the
	// writing pass is what honors them
	notes := make([]string, len(segs))
	kept := 0
	for i, s := range segs {
		if notes[i] = n.noteFor(s); notes[i] != "" {
			kept++
		}
	}
	speak := append([]narrEntry(nil), n.entries...)
	if why == "" && n.unspoken() == 0 {
		// a run with nothing in it is not a failure, but it must not look like
		// one either: say which of the two halves is already done
		a.setStatus("the narration matches the cut and every line is spoken — edit a line, change the voice, or leave a ✎ note")
		return
	}
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	// this run's bar is this run's: the two tracks are summed, so a fraction
	// left behind by the previous step would be added to every reading here
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	// True only while the model is writing. The pulse used to stop when the run
	// did, which is the wrong end of it: the writing is one call with nothing to
	// measure, but the speaking that follows it is n lines and counts them, and
	// a bar still pulsing over that reading turned a real "speaking 4/9" into a
	// block sliding back and forth. It is read and written on the GUI thread
	// only -- the timeout below and the idle callback that clears it.
	writing := why != ""
	if writing {
		a.logf(">>> narrate: %s — writing %d clip(s), %d with a note, one thinking call (a minute or two), then speaking them",
			why, len(segs), kept)
		a.progress.SetText("thinking about the narration…")
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
	} else {
		a.logf(">>> narrate: the narration matches the cut; speaking the %d line(s) not in the cache",
			n.unspoken())
	}

	// where the speaking half of the bar starts. With writing to do it is the
	// second half of the run; without, the speaking is the whole run.
	speakBase, speakSpan := 0.0, 1.0
	if writing {
		speakBase, speakSpan = narrWriteShare, 1-narrWriteShare
	}

	go func() {
		if why != "" {
			a.logCtx("narration")
			entries, err := a.writeNarration(segs, notes)
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
			a.prog(trackSTT, narrWriteShare, "narration written")
			done := make(chan struct{})
			glib.IdleAdd(func() {
				writing = false // hands the bar to the count below
				n.entries = entries
				n.rewrite = false
				n.save()
				n.rebuildRows()
				a.logf(">>> narration written for %d clips", len(entries))
				a.setStatus("narration written — speaking it")
				close(done)
			})
			<-done
		}
		var failed error
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
			a.prog(trackSTT, speakBase+speakSpan*float64(i)/float64(len(speak)),
				"speaking %d/%d", i+1, len(speak))
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
		if n := a.narr5; n != nil {
			n.updateInputs()
			n.updateOut()
		}
		if err != nil {
			if !errors.Is(err, errStopped) {
				a.logf("%s FAILED: %v", stage, err)
				a.progress.SetText(stage + " failed — see log")
				a.setStatus(stage + " failed — see log")
				return
			}
			a.progress.SetText(stage + " stopped")
			a.setStatus(stage + " stopped")
			return
		}
		a.progress.SetFraction(1)
		a.progress.SetText("narration ready and spoken")
		a.setStatus("narration ready and spoken — ▶ the preview to hear it in place")
	})
}

// writeNarration writes every clip's line in one call: they refer to each
// other, so the lines only fit together if they are written together. notes is
// per clip and may be empty; a note is the editor's word on that clip and goes
// in with it.
// clipBriefs is everything the writer is told about the clips: for each one its
// length, its word budget, any standing ✎ note, and the session timeline over
// it -- what was said and what was on screen, in order.
//
// Every line is stamped with where inside the clip it falls, because "when" is
// half of what makes a narration line wrong. A clip whose first forty seconds
// are a fall and a ghost, and whose last two are a pickaxe finally coming out,
// reads as a clip about digging if the order and the offsets are stripped off,
// and the line written for it then talks about digging over forty seconds of
// something else.
// heardSources names the recordings whose audio the finished video carries:
// the footage, and only the footage. encodeClip takes each clip's sound from
// the video it was cut from and mixes the narration over it -- a separate
// microphone recording is transcribed, aligned and then never heard again.
//
// A simplification worth knowing about: where two captures overlap, a line is
// marked heard if it is on either of them, though a clip only carries the one
// it was cut from. Erring this way costs a quotable line; erring the other way
// puts the narration on top of the person saying it.
func (a *App) heardSources() map[string]bool {
	m := map[string]bool{}
	for _, p := range a.selVid {
		m[baseName(p)] = true
	}
	return m
}

// clipBriefs writes each clip's block. heard maps a recording's name to whether
// the video will carry its sound; an empty map means "no idea", and then every
// line is treated as heard, which is the assumption that cannot embarrass the
// narration.
func clipBriefs(segs []cutSeg, notes []string, rows []tsvRow, heard map[string]bool) string {
	var b strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&b, "\nCLIP %d: %.1f–%.1f (%.0f s, narration budget ~%d words)\n",
			i+1, s.S, s.E, s.E-s.S, int((s.E-s.S)*2.5))
		if i < len(notes) && notes[i] != "" {
			fmt.Fprintf(&b, "  EDITOR'S NOTE for this clip -- follow it: %s\n", notes[i])
		}
		n := 0
		for _, r := range rows {
			if r.e <= s.S-4 || r.s >= s.E+4 {
				continue
			}
			who := "HEARD " + r.spk
			switch {
			case r.spk == "EVENT":
				who = "ON SCREEN"
			case len(heard) == 0 || r.src == "" || heard[r.src]:
				// the viewer will hear this one under the narration
			default:
				who = "SAID " + r.spk + " (not in the video's audio)"
			}
			// seconds from the clip's own start, so the narration can follow the
			// clip instead of arriving at it; a line just outside the clip goes
			// negative or past the end, which is exactly what it is
			fmt.Fprintf(&b, "  [%+.0fs] %s: %s\n", r.s-s.S, who, r.text)
			n++
		}
		if n == 0 {
			b.WriteString("  (nothing recorded over this clip -- say something general, and invent nothing)\n")
		}
	}
	return b.String()
}

// narrWriteShare is how much of the run bar the writing owns when there is
// writing to do. The speaking takes the rest, so the one bar goes forward from
// the first clip written to the last line spoken.
const narrWriteShare = 0.5

// narrEntriesDone counts the clips finished in a reply that is still arriving:
// the objects inside "entries" whose braces have closed. Only what is complete
// counts, so the reading never claims a clip the model is still writing.
//
// It starts at the LAST "entries": [ in the text, because everything before
// the answer is the model thinking, out loud, about the answer -- braces,
// quotes, the word entries and all. Requiring the key's punctuation is what
// keeps a mention of it in that thinking from starting the count early; a
// worked example in there would still fool it, and the cost of that is one
// reading of a progress bar, which is the right price for not parsing prose.
func narrEntriesDone(s string) int {
	i := -1
	for at := 0; ; {
		j := strings.Index(s[at:], `"entries"`)
		if j < 0 {
			break
		}
		j += at
		at = j + 1
		rest := strings.TrimLeft(s[j+len(`"entries"`):], " \t\r\n")
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(rest[1:], " \t\r\n"), "[") {
			i = j
		}
	}
	if i < 0 {
		return 0
	}
	depth, n := 0, 0
	inStr, esc := false, false
	for _, r := range s[i:] {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr: // braces inside a line of narration are just text
		case r == '{':
			depth++
		case r == '}':
			if depth--; depth == 0 {
				n++
			}
		}
	}
	return n
}

func (a *App) writeNarration(segs []cutSeg, notes []string) ([]narrEntry, error) {
	rows := loadTSVRows(filepath.Join(a.transcriptDir(), "session.tsv"))
	// the box on the page is the whole system message: what used to be a
	// separate context field is part of it now (see buildStep4)
	system := a.prompt("narrate")
	user := a.ctxBlock() + "THE CLIPS AND WHAT IS KNOWN ABOUT EACH:" +
		clipBriefs(segs, notes, rows, a.heardSources())
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	// the bar, while the one long call runs: clips counted as they close. Only
	// ever forward -- a retry starts the count again, and a bar that fell back
	// to 1/9 would read as work being undone rather than redone.
	best := 0
	onText := func(s string) {
		if d := narrEntriesDone(s); d > best {
			best = d
			a.prog(trackSTT, narrWriteShare*math.Min(1, float64(best)/float64(len(segs))),
				"writing narration %d/%d", best, len(segs))
		}
	}
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		reply, err := a.llmChatRetryOn(msgs, true, onText)
		if err != nil {
			return nil, err
		}
		clean := strings.TrimSpace(reply)
		if i := strings.Index(clean, "{"); i >= 0 {
			clean = clean[i:]
		}
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
		var out struct {
			Entries []struct {
				Start, End    float64
				Text, Emotion string
			} `json:"entries"`
		}
		problem := ""
		if err := json.Unmarshal([]byte(clean), &out); err != nil {
			problem = "not valid JSON: " + err.Error()
		} else if len(out.Entries) != len(segs) {
			problem = fmt.Sprintf("%d entries for %d clips", len(out.Entries), len(segs))
		} else {
			var entries []narrEntry
			for i, e := range out.Entries {
				if e.Text == "" {
					problem = "empty text"
					break
				}
				instr := ""
				if i < len(notes) {
					instr = notes[i] // a note is standing: it survives the rewrite it caused
				}
				entries = append(entries, narrEntry{
					S: segs[i].S, E: segs[i].E, Text: e.Text, Emotion: e.Emotion, Instr: instr})
			}
			if problem == "" {
				return entries, nil
			}
		}
		a.logfIdle(">>> narration attempt %d rejected: %s", try+1, problem)
		msgs = append(msgs, msg("assistant", reply),
			msg("user", "Your answer failed validation: "+problem+". Return corrected strict JSON only."))
	}
	return nil, fmt.Errorf("no valid narration after 3 attempts")
}

// instructDialog sets the standing note for one clip. It used to rewrite that
// entry on its own, which is why it is not a text box in the row: a note is
// something you write once and the writer honors from then on, not a button
// that spends a request and moves one line out of step with the others.
func (a *App) instructDialog(i int) {
	if a.narr5 == nil || i < 0 || i >= len(a.narr5.entries) {
		return
	}
	n := a.narr5
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(fmt.Sprintf("Note for clip %d", i+1))
	win.SetDefaultSize(560, -1)

	entry := gtk.NewEntry()
	entry.SetText(n.entries[i].Instr)
	entry.SetPlaceholderText(`e.g. "mention the countdown", "shorter and punchier"`)
	entry.SetHExpand(true)
	lbl := gtk.NewLabel("Kept with the clip and honored the next time the whole narration " +
		"is written — narration is written in one pass so the lines fit together, so " +
		"there is no rewriting one of them on its own.")
	lbl.SetXAlign(0)
	lbl.SetWrap(true)
	lbl.AddCSSClass("dim-label")

	goBtn := gtk.NewButtonWithLabel("Save note")
	goBtn.AddCSSClass("suggested-action")
	save := func() {
		instr := strings.TrimSpace(entry.Text())
		win.Close()
		if instr == n.entries[i].Instr {
			return
		}
		n.pullRows() // an edit in a text box must not be lost to the rebuild below
		n.entries[i].Instr = instr
		// a note is a request for a rewrite, and the next ▶ is the rewrite. It
		// is the only thing that makes ▶ ask the model again about a cut that
		// has not moved, so it is recorded rather than remembered.
		n.rewrite = true
		n.save()
		n.rebuildRows()
		n.updateInputs()
		if instr == "" {
			a.setStatus(fmt.Sprintf("note on clip %d cleared — ▶ writes the narration again", i+1))
		} else {
			a.setStatus(fmt.Sprintf("note on clip %d saved — ▶ writes the narration again and applies it", i+1))
		}
	}
	goBtn.ConnectClicked(save)
	entry.ConnectActivate(save)

	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(entry)
	row.Append(goBtn)
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(14)
	box.SetMarginBottom(14)
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.Append(lbl)
	box.Append(row)
	win.SetChild(box)
	win.SetVisible(true)
}

// ---- TTS ---------------------------------------------------------------------
//
// The server itself lives in audiocpp.go: it does the listening in step 1 too,
// and the parts below are only what speaking adds to it.

// ttsModelID is the model the narration is spoken through: the id set in
// Settings (default index-tts2), verified once per session against the
// server's catalog -- a guessed or stale id would otherwise come back as an
// error that reads like a broken model.
func (a *App) ttsModelID() (string, error) {
	if a.ttsModel != "" {
		return a.ttsModel, nil
	}
	cat, err := audioCatalog(a.audioURL())
	if err != nil {
		return "", err
	}
	want := a.readConf().TTSModel
	m, ok := cat[want]
	if !ok {
		return "", fmt.Errorf("the audio.cpp server at %s serves %s, but not %q -- "+
			"the TTS model set in Settings", a.audioURL(), catalogIDs(cat), want)
	}
	// only a cloning model: that server also serves the ASR and diarization
	// models, and either would take the job and fail at it
	if m.Task != "" && m.Task != "clon" && m.Family != "index_tts2" {
		return "", fmt.Errorf("%q on %s is declared task %q -- it cannot clone a voice",
			want, a.audioURL(), m.Task)
	}
	a.ttsModel = want
	return want, nil
}

// ttsWav is the cache location for one entry's synthesis. The voice, pitch
// included, is part of the key: changing either must not serve the
// old speaker from cache, and must not throw away the old speaker's lines
// either -- switch back and they are all still there.
func (a *App) ttsWav(e narrEntry) string {
	// the default voice keeps the key it had before the voice picker existed, so a project
	// narrated back then does not re-speak every line the first time it opens
	key := e.Text + "|" + e.Emotion
	if v := a.voiceKey(); v != ownVoice {
		key = v + "|" + key
	}
	h := sha1.Sum([]byte(key))
	return filepath.Join(a.narrateDir(), "tts", fmt.Sprintf("%x.wav", h[:8]))
}

// synthesize speaks one entry through the resident server into the cache.
func (a *App) synthesize(e narrEntry) error { return a.speak(e.Text, e.Emotion, a.ttsWav(e)) }

// speak is the one call that reaches the model: text in, wav at out. The voice
// sample at the top of this page and the narration lines below it go through it
// identically, so a sample is a true preview -- same server, same reference,
// same settings.
func (a *App) speak(text, emotion, out string) error {
	if err := a.ensureVoiceRef(); err != nil {
		return err
	}
	if err := a.ensureAudioServer(); err != nil {
		return err
	}
	model, err := a.ttsModelID()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"model":     model,
		"input":     text,
		"voice_ref": a.refPath(),
		"emotion":   emotion,
		"language":  "en",
		"options":   map[string]any{"emotion_alpha": emoAlpha},
	})
	resp, err := http.Post(a.audioURL()+"/v1/audio/speech", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(data) < 1000 {
		return fmt.Errorf("tts (%s): %.300s", resp.Status, string(data))
	}
	os.MkdirAll(filepath.Dir(out), 0o755)
	return os.WriteFile(out, data, 0o644)
}

// syncSpeakIcons draws the per-line buttons: the row that is running shows the
// ⏸ that will stop it, every other row the ▶ that starts it.
//
// Running is three states, not one. The voice is sounding on that row; or the
// row is auditioning and its picture is rolling, which happens a moment before
// any sound does -- the audio pipeline only reports playing once it has
// prerolled; or the audition is stopped waiting for a line that has never been
// spoken, which is seconds, or a cold model load. Drawing only the first left
// the button the user had just pressed sitting on ▶ through all of it, which
// reads as a click that did nothing.
func (n *narrator) syncSpeakIcons() {
	sounding := n.voice != nil && n.voice.Playing()
	rolling := n.soloPic && n.player != nil && n.player.Playing()
	for i, r := range n.rows {
		setPlayIcon(r.speak, (sounding && i == n.speaking) || (i == n.solo && (rolling || n.synthing)),
			"play this clip with its line spoken over it", "pause this clip")
	}
}

// speakEntry auditions one line: the clip it was written for, played with the
// line spoken over it, and stopping at the end of that clip. A line read over a
// frozen frame is not what it was written against -- half of judging a line is
// whether it lands on what is happening -- and this button used to give you
// exactly that. Clicked again it pauses, and again resumes, the picture and the
// voice together. Another line's button interrupts it, which is what a list of
// lines to audition wants: one click, not stop-then-play.
func (a *App) speakEntry(i int) {
	n := a.narr5
	n.pullRows()
	if i < 0 || i >= len(n.entries) {
		return
	}
	e := n.entries[i]
	// this line has never been spoken and the server is on it: with the picture
	// the video is stopped waiting, without it there is nothing to play yet.
	// Either way a second click can only start the same synthesis twice.
	if n.solo == i && n.synthing {
		a.setStatus(fmt.Sprintf("still speaking line %d for the first time — ⏹ gives up on it", i+1))
		return
	}
	if n.solo == i && n.soloPic && n.player != nil {
		if n.player.Playing() {
			n.player.Pause()
			if n.voice != nil {
				n.voice.Pause()
			}
		} else {
			// playSeg back to -1, or the tick would see a line it has already
			// played and run the rest of the clip mute
			n.playSeg = -1
			n.player.Toggle()
		}
		n.syncSpeakIcons()
		a.updateRunControls()
		return
	}
	// spoken over a still frame, and clicked again: pause, resume, or -- if it
	// ran to its end -- play it again, all of which Toggle knows how to do
	if n.voice != nil && n.solo == i && !n.soloPic && (n.voice.Playing() || n.voice.Cued()) {
		n.voice.Toggle()
		n.syncSpeakIcons()
		return
	}
	n.claimVoice() // whatever else was sounding, this button takes over from it
	// The clip, with its line over it. The tick does the speaking from here: it
	// is the one thing that knows where the picture has got to, and it already
	// knows how to wait for a line that has never been spoken (holdForSynth) and
	// where to stop (solo, in followPlayback).
	if ed := a.ed; ed != nil && n.player != nil && ed.videoAt(e.S) != nil {
		n.playSeg, n.jumped = -1, -1
		n.solo, n.soloPic = i, true
		n.cue(e.S, true)
		a.updateRunControls()
		return
	}
	a.speakAlone(i, e)
}

// speakAlone is the audition for a clip no recording covers -- a source not on
// this machine, or one taken off the Inputs step since the cut was made. The
// line is spoken over whatever the picture is showing, because that is all
// there is, and this path does its own synthesis: with the picture rolling that
// is the tick's job, and it stops the video to wait rather than speaking late.
func (a *App) speakAlone(i int, e narrEntry) {
	n := a.narr5
	// claimed before the request goes out, not after it comes back: the row is
	// this button's from the click, and the wait for a first synthesis is the
	// part of it the user is most likely to press again
	n.speaking, n.solo, n.soloPic, n.synthing = i, i, false, true
	n.syncSpeakIcons()
	a.snapSources()
	go func() {
		wav := a.ttsWav(e)
		if !exists(wav) {
			glib.IdleAdd(func() { a.setStatus("synthesizing… (first line after a cold start also loads the model)") })
			if err := a.synthesize(e); err != nil {
				a.logfIdle("synthesis failed: %v", err)
				glib.IdleAdd(func() {
					n.synthing, n.solo = false, -1
					n.syncSpeakIcons()
					a.setStatus("synthesis failed — see log")
				})
				return
			}
		}
		glib.IdleAdd(func() {
			n.synthing = false
			a.setStatus(fmt.Sprintf("entry %d — no recording covers this clip, so the line plays on its own", i+1))
			if n.voice != nil {
				n.voice.PlaySegment(wav, 0, -1, true)
			}
			n.syncSpeakIcons()
		})
	}()
}

// ---- voice reference --------------------------------------------------------

// ensureVoiceRef makes sure the file the server clones from is on disk and
// matches the voice above: a base recording, moved by the pitch slider.
func (a *App) ensureVoiceRef() error {
	if err := a.ensureVoiceBase(); err != nil {
		return err
	}
	if exists(a.refPath()) {
		return nil
	}
	return a.shiftRef()
}

// ensureVoiceBase builds the unshifted reference. For a picked voice that is a
// copy of the wav it names -- rebuilt here so a project folder carried to
// another machine heals itself. For your own voice it is cut from the first
// recording's cleanest solo stretches: long turns of the dominant speaker with
// clearance from everyone else, taken from the ORIGINAL file for fidelity.
func (a *App) ensureVoiceBase() error {
	ref := a.refBase()
	if exists(ref) {
		return nil
	}
	// before the pitch slider there was one file and it was never shifted, so
	// an existing voice_ref.wav is exactly the base this now keeps apart
	if exists(a.refPath()) {
		return os.Rename(a.refPath(), ref)
	}
	id := a.voiceID()
	slot := narratorSlot(id)
	if slot == 0 {
		src := filepath.Join(a.voicesDir(), id+".wav")
		if !exists(src) {
			return fmt.Errorf("voice %q is no longer in %s -- pick another at the top of this step", id, a.voicesDir())
		}
		os.MkdirAll(filepath.Dir(ref), 0o755)
		return a.runCmd("ffmpeg", "-v", "error", "-y", "-i", src,
			"-ac", "1", "-c:a", "pcm_s16le", ref)
	}
	aud := a.narratorSource(slot)
	if aud == "" {
		return fmt.Errorf("nothing is tagged as narrator %d on the Inputs step", slot)
	}
	base := baseName(aud)
	turns, err := loadJSON(filepath.Join(a.outDir, "step1", base, "turns.json"))
	if err != nil {
		return fmt.Errorf("no diarization for %s -- run step 1", base)
	}
	type turn struct {
		s, e float64
		spk  string
	}
	var all []turn
	walkObjects(turns, func(m map[string]any) {
		spk, ok := m["speaker_id"].(string)
		ss, okS := m["start_sample"].(float64)
		es, okE := m["end_sample"].(float64)
		if ok && okS && okE {
			all = append(all, turn{ss / sampleRate, es / sampleRate, spk})
		}
	})
	// dominant speaker by total time
	durBy := map[string]float64{}
	for _, t := range all {
		durBy[t.spk] += t.e - t.s
	}
	dom, best := "", 0.0
	for s, d := range durBy {
		if d > best {
			dom, best = s, d
		}
	}
	// long solo turns, clear of other speakers
	var picks []turn
	for _, t := range all {
		if t.spk != dom || t.e-t.s < 5 {
			continue
		}
		clean := true
		for _, o := range all {
			if o.spk != dom && o.e > t.s-2 && o.s < t.e+2 {
				clean = false
				break
			}
		}
		if clean {
			picks = append(picks, t)
		}
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].e-picks[i].s > picks[j].e-picks[j].s })
	if len(picks) == 0 {
		return fmt.Errorf("no clean solo stretch found for the voice reference")
	}
	dir := a.narrateDir()
	os.MkdirAll(dir, 0o755)
	var list strings.Builder
	total := 0.0
	for i, t := range picks {
		if total >= 14 || i >= 3 {
			break
		}
		d := math.Min(t.e-t.s, 14-total)
		f := filepath.Join(dir, fmt.Sprintf(".ref%d.wav", i))
		if err := a.runCmd("ffmpeg", "-v", "error", "-y",
			"-ss", fmt.Sprint(t.s), "-t", fmt.Sprint(d),
			"-i", aud, "-ac", "1", f); err != nil {
			return err
		}
		fmt.Fprintf(&list, "file '%s'\n", f)
		total += d
	}
	lf := filepath.Join(dir, ".ref.list")
	if err := os.WriteFile(lf, []byte(list.String()), 0o644); err != nil {
		return err
	}
	if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-f", "concat", "-safe", "0",
		"-i", lf, "-c:a", "pcm_s16le", ref); err != nil {
		return err
	}
	a.logfIdle(">>> voice reference built: %.1f s from %s", total, base)
	return nil
}
