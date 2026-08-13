package main

// Step 5: the voice every narrated line is spoken in. IndexTTS2 keeps timbre
// and emotion apart -- the speaker comes from a reference wav, the emotion from
// each line's own text -- so picking a voice is exactly picking which wav to
// clone, and step 6's per-line emotion goes on working unchanged.
//
// Two sources: the recording's own dominant speaker, cut from step 1's
// diarization (the pipeline's default), or any of the CC0 reference wavs that
// ship beside audio.cpp's models. The pick is installed as step5/voice_ref.wav
// because that is the file the TTS server is handed, and the output folder is
// the one mounted into the server at its own absolute path -- the voices folder
// sits at a different path inside the container, so aiming the server straight
// at it would not resolve.
//
// The pitch slider post-processes the reference before the model ever sees it,
// which is the difference between a new speaker and a familiar one transposed:
// IndexTTS2 takes its timbre from this file, so a shifted reference is cloned
// as a shifted voice, and step 6 narrates in it without knowing anything
// changed. The unshifted recording is kept beside it as voice_ref_base.wav, so
// moving the slider costs one ffmpeg pass rather than re-cutting the
// diarization.
//
// Switching is free: the synthesis cache is keyed on the voice and its pitch as
// well as the text, so lines spoken in an earlier voice are still there when
// you switch back.

import (
	"crypto/sha1"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// ownVoice is the id of "clone me", the default the pipeline had before this
// step existed.
const ownVoice = "own"

const sampleDefault = "This is the voice the narration will be spoken in. " +
	"It should stay clear and easy to follow for a couple of minutes."

// pitchRange caps the reference shift at half an octave either way. Past that
// the clone stops sounding like a person rather than like a different one.
const pitchRange = 6.0

type voiceOpt struct {
	id   string // "own", or the wav's base name; also part of the cache key
	name string // what the row reads
	path string // source wav; empty for "own"
}

type voicePicker struct {
	a       *App
	list    *gtk.ListBox
	filter  *gtk.SearchEntry
	sample  *gtk.Entry
	play    *gtk.Button
	cur     *gtk.Label
	pitch   *gtk.Scale // semitones on the reference: a different speaker
	out     *gtk.Scale // rubberband over the finished audio: the same one, moved
	voices  []voiceOpt
	player  *Player
	busy    bool // one synthesis at a time; the button says so
	syncing bool // showing the stored voice, which is not a fresh choice
	drag    int  // bumped per slider move; only the last one rebuilds the reference
}

// voicesDir is where audio.cpp keeps its CC0 reference wavs -- the same folder
// the WebUI lists in its "Built-in voice" dropdown.
func voicesDir() string { return filepath.Join(modelsDir, "voices") }

func listVoices() []voiceOpt {
	out := []voiceOpt{{id: ownVoice, name: "My own voice — cut from the recording"}}
	ents, _ := os.ReadDir(voicesDir())
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, voiceOpt{
			id:   strings.TrimSuffix(n, filepath.Ext(n)),
			name: prettyVoice(n),
			path: filepath.Join(voicesDir(), n),
		})
	}
	return out
}

// prettyVoice turns cv-gb-female-30s.wav into "gb · female · 30s", which scans
// better than a file name does in a list of eighty.
func prettyVoice(file string) string {
	s := strings.TrimSuffix(file, filepath.Ext(file))
	return strings.ReplaceAll(strings.TrimPrefix(s, "cv-"), "-", " · ")
}

// ---- the chosen voice -------------------------------------------------------

// voiceID is the voice this project speaks in, remembered in the output folder
// so it survives a restart, and mixed into the synthesis cache key so switching
// voices does not throw away work.
func (a *App) voiceID() string {
	if a.voiceSel != "" {
		return a.voiceSel
	}
	a.voiceSel = ownVoice
	if b, err := os.ReadFile(filepath.Join(a.outDir, "step5", "voice.txt")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			a.voiceSel = s
		}
	}
	return a.voiceSel
}

// refBase is the chosen recording as it was picked, refPath the shifted copy
// the server is handed. Keeping them apart is what makes the slider cheap: the
// base is cut or converted once, the shift is redone from it.
func (a *App) refBase() string { return filepath.Join(a.outDir, "step5", "voice_ref_base.wav") }
func (a *App) refPath() string { return filepath.Join(a.outDir, "step5", "voice_ref.wav") }

