package main

// Step 5: Narrate. One entry per cut segment in a vertical list -- time range,
// editable text, editable emotion -- next to a preview player. Clicking an
// entry jumps the video there (play state preserved); ✎ regenerates a single
// entry under a user instruction; the global context box (prefilled from the
// cut page's context) carries goals like "open with an intro".
//
// Emotions are taken from how the moment was actually spoken -- the generator
// sees the original lines and the events -- and then heightened; the user has
// the last word per entry. Quotable original lines are reused verbatim -- as
// narration text, spoken by the clone like everything else.
//
// Voice on the fly: audio.cpp's own audiocpp_server keeps IndexTTS2 loaded, so
// per-line synthesis skips the model reload. This is the same HTTP API the
// audio.cpp WebUI proxies to, called the same way (voice_ref by path, since
// both sides share a filesystem). Autocut uses whatever server is listening
// (AUDIOCPP_SERVER or AUTOCUT_TTS_URL, default 127.0.0.1:8765) and never starts
// one itself.
// Synthesized lines are cached by hash of (voice, text, emotion). Two pitch
// knobs, doing different jobs: step 5's moves the reference before it is
// cloned, which changes who is speaking, and this page's moves the finished
// audio as a rubberband post-process at playback and at render.
//
// step5/narration.json      entries
// step5/voice_ref_base.wav  the reference as chosen or cut
// step5/voice_ref.wav       ...shifted by step 5's pitch: what the server gets
// step5/tts/<hash>.wav      synthesis cache

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
)

const (
	ttsPort  = 8765
	emoAlpha = "0.6"
)

const narrSystem = `You write the narration for a highlight video cut from a longer
session recording. You get the chosen clips with everything known about each:
what is on screen and what was actually said around that moment.
Rules:
- One entry per clip, exactly the clip's start/end.
- First person, the recording person's voice and personality. Clear about
  what is happening -- narration may explain better than the original did.
- Reuse the speaker's own quotable lines VERBATIM when they fall in a clip;
  never paraphrase a good line.
- Clips where nothing was said get narration built from what happens on
  screen ("fill the gaps").
- Max 2.5 words per second of clip length; breathing room matters.
- emotion: a short delivery phrase for the TTS ("excited, amazed",
  "tense whisper", "calm, storytelling"). Derive it from how the moment was
  actually spoken and what happens, then heighten it a little.
- Honor the editor's goals below (e.g. an intro over the first clip).
Return strict JSON, nothing else:
{"entries":[{"start":<sec>,"end":<sec>,"text":"...","emotion":"..."}]}`

type narrEntry struct {
	S       float64 `json:"s"`
	E       float64 `json:"e"`
	Text    string  `json:"text"`
	Emotion string  `json:"emotion"`
}

type narrator struct {
	a       *App
	entries []narrEntry

	player         *Player // video preview
	voice          *Player // narration audio rides along on this one
	playSeg        int     // entry currently voiced, -1 none
	jumped         int     // entry we last skipped a gap to, -1 none
	playVideoStart float64 // session start of the video loaded in the preview
	hints          *gtk.TextView
	list           *gtk.ListBox
	rows           []*narrRow
	building       bool // guards feedback loops while (re)building rows
}

type narrRow struct {
	emotion *gtk.Entry
	text    *gtk.TextView
}

func (a *App) narrPath() string { return filepath.Join(a.outDir, "step5", "narration.json") }

// ---- persistence ------------------------------------------------------------

func (n *narrator) load() {
	n.entries = nil
	if b, err := os.ReadFile(n.a.narrPath()); err == nil {
		var f struct{ Entries []narrEntry }
		if json.Unmarshal(b, &f) == nil {
			n.entries = f.Entries
		}
	}
}

