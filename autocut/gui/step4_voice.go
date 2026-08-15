package main

// The voice every narrated line is spoken in: the top of the Narrate step,
// above the lines themselves. It was a step of its own, two rows away from the
// text it speaks, so hearing whether a voice suited the narration meant
// remembering the narration. IndexTTS2 keeps timbre and emotion apart --
// the speaker comes from a reference wav, the emotion from each line's own
// text -- so picking a voice is exactly picking which wav to clone, and the
// narrate step's per-line emotion goes on working unchanged.
//
// Two sources: one of the session's own voices -- the dominant speaker in the
// recording tagged with that narrator slot on the Inputs step, cut from step 1's
// diarization, which is the pipeline's default -- or a wav in the voices folder, the
// CC0 references that ship beside audio.cpp's models, plus anything added with
// "Add sample…", which converts and copies it in. The list shows file names
// because that is what they are: a row can be opened, replaced or deleted in
// the folder the button beside it opens. The pick is installed as step4/voice_ref.wav
// because that is the file the TTS server is handed, and the output folder is
// the one mounted into the server at its own absolute path -- the voices folder
// sits at a different path inside the container, so aiming the server straight
// at it would not resolve.
//
// The pitch slider post-processes the reference before the model ever sees it,
// which is the difference between a new speaker and a familiar one transposed:
// IndexTTS2 takes its timbre from this file, so a shifted reference is cloned
// as a shifted voice, and the narrate step speaks in it without knowing anything
// changed. The unshifted recording is kept beside it as voice_ref_base.wav, so
// moving the slider costs one ffmpeg pass rather than re-cutting the
// diarization.
//
// Switching is free: the synthesis cache is keyed on the voice and its pitch as
// well as the text, so lines spoken in an earlier voice are still there when
// you switch back.

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// ownVoice is the id of "clone me", the default the pipeline had before this
// step existed. It is narrator 1's id and stays spelled "own": it is what every
// existing project's voice.txt says and what every synthesis cache key was
// built from, and renaming it would silently re-speak finished work.
const ownVoice = "own"

// narratorPrefix names the other three. They exist because a session is often a
// group: the Inputs step tags up to four voices, and any of them can be the one
// the narration is spoken in.
const narratorPrefix = "narrator"

// narratorVoiceID is the voice id for a narrator slot.
func narratorVoiceID(n int) string {
	if n <= 1 {
		return ownVoice
	}
	return narratorPrefix + strconv.Itoa(n)
}

// narratorSlot is the reverse: the slot an id names, or 0 for a wav out of the
// voices folder.
func narratorSlot(id string) int {
	if id == ownVoice {
		return 1
	}
	if !strings.HasPrefix(id, narratorPrefix) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, narratorPrefix))
	if err != nil || n < 2 || n > narratorSlots {
		return 0
	}
	return n
}

const sampleDefault = "This is the voice the narration will be spoken in. " +
	"It should stay clear and easy to follow for a couple of minutes."

// pitchRange caps the reference shift at half an octave either way. Past that
// the clone stops sounding like a person rather than like a different one.
const pitchRange = 6.0

type voiceOpt struct {
	id   string // a narrator slot ("own", "narrator2"..) or the wav's base name;
	name string // also part of the cache key. name is what the row reads
	path string // source wav; empty for a narrator, who is cut from the recording
}

type voicePicker struct {
	a       *App
	list    *gtk.ListBox
	filter  *gtk.SearchEntry
	sample  *gtk.Entry
	cur     *gtk.Label
	pitch   *gtk.Scale // semitones on the reference: a different speaker
	voices  []voiceOpt
	player  *Player
	playBtn *gtk.Button // ▶/⏸ for the sample; drawn by syncPlayIcons
	stopBtn *gtk.Button // and its ⏹, lit only while there is a sample to end
	busy    bool        // one synthesis at a time; the button says so
	syncing bool        // showing the stored voice, which is not a fresh choice
	drag    int         // bumped per slider move; only the last one rebuilds the reference
}

// voicesDir is where the reference wavs live -- the same folder the WebUI
// lists in its "Built-in voice" dropdown, mounted into the server by the
// compose file.
func (a *App) voicesDir() string { return a.readConf().Voices }