// setVoice installs the reference the model clones from. "own" just removes the
// files, so ensureVoiceRef cuts a fresh one from the diarization the next time
// anything speaks; anything else is converted to the mono PCM the model wants.
// Safe off the GUI thread -- it only runs ffmpeg and writes files.
func (a *App) setVoice(v voiceOpt) error {
	dir := filepath.Join(a.outDir, "step5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// the shifted copy belongs to the old voice; drop it either way and let
	// ensureVoiceRef rebuild it from whatever base we end up with
	os.Remove(a.refPath())
	if v.id == ownVoice {
		os.Remove(a.refBase())
	} else if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", v.path,
		"-ac", "1", "-c:a", "pcm_s16le", a.refBase()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "voice.txt"), []byte(v.id), 0o644); err != nil {
		return err
	}
	a.voiceSel = v.id
	return nil
}

// ---- the reference pitch ----------------------------------------------------

// pitchST is how far the reference is moved, in semitones, remembered in the
// output folder next to the voice it belongs to.
func (a *App) pitchST() float64 {
	if a.pitchRead {
		return a.pitchSel
	}
	a.pitchRead, a.pitchSel = true, 0
	if b, err := os.ReadFile(filepath.Join(a.outDir, "step5", "pitch.txt")); err == nil {
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
	dir := filepath.Join(a.outDir, "step5")
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
	return filepath.Join(a.outDir, "step5", "samples", fmt.Sprintf("%s_%x.wav", id, h[:6]))
}

// ---- the page ---------------------------------------------------------------

func (a *App) buildStep5Voice() gtk.Widgetter {
	vp := &voicePicker{a: a}
	a.voice5 = vp
	if p, err := NewPlayer(); err == nil {
		vp.player = p
	}
	vp.voices = listVoices()

	vp.list = gtk.NewListBox()
	vp.list.SetSelectionMode(gtk.SelectionSingle)
	for _, v := range vp.voices {
		l := gtk.NewLabel(v.name)
		l.SetXAlign(0)
		l.SetMarginTop(5)
		l.SetMarginBottom(5)
		l.SetMarginStart(8)
		vp.list.Append(l)
	}
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

	leftBox := gtk.NewBox(gtk.OrientationVertical, 6)
	leftBox.SetMarginStart(10)
	leftBox.SetMarginTop(10)
	leftBox.SetMarginBottom(10)
	leftBox.SetSizeRequest(300, -1)
	leftBox.Append(vp.filter)
	leftBox.Append(scroll)

	// right: what the choice means, and the way to hear it
	head := gtk.NewLabel("Every line in step 6 is spoken by cloning one reference " +
		"recording. Pick it here and listen before you narrate — the emotion of " +
		"each line stays per-line either way. Reference pitch moves the recording " +
		"before it is cloned, which makes a different speaker; output pitch moves " +
		"the finished narration, which is that speaker transposed.")
	head.SetWrap(true)
	head.SetXAlign(0)
	head.AddCSSClass("dim-label")

	vp.cur = gtk.NewLabel("")
	vp.cur.SetXAlign(0)
	vp.cur.SetWrap(true)

	vp.sample = gtk.NewEntry()
	vp.sample.SetText(sampleDefault)
	vp.sample.SetHExpand(true)
	vp.sample.ConnectActivate(func() { vp.playSample() })

	vp.play = gtk.NewButtonWithLabel("▶ Play sample")
	vp.play.SetTooltipText("Speak the sample text in the selected voice")
	vp.play.ConnectClicked(func() { vp.playSample() })

	stopBtn := gtk.NewButtonWithLabel("⏹")
	stopBtn.SetTooltipText("Stop the sample")
	stopBtn.ConnectClicked(func() {
		if vp.player != nil {
			vp.player.Stop()
		}
	})

	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(vp.sample)
	row.Append(vp.play)
	row.Append(stopBtn)

	// the reference is shifted, not the narration: this changes who is speaking
	// in step 6, so it belongs next to the choice of voice rather than next to
	// the render
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
	pitchRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	pitchRow.Append(gtk.NewLabel("reference pitch:"))
	pitchRow.Append(vp.pitch)

	// the second knob moves the finished audio instead of the reference: the
	// same speaker, nudged, and free -- no resynthesis. It lives here rather
	// than on the narrate page because both answer "how does this voice sound".
	vp.out = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0.8, 1.2, 0.02)
	vp.out.SetValue(1.0)
	vp.out.SetDrawValue(true)
	vp.out.SetHExpand(true)
	vp.out.AddMark(1.0, gtk.PosBottom, "1.0")
	vp.out.SetTooltipText("Nudge the synthesized narration at playback and render — " +
		"the same speaker, transposed, with nothing to re-speak")
	// synthesis and rendering run off the GUI thread and must not read the
	// widget; they read this copy, which only the GUI thread writes
	a.pitchNow.Store(math.Float64bits(1.0))
	vp.out.ConnectValueChanged(func() { a.pitchNow.Store(math.Float64bits(vp.out.Value())) })
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	outRow.Append(gtk.NewLabel("output pitch:"))
	outRow.Append(vp.out)

	note := gtk.NewLabel("Switching voices keeps what you already synthesized: " +
		"each voice and reference pitch has its own cache, so going back is " +
		"instant. Output pitch costs nothing either way — it is applied after.")
	note.SetWrap(true)
	note.SetXAlign(0)
	note.AddCSSClass("dim-label")

	right := gtk.NewBox(gtk.OrientationVertical, 10)
	right.SetMarginStart(12)
	right.SetMarginEnd(12)
	right.SetMarginTop(10)
	right.SetMarginBottom(10)
	right.SetHExpand(true)
	right.Append(head)
	right.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	right.Append(vp.cur)
	right.Append(gtk.NewLabel("")) // a little air before the controls
	right.Append(pitchRow)
	right.Append(outRow)
	right.Append(row)
	right.Append(note)

	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.Append(leftBox)
	box.Append(gtk.NewSeparator(gtk.OrientationVertical))
	box.Append(right)

	vp.syncSelection()
	return box
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
	// a voice.txt naming a wav that is no longer on disk: say so rather than
	// silently speaking in something else
	vp.cur.SetText(fmt.Sprintf("Voice %q is no longer in %s — pick another.", want, voicesDir()))
}

