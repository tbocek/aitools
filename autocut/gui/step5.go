package main

// Step 5: Produce. Cuts every clip from ITS OWN source recording, lays the
// narration over ducked game audio, joins, normalizes loudness and writes the
// upload -- plus subtitles, burned in, muxed as a track or as a sidecar .srt.
//
// A clip grows (up to maxExtend) when its narration needs more room; if the
// line still does not fit, the narration is sped up to at most maxTempo. Both
// are logged, never silent.
//
// Encoding happens ONCE, per clip: the join is a stream copy and the loudness
// pass copies the video. Burned subtitles therefore go into the clip encode,
// not into a second full-video pass.
//
// step5/clips/c000.<ext>   per-clip encodes
// step5/final.srt          subtitles on the produced timeline
// <output file>            the upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	narrLead  = 0.3  // the earliest a line may start into its clip (writer's "at" 0 lands here)
	maxExtend = 4.0  // seconds a clip may grow to fit its line
	maxTempo  = 1.25 // ... and how much the line may be sped up after that
	loudFlt   = "loudnorm=I=-14:TP=-1.5:LRA=11"
	// every clip is forced to the same audio shape, or the concat copy
	// produces a file whose second half is silent on half the players
	audFmt = "aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo"
)

type prodSettings struct {
	Container string  `json:"container"`
	Codec     string  `json:"codec"`
	CRF       int     `json:"crf"`
	Preset    string  `json:"preset"`
	Height    int     `json:"height"` // 0 = keep source
	FPS       float64 `json:"fps"`    // 0 = keep source
	VFR       bool    `json:"vfr,omitempty"`
	AudioKbps int     `json:"audio_kbps"`
	GameVol   float64 `json:"game_vol"`
	Subs      string  `json:"subs"` // burn | mux | sidecar | none
	OutFile   string  `json:"out_file"`
}

var (
	prodContainers = []string{"mp4", "mkv", "webm"}
	prodCodecs     = []string{"h264", "h265", "vp9"}
	prodPresets    = []string{"ultrafast", "veryfast", "fast", "medium", "slow", "veryslow"}
	prodHeights    = []string{"source", "2160", "1440", "1080", "720", "480"}
	prodFPS        = []string{"source", "60", "30", "24"}
	prodABR        = []string{"128", "192", "256", "320"}
	prodSubsLbl    = []string{"burned into the picture", "separate track in the file",
		"sidecar .srt file only", "none"}
	prodSubsKey = []string{"burn", "mux", "sidecar", "none"}
)

type producer struct {
	a *App

	container, codec, preset, height, fps, abr, subs *gtk.DropDown
	vfr                                              *gtk.CheckButton
	crf, gvol                                        *gtk.Scale
	outLbl                                           *gtk.Label
	outFile                                          string
	outAuto                                          bool       // still the default -- follows the output folder
	inputs, info, out                                *gtk.Label // the two rows every step has, and the encode summary between them
	player                                           *Player
	started                                          bool // the result is being watched, so the run bar is its transport
	guard                                            bool // suppresses feedback while applying a project
}

// ---- settings ---------------------------------------------------------------

func pickText(d *gtk.DropDown, list []string) string {
	i := int(d.Selected())
	if i < 0 || i >= len(list) {
		i = 0
	}
	return list[i]
}