func (n *narrator) save() {
	b, _ := json.MarshalIndent(struct {
		Entries []narrEntry `json:"entries"`
	}{n.entries}, "", "  ")
	os.MkdirAll(filepath.Dir(n.a.narrPath()), 0o755)
	if err := os.WriteFile(n.a.narrPath(), append(b, '\n'), 0o644); err != nil {
		n.a.logf("save narration: %v", err)
	}
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

func (a *App) buildStep5() gtk.Widgetter {
	n := &narrator{a: a, playSeg: -1, jumped: -1}
	a.narr5 = n
	if p, err := NewPlayer(); err == nil {
		n.player = p
	}
	if p, err := NewPlayer(); err == nil {
		n.voice = p
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
	// wide enough that a narration line wraps into about three, which is what
	// the entry boxes below are sized to show without scrolling
	left.SetSizeRequest(780, -1)

	// right side: video + controls + global context
	vframe := gtk.NewFrame("")
	if n.player != nil {
		n.player.Picture.SetVExpand(true)
		vframe.SetChild(n.player.Picture)
		click := gtk.NewGestureClick()
		click.ConnectReleased(func(cnt int, x, y float64) { n.player.Toggle() })
		n.player.Picture.AddController(click)
		glib.TimeoutAdd(100, n.followPlayback)
	}
	vframe.SetMarginTop(10)
	vframe.SetMarginEnd(12)
	vframe.SetMarginBottom(4)

	toggle := gtk.NewButtonWithLabel("⏯")
	toggle.SetTooltipText("play / pause")
	toggle.ConnectClicked(func() {
		if n.player != nil {
			n.player.Toggle()
		}
	})
	gen := gtk.NewButtonWithLabel("Generate narration")
	gen.AddCSSClass("suggested-action")
	gen.ConnectClicked(func() { a.generateNarration() })
	synthAll := gtk.NewButtonWithLabel("Synthesize all")
	synthAll.SetTooltipText("speak every entry that is not cached yet")
	synthAll.ConnectClicked(func() { a.synthAllClicked() })
	ctl := gtk.NewBox(gtk.OrientationHorizontal, 8)
	ctl.Append(toggle)
	ctl.Append(gen)
	ctl.Append(synthAll)

	hintLbl := gtk.NewLabel("Context & goals for the narration — what this session was, what matters, " +
		"and instructions like \"open with an intro that explains the event\"")
	hintLbl.SetXAlign(0)
	hintLbl.SetWrap(true)
	hintLbl.AddCSSClass("dim-label")
	n.hints = gtk.NewTextView()
	n.hints.SetWrapMode(gtk.WrapWord)
	n.hints.SetTopMargin(4)
	n.hints.SetLeftMargin(6)
	hintScroll := gtk.NewScrolledWindow()
	hintScroll.SetChild(n.hints)
	hintScroll.SetSizeRequest(-1, 80)
	hintScroll.AddCSSClass("frame")

	right := gtk.NewBox(gtk.OrientationVertical, 8)
	right.SetMarginEnd(12)
	right.SetMarginBottom(8)
	right.Append(vframe)
	right.Append(ctl)
	right.Append(hintLbl)
	right.Append(hintScroll)

	pane := gtk.NewPaned(gtk.OrientationHorizontal)
	pane.SetStartChild(left)
	pane.SetEndChild(right)
	pane.SetPosition(790)

	n.load()
	n.rebuildRows()
	return pane
}

// pitchValue is the output pitch, which lives on step 5 next to the reference
// pitch -- both belong to the voice, not to the writing.
func (a *App) pitchValue() float64 {
	if a.voice5 == nil || a.voice5.out == nil {
		return 1.0
	}
	return a.voice5.out.Value()
}

func (a *App) narrateHints() string {
	if a.narr5 == nil || a.narr5.hints == nil {
		return ""
	}
	buf := a.narr5.hints.Buffer()
	return strings.TrimSpace(buf.Text(buf.StartIter(), buf.EndIter(), false))
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
		speak := gtk.NewButtonWithLabel("🔊")
		speak.SetTooltipText("synthesize (cached) and play this line")
		speak.ConnectClicked(func() { n.a.speakEntry(i) })
		instruct := gtk.NewButtonWithLabel("✎")
		instruct.SetTooltipText("regenerate this entry under an instruction")
		instruct.ConnectClicked(func() { n.a.instructDialog(i) })
		head.Append(tl)
		head.Append(emotion)
		head.Append(speak)
		head.Append(instruct)

		text := gtk.NewTextView()
		text.SetWrapMode(gtk.WrapWord)
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
		n.list.Append(box)
		n.rows = append(n.rows, &narrRow{emotion: emotion, text: text})
	}
	n.building = false
}

// seekTo cues the preview at a session time, preserving play state.
func (n *narrator) seekTo(t float64) {
	ed := n.a.ed
	if ed == nil || n.player == nil {
		return
	}
	v := ed.videoAt(t)
	if v == nil {
		return
	}
	was := n.player.playing
	n.player.PlaySegment(v.path, t-v.start, -1, was)
	n.playVideoStart = v.start
	n.playSeg = -1 // re-trigger the voice on the next tick
}

// ---- playback with voice ----------------------------------------------------

// followPlayback rides the preview clock: when it enters an entry that has
// already been synthesized, that audio plays along.
func (n *narrator) followPlayback() bool {
	if n.player == nil || !n.player.playing {
		if n.voice != nil && n.voice.playing {
			n.voice.Pause()
		}
		return true
	}
	pos, ok := n.player.Position()
	if !ok {
		return true
	}
	t := n.playVideoStart + pos
	cur := -1
	for i, e := range n.entries {
		if t >= e.S && t < e.E {
			cur = i
			break
		}
	}
	// The preview plays the CUT, not the source: the stretch between two
	// entries is material the edit removed, so skip it instead of playing
	// through it. Re-entry is guarded twice over -- seekTo drops
	// player.playing until the new position prerolls, which stops the tick
	// above from arriving meanwhile, and jumped covers the case where the
	// seek cannot happen at all (no video covers that session time).
	if cur < 0 && len(n.entries) > 0 {
		next := -1
		for i, e := range n.entries {
			if e.S > t {
				next = i
				break
			}
		}
		switch {
		case next < 0: // past the last entry: the cut is over
			n.player.Pause()
			if n.voice != nil {
				n.voice.Pause()
			}
			n.playSeg, n.jumped = -1, -1
		case next != n.jumped:
			n.jumped = next
			n.seekTo(n.entries[next].S)
		}
		return true
	}
	n.jumped = -1
	if cur == n.playSeg {
		return true
	}
	n.playSeg = cur
	if cur < 0 || n.voice == nil {
		return true
	}
	e := n.entries[cur]
	wav := n.a.ttsWav(e)
	if exists(wav) {
		n.voice.PlaySegment(n.a.pitchedWav(wav), t-e.S, -1, true)
	}
	return true
}

func (a *App) firstAudioPath() string {
	_, auds := a.snappedSources()
	if len(auds) == 0 {
		return ""
	}
	return filepath.Join(a.root, auds[0])
}

func (a *App) sessionZero() float64 {
	zero := math.MaxFloat64
	vids, auds := a.snappedSources()
	for _, r := range append(append([]string{}, vids...), auds...) {
		if s, err := sourceStart(filepath.Join(a.root, r)); err == nil {
			zero = math.Min(zero, s)
		}
	}
	return zero
}

// ---- generation -------------------------------------------------------------

func (a *App) generateNarration() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	if a.ed == nil || len(a.ed.segs) == 0 {
		a.setStatus("no cut yet — build one on step 4 first")
		return
	}
	session, err := os.ReadFile(filepath.Join(a.outDir, "step3", "session.tsv"))
	if err != nil {
		a.setStatus("run step 3 first — no session timeline")
		return
	}
	segs := append([]cutSeg(nil), a.ed.segs...)
	hints := a.narrateHints()
	if hints == "" {
		hints = a.cutHints() // start from the cut page's context
	}
	a.saveProjectTo(filepath.Join(a.root, "project.json"))

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	a.logf(">>> narration: %d clips, one thinking call — expect a minute or two", len(segs))
	a.progress.SetText("writing narration…")
	glib.TimeoutAdd(150, func() bool {
		if !a.running {
			return false
		}
		a.progress.Pulse()
		return true
	})
	go func() {
		entries, err := a.writeNarration(string(session), segs, hints)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				if !errors.Is(err, errStopped) {
					a.logf("narration FAILED: %v", err)
				}
				a.progress.SetText("narration failed — see log")
				a.setStatus("narration failed")
				return
			}
			a.narr5.entries = entries
			a.narr5.save()
			a.narr5.rebuildRows()
			a.progress.SetFraction(1)
			a.progress.SetText("narration ready")
			a.logf(">>> narration written for %d clips", len(entries))
			a.setStatus("narration ready — edit, listen, adjust")
		})
	}()
}