func (a *App) listVoices() []voiceOpt {
	// the people in the session first, then the CC0 samples. Slot 1 is always
	// offered -- it is the default, and an untagged session still falls back to
	// a recording -- while 2..4 appear only once somebody is tagged as them.
	var out []voiceOpt
	for n := 1; n <= narratorSlots; n++ {
		if n > 1 && a.narratorPath(n) == "" {
			continue
		}
		out = append(out, voiceOpt{id: narratorVoiceID(n), name: a.narratorVoiceName(n)})
	}
	dir := a.voicesDir()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		// the file name, not a prettified version of it: these are files on
		// disk that can be opened, replaced and added to, and a row that reads
		// "gb · female · 30s" cannot be looked up in a folder
		out = append(out, voiceOpt{
			id:   strings.TrimSuffix(n, filepath.Ext(n)),
			name: n,
			path: filepath.Join(dir, n),
		})
	}
	return out
}

// narratorSource is the recording slot n is actually cut from: whoever carries
// the tag on the Inputs step, and for slot 1 the session's fallback as well --
// a project from before the tags existed still has to speak.
func (a *App) narratorSource(n int) string {
	if p := a.narratorPath(n); p != "" {
		return p
	}
	if n == 1 {
		return a.voiceSource()
	}
	return ""
}

// narratorFile is the same by name. Which file it is decides who speaks the
// narration, so the row says it rather than leaving it to be inferred.
func (a *App) narratorFile(n int) string {
	if p := a.narratorSource(n); p != "" {
		return filepath.Base(p)
	}
	return ""
}

// narratorVoiceName is what the picker lists a slot as. Slot 1 is "Narrator 1"
// like the other three: it is a tag on the Inputs step, and whoever is running
// the app need not be the one wearing it. Its id stays "own" -- that spelling
// is in every synthesis cache key written before the tags existed.
func (a *App) narratorVoiceName(n int) string {
	who := fmt.Sprintf("Narrator %d", n)
	if f := a.narratorFile(n); f != "" {
		return who + " — " + f
	}
	return who + " — cut from the recording"
}

// ---- the chosen voice -------------------------------------------------------

// voiceID is the voice this project speaks in, remembered in the output folder
// so it survives a restart, and mixed into the synthesis cache key so switching
// voices does not throw away work.
func (a *App) voiceID() string {
	a.voiceMu.Lock()
	defer a.voiceMu.Unlock()
	if a.voiceSel != "" {
		return a.voiceSel
	}
	a.voiceSel = ownVoice
	if b, err := os.ReadFile(filepath.Join(a.narrateDir(), "voice.txt")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			a.voiceSel = s
		}
	}
	return a.voiceSel
}

// refBase is the chosen recording as it was picked, refPath the shifted copy
// the server is handed. Keeping them apart is what makes the slider cheap: the
// base is cut or converted once, the shift is redone from it.
func (a *App) refBase() string { return filepath.Join(a.narrateDir(), "voice_ref_base.wav") }
func (a *App) refPath() string { return filepath.Join(a.narrateDir(), "voice_ref.wav") }

// setVoice installs the reference the model clones from. "own" just removes the
// files, so ensureVoiceRef cuts a fresh one from the diarization the next time
// anything speaks; anything else is converted to the mono PCM the model wants.
// Safe off the GUI thread -- it only runs ffmpeg and writes files.
func (a *App) setVoice(v voiceOpt) error {
	dir := a.narrateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// the shifted copy belongs to the old voice; drop it either way and let
	// ensureVoiceRef rebuild it from whatever base we end up with
	os.Remove(a.refPath())
	if narratorSlot(v.id) > 0 {
		os.Remove(a.refBase())
	} else if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", v.path,
		"-ac", "1", "-c:a", "pcm_s16le", a.refBase()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "voice.txt"), []byte(v.id), 0o644); err != nil {
		return err
	}
	a.voiceMu.Lock()
	a.voiceSel = v.id
	a.voiceMu.Unlock()
	return nil
}

// importVoice adds a recording to the voices folder as the mono PCM wav the
// model wants -- so any audio file works, not just the ones already in the
// right format. It lands beside the built-in voices rather than in the project
// because the id the caches and voice.txt use is the file's base name, which
// only keeps resolving while the file stays somewhere the list looks; a voice
// added here is therefore available to every project, and to the WebUI.
// Safe off the GUI thread -- ffmpeg and files only.
func (a *App) importVoice(src string) (voiceOpt, error) {
	dir := a.voicesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return voiceOpt{}, fmt.Errorf("%s is not writable: %w", dir, err)
	}
	base := sanitizeVoiceID(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	id, dst := base, filepath.Join(dir, base+".wav")
	// never overwrite: a name already taken belongs to another voice, and every
	// project pointing at it would start speaking in this one instead
	for i := 2; exists(dst); i++ {
		id = fmt.Sprintf("%s-%d", base, i)
		dst = filepath.Join(dir, id+".wav")
	}
	if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", src,
		"-ac", "1", "-c:a", "pcm_s16le", dst); err != nil {
		return voiceOpt{}, err
	}
	return voiceOpt{id: id, name: filepath.Base(dst), path: dst}, nil
}