func setPick(d *gtk.DropDown, list []string, want string) {
	for i, s := range list {
		if s == want {
			d.SetSelected(uint(i))
			return
		}
	}
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (a *App) prodSettings() prodSettings {
	p := a.prod
	if p == nil {
		return prodSettings{Container: "mp4", Codec: "h264", CRF: 24, Preset: "veryslow",
			FPS: 30, AudioKbps: 128, GameVol: 0.22, Subs: "sidecar"}
	}
	st := prodSettings{
		Container: pickText(p.container, prodContainers),
		Codec:     pickText(p.codec, prodCodecs),
		CRF:       int(math.Round(p.crf.Value())),
		Preset:    pickText(p.preset, prodPresets),
		Height:    atoiOr(pickText(p.height, prodHeights), 0),
		FPS:       float64(atoiOr(pickText(p.fps, prodFPS), 0)),
		VFR:       p.vfr.Active(),
		AudioKbps: atoiOr(pickText(p.abr, prodABR), 128),
		GameVol:   p.gvol.Value(),
		Subs:      prodSubsKey[int(p.subs.Selected())],
		OutFile:   p.outFile,
	}
	// webm carries neither h264 nor aac; silently producing an unplayable
	// file would be worse than overriding the pick and saying so
	if st.Container == "webm" && st.Codec != "vp9" {
		st.Codec = "vp9"
	}
	return st
}

func (a *App) applyProdSettings(st *prodSettings) {
	p := a.prod
	if p == nil || st == nil {
		return
	}
	p.guard = true
	setPick(p.container, prodContainers, st.Container)
	setPick(p.codec, prodCodecs, st.Codec)
	setPick(p.preset, prodPresets, st.Preset)
	setPick(p.height, prodHeights, fmtOpt(float64(st.Height)))
	setPick(p.fps, prodFPS, fmtOpt(st.FPS))
	p.vfr.SetActive(st.VFR)
	setPick(p.abr, prodABR, strconv.Itoa(st.AudioKbps))
	for i, k := range prodSubsKey {
		if k == st.Subs {
			p.subs.SetSelected(uint(i))
		}
	}
	if st.CRF > 0 {
		p.crf.SetValue(float64(st.CRF))
	}
	if st.GameVol > 0 {
		p.gvol.SetValue(st.GameVol)
	}
	p.guard = false
	if st.OutFile != "" {
		out := st.OutFile
		if !filepath.IsAbs(out) {
			out = filepath.Join(a.root, out)
		}
		p.setOut(out)
		p.outAuto = false
	}
}

// followOutDir retargets the produced file when the user changes the output
// folder -- but only while they have not named a file of their own.
func (a *App) followOutDir() {
	p := a.prod
	if p == nil || !p.outAuto {
		return
	}
	p.setOut(filepath.Join(a.outDir, filepath.Base(p.outFile)))
	p.outAuto = true
}

// fmtOpt renders a numeric dropdown value, with 0 meaning "source".
func fmtOpt(v float64) string {
	if v <= 0 {
		return "source"
	}
	return strconv.Itoa(int(v))
}

func (p *producer) setOut(path string) {
	p.outFile = path
	p.outLbl.SetText(path)
	p.outLbl.SetTooltipText(path)
}

// syncExt keeps the output filename's extension on the chosen container.
func (p *producer) syncExt() {
	if p.guard || p.outFile == "" {
		return
	}
	want := "." + pickText(p.container, prodContainers)
	if filepath.Ext(p.outFile) != want {
		p.setOut(strings.TrimSuffix(p.outFile, filepath.Ext(p.outFile)) + want)
	}
}

// The run bar drives the result through these; see transport in pipeline.go.
// ▶ means produce until the result is actually being watched, because that is
// what this page is for -- watching it back is the aside.
func (p *producer) playing() bool { return p.player != nil && p.player.Playing() }
func (p *producer) cued() bool    { return p.player != nil && p.player.Cued() }

func (p *producer) toggle() {
	if p.player != nil {
		p.player.Toggle()
		p.started = p.started || p.player.Playing()
		p.a.updateRunControls()
	}
}

func (p *producer) stop() {
	if p.player != nil {
		p.player.Stop()
	}
	p.started = false // ⏹ hands ▶ back to producing
}

// ---- page -------------------------------------------------------------------

func (a *App) buildStep5() gtk.Widgetter {
	p := &producer{a: a}
	a.prod = p
	p.player = a.player

	// no paragraph at the top: what this step does is in the ⓘ in the header bar
	// (steps[].help), which the settings below it can now have the space of
	grid := gtk.NewGrid()
	grid.SetColumnSpacing(10)
	grid.SetRowSpacing(6)
	grid.SetColumnHomogeneous(false)
	at := func(col, row int, label string, w gtk.Widgetter) {
		l := gtk.NewLabel(label)
		l.SetXAlign(1)
		l.AddCSSClass("dim-label")
		grid.Attach(l, col*2, row, 1, 1)
		grid.Attach(w, col*2+1, row, 1, 1)
	}
	dd := func(list []string, sel int, tip string) *gtk.DropDown {
		d := gtk.NewDropDownFromStrings(list)
		d.SetSelected(uint(sel))
		d.SetTooltipText(tip)
		d.Connect("notify::selected", func() {
			if !p.guard {
				a.updateStep5Info()
			}
		})
		return d
	}

	p.container = dd(prodContainers, 0, "mp4 plays everywhere; mkv keeps subtitle tracks best; webm forces VP9 + Opus")
	p.container.Connect("notify::selected", func() { p.syncExt() })
	p.codec = dd(prodCodecs, 0, "h264 is the safe upload; h265 is smaller but slower; vp9 is for webm")
	p.preset = dd(prodPresets, 5, "how long the encoder may think — slower means smaller at the same quality")
	p.height = dd(prodHeights, 3, "output height; the width follows the source aspect")
	p.fps = dd(prodFPS, 2, "output frame rate — a ceiling rather than a target with VFR on")
	p.abr = dd(prodABR, 0, "audio bitrate in kbit/s")
	p.subs = dd(prodSubsLbl, 2, "what to do with the narration subtitles")

	// VFR makes the rate above a ceiling. Capture from a headset is variable by
	// nature -- it renders what it can, and the rate above is the peak it
	// reaches, not the rate it holds. Forced up to a constant rate that becomes
	// duplicated frames, which cost bitrate and buy nothing.
	p.vfr = gtk.NewCheckButtonWithLabel("Peak frame rate (VFR)")
	p.vfr.SetTooltipText("Treat the frame rate above as a ceiling: footage faster than it is " +
		"dropped down to it, footage slower keeps its own rate instead of having frames " +
		"duplicated. Off, every clip is resampled to exactly that rate.")
	p.vfr.ConnectToggled(func() {
		if !p.guard {
			a.updateStep5Info()
		}
	})

	p.crf = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 14, 34, 1)
	p.crf.SetValue(24)
	p.crf.SetDrawValue(true)
	p.crf.SetSizeRequest(200, -1)
	p.crf.SetTooltipText("quality: lower is better and bigger (18–24 is the usual range)")
	p.crf.AddMark(24, gtk.PosBottom, "24")
	p.crf.ConnectValueChanged(func() { a.updateStep5Info() })

	p.gvol = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 1, 0.02)
	p.gvol.SetValue(0.22)
	p.gvol.SetDrawValue(true)
	p.gvol.SetSizeRequest(200, -1)
	p.gvol.SetTooltipText("how loud the original game audio sits under the narration")
	p.gvol.ConnectValueChanged(func() { a.updateStep5Info() }) // it is on the settings line now

	at(0, 0, "Container:", p.container)
	at(0, 1, "Video codec:", p.codec)
	at(0, 2, "Encoder preset:", p.preset)
	at(0, 3, "Quality (CRF):", p.crf)
	at(1, 0, "Resolution:", p.height)
	at(1, 1, "Frame rate:", p.fps)
	at(1, 2, "Audio bitrate:", p.abr)
	at(1, 3, "Game audio under voice:", p.gvol)
	at(0, 4, "Subtitles:", p.subs)
	at(1, 4, "Frame timing:", p.vfr)

	// Where the video is written. This is a setting, not the Outputs line: it
	// says where the file WILL go, and the row at the foot of the page says
	// what is actually there -- which is why it is no longer labelled "Output",
	// one letter from the heading below and meaning something else.
	choose := gtk.NewButtonWithLabel("Choose…")
	choose.ConnectClicked(func() { a.chooseOutFileDialog() })
	p.outLbl = gtk.NewLabel("")
	p.outLbl.SetXAlign(0)
	p.outLbl.SetHExpand(true)
	p.outLbl.SetEllipsize(pango.EllipsizeMiddle)
	p.outLbl.SetSelectable(true)
	destRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	destRow.Append(choose)
	destRow.Append(gtk.NewLabel("Save to:"))
	destRow.Append(p.outLbl)
	p.setOut(filepath.Join(a.outDir, "final.mp4"))
	p.outAuto = true

	// No buttons of its own down here. Rendering is what this page does, so it
	// is what ▶ in the run bar means, and a finished run cues its result into
	// the picture below by itself -- a "Preview result" button beside a second
	// ▶ was two more ways to press the one thing the run bar already does, and
	// the pair of ▶s did not even mean the same thing.
	p.info = gtk.NewLabel("")
	p.info.SetXAlign(0)
	p.info.SetWrap(true)
	p.info.AddCSSClass("dim-label")

	vframe := videoFrame(nil)
	if p.player != nil {
		p.player.Picture.SetVExpand(true)
		p.player.Picture.SetSizeRequest(-1, 320)
		vframe.SetChild(p.player.Picture)
	}
	vframe.SetMarginTop(4)

	// The two rows every other step has, in the two places every other step has
	// them: what this one is working from, at the top, and what it has put on
	// disk, at the bottom right. Cut and Narrate answer the same question in the
	// same words, and this page used to answer neither -- what it had instead
	// was one dim paragraph in the middle that mixed its inputs in with its
	// encoder settings.
	p.inputs = gtk.NewLabel("")
	p.inputs.SetXAlign(0)
	p.inputs.SetHExpand(true)
	p.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(p.inputs)

	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("Open the folder holding the produced file (step5/ beside it holds the per-clip encodes)")
	openOut.ConnectClicked(func() { a.openFolder(filepath.Dir(p.outFile)) })
	p.out = gtk.NewLabel("")
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.SetHAlign(gtk.AlignEnd)
	outRow.SetMarginEnd(12)
	outRow.SetMarginBottom(6)
	outRow.Append(outLbl)
	outRow.Append(openOut)
	outRow.Append(p.out)

	box := gtk.NewBox(gtk.OrientationVertical, 10)
	box.SetMarginTop(10)
	box.SetMarginBottom(8)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.Append(grid)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	box.Append(destRow)
	box.Append(p.info)
	box.Append(vframe)

	a.updateStep5Info() // the three rows say something before anything is clicked

	// only the settings scroll: the two rows are the page's frame, and a frame
	// that slides out from under the reader is the thing they are there to stop
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(box)
	scroll.SetVExpand(true)

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(scroll)
	page.Append(outRow)
	return page
}