func (a *App) writeNarration(session string, segs []cutSeg, hints string) ([]narrEntry, error) {
	rows := loadTSVRows(filepath.Join(a.outDir, "step3", "session.tsv"))
	var clips strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&clips, "\nCLIP %d: %.1f–%.1f (%.0f s, narration budget ~%d words)\n",
			i+1, s.S, s.E, s.E-s.S, int((s.E-s.S)*2.5))
		for _, r := range rows {
			if r.e > s.S-4 && r.s < s.E+4 {
				fmt.Fprintf(&clips, "  %s\n", r.text)
			}
		}
	}
	system := narrSystem
	if hints != "" {
		system += "\nEditor's goals and context -- honor them:\n" + hints
	}
	user := fmt.Sprintf("THE CLIPS AND WHAT IS KNOWN ABOUT EACH:%s", clips.String())
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		reply, err := a.llmChatRetry(msgs, true)
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
				entries = append(entries, narrEntry{
					S: segs[i].S, E: segs[i].E, Text: e.Text, Emotion: e.Emotion})
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

// instructDialog regenerates one entry under a user instruction.
func (a *App) instructDialog(i int) {
	if i < 0 || i >= len(a.narr5.entries) {
		return
	}
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(fmt.Sprintf("Rewrite entry %d", i+1))
	win.SetDefaultSize(520, -1)
	entry := gtk.NewEntry()
	entry.SetPlaceholderText(`e.g. "mention the countdown", "shorter and punchier"`)
	entry.SetHExpand(true)
	goBtn := gtk.NewButtonWithLabel("Rewrite")
	goBtn.AddCSSClass("suggested-action")
	run := func() {
		instr := strings.TrimSpace(entry.Text())
		win.Close()
		if instr != "" {
			a.rewriteEntry(i, instr)
		}
	}
	goBtn.ConnectClicked(run)
	entry.ConnectActivate(run)
	box := gtk.NewBox(gtk.OrientationHorizontal, 8)
	box.SetMarginTop(14)
	box.SetMarginBottom(14)
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.Append(entry)
	box.Append(goBtn)
	win.SetChild(box)
	win.SetVisible(true)
}