func (vp *voicePicker) showCurrent(v voiceOpt) {
	if v.id == ownVoice {
		vp.cur.SetText("Voice: your own, cut from the recording's dominant speaker.")
		return
	}
	vp.cur.SetText(fmt.Sprintf("Voice: %s   (%s)", v.name, filepath.Base(v.path)))
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
// show up as the wrong speaker in step 6.
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
			if v.id == ownVoice {
				a.setStatus("voice: your own — it is re-cut from the recording on the next line spoken")
				return
			}
			a.setStatus(fmt.Sprintf("voice: %s — ▶ Play sample to hear it", v.name))
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
				a.setStatus("voice as recorded — ▶ Play sample to hear it")
				return
			}
			a.setStatus(fmt.Sprintf("voice shifted %+.1f semitones — ▶ Play sample to hear it", st))
		})
	}()
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
	vp.play.SetSensitive(false)
	vp.play.SetLabel("… speaking")
	a.setStatus("synthesizing the sample…")
	a.snapSources() // ensureVoiceRef reads the checked recording for "own"
	go func() {
		// keyed on the pitch too, or the slider would appear to do nothing:
		// the first sample would answer for every setting after it
		out := a.sampleWav(a.voiceKey(), text)
		var err error
		if !exists(out) {
			err = a.speak(text, "", out)
		}
		glib.IdleAdd(func() {
			vp.busy = false
			vp.play.SetSensitive(true)
			vp.play.SetLabel("▶ Play sample")
			if err != nil {
				a.logf("sample: %v", err)
				a.setStatus("could not speak the sample — see log")
				return
			}
			if vp.player != nil {
				vp.player.PlaySegment(a.pitchedWav(out), 0, -1, true)
			}
			a.setStatus(fmt.Sprintf("sample in %s", v.name))
		})
	}()
}