func (a *App) chooseOutFileDialog() {
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(filepath.Dir(a.prod.outFile)))
	d.SetInitialName(filepath.Base(a.prod.outFile))
	d.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SaveFinish(res)
		if err != nil || f == nil {
			return
		}
		a.prod.setOut(f.Path())
		a.prod.outAuto = false
		a.prod.syncExt()
	})
}

// updateStep5Info redraws all three lines: what the render reads, what it will
// encode, and what it has written. Everything that changes any of them comes
// through here -- a dropdown, a cut edited next door, a finished run.
func (a *App) updateStep5Info() {
	p := a.prod
	if p == nil {
		return
	}
	p.updateInputs()
	p.updateSettings()
	p.updateOut()
}

// updateInputs is the row Cut and Narrate open with, asking the same question
// of this page: what is actually going into the render? Three things, and not
// one of them is made here -- the clips come from Cut, the lines and their
// recordings from Narrate, and a line whose wav is missing is spoken before any
// video is encoded, which is minutes this row is the only warning of.
func (p *producer) updateInputs() {
	if p == nil || p.inputs == nil {
		return
	}
	a := p.a
	segs := a.produceSegs()
	entries := a.produceEntries()
	total := 0.0
	for _, s := range segs {
		total += s.E - s.S
	}
	line := fmt.Sprintf("%d clip(s) · %s of video", len(segs), mmss(total))
	detail := fmt.Sprintf("step3/cut.json — %d clips, %s of video (the produced file grows a little where the narration needs room)",
		len(segs), mmss(total))
	if len(segs) == 0 {
		line, detail = "no cut yet — build one on the Cut step", ""
	}
	spoken := 0
	for _, e := range entries {
		if exists(a.ttsWav(e)) {
			spoken++
		}
	}
	switch {
	case len(entries) == 0:
		line += " · no narration — the clips would carry only game audio"
	case spoken < len(entries):
		line += fmt.Sprintf(" · %d line(s), %d still to speak", len(entries), len(entries)-spoken)
		detail += fmt.Sprintf("\n\nstep4/narration.json — %d lines, %d already in step4/tts; the other %d are spoken first, before any video is encoded",
			len(entries), spoken, len(entries)-spoken)
	default:
		line += fmt.Sprintf(" · %d line(s), all spoken", len(entries))
		detail += fmt.Sprintf("\n\nstep4/narration.json — %d lines, all of them already in step4/tts", len(entries))
	}
	// the voice is named on Narrate's inputs row too: it is what the cached
	// takes were spoken in, and the thing nothing else on this page says
	if vp := a.voice5; vp != nil && len(entries) > 0 {
		if v, ok := vp.current(); ok {
			line += " · voice: " + v.name
			detail += "\n\nSpoken by " + v.name + " (step4/voice_ref.wav)"
		}
	}
	p.inputs.SetText(line)
	p.inputs.SetTooltipText(strings.TrimSpace(detail))
}