// voiceImport is what a folder import did, in enough detail to say it in one
// line without the caller counting anything itself.
type voiceImport struct {
	added   []voiceOpt
	skipped int // a wav of that name is in the voices folder already
	failed  int // ffmpeg refused it; the log says why
}

func (r voiceImport) summary() string {
	parts := []string{fmt.Sprintf("%d added", len(r.added))}
	if r.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already there", r.skipped))
	}
	if r.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d could not be converted — see log", r.failed))
	}
	return strings.Join(parts, ", ")
}

// importVoiceDir adds every recording sitting directly in dir, because samples
// arrive as a folder far more often than one at a time.
//
// Sub-folders are left alone: aiming this at a music library by accident should
// cost one folder, not a tree. A name already in the voices folder is skipped
// rather than copied beside itself as "-2" -- re-adding a folder is the likely
// mistake, and answering it with thirty duplicates is worse than answering it
// with nothing. Picking one file by hand still suffixes: that is a deliberate
// act on a particular recording.
// Safe off the GUI thread -- ffmpeg and files only.
func (a *App) importVoiceDir(dir string) (voiceImport, error) {
	var res voiceImport
	if sameDir(dir, a.voicesDir()) {
		return res, fmt.Errorf("that is the voices folder itself — everything in it is listed already")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return res, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && mediaExt[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return res, fmt.Errorf("no audio or video files directly in %s", dir)
	}
	sort.Strings(names)
	for _, n := range names {
		src := filepath.Join(dir, n)
		if a.voiceTaken(src) {
			res.skipped++
			continue
		}
		v, err := a.importVoice(src)
		if err != nil {
			a.logfIdle("add voice %s: %v", n, err)
			res.failed++
			continue
		}
		// one line each: converting a folder of samples takes long enough that
		// a still status bar looks like a hang
		a.logfIdle("added voice %s", v.name)
		res.added = append(res.added, v)
	}
	return res, nil
}

// voiceTaken reports whether src would land on a wav that is there already.
func (a *App) voiceTaken(src string) bool {
	id := sanitizeVoiceID(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	return exists(filepath.Join(a.voicesDir(), id+".wav"))
}

// sameDir compares by inode rather than by string, so a symlink or a trailing
// slash cannot make the voices folder look like somewhere else.
func sameDir(x, y string) bool {
	fx, err := os.Stat(x)
	if err != nil {
		return false
	}
	fy, err := os.Stat(y)
	return err == nil && os.SameFile(fx, fy)
}

// sanitizeVoiceID keeps the id usable as a file name: it is pasted into the
// sample cache's names and read back out of voice.txt, so a slash in it would
// write somewhere else entirely.
func sanitizeVoiceID(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
	if s = strings.Trim(s, "-._"); s == "" {
		return "voice"
	}
	return s
}

// ---- the reference pitch ----------------------------------------------------

// pitchST is how far the reference is moved, in semitones, remembered in the
// output folder next to the voice it belongs to.
func (a *App) pitchST() float64 {
	if a.pitchRead {
		return a.pitchSel
	}
	a.pitchRead, a.pitchSel = true, 0
	if b, err := os.ReadFile(filepath.Join(a.narrateDir(), "pitch.txt")); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
			a.pitchSel = clampPitch(f)
		}
	}
	return a.pitchSel
}

func clampPitch(st float64) float64 {
	return math.Max(-pitchRange, math.Min(pitchRange, st))
}

// setPitchST remembers the shift and drops the shifted reference so it is built
// again. When there is no base yet -- "own voice" before step 1 has run -- that
// is all there is to do; the next line spoken cuts one and shifts it then.
// Safe off the GUI thread.
func (a *App) setPitchST(st float64) error {
	st = clampPitch(st)
	dir := a.narrateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pitch.txt"),
		[]byte(strconv.FormatFloat(st, 'f', 1, 64)), 0o644); err != nil {
		return err
	}
	a.pitchRead, a.pitchSel = true, st
	os.Remove(a.refPath())
	if !exists(a.refBase()) {
		return nil
	}
	return a.shiftRef()
}