func (a *App) rewriteEntry(i int, instr string) {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	n := a.narr5
	n.pullRows()
	e := n.entries[i]
	ctxOf := func(j int) string {
		if j < 0 || j >= len(n.entries) {
			return "(none)"
		}
		return n.entries[j].Text
	}
	user := fmt.Sprintf(`One narration entry of a highlight video needs a rewrite.
Clip: %.1f–%.1f (%.0f s, budget ~%d words).
Previous entry: %s
Current text: %s
Current emotion: %s
Next entry: %s
Instruction from the editor: %s
Return strict JSON only: {"text":"...","emotion":"..."}`,
		e.S, e.E, e.E-e.S, int((e.E-e.S)*2.5),
		ctxOf(i-1), e.Text, e.Emotion, ctxOf(i+1), instr)
	a.running = true
	a.updateRunControls()
	a.setStatus("rewriting entry…")
	go func() {
		reply, err := a.llmChatRetry([]map[string]any{msg("user", user)}, true)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				a.logf("rewrite failed: %v", err)
				a.setStatus("rewrite failed")
				return
			}
			clean := strings.TrimSpace(reply)
			if k := strings.Index(clean, "{"); k >= 0 {
				clean = clean[k:]
			}
			clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
			var out struct{ Text, Emotion string }
			if json.Unmarshal([]byte(clean), &out) != nil || out.Text == "" {
				a.logf("rewrite: unusable reply: %.200s", reply)
				a.setStatus("rewrite failed")
				return
			}
			n.entries[i].Text = out.Text
			if out.Emotion != "" {
				n.entries[i].Emotion = out.Emotion
			}
			n.save()
			n.rebuildRows()
			a.setStatus(fmt.Sprintf("entry %d rewritten", i+1))
		})
	}()
}