// updateSettings echoes the grid above it in the words the encoder would use --
// the one line on this page that is neither an input nor an output, which is
// why it sits between them and beside the controls it is reporting.
func (p *producer) updateSettings() {
	if p == nil || p.info == nil {
		return
	}
	st := p.a.prodSettings()
	res := "source resolution"
	if st.Height > 0 {
		res = fmt.Sprintf("%dp", st.Height)
	}
	fps := "source fps"
	switch {
	case st.FPS > 0 && st.VFR:
		fps = fmt.Sprintf("up to %g fps", st.FPS)
	case st.FPS > 0:
		fps = fmt.Sprintf("%g fps", st.FPS)
	case st.VFR:
		fps = "source fps, variable"
	}
	p.info.SetText(fmt.Sprintf("%s, %s, %s crf %d · %s · audio %d kbit/s, game at %.0f%% under the voice · subtitles %s",
		res, fps, st.Codec, st.CRF, st.Container, st.AudioKbps, st.GameVol*100,
		prodSubsLbl[subsIndex(st.Subs)]))
}

// updateOut is the line every step ends on. Here it is one file rather than a
// folder: the video is the whole point of the page, so its size and age are
// what "what is on disk" means -- with step5/ named too, because a run that
// stopped half way leaves its finished clips there and nothing else says so.
func (p *producer) updateOut() {
	if p == nil || p.out == nil {
		return
	}
	part := ""
	if s := summarizeOutputs(p.a.produceDir()); s != "nothing yet" {
		part = " · step5/ " + s
	}
	fi, err := os.Stat(p.outFile)
	if err != nil {
		p.out.SetText("nothing produced yet" + part)
		p.out.SetTooltipText("nothing at " + p.outFile)
		return
	}
	p.out.SetText(fmt.Sprintf("%s — %s, %s%s", filepath.Base(p.outFile),
		humanSize(fi.Size()), humanAgo(fi.ModTime()), part))
	p.out.SetTooltipText(p.outFile)
}

// subsIndex is the label for a stored subtitle mode, defaulting to the first
// rather than panicking on a project written by a later version.
func subsIndex(key string) int {
	for i, k := range prodSubsKey {
		if k == key {
			return i
		}
	}
	return 0
}

// ---- inputs -----------------------------------------------------------------

// produceSegs prefers the live editor, so an unsaved tweak still renders.
func (a *App) produceSegs() []cutSeg {
	if a.ed != nil && len(a.ed.segs) > 0 {
		return append([]cutSeg(nil), a.ed.segs...)
	}
	b, err := os.ReadFile(a.cutPath())
	if err != nil {
		return nil
	}
	var c struct{ Segs []cutSeg }
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return c.Segs
}

func (a *App) produceEntries() []narrEntry {
	if a.narr5 != nil {
		a.narr5.pullRows()
		if len(a.narr5.entries) > 0 {
			return append([]narrEntry(nil), a.narr5.entries...)
		}
	}
	b, err := os.ReadFile(a.narrPath())
	if err != nil {
		return nil
	}
	var f struct{ Entries []narrEntry }
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.Entries
}