// shiftRef writes the file the server actually clones: the base moved by the
// slider. formant=preserved moves the pitch and leaves the vowels where a
// human's are, so the reference still sounds like someone worth cloning
// instead of like a sped-up tape -- the model reproduces artifacts as
// faithfully as it reproduces the voice.
func (a *App) shiftRef() error {
	if st := a.pitchST(); st != 0 {
		return a.runCmd("ffmpeg", "-v", "error", "-y", "-i", a.refBase(),
			"-af", fmt.Sprintf("rubberband=pitch=%.4f:formant=preserved:pitchq=quality",
				math.Pow(2, st/12)),
			"-ac", "1", "-c:a", "pcm_s16le", a.refPath())
	}
	// unshifted: the server still reads one fixed path, so hand it a copy
	// rather than teaching every caller which of the two files to use
	b, err := os.ReadFile(a.refBase())
	if err != nil {
		return err
	}
	return os.WriteFile(a.refPath(), b, 0o644)
}

// voiceKey is the voice as the caches see it. Pitch 0 keeps the key the voice
// had before the slider existed, so nothing already spoken is orphaned by this
// step gaining a knob.
func (a *App) voiceKey() string {
	if st := a.pitchST(); st != 0 {
		return fmt.Sprintf("%s@%+.1f", a.voiceID(), st)
	}
	return a.voiceID()
}

// sampleWav caches one voice's reading of one sample text, so hearing a voice
// again is instant and comparing two is a click each.
func (a *App) sampleWav(id, text string) string {
	h := sha1.Sum([]byte(text))
	return filepath.Join(a.narrateDir(), "samples", fmt.Sprintf("%s_%x.wav", id, h[:6]))
}

// ---- the page ---------------------------------------------------------------