// ---- TTS server -------------------------------------------------------------

// ttsURL is where an audio.cpp server is expected. AUDIOCPP_SERVER is audio.cpp's
// own variable -- the WebUI reads it to talk to an already-running server instead
// of managing one -- so setting it once points both frontends at the same server.
// AUTOCUT_TTS_URL overrides it when only autocut should move. Either way the
// server must see the project's output folder at that same absolute path: like
// the WebUI, autocut passes voice_ref as a path, not as an upload.
// The settings dialog wins over the environment: it is the one of the two a
// user can see and clear, and a stale export in the launching shell should not
// quietly outrank what the dialog says.
func (a *App) ttsURL() string {
	if u := a.configuredTTS(); u != "" {
		return u
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ttsPort)
}

// configuredTTS is the server the user pointed us at, from the settings file or
// the environment; "" means the compose default on the loopback port.
func (a *App) configuredTTS() string {
	if u := a.readConf().TTS; u != "" {
		return u
	}
	for _, k := range []string{"AUTOCUT_TTS_URL", "AUDIOCPP_SERVER"} {
		if u := strings.TrimRight(strings.TrimSpace(os.Getenv(k)), "/"); u != "" {
			return u
		}
	}
	return ""
}

// ttsModelID asks the server what it actually serves. Autocut's own server
// loads one model, but a shared one is named from audio.cpp's catalog and a
// guessed id comes back as an error that reads like a broken model.
func (a *App) ttsModelID() (string, error) {
	if a.ttsModel != "" {
		return a.ttsModel, nil
	}
	r, err := http.Get(a.ttsURL() + "/v1/models")
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var out struct {
		Data []struct{ ID, Family string }
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("model list from %s: %w", a.ttsURL(), err)
	}
	pick := ""
	for _, m := range out.Data {
		if m.Family == "index_tts2" {
			pick = m.ID
			break
		}
		if pick == "" {
			pick = m.ID
		}
	}
	if pick == "" {
		return "", fmt.Errorf("the audio.cpp server at %s has no model loaded", a.ttsURL())
	}
	a.ttsModel = pick
	return pick, nil
}

// ensureTTSServer checks that something is listening, and says so when nothing
// is. Autocut never starts a server: one model on one GPU, started by whoever
// owns the stack. The compose "audio" service runs audiocpp_server and points
// the WebUI at it, so bringing that up is what makes narration work.
func (a *App) ensureTTSServer() error {
	url := a.ttsURL()
	r, err := http.Get(url + "/health")
	if err != nil {
		return fmt.Errorf("no audio.cpp server answering at %s -- start it with: "+
			"cd ../cpp && docker compose up -d audio", url)
	}
	r.Body.Close()
	if a.ttsNoted != url {
		a.ttsNoted, a.ttsModel = url, "" // a different server, a different catalog
		a.logfIdle(">>> using the audio.cpp server on %s", url)
	}
	return nil
}

// ttsWav is the cache location for one entry's synthesis. The voice, pitch
// included, is part of the key: changing either in step 5 must not serve the
// old speaker from cache, and must not throw away the old speaker's lines
// either -- switch back and they are all still there.
func (a *App) ttsWav(e narrEntry) string {
	// the default voice keeps the key it had before step 5 existed, so a project
	// narrated back then does not re-speak every line the first time it opens
	key := e.Text + "|" + e.Emotion
	if v := a.voiceKey(); v != ownVoice {
		key = v + "|" + key
	}
	h := sha1.Sum([]byte(key))
	return filepath.Join(a.outDir, "step5", "tts", fmt.Sprintf("%x.wav", h[:8]))
}