// sessionVideos places every selected video on the session timeline, using the
// same zero as step 3 (earliest start over ALL sources, video and audio).
func (a *App) sessionVideos(vids, auds []string) ([]tlVideo, error) {
	if len(vids) == 0 {
		return nil, fmt.Errorf("no videos selected")
	}
	zero := math.MaxFloat64
	type st struct {
		path  string
		start float64
	}
	var all []st
	for _, p := range append(append([]string{}, vids...), auds...) {
		s, err := sourceStart(p)
		if err != nil {
			return nil, fmt.Errorf("cannot place %s in time: %w", baseName(p), err)
		}
		all = append(all, st{p, s})
		zero = math.Min(zero, s)
	}
	var out []tlVideo
	for _, s := range all[:len(vids)] { // the videos are the leading entries
		dur, err := ffprobeDur(s.path)
		if err != nil {
			return nil, err
		}
		out = append(out, tlVideo{base: baseName(s.path), path: s.path,
			start: s.start - zero, wall: s.start, dur: dur, fps: ffprobeFPS(s.path)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out, nil
}

func hasAudioStream(path string) bool {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", path).Output()
	return err == nil && strings.Contains(string(out), "audio")
}

// ---- render -----------------------------------------------------------------

// prodLine is one narration line placed inside a clip. at is where the writer
// put it, seconds from the clip's start; delay is where the mix actually
// starts it -- at, unless the line had to be pulled earlier to fit. delay is
// what adelay and the subtitles use; at is only its input.
type prodLine struct {
	wav   string // synthesized wav, "" = not spoken (captioned only)
	dur   float64
	text  string
	at    float64
	delay float64
}

type prodClip struct {
	idx    int
	video  *tlVideo
	local  float64 // start inside that recording
	length float64 // slot length after growing for the narration
	tempo  float64
	lines  []prodLine // empty = original audio only
}

func (a *App) produceClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	segs := a.produceSegs()
	if len(segs) == 0 {
		a.setStatus("no cut yet — build one on the Cut step first")
		return
	}
	entries := a.produceEntries()
	st := a.prodSettings()
	vids, auds := a.snapSources()
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	a.logf(">>> producing %s: %d clips, %s/%s crf %d", filepath.Base(st.OutFile),
		len(segs), st.Container, st.Codec, st.CRF)
	a.prog(trackFrames, 0, "")
	a.prog(trackSTT, 0, "preparing…")

	go func() {
		err := a.produce(segs, entries, st, vids, auds)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				if !errors.Is(err, errStopped) {
					a.logf("produce FAILED: %v", err)
				}
				a.progress.SetText("production stopped")
				a.setStatus("production stopped — see log")
				return
			}
			a.progress.SetFraction(1)
			a.progress.SetText("done")
			dur, _ := ffprobeDur(st.OutFile)
			fi, _ := os.Stat(st.OutFile)
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			a.logf(">>> %s  (%.1f s, %s)", st.OutFile, dur, humanSize(size))
			a.setStatus(fmt.Sprintf("produced %s — %.1f s, %s", filepath.Base(st.OutFile), dur, humanSize(size)))
			a.updateStep5Info()
			if a.prod != nil && a.prod.player != nil {
				a.prod.player.PlaySegment(st.OutFile, 0, -1, false)
			}
		})
	}()
}