// buildVoicePicker is the top of the Narrate step: who speaks. It was a step of
// its own, which put the choice of voice and the lines it would speak on
// different screens -- and made the run bar's ▶ mean "play a sample" here and
// "run this step" everywhere else. Sharing the page, the sample gets its own ▶
// and the run bar keeps one meaning.
func (a *App) buildVoicePicker() gtk.Widgetter {
	vp := &voicePicker{a: a}
	a.voice5 = vp
	if p, err := NewPlayer(); err == nil {
		vp.player = p
		p.OnState = a.updateRunControls
		p.OnError = a.playerErr("the voice sample")
	}
	vp.voices = vp.a.listVoices()

	vp.list = gtk.NewListBox()
	vp.list.SetSelectionMode(gtk.SelectionSingle)
	vp.fillRows()
	// selecting IS choosing: this is the step whose whole job is that choice,
	// and switching costs nothing now that the cache knows about voices.
	vp.list.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil || vp.syncing {
			return
		}
		vp.choose(row.Index())
	})

	// eighty-odd rows is more than the eye scans, so let the accent or the age
	// be typed instead
	vp.filter = gtk.NewSearchEntry()
	vp.filter.SetPlaceholderText("filter — gb, female, 30s…")
	vp.filter.ConnectSearchChanged(func() {
		vp.list.InvalidateFilter()
	})
	vp.list.SetFilterFunc(func(row *gtk.ListBoxRow) bool {
		q := strings.ToLower(strings.TrimSpace(vp.filter.Text()))
		if q == "" || row.Index() < 0 || row.Index() >= len(vp.voices) {
			return true
		}
		return strings.Contains(strings.ToLower(vp.voices[row.Index()].name), q)
	})

	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(vp.list)
	scroll.SetVExpand(true)

	add := flexButton("Add file…", "Copy one recording into the voices folder and use it")
	add.ConnectClicked(vp.addVoiceDialog)
	addDir := flexButton("Add folder…",
		"Copy every recording in a folder into the voices folder — sub-folders are left alone")
	addDir.ConnectClicked(vp.addVoiceDirDialog)
	openDir := gtk.NewButtonFromIconName("folder-open-symbolic")
	openDir.SetTooltipText("Open the voices folder")
	openDir.ConnectClicked(func() { a.openFolder(a.voicesDir()) })
	addRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	add.SetHExpand(true)
	addDir.SetHExpand(true)
	addRow.Append(add)
	addRow.Append(addDir)
	addRow.Append(openDir)

	leftBox := gtk.NewBox(gtk.OrientationVertical, 6)
	leftBox.SetMarginStart(10)
	leftBox.SetMarginTop(10)
	leftBox.SetMarginBottom(10)
	leftBox.SetMarginEnd(10)
	leftBox.Append(vp.filter)
	leftBox.Append(scroll)
	leftBox.Append(addRow)

	// right: what the choice means, and the way to hear it. This is a third of a
	// page now, so what used to be two explanatory paragraphs is one line and a
	// set of tooltips -- the narration below is what the height is for.
	vp.cur = gtk.NewLabel("")
	vp.cur.SetXAlign(0)
	vp.cur.SetWrap(true)

	vp.sample = gtk.NewEntry()
	vp.sample.SetText(sampleDefault)
	vp.sample.SetHExpand(true)
	vp.sample.SetTooltipText("Enter — or ▶ beside it — speaks this in the selected voice")
	vp.sample.ConnectActivate(func() { vp.playSample() })

	// its own transport, both halves of it, because the run bar belongs to the
	// narration preview under it: one page, two things that can play, and a
	// shared button could only ever mean one of them. ▶ is the ⏸ for the sample
	// while it sounds, like every other play button in the app (syncPlayIcons),
	// and ⏹ is here rather than borrowed from the run bar for the same reason.
	vp.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	vp.playBtn.SetTooltipText("Speak the sample in the selected voice")
	vp.playBtn.ConnectClicked(vp.playClicked)
	vp.stopBtn = gtk.NewButtonFromIconName("media-playback-stop-symbolic")
	vp.stopBtn.SetTooltipText("Stop the sample")
	vp.stopBtn.SetSensitive(false)
	vp.stopBtn.ConnectClicked(vp.stopClicked)
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(vp.sample)
	row.Append(vp.playBtn)
	row.Append(vp.stopBtn)

	// the reference is shifted, not the narration: this changes who is speaking
	// in the finished video, so it belongs next to the choice of voice rather
	// than next to the render
	vp.pitch = gtk.NewScaleWithRange(gtk.OrientationHorizontal, -pitchRange, pitchRange, 0.5)
	vp.pitch.SetDrawValue(true)
	vp.pitch.SetHExpand(true)
	vp.pitch.SetTooltipText("Shift the reference recording before it is cloned — " +
		"a different speaker, not the same one transposed")
	vp.pitch.AddMark(-pitchRange, gtk.PosBottom, "deeper")
	vp.pitch.AddMark(0, gtk.PosBottom, "as recorded")
	vp.pitch.AddMark(pitchRange, gtk.PosBottom, "higher")
	// dragging crosses two dozen stops; only the one it is let go on is worth
	// an ffmpeg pass, so each move cancels the pass the previous one queued
	vp.pitch.ConnectValueChanged(func() {
		if vp.syncing {
			return
		}
		vp.drag++
		gen, st := vp.drag, vp.pitch.Value()
		glib.TimeoutAdd(400, func() bool {
			if gen == vp.drag {
				vp.applyPitch(st)
			}
			return false
		})
	})
	pitchLbl := gtk.NewLabel("pitch:")
	pitchLbl.SetXAlign(0)

	// there used to be a second slider here that moved the finished narration
	// instead of the reference. It was free where this one costs a re-speak, but
	// it post-processed generated speech without preserving formants and stacked
	// with the tempo fit in produce -- two passes over exactly the lines already
	// struggling to fit their slot. One knob, applied where the quality is.
	knobs := gtk.NewBox(gtk.OrientationHorizontal, 8)
	knobs.Append(pitchLbl)
	knobs.Append(vp.pitch)

	note := gtk.NewLabel("Every line is spoken by cloning the selected recording; " +
		"switching voices keeps what you already synthesized.")
	note.SetWrap(true)
	note.SetXAlign(0)
	note.AddCSSClass("dim-label")

	right := gtk.NewBox(gtk.OrientationVertical, 8)
	right.SetMarginStart(12)
	right.SetMarginEnd(12)
	right.SetMarginTop(10)
	right.SetMarginBottom(10)
	right.SetHExpand(true)
	right.Append(vp.cur)
	right.Append(knobs)
	right.Append(row)
	right.Append(note)

	// scrolled, because this column is the one that runs out of height first on
	// a laptop screen, and a clipped slider cannot be dragged
	rightScroll := gtk.NewScrolledWindow()
	rightScroll.SetChild(right)
	rightScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	rightScroll.SetHExpand(true)

	// a divider rather than a fixed 300px list: the file names decide how much
	// width the list wants, and on a small screen that is width the knobs need
	box := gtk.NewPaned(gtk.OrientationHorizontal)
	box.SetStartChild(leftBox)
	box.SetEndChild(rightScroll)
	// see step 1: with shrink left on, a pane dragged past its minimum is
	// clipped rather than resized, and the margin goes off the window with it
	box.SetShrinkStartChild(false)
	box.SetShrinkEndChild(false)
	box.SetPosition(280)

	vp.syncSelection()
	return box
}