// pitchedWav applies the pitch slider via rubberband, cached per pitch value.
func (a *App) pitchedWav(wav string) string {
	p := math.Float64frombits(a.pitchNow.Load())
	if p == 0 {
		p = 1.0
	}
	if math.Abs(p-1.0) < 0.011 {
		return wav
	}
	out := strings.TrimSuffix(wav, ".wav") + fmt.Sprintf("_p%.2f.wav", p)
	if !exists(out) {
		if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", wav,
			"-af", fmt.Sprintf("rubberband=pitch=%.3f", p), out); err != nil {
			a.logf("pitch: %v", err)
			return wav
		}
	}
	return out
}

// synthesize speaks one entry through the resident server into the cache.
func (a *App) synthesize(e narrEntry) error { return a.speak(e.Text, e.Emotion, a.ttsWav(e)) }

// speak is the one call that reaches the model: text in, wav at out. Step 5's
// voice samples and step 6's narration lines go through it identically, so a
// sample is a true preview -- same server, same reference, same settings.
func (a *App) speak(text, emotion, out string) error {
	if err := a.ensureVoiceRef(); err != nil {
		return err
	}
	if err := a.ensureTTSServer(); err != nil {
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
	resp, err := http.Post(a.ttsURL()+"/v1/audio/speech", "application/json", bytes.NewReader(body))
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

func (a *App) speakEntry(i int) {
	n := a.narr5
	n.pullRows()
	if i < 0 || i >= len(n.entries) {
		return
	}
	a.snapSources()
	e := n.entries[i]
	go func() {
		wav := a.ttsWav(e)
		if !exists(wav) {
			glib.IdleAdd(func() { a.setStatus("synthesizing… (first line after a cold start also loads the model)") })
			if err := a.synthesize(e); err != nil {
				a.logfIdle("synthesis failed: %v", err)
				glib.IdleAdd(func() { a.setStatus("synthesis failed — see log") })
				return
			}
		}
		glib.IdleAdd(func() {
			a.setStatus(fmt.Sprintf("entry %d", i+1))
			if n.voice != nil {
				n.voice.PlaySegment(a.pitchedWav(wav), 0, -1, true)
			}
		})
	}()
}

func (a *App) synthAllClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	n := a.narr5
	n.pullRows()
	a.snapSources()
	entries := append([]narrEntry(nil), n.entries...)
	if len(entries) == 0 {
		a.setStatus("no narration yet — Generate first")
		return
	}
	a.running = true
	a.stopFlag.Store(false)
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	go func() {
		var failed error
		for i, e := range entries {
			if a.stopFlag.Load() {
				failed = errStopped
				break
			}
			if exists(a.ttsWav(e)) {
				continue
			}
			a.prog(trackSTT, float64(i)/float64(len(entries)), "speaking %d/%d", i+1, len(entries))
			if err := a.synthesize(e); err != nil {
				failed = err
				break
			}
		}
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if failed != nil {
				if !errors.Is(failed, errStopped) {
					a.logf("synthesize all FAILED: %v", failed)
				}
				a.progress.SetText("synthesis stopped")
				a.setStatus("synthesis stopped")
				return
			}
			a.progress.SetFraction(1)
			a.progress.SetText("all lines spoken")
			a.setStatus("all lines spoken")
		})
	}()
}

// ---- voice reference --------------------------------------------------------

// ensureVoiceRef makes sure the file the server clones from is on disk and
// matches step 5: a base recording, moved by the pitch slider.
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
	if id := a.voiceID(); id != ownVoice {
		src := filepath.Join(voicesDir(), id+".wav")
		if !exists(src) {
			return fmt.Errorf("voice %q is no longer in %s -- pick another in step 5", id, voicesDir())
		}
		os.MkdirAll(filepath.Dir(ref), 0o755)
		return a.runCmd("ffmpeg", "-v", "error", "-y", "-i", src,
			"-ac", "1", "-c:a", "pcm_s16le", ref)
	}
	aud := a.firstAudioPath()
	if aud == "" {
		return fmt.Errorf("no voice recording selected")
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
	dir := filepath.Join(a.outDir, "step5")
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