func (a *App) produce(segs []cutSeg, entries []narrEntry, st prodSettings, srcVid, srcAud []string) error {
	vids, err := a.sessionVideos(srcVid, srcAud)
	if err != nil {
		return err
	}
	dir := a.produceDir()
	clipDir := filepath.Join(dir, "clips")
	if err := os.RemoveAll(clipDir); err != nil {
		return err
	}
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		return err
	}

	// 1. speak whatever is still missing, so a render never silently drops a line
	var todo []narrEntry
	for _, e := range entries {
		if strings.TrimSpace(e.Text) != "" && !exists(a.ttsWav(e)) {
			todo = append(todo, e)
		}
	}
	for i, e := range todo {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.prog(trackSTT, 0.2*float64(i)/float64(len(todo)), "speaking %d/%d", i+1, len(todo))
		if err := a.synthesize(e); err != nil {
			return fmt.Errorf("synthesis for %.0fs: %w", e.S, err)
		}
	}
	base := 0.0
	if len(todo) > 0 {
		base = 0.2
	}

	// 2. plan every clip: which recording, how long, which voice file
	var clips []prodClip
	for i, s := range segs {
		v := pickVideo(vids, s.S)
		if v == nil {
			a.logfIdle("clip %d at %.0f s falls in no recording — skipped", i+1, s.S)
			continue
		}
		c := prodClip{idx: i, video: v, local: s.S - v.start, tempo: 1, length: s.E - s.S}
		if end := v.start + v.dur; s.E > end {
			c.length = end - s.S
			a.logfIdle("clip %d runs past the end of %s — shortened to %.1f s", i+1, v.base, c.length)
		}
		if c.length < 0.5 {
			continue
		}
		matched := matchEntries(entries, s)
		for _, e := range matched {
			text := strings.TrimSpace(e.Text)
			if text == "" {
				continue
			}
			// at is an offset into the clip the line was WRITTEN against, and
			// matchEntries deliberately accepts a clip that has moved since --
			// so it has to be re-based onto the clip actually being rendered. A
			// border dragged 20 s to the left and the line kept its old offset,
			// which is 20 s early: near the head of the clip, over whatever the
			// line before it was saying. Identical arithmetic to Narrate's
			// refit, and a no-op for a clip that has not moved.
			at := e.S + e.At - s.S
			ln := prodLine{text: text, at: math.Min(math.Max(0, at), math.Max(0, c.length-1))}
			ln.delay = math.Max(narrLead, ln.at)
			wav := a.ttsWav(*e)
			if !exists(wav) {
				a.logfIdle("clip %d: no synthesis for a line — it is captioned only", i+1)
			} else {
				ln.wav = wav
				ln.dur, _ = ffprobeDur(wav)
			}
			c.lines = append(c.lines, ln)
		}
		if len(c.lines) == 0 && len(entries) > 0 && len(matched) == 0 {
			a.logfIdle("clip %d at %.0f s has no narration entry — it keeps its own audio", i+1, s.S)
		}
		// make the lines fit where the writer put them: each starts no earlier
		// than the one before it ended, the slot grows first (the placement
		// survives whole -- a sign-off at the end stays at the end), then the
		// schedule slides earlier as one piece, and only as the last resort
		// are the lines distorted
		if len(c.lines) > 0 {
			pack := func() float64 { // returns when the last line ends
				prev := 0.0
				for k := range c.lines {
					d := math.Max(narrLead, c.lines[k].at)
					if d < prev {
						d = prev
					}
					c.lines[k].delay = d
					prev = d + c.lines[k].dur/c.tempo + 0.3
				}
				return prev - 0.3
			}
			need := pack() + 0.2
			if need > c.length {
				c.length += math.Min(need-c.length, maxExtend)
				if end := v.start + v.dur; s.S+c.length > end {
					c.length = end - s.S
				}
			}
			if over := need - c.length; over > 0 {
				shift := math.Min(over, c.lines[0].delay-narrLead)
				for k := range c.lines {
					c.lines[k].delay -= shift
				}
				need -= shift
				a.logfIdle("clip %d: the narration does not fit where it was placed — moved %.1f s earlier",
					i+1, shift)
			}
			if need > c.length {
				var talk float64
				for _, ln := range c.lines {
					talk += ln.dur
				}
				avail := c.length - narrLead - 0.2 - 0.3*float64(len(c.lines)-1)
				c.tempo = math.Min(talk/math.Max(0.1, avail), maxTempo)
				// repack with the sped-up lines, then slide once more: the
				// placements are re-derived from at, so the schedule is the
				// writer's shape at the new speed
				need = pack() + 0.2
				if over := need - c.length; over > 0 {
					shift := math.Min(over, c.lines[0].delay-narrLead)
					for k := range c.lines {
						c.lines[k].delay -= shift
					}
				}
				a.logfIdle("clip %d: narration %.1f s does not fit %.1f s — sped up %.2fx",
					i+1, talk, c.length, c.tempo)
			}
		}
		clips = append(clips, c)
	}
	if len(clips) == 0 {
		return fmt.Errorf("no clip could be placed on a recording")
	}

	// 3. subtitles, on the produced timeline
	srt, cum := "", 0.0
	cue := 0
	for _, c := range clips {
		for k, ln := range c.lines {
			end := cum + ln.delay + ln.dur/c.tempo
			if ln.dur == 0 { // unspoken: hold the caption until the next line, or the clip's end
				end = cum + c.length
				if k+1 < len(c.lines) {
					end = cum + c.lines[k+1].delay
				}
			}
			cue++
			srt += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", cue,
				srtTime(cum+ln.delay), srtTime(math.Min(end, cum+c.length)), wrapSub(ln.text))
		}
		cum += c.length
	}
	srtPath := filepath.Join(dir, "final.srt")
	if err := os.WriteFile(srtPath, []byte(srt), 0o644); err != nil {
		return err
	}

	// 4. encode each clip -- the only video encode in the whole pipeline
	ext := "." + st.Container
	var list strings.Builder
	for i, c := range clips {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.prog(trackSTT, base+(0.9-base)*float64(i)/float64(len(clips)),
			"clip %d/%d (%s)", i+1, len(clips), c.video.base)
		// numbered by their place in the cut, which is what the concat list and
		// the finished video are in, and then by the second they were shot: a
		// clip folder says where in the session every piece came from
		stem := fmt.Sprintf("c%03d_%s", i, stampName(c.video.wall+c.local))
		name := stem + ext
		var cueFile string
		if st.Subs == "burn" && len(c.lines) > 0 {
			cueFile = filepath.Join(clipDir, stem+".srt")
			one := ""
			for k, ln := range c.lines {
				end := ln.delay + ln.dur/c.tempo
				if ln.dur == 0 {
					end = c.length
					if k+1 < len(c.lines) {
						end = c.lines[k+1].delay
					}
				}
				one += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", k+1, srtTime(ln.delay),
					srtTime(math.Min(end, c.length)), wrapSub(ln.text))
			}
			if err := os.WriteFile(cueFile, []byte(one), 0o644); err != nil {
				return err
			}
		}
		if err := a.encodeClip(c, filepath.Join(clipDir, name), cueFile, st); err != nil {
			return fmt.Errorf("clip %d: %w", i+1, err)
		}
		// bare name: concat-list entries resolve against the LIST's directory
		fmt.Fprintf(&list, "file '%s'\n", name)
	}
	lf := filepath.Join(clipDir, "concat.txt")
	if err := os.WriteFile(lf, []byte(list.String()), 0o644); err != nil {
		return err
	}

	// 5. join (stream copy) and normalize loudness over the whole thing, so
	//    clip boundaries do not pump
	if err := a.checkpoint(); err != nil {
		return err
	}
	a.prog(trackSTT, 0.92, "joining %d clips", len(clips))
	joined := filepath.Join(clipDir, "joined"+ext)
	if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-f", "concat", "-safe", "0",
		"-i", lf, "-c", "copy", joined); err != nil {
		return err
	}

	if err := a.checkpoint(); err != nil {
		return err
	}
	a.prog(trackSTT, 0.96, "loudness + mux")
	if err := os.MkdirAll(filepath.Dir(st.OutFile), 0o755); err != nil {
		return err
	}
	args := []string{"-v", "error", "-y", "-i", joined}
	muxSubs := st.Subs == "mux" && st.Container != "webm" && cue > 0
	if muxSubs {
		args = append(args, "-i", srtPath)
	}
	args = append(args, "-map", "0:v", "-map", "0:a", "-c:v", "copy",
		"-af", loudFlt, "-ar", "48000")
	args = append(args, audioArgs(st)...)
	if muxSubs {
		codec := "srt"
		if st.Container == "mp4" {
			codec = "mov_text"
		}
		args = append(args, "-map", "1:0", "-c:s", codec,
			"-metadata:s:s:0", "language=eng")
	}
	args = append(args, st.OutFile)
	if err := a.runCmd("ffmpeg", args...); err != nil {
		return err
	}

	switch {
	case st.Subs == "mux" && st.Container == "webm":
		a.logfIdle("webm cannot carry an srt track — subtitles written next to the video instead")
		fallthrough
	case st.Subs == "sidecar":
		side := strings.TrimSuffix(st.OutFile, filepath.Ext(st.OutFile)) + ".srt"
		if err := os.WriteFile(side, []byte(srt), 0o644); err != nil {
			return err
		}
		a.logfIdle(">>> subtitles: %s", side)
	}
	return nil
}