// flexButton is a button whose label may be ellipsized, so a row of them keeps
// this column's minimum width small: an unellipsized label is a floor under the
// whole pane, and this pane is one of the parts meant to give way first.
func flexButton(text, tip string) *gtk.Button {
	l := gtk.NewLabel(text)
	l.SetEllipsize(pango.EllipsizeEnd)
	b := gtk.NewButton()
	b.SetChild(l)
	b.SetTooltipText(tip)
	return b
}

func (vp *voicePicker) fillRows() {
	for _, v := range vp.voices {
		l := gtk.NewLabel(v.name)
		l.SetXAlign(0)
		l.SetMarginTop(5)
		l.SetMarginBottom(5)
		l.SetMarginStart(8)
		l.SetEllipsize(pango.EllipsizeMiddle) // so the divider can be dragged in
		l.SetTooltipText(v.name)
		vp.list.Append(l)
	}
}

// reload rebuilds the list from the folder -- after an import, there is a file
// in it that was not there when the page was built. sel names the voice to
// leave selected, and choose says whether landing on it counts as picking it.
// Importing one file picks it, which is what asking for that file meant.
// Importing a folder must not: thirty arrivals have no claim on the project,
// and re-picking the voice already in use is not free either -- for "own" it
// deletes the reference cut from the recording.
func (vp *voicePicker) reload(sel string, choose bool) {
	vp.syncing = true
	for {
		row := vp.list.RowAtIndex(0)
		if row == nil {
			break
		}
		vp.list.Remove(row)
	}
	vp.voices = vp.a.listVoices()
	vp.fillRows()
	vp.syncing = !choose
	for i, v := range vp.voices {
		if v.id == sel {
			if r := vp.list.RowAtIndex(i); r != nil {
				vp.list.SelectRow(r)
			}
			if !choose {
				vp.showCurrent(v) // nothing chose, so nothing else says what is current
				vp.syncing = false
			}
			return
		}
	}
	vp.syncing = false
	vp.syncSelection()
}

// refreshNarrators rebuilds the list when the Inputs step changes -- the
// narrator rows ARE that step's tags, so tagging somebody as narrator 2 adds a
// row and untagging them takes it away, and the file each row clones is part of
// what it reads. Rebuilding rather than renaming in place, because the set of
// rows moves and not just their text; false, because none of this is a choice
// the user just made and re-choosing "own" would throw the reference away.
func (vp *voicePicker) refreshNarrators() {
	if vp.list == nil {
		return
	}
	vp.reload(vp.a.voiceID(), false)
}

// addVoiceDirDialog installs every recording in a folder -- the shape samples
// usually come in, and thirty trips through a file chooser is not a workflow.
// Same import and the same thread rule as addVoiceDialog.
func (vp *voicePicker) addVoiceDirDialog() {
	a := vp.a
	d := gtk.NewFileDialog()
	d.SetTitle("Choose a folder of voice samples")
	d.SetInitialFolder(gio.NewFileForPath(a.audDir))
	d.SelectFolder(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SelectFolderFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		dir := f.Path()
		a.setStatus("adding samples from " + filepath.Base(dir) + "…")
		go func() {
			r, err := a.importVoiceDir(dir)
			glib.IdleAdd(func() {
				if err != nil {
					a.logf("add folder: %v", err)
					a.setStatus(err.Error())
					return
				}
				// keep speaking in the voice we were already speaking in
				vp.reload(a.voiceID(), false)
				a.setStatus(r.summary())
			})
		}()
	})
}

