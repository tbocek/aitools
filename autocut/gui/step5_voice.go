package main

// The voice every narrated line is spoken in, on a step of its own because
// ▶ here plays a sample instead of running anything, which is not what the run
// bar's play button means anywhere else. IndexTTS2 keeps timbre and emotion apart --
// the speaker comes from a reference wav, the emotion from each line's own
// text -- so picking a voice is exactly picking which wav to clone, and the
// narrate step's per-line emotion goes on working unchanged.
//
// Two sources: the recording's own dominant speaker, cut from step 1's
// diarization (the pipeline's default), or a wav in the voices folder -- the
// CC0 references that ship beside audio.cpp's models, plus anything added with
// "Add sample…", which converts and copies it in. The list shows file names
// because that is what they are: a row can be opened, replaced or deleted in
// the folder the button beside it opens. The pick is installed as step5/voice_ref.wav
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

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
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
	cur     *gtk.Label
	ownLbl  *gtk.Label // the "my own voice" row, renamed when step 1's pick changes
	pitch   *gtk.Scale // semitones on the reference: a different speaker
	voices  []voiceOpt
	player  *Player
	busy    bool // one synthesis at a time; the button says so
	syncing bool // showing the stored voice, which is not a fresh choice
	drag    int  // bumped per slider move; only the last one rebuilds the reference
}

// voicesDir is where audio.cpp keeps its CC0 reference wavs -- the same folder
// the WebUI lists in its "Built-in voice" dropdown, under the configured models
// root.
func (a *App) voicesDir() string { return filepath.Join(a.readConf().Models, "voices") }

func (a *App) listVoices() []voiceOpt {
	out := []voiceOpt{{id: ownVoice, name: a.ownVoiceName()}}
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

// ownVoiceFile is the recording "my own voice" would actually be cut from: the
// first checked voice recording on step 1, or "" before anything is checked.
// Which file that is decides who speaks the narration, so the row names it
// rather than leaving it to be inferred.
func (a *App) ownVoiceFile() string {
	if a.audList != nil {
		if sel := a.audList.selected(); len(sel) > 0 {
			return filepath.Base(sel[0])
		}
	}
	return ""
}

func (a *App) ownVoiceName() string {
	if f := a.ownVoiceFile(); f != "" {
		return "My own voice — " + f
	}
	return "My own voice — cut from the recording"
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
		p.OnState = a.updateRunControls
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

	// right: what the choice means, and the way to hear it
	head := gtk.NewLabel("Every narrated line is spoken by cloning one of these " +
		"recordings. Pitch moves the recording before it is cloned, so the model " +
		"hears a different speaker — the narration is then spoken that way rather " +
		"than shifted afterwards.")
	head.SetWrap(true)
	head.SetXAlign(0)
	head.AddCSSClass("dim-label")

	vp.cur = gtk.NewLabel("")
	vp.cur.SetXAlign(0)
	vp.cur.SetWrap(true)

	vp.sample = gtk.NewEntry()
	vp.sample.SetText(sampleDefault)
	vp.sample.SetHExpand(true)
	vp.sample.SetTooltipText("Enter — or ▶ in the run bar — speaks this in the selected voice")
	vp.sample.ConnectActivate(func() { vp.playSample() })

	// no transport of its own: ▶ in the run bar runs the step you are looking
	// at, and on this step that is the sample. A second pair of buttons meant
	// two of everything, one of which is easy to overlook.
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(vp.sample)

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

	note := gtk.NewLabel("Switching voices keeps what you already synthesized — " +
		"each voice and pitch has its own cache.")
	note.SetWrap(true)
	note.SetXAlign(0)
	note.AddCSSClass("dim-label")

	right := gtk.NewBox(gtk.OrientationVertical, 8)
	right.SetMarginStart(12)
	right.SetMarginEnd(12)
	right.SetMarginTop(10)
	right.SetMarginBottom(10)
	right.SetHExpand(true)
	right.Append(head)
	right.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
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

// fillRows builds one row per voice, remembering the "my own voice" label so
// step 1 can rename it when the checked recording changes.
func (vp *voicePicker) fillRows() {
	for _, v := range vp.voices {
		l := gtk.NewLabel(v.name)
		l.SetXAlign(0)
		l.SetMarginTop(5)
		l.SetMarginBottom(5)
		l.SetMarginStart(8)
		l.SetEllipsize(pango.EllipsizeMiddle) // so the divider can be dragged in
		l.SetTooltipText(v.name)
		if v.id == ownVoice {
			vp.ownLbl = l
		}
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
	vp.ownLbl = nil
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

// refreshOwn renames the first row when step 1's checked recording changes --
// that recording is what "my own voice" clones, so the row has to keep up.
func (vp *voicePicker) refreshOwn() {
	if len(vp.voices) == 0 || vp.voices[0].id != ownVoice {
		return
	}
	vp.voices[0].name = vp.a.ownVoiceName()
	if vp.ownLbl != nil {
		vp.ownLbl.SetText(vp.voices[0].name)
		vp.ownLbl.SetTooltipText(vp.voices[0].name)
	}
	if vp.a.voiceID() == ownVoice {
		vp.showCurrent(vp.voices[0])
	}
}

// addVoiceDirDialog installs every recording in a folder -- the shape samples
// usually come in, and thirty trips through a file chooser is not a workflow.
// Same import and the same thread rule as addVoiceDialog.
func (vp *voicePicker) addVoiceDirDialog() {
	a := vp.a
	d := gtk.NewFileDialog()
	d.SetTitle("Choose a folder of voice samples")
	d.SetInitialFolder(gio.NewFileForPath(a.inDir))
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
	d.SetInitialFolder(gio.NewFileForPath(a.inDir))
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
	// a voice.txt naming a wav that is no longer on disk: say so rather than
	// silently speaking in something else
	vp.cur.SetText(fmt.Sprintf("Voice %q is no longer in %s — pick another.", want, vp.a.voicesDir()))
}

func (vp *voicePicker) showCurrent(v voiceOpt) {
	if v.id == ownVoice {
		if f := vp.a.ownVoiceFile(); f != "" {
			vp.cur.SetText("Voice: your own — the dominant speaker in " + f + ".")
			return
		}
		vp.cur.SetText("Voice: your own, cut from the recording's dominant speaker.")
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
			if v.id == ownVoice {
				a.setStatus("voice: your own — it is re-cut from the recording on the next line spoken")
				return
			}
			a.setStatus(fmt.Sprintf("voice: %s — press ▶ to hear it", v.name))
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
				a.setStatus("voice as recorded — press ▶ to hear it")
				return
			}
			a.setStatus(fmt.Sprintf("voice shifted %+.1f semitones — press ▶ to hear it", st))
		})
	}()
}

// The run bar drives the sample through these: ▶ plays it, then pauses and
// resumes it, ⏹ ends it. See transport in pipeline.go.
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
			if err != nil {
				a.logf("sample: %v", err)
				a.setStatus("could not speak the sample — see log")
				return
			}
			if vp.player != nil {
				vp.player.PlaySegment(out, 0, -1, true)
			}
			a.setStatus(fmt.Sprintf("sample in %s", v.name))
		})
	}()
}