// delayMS is a line's start in the units adelay actually reads: whole
// milliseconds, never below the lead-in. Rounded rather than truncated, so a
// placement is out by at most half a millisecond either way.
func delayMS(delay float64) int {
	return int(math.Round(math.Max(narrLead, delay) * 1000))
}

// encodeClip cuts one slot out of its recording and mixes the narration in.
func (a *App) encodeClip(c prodClip, out, cueFile string, st prodSettings) error {
	args := []string{"-v", "error", "-y",
		"-ss", fmt.Sprintf("%.3f", math.Max(0, c.local)),
		"-t", fmt.Sprintf("%.3f", c.length), "-i", c.video.path}
	game := "0:a"
	if !hasAudioStream(c.video.path) {
		args = append(args, "-f", "lavfi", "-t", fmt.Sprintf("%.3f", c.length),
			"-i", "anullsrc=channel_layout=stereo:sample_rate=48000")
		game = "1:a"
	}
	// every spoken line is its own input, mixed over the ducked game together
	var spoken []prodLine
	for _, ln := range c.lines {
		if ln.wav != "" {
			spoken = append(spoken, ln)
			args = append(args, "-i", ln.wav)
		}
	}
	voiceBase := 1
	if game == "1:a" {
		voiceBase = 2
	}

	var vf []string
	// the fps FILTER is what pins a stream to a rate: it duplicates and drops
	// frames until they land on that grid, whether the source was faster or
	// slower. Under VFR the rate is a ceiling instead, which is the encoder's
	// job and not a filter's (see fpsArgs).
	if st.FPS > 0 && !st.VFR {
		vf = append(vf, fmt.Sprintf("fps=%g", st.FPS))
	}
	if st.Height > 0 {
		vf = append(vf, fmt.Sprintf("scale=-2:%d", st.Height))
	}
	vf = append(vf, "setsar=1")
	if cueFile != "" {
		vf = append(vf, "subtitles="+ffEscape(cueFile))
	}
	fc := "[0:v]" + strings.Join(vf, ",") + "[v];"
	if len(spoken) > 0 {
		nrs := ""
		for k, ln := range spoken {
			// MILLISECONDS, as a whole number, and nothing else. adelay takes an
			// integer per channel; a seconds suffix on a fraction ("13.09s") is
			// not read as 13.09 s and is not rejected either -- ffmpeg drops the
			// value and delays by nothing, so the line played from the top of its
			// clip. Every line whose placement was fractional -- which is every
			// line dropped by hand or moved by the wheel -- started at zero, all
			// of them together, which is the overlapping narration. An integer
			// number of seconds ("40s") happened to survive, so the fault looked
			// intermittent.
			fc += fmt.Sprintf("[%d:a]atempo=%.3f,aresample=48000,pan=stereo|c0=c0|c1=c0,adelay=%d:all=1[nr%d];",
				voiceBase+k, math.Max(0.5, c.tempo), delayMS(ln.delay), k)
			nrs += fmt.Sprintf("[nr%d]", k)
		}
		fc += fmt.Sprintf("[%s]volume=%.3f,aresample=48000[bg];", game, st.GameVol)
		fc += fmt.Sprintf("[bg]%samix=inputs=%d:duration=first:normalize=0,", nrs, 1+len(spoken)) + audFmt + "[a]"
	} else {
		fc += fmt.Sprintf("[%s]%s[a]", game, audFmt)
	}
	args = append(args, "-filter_complex", fc, "-map", "[v]", "-map", "[a]")
	args = append(args, fpsArgs(st)...)
	args = append(args, codecArgs(st)...)
	args = append(args, audioArgs(st)...)
	args = append(args, out)
	return a.runCmd("ffmpeg", args...)
}