// addVoiceDialog picks a recording anywhere on disk and installs it. The
// conversion runs off the GUI thread, since it is ffmpeg.
func (vp *voicePicker) addVoiceDialog() {
	a := vp.a
	d := gtk.NewFileDialog()
	d.SetTitle("Choose a voice sample")
	d.SetInitialFolder(gio.NewFileForPath(a.audDir))
	filt := gtk.NewFileFilter()
	filt.SetName("Audio and video")
	for e := range mediaExt {
		filt.AddSuffix(strings.TrimPrefix(e, "."))
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(filt.Object)
	d.SetFilters(filters)
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.OpenFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		src := f.Path()
		a.setStatus("adding " + filepath.Base(src) + "…")
		go func() {
			v, err := a.importVoice(src)
			glib.IdleAdd(func() {
				if err != nil {
					a.logf("add voice: %v", err)
					a.setStatus("could not add that sample — see log")
					return
				}
				vp.reload(v.id, true)
			})
		}()
	})
}

// syncSelection points the list at whatever the output folder says the voice
// is. The guard matters: without it, showing the stored voice would count as
// choosing it, and choosing "own" deletes voice_ref.wav -- so every start, and
// every switch of output folder, would quietly throw the reference away.
func (vp *voicePicker) syncSelection() {
	vp.syncing = true
	defer func() { vp.syncing = false }()
	if vp.pitch != nil {
		vp.pitch.SetValue(vp.a.pitchST())
	}
	want := vp.a.voiceID()
	for i, v := range vp.voices {
		if v.id == want {
			if r := vp.list.RowAtIndex(i); r != nil {
				vp.list.SelectRow(r)
			}
			vp.showCurrent(v)
			return
		}
	}
	// the voice this project speaks in is not on offer: say so rather than
	// silently speaking in something else. Which of the two ways that happened
	// decides where to go and fix it.
	if n := narratorSlot(want); n > 0 {
		vp.cur.SetText(fmt.Sprintf("Narrator %d is not tagged on the Inputs step — tag a recording, or pick another voice.", n))
		return
	}
	vp.cur.SetText(fmt.Sprintf("Voice %q is no longer in %s — pick another.", want, vp.a.voicesDir()))
}

func (vp *voicePicker) showCurrent(v voiceOpt) {
	if n := narratorSlot(v.id); n > 0 {
		who := fmt.Sprintf("narrator %d", n)
		if f := vp.a.narratorFile(n); f != "" {
			vp.cur.SetText("Voice: " + who + " — the dominant speaker in " + f + ".")
			return
		}
		vp.cur.SetText("Voice: " + who + " — nothing is tagged with that slot on the Inputs step.")
		return
	}
	vp.cur.SetText("Voice: " + v.name)
}

func (vp *voicePicker) current() (voiceOpt, bool) {
	row := vp.list.SelectedRow()
	if row == nil {
		return voiceOpt{}, false
	}
	i := row.Index()
	if i < 0 || i >= len(vp.voices) {
		return voiceOpt{}, false
	}
	return vp.voices[i], true
}

// choose installs the voice off the GUI thread -- it shells out to ffmpeg -- and
// reports either way, since a voice that silently failed to install would only
// show up as the wrong speaker in the finished video.
func (vp *voicePicker) choose(i int) {
	if i < 0 || i >= len(vp.voices) {
		return
	}
	v := vp.voices[i]
	a := vp.a
	go func() {
		if err := a.setVoice(v); err != nil {
			a.logfIdle("voice: %v", err)
			glib.IdleAdd(func() { a.setStatus("could not install that voice — see log") })
			return
		}
		glib.IdleAdd(func() {
			vp.showCurrent(v)
			if n := narratorSlot(v.id); n > 0 {
				who := fmt.Sprintf("narrator %d's", n)
				a.setStatus("voice: " + who + " — it is re-cut from the recording on the next line spoken")
				return
			}
			a.setStatus(fmt.Sprintf("voice: %s — ▶ beside the sample plays it", v.name))
		})
	}()
}

// applyPitch commits the slider off the GUI thread -- it reruns ffmpeg over the
// reference -- and says what it means, since the change is only audible after
// the next synthesis.
func (vp *voicePicker) applyPitch(st float64) {
	a := vp.a
	go func() {
		if err := a.setPitchST(st); err != nil {
			a.logfIdle("pitch: %v", err)
			glib.IdleAdd(func() { a.setStatus("could not shift the reference — see log") })
			return
		}
		glib.IdleAdd(func() {
			if st == 0 {
				a.setStatus("voice as recorded — ▶ beside the sample plays it")
				return
			}
			a.setStatus(fmt.Sprintf("voice shifted %+.1f semitones — ▶ beside the sample plays it", st))
		})
	}()
}