// fpsArgs decides the output's frame timing, explicitly in every direction.
// Left to itself ffmpeg decides per container and per whether a rate was named,
// so the same checkbox would have meant different things depending on a
// dropdown three rows up.
//
// A ceiling and pure passthrough cannot be asked for together: -fpsmax next to
// an explicit -fps_mode vfr is refused as contradictory ("One of -r/-fpsmax was
// specified together a non-CFR -fps_mode"). So a rate under VFR goes in as
// -fpsmax, which is a ceiling rather than a target -- footage slower than it
// keeps its own rate instead of being duplicated up onto the grid, and footage
// faster is dropped down to it. That is what a headset capture wants: 72 is the
// peak it reaches, not the rate it holds. With no rate named there is nothing
// to clamp, and every source timestamp goes through as it came.
func fpsArgs(st prodSettings) []string {
	switch {
	case !st.VFR:
		return []string{"-fps_mode", "cfr"} // the fps filter already set the grid
	case st.FPS > 0:
		return []string{"-fpsmax", fmt.Sprintf("%g", st.FPS)}
	default:
		return []string{"-fps_mode", "vfr"}
	}
}

func codecArgs(st prodSettings) []string {
	switch st.Codec {
	case "h265":
		// hvc1 instead of hev1: QuickTime and Safari refuse the other tag
		return []string{"-c:v", "libx265", "-preset", st.Preset,
			"-crf", strconv.Itoa(st.CRF), "-tag:v", "hvc1", "-pix_fmt", "yuv420p"}
	case "vp9":
		return []string{"-c:v", "libvpx-vp9", "-crf", strconv.Itoa(st.CRF), "-b:v", "0",
			"-row-mt", "1", "-cpu-used", vp9Speed(st.Preset), "-pix_fmt", "yuv420p"}
	default:
		return []string{"-c:v", "libx264", "-preset", st.Preset,
			"-crf", strconv.Itoa(st.CRF), "-pix_fmt", "yuv420p"}
	}
}

// vp9Speed maps the x264 preset names onto libvpx's -cpu-used scale.
func vp9Speed(preset string) string {
	switch preset {
	case "ultrafast":
		return "8"
	case "veryfast":
		return "5"
	case "fast":
		return "4"
	case "medium":
		return "2"
	case "slow":
		return "1"
	default:
		return "0"
	}
}

func audioArgs(st prodSettings) []string {
	br := strconv.Itoa(st.AudioKbps) + "k"
	if st.Container == "webm" {
		return []string{"-c:a", "libopus", "-b:a", br}
	}
	return []string{"-c:a", "aac", "-b:a", br}
}

func pickVideo(vids []tlVideo, t float64) *tlVideo {
	for i := range vids {
		if t >= vids[i].start && t < vids[i].start+vids[i].dur {
			return &vids[i]
		}
	}
	return nil
}

// matchEntries finds the narration written for a segment -- every line of it,
// in placement order, since a clip may carry more than one. The entries carry
// the clip's own times, so they usually match to the decimal -- but a cut
// edited after narrating can shift underneath, and a line silently dropped
// from the render is the worst possible failure here. So: real overlap is
// enough, merely touching is not.
func matchEntries(entries []narrEntry, s cutSeg) []*narrEntry {
	var out []*narrEntry
	for i := range entries {
		e := &entries[i]
		ov := math.Min(e.E, s.E) - math.Max(e.S, s.S)
		shorter := math.Min(e.E-e.S, s.E-s.S)
		if ov > 0 && (shorter <= 0 || ov >= shorter/2) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].At < out[b].At })
	return out
}

func srtTime(t float64) string {
	if t < 0 {
		t = 0
	}
	ms := int(math.Round(t * 1000))
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// wrapSub breaks a line into at most two subtitle rows of readable width.
func wrapSub(s string) string {
	words := strings.Fields(s)
	var rows []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if len(cur)+1+len(w) <= 42 {
			cur += " " + w
		} else {
			rows = append(rows, cur)
			cur = w
		}
	}
	if cur != "" {
		rows = append(rows, cur)
	}
	if len(rows) > 2 { // rebalance rather than drop text off the screen
		joined := strings.Join(rows, " ")
		half := len(joined) / 2
		cut := strings.LastIndex(joined[:half], " ")
		if cut < 0 {
			cut = half
		}
		rows = []string{joined[:cut], joined[cut+1:]}
	}
	return strings.Join(rows, "\n")
}

// ffEscape quotes a path for use inside a filtergraph argument, where ':' and
// ',' separate options and '\' escapes.
func ffEscape(p string) string {
	return strings.NewReplacer(
		`\`, `\\`, `:`, `\:`, `'`, `\'`, `[`, `\[`, `]`, `\]`, `,`, `\,`,
	).Replace(p)
}