// The sample's own two buttons drive it through these. The run bar does not:
// auditioning a voice must not take ▶ away from the step's own run, which is what
// the page is for (pageTransport in pipeline.go).
func (vp *voicePicker) playing() bool { return vp.player != nil && vp.player.Playing() }
func (vp *voicePicker) cued() bool    { return vp.player != nil && vp.player.Cued() }

func (vp *voicePicker) toggle() {
	if vp.player != nil {
		vp.player.Toggle()
	}
}

func (vp *voicePicker) stop() {
	if vp.player != nil {
		vp.player.Stop()
	}
}

// playClicked is the button beside the sample: speak it, then pause and resume
// that same sample. A second click on a ▶ that is already sounding meant
// "synthesize and start over" before, which on a cold server is a ten second
// wait for a sentence that was already playing.
func (vp *voicePicker) playClicked() {
	if vp.playing() || vp.cued() {
		vp.toggle()
		// this branch used to be the one silent button on the page: a click on
		// a sounding ⏸ changed the icon and said nothing, which is
		// indistinguishable from a click that did not register
		if vp.playing() {
			vp.a.setStatus("sample playing")
		} else {
			vp.a.setStatus("sample paused — ▶ resumes, ⏹ starts over")
		}
		vp.a.updateRunControls()
		return
	}
	vp.playSample()
}

// stopClicked ends the sample. Stopping forgets the file as well as the
// position, so the next ▶ speaks the sample text as it reads NOW -- which is
// the way to get out of a sample you have since edited, or a voice you have
// since changed your mind about.
func (vp *voicePicker) stopClicked() {
	vp.stop()
	vp.a.setStatus("sample stopped")
	vp.a.updateRunControls()
}

// playSample speaks the sample text in the selected voice. The first one after
// a cold start also loads the model, which is why the button says what it is
// doing rather than just going quiet for ten seconds.
func (vp *voicePicker) playSample() {
	a := vp.a
	if vp.busy {
		a.setStatus("still synthesizing the last sample…")
		return
	}
	v, ok := vp.current()
	if !ok {
		a.setStatus("pick a voice first")
		return
	}
	text := strings.TrimSpace(vp.sample.Text())
	if text == "" {
		a.setStatus("type a sample sentence to hear the voice")
		return
	}
	vp.busy = true
	a.setStatus("synthesizing the sample…")
	// The log is the only account of this that survives the next click, and a
	// sample is the one thing on the page with no output file to inspect
	// afterwards -- so it says which voice, at what pitch, in what words,
	// before anything can go wrong.
	a.logf(">>> sample: %s at %+.1f semitones — %q", v.name, vp.pitch.Value(), text)
	a.snapSources() // ensureVoiceRef reads the narrator's recording
	go func() {
		// keyed on the pitch too, or the slider would appear to do nothing:
		// the first sample would answer for every setting after it
		out := a.sampleWav(a.voiceKey(), text)
		cached := exists(out)
		t0 := time.Now()
		var err error
		if !cached {
			// the sample is keyed by voice and words, so seed it the same way:
			// replaying a sample must be the take you just heard, not a new one
			err = a.speak(text, "", ttsSeed(out), out)
		}
		took := time.Since(t0)
		glib.IdleAdd(func() {
			vp.busy = false
			if err != nil {
				a.logf("sample FAILED: %v", err)
				a.setStatus("could not speak the sample — see log")
				return
			}
			// what actually came back. A server that answers with nothing, or
			// with a header and no audio, is the failure this page cannot show
			// you: the player takes the file, reports itself playing, and the
			// room stays quiet.
			var size int64
			if fi, e := os.Stat(out); e == nil {
				size = fi.Size()
			}
			if size < 1024 {
				a.logf("!!! sample: %s is %d bytes — no audio came back", filepath.Base(out), size)
				a.setStatus("the sample came back empty — see log")
				return
			}
			if cached {
				a.logf("    sample: %s (%d kB, spoken earlier)", filepath.Base(out), size>>10)
			} else {
				a.logf("    sample: %s (%d kB, spoken in %s)", filepath.Base(out), size>>10, took.Round(time.Second/10))
			}
			if vp.player == nil {
				// no GStreamer, so nothing will play and nothing will say why
				a.logf("!!! sample: there is no player — %s was written but cannot be heard", out)
				a.setStatus("no player available — see log")
				return
			}
			vp.player.PlaySegment(out, 0, -1, true)
			a.setStatus(fmt.Sprintf("sample in %s", v.name))
		})
	}()
}
