package main

// Produce. Cuts every clip from ITS OWN source recording, lays the
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
	narrGap   = 0.3  // ...and the breath left between two lines on the same clip
	narrTail  = 0.2  // ...and after the last one, before the clip is over
	maxExtend = 4.0  // seconds a clip may grow to fit its line
	maxTempo  = 1.25 // ... and how much the line may be sped up after that
	loudFlt   = "loudnorm=I=-14:TP=-1.5:LRA=11"
	// clipCeil is the last thing every clip's audio passes through before the
	// encoder. The mix keeps every lane at the level it was recorded at
	// (normalize=0, below), which is what a hand on a desk would do -- switching
	// a second lane on must not duck the first. But two lanes at their own level
	// ARE louder than one, and while the filter graph is float and does not care,
	// the AAC encode of each clip happens right here, long before the loudnorm
	// pass over the joined file (produceFinal) could undo anything. So the peaks
	// are caught at the one point that is too late to fix afterwards.
	//
	// level=disabled matters and is not a detail: alimiter's default is to scale
	// its output back up by 1/limit, which hands straight back the headroom the
	// limit just made -- the ceiling lands at 0 dBFS again and the whole filter
	// buys nothing. Disabled, it is a pass-through below the ceiling, which is
	// the other thing wanted here: a quiet scene has to STAY quieter than a loud
	// one, or the loudnorm pass at the end is handed a file whose moments no
	// longer agree with each other.
	//
	// It is on EVERY clip and not only the mixed ones, for the same reason audFmt
	// is: the clips are joined by copying, so a filter some of them went through
	// is a seam.
	clipCeil = "alimiter=limit=0.891:level=disabled" // -1 dBFS
)

// audFmt is the shape every clip's audio is forced into. Every clip the same,
// or the concat copy produces a file whose second half is silent on half the
// players -- which is also why the stereo/mono choice has to reach in here
// rather than sit on the encoder at the end: the clips are joined by copying,
// so a layout decided per clip IS the layout of the video.
func audFmt(st prodSettings) string {
	return "aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=" + audLayout(st)
}

// audLayout is that choice by its ffmpeg name. Mono halves the audio bitrate's
// work for a screen recording whose two sides were the same signal all along --
// which is most of them, and is exactly what the timeline says when it draws
// such a recording on one lane (see sameLanes).
func audLayout(st prodSettings) string {
	if st.Mono {
		return "mono"
	}
	return "stereo"
}

type prodSettings struct {
	Container string  `json:"container"`
	Codec     string  `json:"codec"`
	CRF       int     `json:"crf"`
	Preset    string  `json:"preset"`
	Height    int     `json:"height"` // 0 = keep source
	FPS       float64 `json:"fps"`    // 0 = keep source
	VFR       bool    `json:"vfr,omitempty"`
	AudioKbps int     `json:"audio_kbps"`
	Mono      bool    `json:"mono,omitempty"` // one channel out, whatever went in
	GameVol   float64 `json:"game_vol"`
	// Bare leaves the parts of the frame the picture does not reach black,
	// instead of filling them with a blurred blow-up of the picture itself.
	// Stored the wrong way round on purpose: the blurred backdrop is the
	// default, and a project written before this setting existed has to keep
	// getting it.
	Bare    bool   `json:"bare,omitempty"`
	Subs    string `json:"subs"` // burn | mux | sidecar | none
	OutFile string `json:"out_file"`
}

var (
	prodContainers = []string{"mp4", "mkv", "webm"}
	prodCodecs     = []string{"h264", "h265", "vp9"}
	prodPresets    = []string{"ultrafast", "veryfast", "fast", "medium", "slow", "veryslow"}
	prodHeights    = []string{"720p", "1080p", "original"}
	prodFPS        = []string{"source", "60", "30", "24"}
	prodABR        = []string{"128", "192", "256", "320"}
	prodSubsLbl    = []string{"burned into the picture", "separate track in the file",
		"sidecar .srt file only", "none"}
	prodSubsKey = []string{"burn", "mux", "sidecar", "none"}
)

type producer struct {
	a *App

	container, codec, preset, height, fps, abr, subs *gtk.DropDown
	vfr, mono, blur                                  *gtk.CheckButton
	crf, gvol                                        *gtk.Scale
	outLbl                                           *gtk.Label
	outFile                                          string
	outAuto                                          bool       // still the default -- follows the output folder
	inputs, info, out                                *gtk.Label // the two rows every step has, and the encode summary between them
	guard                                            bool       // suppresses feedback while applying a project
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

// defaultProdSettings is the render a project gets before anyone touches the
// Produce page: h264 in mp4 at the quality this pipeline was tuned on. Named
// because two places need it -- the page before it is built, and a new project,
// which has to come back to these rather than to zeroes (a 0 CRF is lossless
// and a 0 fps is not a video at all).
func defaultProdSettings() prodSettings {
	return prodSettings{Container: "mp4", Codec: "h264", CRF: 24, Preset: "veryslow",
		Height: 1080, FPS: 30, AudioKbps: 128, GameVol: 0.22, Subs: "sidecar"}
}

// UnmarshalJSON seeds the defaults before decoding, so that an ABSENT game_vol
// and a stored 0 stop meaning the same thing. They are different answers: a
// project written before the setting existed has no key at all and has to keep
// getting 0.22, the way Bare's inverted tag keeps the blurred backdrop, while a
// stored 0 is a deliberate pick -- silence the game entirely under the voice,
// which is what a talking head over gameplay wants. The page used to tell them
// apart with "if st.GameVol > 0", which read that 0 as "never set" and sprang
// the slider back to 0.22 on the next load, quietly un-silencing the game.
//
// Only GameVol is seeded. CRF's guard is left alone because it cannot be wrong:
// its slider starts at 14, so the 0 it tests for is not a value anyone can save.
func (s *prodSettings) UnmarshalJSON(b []byte) error {
	type raw prodSettings // a defined type carries no methods, so no recursion
	v := raw{GameVol: defaultProdSettings().GameVol}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = prodSettings(v)
	return nil
}

// gameVol is the level the render plays the game at under a narration line.
// Asked on its own because the Narrate preview has to duck to it on the
// playback tick, and reading a page of widgets ten times a second to get one
// number is not what that tick is for.
func (a *App) gameVol() float64 {
	if a == nil || a.prod == nil || a.prod.gvol == nil {
		return defaultProdSettings().GameVol
	}
	return a.prod.gvol.Value()
}

func (a *App) prodSettings() prodSettings {
	p := a.prod
	if p == nil {
		return defaultProdSettings()
	}
	st := prodSettings{
		Container: pickText(p.container, prodContainers),
		Codec:     pickText(p.codec, prodCodecs),
		CRF:       int(math.Round(p.crf.Value())),
		Preset:    pickText(p.preset, prodPresets),
		Height:    atoiOr(strings.TrimSuffix(pickText(p.height, prodHeights), "p"), 0),
		FPS:       float64(atoiOr(pickText(p.fps, prodFPS), 0)),
		VFR:       p.vfr.Active(),
		AudioKbps: atoiOr(pickText(p.abr, prodABR), 128),
		Mono:      p.mono.Active(),
		Bare:      !p.blur.Active(),
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
	setPick(p.height, prodHeights, tierLabel(st.Height))
	setPick(p.fps, prodFPS, fmtOpt(st.FPS))
	p.vfr.SetActive(st.VFR)
	setPick(p.abr, prodABR, strconv.Itoa(st.AudioKbps))
	p.mono.SetActive(st.Mono)
	p.blur.SetActive(!st.Bare)
	for i, k := range prodSubsKey {
		if k == st.Subs {
			p.subs.SetSelected(uint(i))
		}
	}
	if st.CRF > 0 {
		p.crf.SetValue(float64(st.CRF))
	}
	// no "> 0" guard here, unlike CRF above: 0 is a real level -- game fully
	// silent under the narration -- and UnmarshalJSON has already filled in the
	// default for a project that stored none.
	p.gvol.SetValue(st.GameVol)
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
// tierLabel is the dropdown's word for a stored height: 0 is "original" and
// anything else its p-name. A height an old project saved that is no longer
// offered ("2160") matches nothing, and setPick then leaves the default 1080p
// standing -- the same trade the shorter list itself makes.
func tierLabel(h int) string {
	if h <= 0 {
		return "original"
	}
	return fmt.Sprintf("%dp", h)
}

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

// ---- page -------------------------------------------------------------------

func (a *App) buildProduce() gtk.Widgetter {
	p := &producer{a: a}
	a.prod = p

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
				a.updateProduceInfo()
			}
		})
		return d
	}

	p.container = dd(prodContainers, 0, "mp4 plays everywhere; mkv keeps subtitle tracks best; webm forces VP9 + Opus")
	p.container.Connect("notify::selected", func() { p.syncExt() })
	p.codec = dd(prodCodecs, 0, "h264 is the safe upload; h265 is smaller but slower; vp9 is for webm")
	p.preset = dd(prodPresets, 5, "how long the encoder may think — slower means smaller at the same quality")
	p.height = dd(prodHeights, 1, "the short side of the frame — the cut page's aspect sets its shape; original keeps the footage's own size")
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
			a.updateProduceInfo()
		}
	})

	// Stereo out of habit rather than out of the footage: a screen capture is
	// usually one signal written to two channels (the timeline says so -- it
	// draws such a recording on one lane), and half the bitrate then goes on
	// carrying that signal a second time. Off by default all the same, because
	// a game that really is in stereo is a game whose stereo you would miss,
	// and this must not quietly flatten it.
	p.mono = gtk.NewCheckButtonWithLabel("Mono (one channel)")
	p.mono.SetTooltipText("Mix the finished audio down to a single channel. Worth it when " +
		"the capture's two sides carry the same signal — the same bitrate then goes on " +
		"one channel instead of two. Leave it off for anything with a real stereo image.")
	p.mono.ConnectToggled(func() {
		if !p.guard {
			a.updateProduceInfo()
		}
	})

	// What fills the frame where the picture does not reach: a camera pulled
	// back past the edge of the recording, a portrait cut of widescreen
	// footage, a card that is not the video's shape. On by default because
	// black bars read as a fault in the video rather than as a choice.
	//
	// It is a toggle rather than a setting the cut carries because the preview
	// cannot draw it -- the timeline paints those edges black -- so this is
	// also how you make the finished video match what you were shown.
	p.blur = gtk.NewCheckButtonWithLabel("Blurred backdrop")
	p.blur.SetActive(true)
	p.blur.SetTooltipText("Fill the empty edges of the frame with a blown-up, blurred " +
		"copy of the picture itself, instead of black. Off gives plain black bars — " +
		"which is also what the Cut preview draws, so turn it off if you want the " +
		"finished video to look exactly like the preview did.")
	p.blur.ConnectToggled(func() {
		if !p.guard {
			a.updateProduceInfo()
		}
	})

	p.crf = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 14, 34, 1)
	p.crf.SetValue(24)
	p.crf.SetDrawValue(true)
	p.crf.SetSizeRequest(200, -1)
	p.crf.SetTooltipText("quality: lower is better and bigger (18–24 is the usual range)")
	p.crf.AddMark(24, gtk.PosBottom, "24")
	p.crf.ConnectValueChanged(func() { a.updateProduceInfo() })

	p.gvol = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 1, 0.02)
	p.gvol.SetValue(0.22)
	p.gvol.SetDrawValue(true)
	p.gvol.SetSizeRequest(200, -1)
	p.gvol.SetTooltipText("how loud the original game audio sits under the narration")
	p.gvol.ConnectValueChanged(func() { a.updateProduceInfo() }) // it is on the settings line now

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
	at(1, 5, "Audio channels:", p.mono)
	at(0, 5, "Empty frame edges:", p.blur)

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
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.Append(openOut)
	outRow.Append(p.out)
	a.outStack.AddNamed(outRow, "produce") // the shared bar's Outputs group; see outStack in main.go

	box := gtk.NewBox(gtk.OrientationVertical, 10)
	box.SetMarginTop(10)
	box.SetMarginBottom(8)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.Append(grid)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	box.Append(destRow)
	box.Append(p.info)

	a.updateProduceInfo() // the rows say something before anything is clicked

	// The thumbnail half of the step -- what was the Publish page. The drawing
	// side fills the left of this page; the words the model wrote sit top
	// right, where they are read back and reworded; and the encoder settings
	// sit under them -- the knobs set once, below the text reread every run.
	// One page because one ▶ runs it all (produceClicked), and what that ▶
	// makes is one thing: the upload.
	drawSide, said, pubOuts := a.buildPublishPanes()
	outRow.Append(gtk.NewSeparator(gtk.OrientationVertical))
	outRow.Append(pubOuts) // step6's files ride the same Outputs group, fenced off the video's

	// only the settings scroll: the words above stay put, and a settings grid
	// taller than its half slides rather than pushing the title off the page
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(box)
	scroll.SetPropagateNaturalHeight(true)

	right := gtk.NewBox(gtk.OrientationVertical, 6)
	right.SetSizeRequest(360, -1)
	right.Append(said)
	right.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	right.Append(scroll)

	outer := gtk.NewPaned(gtk.OrientationHorizontal)
	outer.SetStartChild(drawSide)
	outer.SetEndChild(right)
	outer.SetResizeStartChild(true)
	outer.SetResizeEndChild(true)
	outer.SetShrinkStartChild(false)
	outer.SetShrinkEndChild(false)
	outer.SetVExpand(true)
	outer.SetMarginEnd(12)

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(outer)
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

// updateProduceInfo redraws all three lines: what the render reads, what it will
// encode, and what it has written. Everything that changes any of them comes
// through here -- a dropdown, a cut edited next door, a finished run.
func (a *App) updateProduceInfo() {
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
		total += s.length()
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
	// the separate recordings go into the sound now, so this row has to say so:
	// a render whose game audio suddenly has the room in it is otherwise a
	// surprise arriving after the encode rather than before it
	if _, auds := a.snappedSources(); len(auds) > 0 {
		line += fmt.Sprintf(" · %d separate recording(s) mixed in", len(auds))
		detail += "\n\nMixed into each clip's own audio, for the stretch of it that was running while that clip was:"
		for _, p := range auds {
			detail += "\n" + baseName(p)
		}
	}
	// the voice is named on Narrate's inputs row too: it is what the cached
	// takes were spoken in, and the thing nothing else on this page says
	if vp := a.voicePick; vp != nil && len(entries) > 0 {
		if v, ok := vp.current(); ok {
			line += " · voice: " + v.name
			detail += "\n\nSpoken by " + v.name + " (step4/voice_ref.wav)"
		}
	}
	// the thumbnail half's inputs, now that this page owns both: what the
	// image model is given, and whether the first ▶ still owes the language
	// model the text -- the once-per-project call the step6 record gates
	// (publishStage)
	if pub := a.pub; pub != nil {
		if n := len(pub.frames); n > 0 {
			line += fmt.Sprintf(" · %d thumbnail image(s)", n)
		}
		if a.publishRecorded() {
			line += " · upload text written"
			detail += "\n\nstep6/publish.json — the upload text is written; ▶ redraws and re-renders without asking the model again (deleting step6/ starts the text over)"
		} else {
			line += " · upload text still to write"
			detail += "\n\nNo step6/publish.json yet — the first ▶ writes the title, the thumbnail instruction and the description before drawing anything"
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
	// the frame in pixels, not the word from the dropdown: "original" over a
	// 4K screen capture is a 4K upload, and the number is the only form of
	// that fact anyone reads before waiting out the encode
	res := "original size"
	if st.Height > 0 {
		res = fmt.Sprintf("%dp", st.Height)
	}
	if w, h, ok := p.a.footageSize(); ok {
		ow, oh := outSize(w, h, st.Height)
		// an aspect picked on the cut page reshapes the frame (outBox does,
		// for the render) -- the number here has to be the frame the render
		// will actually make, or the one line on this page that exists to be
		// believed is wrong exactly when the shape was changed on purpose
		if p.a.ed != nil {
			if asp := parseAspect(p.a.ed.aspect); asp > 0 {
				ow, oh = tierBox(asp, h, st.Height)
			}
		}
		res = fmt.Sprintf("%d×%d", ow, oh)
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
	edges := "" // the blurred backdrop is the default; only its absence is news
	if st.Bare {
		edges = " · empty frame edges black"
	}
	p.info.SetText(fmt.Sprintf("%s, %s, %s crf %d · %s · audio %d kbit/s %s, game at %.0f%% under the voice · subtitles %s%s",
		res, fps, st.Codec, st.CRF, st.Container, st.AudioKbps, audLayout(st), st.GameVol*100,
		prodSubsLbl[subsIndex(st.Subs)], edges))
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
//
// It is the cut as a SEQUENCE, which is not quite the cut as the timeline holds
// it: a spliced insert cuts the clip it sits in, and here it comes out as two
// clips with the card between them (splitSpliced). Every step after this one
// reads the cut through here, so they all see the same running order -- the one
// the narration is written against and the one that gets rendered.
func (a *App) produceSegs() []cutSeg {
	c := a.produceCut()
	return applyFx(splitSpliced(c.Segs), c.Fx)
}

// produceCut is the whole cut file the render works from -- the live editor
// when it has one, what is saved otherwise. Everything above the segment level
// (the camera, the output frame, the microphone, the timeline's own
// corrections) reads it through here so it cannot disagree with produceSegs.
//
// The editor counts as having a cut when it has segments, a correction to the
// timeline, or a row of its own: shifting a lane or adding one before cutting
// anything is a real edit of a real project, and reading past it to a stale file
// would render the old placement.
func (a *App) produceCut() cutFile {
	if ed := a.ed; ed != nil && (len(ed.segs) > 0 || len(ed.shift) > 0 || len(ed.cutLanes) > 0) {
		return cutFile{Segs: ed.segs, Aspect: ed.aspect, Fx: ed.fx, Shift: ed.shift,
			Rows: ed.rows, Lanes: ed.cutLanes, NRows: ed.nRows}
	}
	b, err := os.ReadFile(a.cutPath())
	if err != nil {
		return cutFile{}
	}
	var c cutFile
	if json.Unmarshal(b, &c) != nil {
		return cutFile{}
	}
	c.Fx = migrateFx(c.Fx)
	return c
}

func (a *App) produceEntries() []narrEntry {
	if a.narr != nil {
		a.narr.pullRows()
		if len(a.narr.entries) > 0 {
			return append([]narrEntry(nil), a.narr.entries...)
		}
	}
	a.narr.flushSave() // a line typed a moment ago belongs in this render
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

// sessionTracks is the same placement for both kinds at once: the footage on
// the timeline, and the separate recordings beside it on the same clock. The
// render needs the second list for the same reason the cut page draws lanes
// from it -- a recording that was running while a clip was running is part of
// that clip's sound, and the only thing that says which stretch of it that is
// is where both of them sit on this one clock.
func (a *App) sessionTracks(vids, auds []string) ([]tlVideo, []tlAudio, error) {
	if len(vids) == 0 {
		return nil, nil, fmt.Errorf("no videos selected")
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
	var out []tlVideo
	for _, s := range all[:len(vids)] { // the videos are the leading entries
		dur, err := ffprobeDur(s.path)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, tlVideo{base: baseName(s.path), path: s.path,
			start: s.start - zero, wall: s.start, dur: dur, fps: ffprobeFPS(s.path)})
	}

	var rec []tlAudio
	for _, s := range all[len(vids):] {
		dur, err := ffprobeDur(s.path)
		if err != nil || dur <= 0 {
			continue // not something with sound in it; the lanes skip it too
		}
		rec = append(rec, tlAudio{base: baseName(s.path), path: s.path,
			start: s.start - zero, dur: dur})
	}
	// the corrections the cut page made to these clocks, put on before anything
	// is sorted or coloured. The render places its own tracks and would
	// otherwise place them off the raw timestamps -- the ones the hand was
	// dragging to correct in the first place.
	c := a.produceCut()
	for i := range out {
		out[i].start += c.Shift[out[i].base]
	}
	for i := range rec {
		rec[i].start += c.Shift[rec[i].base]
	}
	// the further tracks of a multi-track capture, which are recordings in every
	// way that matters here: a lane each, on the file's own clock, mixed under
	// the clips they were running through. They are built from the videos AFTER
	// those were corrected, so a row dragged to fix its clock takes its own
	// second track with it, and their own name is still a name a further drag
	// can be stored under (slideSrc, cut_shift.go).
	for _, au := range srcLanes(out, a.snappedTracks()) {
		if au.master {
			continue // that one is the footage's own sound, taken off its input
		}
		au.start += c.Shift[au.base]
		rec = append(rec, au)
	}
	// and the rows the cut put on the band itself, which are in cut.json and in
	// no source list -- the render has to be told about them or every scene cut
	// to one would resolve to whatever recording was rolling at that second
	// (cut_lane.go). They are placed by laneVideos and corrected here, exactly
	// as the editor does it.
	lanes := a.laneVideos(c.Lanes, out)
	for i := range lanes {
		lanes[i].start += c.Shift[lanes[i].base]
	}
	out = append(out, lanes...)
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	sort.Slice(rec, func(i, j int) bool { return rec[i].start < rec[j].start })
	// the rows, worked out the same way the cut page works them out, because a
	// scene says which ROW its picture comes from (cutSeg.Cam) and the number
	// has to mean the same thing on both sides. Without this every scene would
	// resolve against row nought and a two-camera cut would render as one.
	assignLanes(out, c.Rows)
	return out, rec, nil
}

func hasAudioStream(path string) bool {
	out, err := exec.Command(ffTool("ffprobe"), "-v", "error", "-select_streams", "a:0",
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
	// pos is where the caption sits on the picture: "top", "center", or ""
	// for the bottom (narrEntry.Pos, already normalized). Only the subtitles
	// read it; the mix does not care where words are drawn.
	pos string
}

type prodClip struct {
	idx int
	// exactly one of these two is set. video is a stretch of a recording; ins is
	// a file that plays instead of one -- a card, a still, an animated ranking
	// (see cutSeg.Ins). An insert has no local time and no recording to run past
	// the end of, so the planning that fits narration into a slot is the same for
	// both and the geometry is not.
	video *tlVideo
	ins   string
	local float64 // start inside that recording
	// where this clip starts on the SESSION clock -- the clock the effects are
	// placed on. The camera reads it off the recording (video.start + local);
	// an insert has no recording, so it is kept here for both.
	sessS float64
	// the frame an insert is fitted into, so every clip in the concat list has
	// the same dimensions (clipBox). Unset for footage, which sets its own.
	boxW, boxH int

	// an audio-only insert: the picture is the session's own (video is set,
	// exactly as for footage), and this file is the clip's sound in the
	// capture's place. Spliced, the picture is one held frame (freeze below);
	// over a selection, it keeps running.
	snd string
	// where in that file the sound starts. Nought for a file chosen from
	// disk, which plays from its own beginning; the copied second for a
	// stretch of a lane copied out of the session, which plays from there.
	//
	// snd is not only for audio inserts. It says "the sound comes from HERE,
	// not from the input the picture came from", and an insert covering the
	// picture alone (cutSeg.Mute) is the mirror case: the recording underneath
	// goes in this slot, so what is heard carries on while the frames change.
	sndAt float64
	// this clip brings no sound of its own -- an insert placed by a selection
	// scoped to the picture alone. Spliced, that means silence, and this is
	// what makes clipInput say the input has no audio to take. Overwriting, the
	// session's own sound is put in snd above instead and this is only a record
	// of why.
	mute bool
	// no separately-recorded lane is heard under this clip. The zero value is
	// the ordinary answer -- every recording that was running is mixed in -- and
	// this is set for the two ways that stops being true: the clip is time put
	// INTO the session rather than a stretch of it (anything spliced in), or
	// what it brought replaced everything that was audible (an ordinary insert
	// over a selection, a picture-alone paste). See clipMixes.
	noLanes bool
	// the one lane the clip's own sound stands in for: a sound laid over a
	// selection scoped to a single recording (▼) replaces THAT recording and
	// leaves the others playing, so it is dropped from the mix by name.
	dropLane string
	// the lanes this clip's scene does not hear (cutSeg.Quiet), carried through
	// unread: clipMixes drops the separate recordings named here, and clipInput
	// treats the capture's own track as having no sound when the camera's own
	// lane is one of them.
	quiet []string

	length float64 // slot length after growing for the narration
	tempo  float64
	lines  []prodLine // empty = original audio only
	mix    []prodMix  // separate recordings running under this clip

	// the effects (cut_fx.go), already resolved into per-clip terms by the
	// planning: rate is the playback rate (1 = normal, 0.5 = half speed --
	// length is output seconds either way), freeze holds the clip's first
	// frame for the whole length, and cam is the camera when an aspect or a
	// view/zoom is in play (nil = the frame as shot).
	rate   float64
	freeze bool
	cam    *camPath
	// the text effects that fall inside this clip, in its own output seconds
	// (textCues). Resolved in the planning beside the camera, because both
	// need the frame the clip comes out at.
	texts []textCue
	// the stop effects that fall inside this clip (freezeCues): each a frame
	// of a recording standing over the running footage, faded on and off.
	stills []stillCue
	// the volume effects that fall inside this clip (gainCues), in its own
	// output seconds. The only effect that reaches the sound and nothing else,
	// so it is the only one resolved here that the picture never sees.
	gains []textCue
}

// stillCue is one stop effect inside one clip: a textCue's window and fades --
// s to e in the clip's own output seconds -- plus where the frame itself comes
// from, the recording and the second in it. Composited before the camera
// (encodeClip), so a zoom or a view crops the still exactly as it crops the
// footage running under it.
type stillCue struct {
	s, e      float64
	fin, fout float64
	path      string
	at        float64
	// the stop asked for its seconds to be silent (cutFx.Mute) rather than
	// letting the footage's sound run on under the held frame.
	mute bool
	// the size the held frame has to come out at, when that is not the size it
	// already is. A stop's bar can hang across a cut into a clip cut from
	// ANOTHER recording, and the frame it holds is still the one the stop
	// started on -- so a 1280x720 webcam frame can end up laid over 3840x2160
	// gameplay. The overlay has no size of its own and no scaling in it, so
	// that frame would sit in the top-left corner at a third of the size. Left
	// at nought when the two agree, which is every single-camera session.
	w, h int
}

// bdrop is what a clip owes the parts of its frame the picture does not
// reach. Three things leave a frame bare -- a camera pulled back past the edge
// of the recording, a recording that is not the shape of the finished video,
// and an insert or a card that is not either -- and all three used to be
// filled with black, which reads as a fault in the video rather than as a
// choice. A blown-up, blurred copy of the picture itself reads as depth.
//
// w,h is the finished frame. fit says the picture has to be scaled down until
// it fits and centred on that frame -- an insert, whose own size is nobody's
// business; otherwise the picture goes on at its own size with its top-left
// corner at x,y, which is exactly where pad would have put it.
//
// bare is the Produce toggle turned off: the same region, filled with black.
// It stays a bdrop rather than becoming a branch at every call site, because
// what fills a bare edge is one question with two answers, and the region it
// covers is worked out the same way either way.
type bdrop struct {
	w, h int
	x, y int
	fit  bool
	bare bool
}

func (b bdrop) on() bool { return b.w > 0 && b.h > 0 }

// minClipLn is the shortest clip the render will make. Under it there are not
// enough frames to encode and splice, and the piece is dropped.
//
// Everything upstream that decides how long a clip will COME OUT measures
// itself against this number rather than one of its own -- the speed clamp and
// the ramp stairs both do -- because a floor here and a different floor there
// is how footage goes missing between the timeline and the video.
const minClipLn = 0.5

// blurSigma is how far the backdrop is smeared, as a fraction of the finished
// frame's height. Enough that no detail survives to be read as a second
// picture, not so much that a bright scene turns into one flat colour.
const blurSigma = 0.02

// chain is the sub-graph that lays the picture on a blurred blow-up of itself:
// one split, the copy scaled to COVER the frame and blurred, the picture laid
// back over it. in is the label it reads, k a number that keeps its own labels
// apart from every other one in the graph; it returns the filter_complex text
// and the label it wrote.
func (b bdrop) chain(in string, k int) (string, string) {
	out := fmt.Sprintf("bd%d", k)
	if b.bare {
		// black, and one filter to say it: pad puts the picture on a bigger
		// frame at the corner it belongs at, and the fitted case scales it
		// down first because an insert brings its own shape
		if b.fit {
			return fmt.Sprintf("[%s]scale=%d:%d:force_original_aspect_ratio=decrease,"+
				"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black[%s];",
				in, b.w, b.h, b.w, b.h, out), out
		}
		return fmt.Sprintf("[%s]pad=%d:%d:%d:%d:color=black[%s];",
			in, b.w, b.h, b.x, b.y, out), out
	}
	sig := math.Max(4, float64(b.h)*blurSigma)
	fg := fmt.Sprintf("bdf%d", k)
	bg := fmt.Sprintf("bdb%d", k)
	fc := fmt.Sprintf("[%s]split=2[%s][%s];", in, bg, fg)
	// increase then crop is "cover": the copy is blown up until it is at
	// least the frame in both directions, and the excess is cut off centred
	fc += fmt.Sprintf("[%s]scale=%d:%d:force_original_aspect_ratio=increase,"+
		"crop=%d:%d,gblur=sigma=%.2f,setsar=1[%s];", bg, b.w, b.h, b.w, b.h, sig, bg)
	if b.fit {
		fc += fmt.Sprintf("[%s]scale=%d:%d:force_original_aspect_ratio=decrease[%s];",
			fg, b.w, b.h, fg)
		fc += fmt.Sprintf("[%s][%s]overlay=(W-w)/2:(H-h)/2[%s];", bg, fg, out)
		return fc, out
	}
	fc += fmt.Sprintf("[%s][%s]overlay=%d:%d[%s];", bg, fg, b.x, b.y, out)
	return fc, out
}

// clipSize is what one encoded clip actually came out at.
type clipSize struct {
	name string
	w, h int
}

// joinMismatch is what to say about clips that will break the join. The clip
// list goes into the concat demuxer as a stream copy: it does not scale, and
// -- this is the part worth guarding -- it does not refuse either. A clip of
// another size is written into the finished file and the decoder comes apart
// on it, so the video plays as blocks and smears from that point on instead
// of failing anywhere a person can see it.
//
// Every branch of encodeClip pins the size it comes out at, so a mismatch
// here means one of them stopped doing it. Measured against the FIRST clip,
// because that is the size the decoder is set up on, and reported by name so
// the offending file in the clips folder can be looked at.
func joinMismatch(made []clipSize) []string {
	var out []string
	for i, c := range made {
		if i == 0 || (c.w == made[0].w && c.h == made[0].h) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s came out %d×%d where %s is %d×%d — the join is a stream copy and cannot mix sizes, so the video breaks at this clip",
			c.name, c.w, c.h, made[0].name, made[0].w, made[0].h))
	}
	return out
}

// fitsFrame is whether this recording, scaled down until it fits the finished
// frame, then covers it -- so that the edges need nothing filling in. Asked of
// the source size the Prepare page probed; a recording nobody measured answers
// no, because a wrong yes here is footage pulled out of shape.
func fitsFrame(v *tlVideo, w, h int) bool {
	if v == nil || v.w <= 0 || v.h <= 0 || w <= 0 || h <= 0 {
		return false
	}
	// what force_original_aspect_ratio=decrease would come out at: the smaller
	// of the two scales, so the picture never overshoots the frame and the
	// only question left is what it falls SHORT of. Within a pixel on both
	// axes is covered -- 1366x768 into 1920x1080 is not 16:9 to the last
	// decimal, and a one-pixel seam of blur is not worth a filter chain
	k := math.Min(float64(w)/float64(v.w), float64(h)/float64(v.h))
	return float64(w)-float64(v.w)*k < 2 && float64(h)-float64(v.h)*k < 2
}

// stillSize is the size a held frame has to be brought to before it is laid
// over a clip, or nought when it needs no bringing. Two recordings and the
// question is only ever asked of a pair that a stop's bar reaches across; a
// session shot on one camera answers nought here every time, and so does one
// whose sizes were never probed -- a made-up size would be worse than none.
func stillSize(from, over *tlVideo) (int, int) {
	if from == nil || over == nil {
		return 0, 0
	}
	if from.w <= 0 || from.h <= 0 || over.w <= 0 || over.h <= 0 {
		return 0, 0
	}
	if from.w == over.w && from.h == over.h {
		return 0, 0
	}
	return over.w, over.h
}

// stillMute is the ffmpeg enable expression for the seconds this clip's stops
// asked to have taken out of its sound: one between() per still, added
// together the way ffmpeg spells "or". Empty when every stop keeps its sound,
// which is the ordinary case and leaves the graph exactly as it was.
func stillMute(stills []stillCue) string {
	var parts []string
	for _, sc := range stills {
		if sc.mute && sc.e > sc.s {
			parts = append(parts, fmt.Sprintf("between(t,%.3f,%.3f)", sc.s, sc.e))
		}
	}
	return strings.Join(parts, "+")
}

// prodMix is a separate recording playing under one clip: a headset recorder, a
// mic on the table, OBS's second track. The picture's own sound is the game and
// whatever the capture card happened to hear; this is the rest of what was said
// while it was recorded, and it is mixed into the same bed rather than over the
// narration -- so "Game audio under voice" ducks both together and the AI voice
// still sits on top of everything that was actually there.
//
// The three numbers are the whole placement: at is where in the clip it comes
// in (0 unless the recorder started later than the clip did), ss is where in
// the recording the clip starts, and dur is how much of it is used. They come
// out of the one session clock the lanes are drawn on, which is why a recording
// that ran for the second half of a clip is heard for the second half of it.
type prodMix struct {
	base string
	path string
	at   float64
	ss   float64
	dur  float64
	// which audio stream of that file, a:N. Nought for a recording and for a
	// sound laid in by hand; above nought for a further track of a multi-track
	// capture, where the same file is in the mix on more than one lane and this
	// is the only thing that says which (cut_tracks.go).
	track int
}

// speed is the clip's playback rate with the zero value meaning normal: the
// planning always writes one, but a prodClip is also built by hand in tests
// and the arithmetic below divides by this.
func (c prodClip) speed() float64 {
	if c.rate > 0 {
		return c.rate
	}
	return 1
}

// name is what a clip is called in the log and in the clips folder.
func (c prodClip) name() string {
	if c.ins != "" {
		return insBase(c.ins)
	}
	return c.video.base
}

// produceClicked is ▶ on this page: everything the upload needs, in the order
// it can be lost. The upload text is written once and the thumbnail is drawn
// (publishStage -- seconds, and the part a dead server fails fast), and only
// then is the video rendered -- minutes that must not be paid before the cheap
// half has succeeded, and must not be re-paid to get a reworded thumbnail.
func (a *App) produceClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	if a.pub == nil {
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
	// the drawing half's inputs, read on this thread like everything above:
	// the goroutine below must not go reading widgets or the editor
	pst := a.pub.snapshot()
	aspect := a.produceCut().Aspect
	written := a.publishRecorded()
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	if written {
		a.logf(">>> producing %s: redraw the thumbnail, then render %d clips, %s/%s crf %d",
			filepath.Base(st.OutFile), len(segs), st.Container, st.Codec, st.CRF)
	} else {
		a.logf(">>> producing %s: write the upload text, draw the thumbnail, then render %d clips, %s/%s crf %d",
			filepath.Base(st.OutFile), len(segs), st.Container, st.Codec, st.CRF)
	}
	a.qReset()
	a.qJob(trackSTT, "publish", 0, 0)
	a.prog(trackSTT, 0, "thinking")
	a.pulseUntilCounted()

	go func() {
		if err := a.publishStage(pst, aspect, segs, entries, !written, written, false); err != nil {
			glib.IdleAdd(func() {
				a.running = false
				a.updateRunControls()
				if p := a.pub; p != nil {
					p.refresh() // whatever was written landed before the failure
				}
				a.updateGates()
				if errors.Is(err, errStopped) {
					a.progress.SetText("production stopped")
					return
				}
				a.logf("produce FAILED: %v", err)
				a.progress.SetText("production failed — see log")
			})
			return
		}
		glib.IdleAdd(func() {
			if p := a.pub; p != nil {
				p.refresh() // the thumbnail and the text, up before the long half starts
			}
			a.qJob(trackSTT, "render", 0, 0)
			a.prog(trackSTT, 0, "preparing")
		})
		err := a.produce(segs, entries, st, vids, auds)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				if !errors.Is(err, errStopped) {
					a.logf("produce FAILED: %v", err)
				}
				if errors.Is(err, errStopped) {
					a.progress.SetText("production stopped")
				} else {
					a.progress.SetText("production failed — see log")
				}
				return
			}
			a.progress.SetFraction(1)
			a.progress.SetText("done")
			// the bar carries the outcome; filled in below, once the file has
			// been measured
			dur, _ := ffprobeDur(st.OutFile)
			fi, _ := os.Stat(st.OutFile)
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			a.logf(">>> %s  (%.1f s, %s)", st.OutFile, dur, humanSize(size))
			a.progress.SetText(fmt.Sprintf("produced %s — %.1f s, %s",
				filepath.Base(st.OutFile), dur, humanSize(size)))
			a.updateProduceInfo()
		})
	}()
}

func (a *App) produce(segs []cutSeg, entries []narrEntry, st prodSettings, srcVid, srcAud []string) error {
	vids, recs, err := a.sessionTracks(srcVid, srcAud)
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
	// -- unless nothing is ever spoken: captions only renders every line as a
	// caption with no wav, which the planning below already knows how to lay out
	var todo []narrEntry
	if !a.captionsOnly() {
		for _, e := range entries {
			if strings.TrimSpace(e.Text) != "" && !exists(a.ttsWav(e)) {
				todo = append(todo, e)
			}
		}
	} else if len(entries) > 0 && st.Subs == "none" {
		a.logfIdle("!!! captions only and subtitles set to none — the lines appear nowhere")
	}
	if len(todo) > 0 {
		// a render that has to speak first is two jobs, and the bar says so;
		// when everything is already spoken there is only ever the one
		a.qJob(trackSTT, "speaking", 1, 2)
		a.qPush(trackSTT, len(todo), "line")
	}
	for i, e := range todo {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.qTake(trackSTT)
		a.prog(trackSTT, 0.2*float64(i)/float64(len(todo)), "")
		if err := a.synthesize(e); err != nil {
			return fmt.Errorf("synthesis for %.0fs: %w", e.S, err)
		}
	}
	base := 0.0
	if len(todo) > 0 {
		base = 0.2
		a.qJob(trackSTT, "render", 2, 2)
		a.prog(trackSTT, base, "planning the clips")
	}

	// 2. plan every clip: which recording, how long, which voice file, and
	// which lanes the scene was told not to hear (cutSeg.Quiet, cut_hear.go)
	var clips []prodClip
	for i, s := range segs {
		var c prodClip
		switch {
		case s.isCopy():
			// a copied stretch: not a file to stretch into a slot but footage
			// to cut from its recording, exactly as the segment it was copied
			// from would be cut
			from, _ := copySrc(s.Ins)
			v := pickVideoOn(vids, s.Cam, from)
			if v == nil {
				a.logfIdle("clip %d copies footage at %.0f s that falls in no recording — skipped", i+1, from)
				continue
			}
			c = copyClip(i, s, from, v)
			if end := v.start + v.dur; from+c.length > end {
				c.length = end - from
				a.logfIdle("clip %d copies past the end of %s — shortened to %.1f s", i+1, v.base, c.length)
			}
		case s.audioIns():
			// sound alone: the picture is the session's own -- held on one
			// frame when the file is spliced in, kept running when it covers a
			// selection -- and the file replaces the session's sound for the
			// slot (encodeClip routes snd where the capture's sound was)
			file, _ := insSplit(s.Ins)
			path := a.fromRoot(file)
			if !exists(path) {
				a.logfIdle("clip %d: %s is not there any more — skipped", i+1, file)
				continue
			}
			v := pickVideoOn(vids, s.Cam, s.S)
			if v == nil {
				a.logfIdle("clip %d at %.0f s falls in no recording — its sound has no picture, skipped", i+1, s.S)
				continue
			}
			c = sndClip(i, s, path, v, recs)
			if end := v.start + v.dur; !c.freeze && s.E > end {
				c.length = end - s.S
				a.logfIdle("clip %d runs past the end of %s — shortened to %.1f s", i+1, v.base, c.length)
			}
		case s.isInsert():
			// an insert is its own picture, so there is nothing to look up and
			// nothing to run past the end of: the slot is exactly as long as the
			// cut says, and assetClip stretches or trims the file to fill it
			// the file is resolved and the card's parameters ride along with it:
			// what is on disk is a picture the parameters are applied TO, and
			// only the part before the "?" is a path at all
			file, q := insSplit(s.Ins)
			path := a.fromRoot(file)
			if !exists(path) {
				a.logfIdle("clip %d: %s is not there any more — skipped", i+1, file)
				continue
			}
			var note string
			c, note = insClip(i, s, path+q.suffix(), vids)
			if note != "" {
				a.logfIdle("clip %d %s", i+1, note)
			}
		default:
			v := pickVideoOn(vids, s.Cam, s.S)
			if v == nil {
				a.logfIdle("clip %d at %.0f s falls in no recording — skipped", i+1, s.S)
				continue
			}
			c = prodClip{idx: i, video: v, local: v.at(s.S), tempo: 1, rate: 1,
				length: s.length()}
			if s.Rate > 0 {
				c.rate = s.Rate
			}
			// the clamp is in session seconds -- what the recording has left --
			// and the slot in output seconds, which under slow motion is longer
			if end := v.start + v.dur; s.E > end {
				c.length = (end - s.S) / c.speed()
				a.logfIdle("clip %d runs past the end of %s — shortened to %.1f s", i+1, v.base, c.length)
			}
		}
		if c.length < minClipLn {
			// under half a second there is nothing to encode, but a piece
			// vanishing out of the middle of the cut in silence reads as a
			// render bug. It is nearly always an effect boundary landing just
			// short of a cut, which is a thing the hand can move.
			a.logfIdle("clip %d at %s is %.2f s — too short to render, dropped%s",
				i+1, mmss(s.S), c.length, spokenHere(entries, s))
			continue
		}
		c.sessS = s.S
		c.quiet = s.Quiet // every clip kind, so one scene answers one way
		if from, ok := copySrc(s.Ins); ok {
			// its text cues are the copied footage's, not the paste point's:
			// a name plate over the original belongs over the copy too
			c.sessS = from
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
			at := (e.S + e.At - s.S) / c.speed() // under slow motion the moment lands later in the clip
			ln := prodLine{text: text, pos: e.Pos,
				at: math.Min(math.Max(0, at), math.Max(0, c.length-1))}
			ln.delay = math.Max(narrLead, ln.at)
			wav := a.ttsWav(*e)
			if !exists(wav) {
				if !a.captionsOnly() { // with no voice chosen, this is every line
					a.logfIdle("clip %d: no synthesis for a line — it is captioned only", i+1)
				}
			} else {
				ln.wav = wav
				ln.dur, _ = ffprobeDur(wav)
			}
			c.lines = append(c.lines, ln)
		}
		if len(c.lines) == 0 && len(entries) > 0 && len(matched) == 0 && s.E > s.S {
			a.logfIdle("clip %d at %.0f s has no narration entry — it keeps its own audio", i+1, s.S)
		}
		// make the lines fit where the writer put them: each starts no earlier
		// than the one before it ended, the slot grows first (the placement
		// survives whole -- a sign-off at the end stays at the end), then the
		// schedule slides earlier as one piece, and only as the last resort
		// are the lines distorted
		if len(c.lines) > 0 {
			pack := func() float64 { return narrRun(c.lines, c.tempo) }
			need := pack()
			if need > c.length {
				c.length += math.Min(need-c.length, maxExtend)
				// footage runs out; an insert does not -- a still or a loop is
				// as long as the slot asks for
				if v := c.video; v != nil {
					if end := v.start + v.dur; s.S+c.length*c.speed() > end {
						c.length = (end - s.S) / c.speed()
					}
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
				need = pack()
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
	// the separate recordings, now that every clip's length is settled: growing
	// a slot for its narration lengthens what was heard under it too
	for i := range clips {
		clips[i].mix = clipMixes(clips[i], recs)
	}
	if len(recs) > 0 {
		for _, line := range laneReport(clips, recs) {
			a.logfIdle("%s", line)
		}
	}
	// every insert is fitted into the size the footage comes out at, which can
	// only be known once it is settled which recordings are in (clipBox) --
	// and when the cut names an aspect, that size is the aspect's own frame
	// instead, for footage and inserts alike (outBox)
	cf := a.produceCut()
	fx, aspect := cf.Fx, cf.Aspect
	boxW, boxH := outBox(clips, st, aspect)
	if boxW > 0 {
		// on every clip, not only the inserts that are fitted into it: a text
		// overlay is drawn at the finished frame's size too, and it has to be
		// the same size for footage as for a card or the words land elsewhere
		for i := range clips {
			clips[i].boxW, clips[i].boxH = boxW, boxH
		}
	}
	// the camera: with an aspect, a view or a zoom in play, every footage clip
	// is taken through fxRectAt -- the same function the preview overlay draws
	// -- sampled at every moment anything starts or stops moving (buildCam)
	if fxHasCamera(aspect, fx) && boxW > 0 {
		camFps := st.FPS
		if camFps <= 0 {
			camFps = 30
		}
		for i := range clips {
			c := &clips[i]
			if c.video == nil {
				continue
			}
			sw, sh, err := ffprobeSize(c.video.path)
			if err != nil {
				return fmt.Errorf("%s: the camera needs the source frame size: %w", c.video.base, err)
			}
			span := c.length * c.speed()
			if c.freeze {
				span = 0 // a freeze is one session moment, not a stretch of it
			}
			c.cam = buildCam(fx, aspect, sw, sh, c.video.sessionAt(c.local), span,
				c.speed(), c.length, boxW, boxH, camFps)
			if c.cam != nil && c.cam.maxZoom() > 10 {
				a.logfIdle("clip %d: the zoom goes deeper than ffmpeg's 10× — it is rendered at 10×", i+1)
			}
		}
	}

	// the words: the same session-to-clip mapping the camera just used, so a
	// title and a camera move placed at the same second happen at the same
	// second in the render. An insert or a freeze is one session moment held
	// for a while (span 0), exactly as buildCam treats it.
	if boxW > 0 {
		for i := range clips {
			c := &clips[i]
			span := c.length * c.speed()
			if c.freeze || c.ins != "" {
				span = 0
			}
			c.texts = textCues(fx, c.sessS, span, c.speed(), c.length)
		}
	}

	// the volume effects: the same mapping a third time, and outside the boxW
	// guard above because a gain needs no frame size -- a clip with no picture
	// to put a title on still has sound to turn up.
	for i := range clips {
		c := &clips[i]
		span := c.length * c.speed()
		if c.freeze || c.ins != "" {
			span = 0
		}
		c.gains = gainCues(fx, c.sessS, span, c.speed(), c.length)
	}

	// the stop effects: the same mapping once more, but the overlay is a frame
	// of a recording rather than a drawn card, resolved here where the
	// recordings are known -- a stop's bar can hang into a clip cut from a
	// different recording than the one its frame is in
	for i := range clips {
		c := &clips[i]
		if c.video == nil || c.ins != "" || c.freeze {
			continue // no footage runs under a card or a held frame
		}
		for _, cue := range freezeCues(fx, c.sessS, c.length*c.speed(), c.speed(), c.length) {
			// the scene's own camera (cutVideoOn), not whichever recording
			// happened to be rolling: on a session shot on two cameras,
			// pickVideo froze camera 1's frame over a scene showing camera 2 --
			// a still of something the clip around it never shows
			v := cutVideoOn(segs, vids, cue.fx.T)
			if v == nil {
				a.logfIdle("a stop at %.0f s falls in no recording — its still is skipped", cue.fx.T)
				continue
			}
			sc := stillCue{s: cue.s, e: cue.e, fin: cue.fin,
				fout: cue.fout, path: v.path, at: v.at(cue.fx.T),
				mute: cue.fx.Mute}
			sc.w, sc.h = stillSize(v, c.video)
			c.stills = append(c.stills, sc)
		}
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
				srtTime(cum+ln.delay), srtTime(math.Min(end, cum+c.length)), subText(ln))
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
	var made []clipSize
	// the queue: one task per clip to encode, which is where nearly all of the
	// time goes and the only part of a render anyone counts. It is filled here
	// rather than at the top because what the clips ARE is worked out above.
	a.qPush(trackSTT, len(clips), "clip")
	for i, c := range clips {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.qTake(trackSTT)
		a.prog(trackSTT, base+(0.9-base)*float64(i)/float64(len(clips)), "")
		// numbered by their place in the cut, which is what the concat list and
		// the finished video are in, and then by the second they were shot: a
		// clip folder says where in the session every piece came from. An insert
		// was never shot, so it is named after the file instead.
		stem := fmt.Sprintf("c%03d_%s", i, safeStem(c.ins))
		if c.video != nil {
			stem = fmt.Sprintf("c%03d_%s", i, stampName(c.video.wall+c.local))
		}
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
					srtTime(math.Min(end, c.length)), subText(ln))
			}
			if err := os.WriteFile(cueFile, []byte(one), 0o644); err != nil {
				return err
			}
		}
		if err := a.encodeClip(c, filepath.Join(clipDir, name), cueFile, st); err != nil {
			return fmt.Errorf("clip %d: %w", i+1, err)
		}
		if w, h, err := ffprobeSize(filepath.Join(clipDir, name)); err == nil {
			made = append(made, clipSize{name: name, w: w, h: h})
		}
		// bare name: concat-list entries resolve against the LIST's directory
		list.WriteString(concatLine(name))
	}
	for _, line := range joinMismatch(made) {
		a.logfIdle("%s", line)
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
	a.prog(trackSTT, 0.92, "joining")
	joined := filepath.Join(clipDir, "joined"+ext)
	if err := a.runCmd(ffTool("ffmpeg"), "-v", "error", "-y", "-f", "concat", "-safe", "0",
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
	if err := a.runCmd(ffTool("ffmpeg"), args...); err != nil {
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

// insKind says what an insert file is, which is the only thing that decides how
// ffmpeg has to be asked for it. By extension, deliberately: the alternative is
// probing every asset on every render, and an .svg that ffprobe calls a png
// stream is not more true than the name the user gave it.
func insKind(path string) string {
	file, _ := insSplit(path)
	switch strings.ToLower(filepath.Ext(file)) {
	case ".mp4", ".mkv", ".mov", ".webm", ".avi", ".m4v", ".mpg", ".mpeg", ".ts":
		return "video"
	case ".svg", ".svgz":
		return "svg"
	case ".mp3", ".wav", ".ogg", ".oga", ".flac", ".m4a", ".aac", ".opus":
		return "audio"
	default:
		return "still"
	}
}

// clipInput builds the ffmpeg input arguments for a clip's picture, and says
// whether that input carries sound. Three shapes come out of here: a stretch of
// a recording, a held still, and a baked SVG sequence.
func (a *App) clipInput(c prodClip, st prodSettings) ([]string, bool, error) {
	if c.ins == "" {
		if c.freeze {
			// one frame is all that is kept (trim=end_frame=1 in encodeClip),
			// so half a second of input is plenty -- and a held frame has no
			// sound of its own, so the anullsrc path supplies the silence
			return []string{
				"-ss", fmt.Sprintf("%.3f", math.Max(0, c.local)),
				"-t", "0.5", "-i", c.video.path,
			}, false, nil
		}
		// input seconds: a slowed clip reads rate·length of footage and
		// stretches it to length on the way out.
		//
		// Its own sound is that input's FIRST track and no other, because it is
		// read straight off the picture's input ([0:a], encodeClip) -- so a row
		// that asked for the second track and not the first has nothing to put
		// there, and that track reaches the clip as a lane in the mix like any
		// other recording (ownTrack, cut_tracks.go).
		return []string{
			"-ss", fmt.Sprintf("%.3f", math.Max(0, c.local)),
			"-t", fmt.Sprintf("%.3f", c.length*c.speed()), "-i", c.video.path,
		}, hasAudioStream(c.video.path) && !c.mute && !laneQuiet(c.quiet, c.video.base) &&
			ownTrack(a.snappedTracks(), c.video.path), nil
	}
	rate := st.FPS
	if rate <= 0 {
		rate = svgFPS
	}
	// everything from here on wants a file to open, and an insert may name one
	// with a card's parameters after it (see svgcards.go)
	file, params := insSplit(c.ins)
	switch insKind(c.ins) {
	case "video":
		// -t on the input, so a long file is trimmed before it is decoded; the
		// short case is tpad's and the output -t's, over in encodeClip
		return []string{"-t", fmt.Sprintf("%.3f", c.length), "-i", file},
			hasAudioStream(file) && !c.mute, nil

	case "svg":
		src, note, err := insSVG(c.ins)
		if err != nil {
			return nil, false, err
		}
		if note != "" {
			a.logfIdle("%s: %s", insBase(c.ins), note)
		}
		if !svgAnimated(src) {
			if svgHasCSSAnimation(src) {
				a.logfIdle("%s: a CSS animation with no @keyframes in the file — drawn as a still",
					insBase(c.ins))
			}
			// a static card with parameters is no longer the file on disk, so it
			// is written out for ffmpeg to open; without them the asset is opened
			// where it lies. Either way it is a still like any other from here.
			if len(params) > 0 {
				file = filepath.Join(a.produceDir(), "clips",
					fmt.Sprintf("c%03d_%s.svg", c.idx, safeStem(c.ins)))
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					return nil, false, err
				}
				if err := os.WriteFile(file, src, 0o644); err != nil {
					return nil, false, err
				}
			}
			break
		}
		// baked beside the clips rather than next to the asset: this is derived
		// from the cut's slot length, so it belongs to the render and goes when
		// the render does
		dir := filepath.Join(a.produceDir(), "clips",
			fmt.Sprintf("c%03d_%s.frames", c.idx, safeStem(c.ins)))
		pat, n, err := bakeSVG(src, dir, rate, c.length)
		if err != nil {
			return nil, false, fmt.Errorf("animating %s: %w", insBase(c.ins), err)
		}
		a.logfIdle("%s: %d frames baked at %g fps", insBase(c.ins), n, rate)
		return []string{"-framerate", fmt.Sprintf("%g", rate), "-i", pat}, false, nil
	}
	// a still, and a static SVG that fell through to here: one frame held for
	// the whole slot. -framerate on the input rather than -r, so the held frames
	// are generated at the output's rate instead of one frame being stretched
	// into a stream nothing downstream can cut at.
	return []string{"-loop", "1", "-framerate", fmt.Sprintf("%g", rate),
		"-t", fmt.Sprintf("%.3f", c.length), "-i", file}, false, nil
}

// safeStem is a file's name reduced to what a clip folder can be named after:
// no separators, no spaces, no surprises for a shell that never sees it anyway.
func safeStem(path string) string {
	file, _ := insSplit(path) // a card's parameters are not part of its name
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "insert"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// clipBox is the frame every insert has to fill: the size the footage clips
// come out at, since the join is a stream copy and a clip of another size is
// simply refused. Taken from the first recording actually used rather than from
// the settings, because the settings only name a height -- the width follows the
// footage's aspect, and an insert has to follow it too.
//
// A cut of nothing but inserts has no footage to ask, so it falls back to the
// output height at 16:9, which is the shape a video is unless told otherwise.
func clipBox(clips []prodClip, st prodSettings) (int, int) {
	for _, c := range clips {
		if c.video == nil {
			continue
		}
		w0, h0, err := ffprobeSize(c.video.path)
		if err != nil {
			continue
		}
		return outSize(w0, h0, st.Height)
	}
	return outSize(0, 0, st.Height)
}

// footageSize is the frame the footage was recorded at, as the Cut page has
// already probed it. The Produce page asks so that it can say what the render
// will come out at without opening a file in a label handler; the render itself
// probes the clip it is actually about to encode (clipBox).
func (a *App) footageSize() (int, int, bool) {
	if a == nil || a.ed == nil {
		return 0, 0, false
	}
	for _, v := range a.ed.vids {
		if v.w > 0 && v.h > 0 {
			return v.w, v.h, true
		}
	}
	return 0, 0, false
}

// outSize is the frame the render comes out at, given the footage's own size
// and the tier that was asked for. The tier names the SHORT side of the frame:
// the height of a wide video -- which is all "1080p" has ever meant there --
// and the width of a tall one, where 1080p is 1080×1920, the size a Short is
// uploaded at, not a 608-wide strip. "original" (0) keeps the footage's own
// frame. Both sides rounded even -- an insert one pixel off is a clip a
// concat will not take.
//
// Its own function, over sizes that are already known, so the Produce page can
// say the frame in pixels without opening a file. "original" on a 4K screen
// capture reads like a setting and lands as a 700 MB upload, and the only
// moment that is worth knowing is before the encode, not after it.
func outSize(w0, h0, height int) (int, int) {
	if w0 <= 0 || h0 <= 0 {
		h := height
		if h <= 0 {
			h = 1080 // nothing probed and nothing chosen
		}
		return int(math.Round(float64(h)*16/9/2)) * 2, h - h%2
	}
	k := 1.0 // original: the footage's own frame
	if height > 0 {
		k = float64(height) / float64(min(w0, h0))
	}
	return int(math.Round(float64(w0)*k/2)) * 2, int(math.Round(float64(h0)*k/2)) * 2
}

// clipMixes is the stretch of every separate recording that was running while
// this clip was, in the clip's own time.
//
// The footage is the master here exactly as it is on the cut page: the clip
// occupies a stretch of the session clock, and a recording is heard for the
// part of that stretch it overlaps -- from its own middle if it started first
// (which is the usual case: the recorder is running before the capture card
// is), and after a wait if it started later. A recording that was not running
// at all is not in the list, so a clip is never silently padded with somebody
// else's audio.
//
// Inserts are left alone. A card is not a moment of the session -- it is time
// added to the cut -- so there is no stretch of any recording that belongs
// under it, and dropping the room audio in there would play a sentence that
// was said somewhere else. A freeze is the same kind of thing: a held frame is
// added time, not a stretch of the session, so nothing was recorded under it.
//
// A slowed clip covers length·rate session seconds, and what was heard in
// them is stretched to match the picture (atempo, in encodeClip) -- so the
// numbers here are: the session span through the rate for where the clip
// ends, the placement divided by it (a recording that came in 3 s into the
// footage comes in 6 s into the half-speed clip), and dur left in file
// seconds, because it is an input trim and the stretch happens in the graph.
func clipMixes(c prodClip, recs []tlAudio) []prodMix {
	var out []prodMix
	if c.dropLane != "" && c.snd != "" {
		// the inserted sound goes in the mix where the recording it replaced
		// would have been, rather than in the capture's slot: that is the whole
		// difference between "these seconds sound like the file" and "this one
		// recording sounds like the file". at is 0 because a sound laid over
		// footage starts where the footage does, by construction.
		out = append(out, prodMix{base: baseName(c.snd), path: c.snd,
			at: 0, ss: c.sndAt, dur: c.length})
	}
	if len(recs) == 0 {
		return out
	}
	for _, au := range recs {
		if au.base == c.dropLane {
			continue // this one is what the inserted sound was put in place of
		}
		if laneQuiet(c.quiet, au.base) {
			continue // and this one the scene was told not to hear
		}
		t0, t1 := laneOverlap(c, au)
		if t1-t0 < laneMinMix { // nothing worth an input, and a 0 s one ffmpeg refuses
			continue
		}
		out = append(out, prodMix{base: au.base, path: au.path, track: au.track,
			at: (t0 - c.sessS) / c.speed(), ss: t0 - au.start, dur: t1 - t0})
	}
	return out
}

// laneMinMix is the shortest overlap worth an ffmpeg input -- and a zero-length
// one it refuses outright.
const laneMinMix = 0.1

// laneReport is what the run has to say about each recording: whether it is in
// the video at all, and when it is not, which of the two reasons it is out for.
// Per recording rather than per clip -- a hundred-clip cut would otherwise bury
// the run in a line each, and the only thing worth saying about a recording is
// whether it reached the render.
//
// The two ways to be out are not the same news, and telling them apart is the
// whole point of this being a function of its own. A recording that was running
// at another time of day is a placement to go and look at; one the SCENES
// silenced -- or whose sound a card was dropped over -- is the cut doing
// exactly what it was told, which is what a split-off narrator track is. Both
// used to print "was not running while any clip was", a sentence that sent you
// hunting a timeline problem that was not there.
//
// Which reason a clip left it out for is deliberately not asked: whatever
// clipMixes decided is the answer, so a lane the mix drops for a reason added
// later still lands in the right sentence instead of quietly in the wrong one.
func laneReport(clips []prodClip, recs []tlAudio) []string {
	var out []string
	for _, au := range recs {
		under, past := 0, 0
		for i := range clips {
			mixed := false
			for _, m := range clips[i].mix {
				mixed = mixed || m.base == au.base
			}
			// three ways for a clip to stand to a recording, and every clip
			// is exactly one of them. The overlap is read off the same
			// function the mix is built from, so the count and the mix
			// cannot disagree about which clips a track was running under.
			if t0, t1 := laneOverlap(clips[i], au); mixed {
				under++
			} else if t1-t0 >= laneMinMix {
				past++
			}
		}
		switch {
		case under > 0:
			out = append(out, fmt.Sprintf("%s is mixed into %d of the %d clips",
				au.base, under, len(clips)))
		case past > 0:
			out = append(out, fmt.Sprintf("%s runs under %d clip(s) and every one of them leaves it out — it is not in the render",
				au.base, past))
		default:
			out = append(out, fmt.Sprintf("%s was not running while any clip was — it is not in the render",
				au.base))
		}
	}
	return out
}

// laneOverlap is the session stretch a clip and a recording were both running
// in, empty when they were not. Its own function because two places ask: the
// mix, which puts the overlapping seconds in, and the run's report, which has
// to tell a recording that was somewhere else apart from one every scene
// silenced -- and a second copy of these four lines could disagree with the
// first about which of the two a track was.
//
// A freeze and a card have no overlap with anything by construction: a held
// frame and an insert are time ADDED to the session, not a stretch of it, so
// nothing was recorded underneath them.
func laneOverlap(c prodClip, au tlAudio) (float64, float64) {
	if c.freeze || c.noLanes {
		return 0, 0
	}
	s0 := c.sessS
	s1 := s0 + c.length*c.speed()
	return math.Max(s0, au.start), math.Min(s1, au.start+au.dur)
}

// narrRun is the slot a clip's narration asks for: where the last line stops,
// plus the moment of air the render leaves after it. It is the number every
// decision about a clip's length is made against.
func narrRun(lines []prodLine, tempo float64) float64 {
	return packLines(lines, tempo) + narrTail
}

// packLines lays a clip's lines out in its slot and answers where the last one
// stops. Each starts where the writer placed it, but never before the line
// above has finished and a breath has passed -- so an overlong line pushes
// everything under it later, and it is the LAST line that falls off the end of
// the clip. The delays are written back into the slice, because that schedule
// is what the render feeds adelay.
//
// A function rather than the closure it used to be, because the Narrate page
// has to be able to ask the same question. Its own per-line ⚠ answers a
// different one -- "has THIS line room before the next" -- and cannot see the
// pushing, which is exactly what the render's "does not fit where it was
// placed" is complaining about (clipOverrun, narrate.go).
func packLines(lines []prodLine, tempo float64) float64 {
	if tempo <= 0 {
		tempo = 1 // a clip built by hand, and every arithmetic here divides
	}
	prev := 0.0
	for k := range lines {
		d := math.Max(narrLead, lines[k].at)
		if d < prev {
			d = prev
		}
		lines[k].delay = d
		prev = d + lines[k].dur/tempo + narrGap
	}
	return prev - narrGap
}

// delayMS is a line's start in the units adelay actually reads: whole
// milliseconds, never below the lead-in. Rounded rather than truncated, so a
// placement is out by at most half a millisecond either way.
func delayMS(delay float64) int {
	return int(math.Round(math.Max(narrLead, delay) * 1000))
}

// encodeClip cuts one slot out of its recording -- or renders an insert into
// one -- and mixes the narration in.
func (a *App) encodeClip(c prodClip, out, cueFile string, st prodSettings) error {
	src, srcSound, err := a.clipInput(c, st)
	if err != nil {
		return err
	}
	args := append([]string{"-v", "error", "-y"}, src...)
	game := "0:a"
	switch {
	case c.snd != "" && c.dropLane == "":
		// the inserted sound goes exactly where the silence would: input 1,
		// which keeps every index downstream where it was. The recording's own
		// sound is simply never mapped -- overwriting is replacing, not mixing.
		//
		// A copied stretch of a lane starts inside its file rather than at the
		// top of it, and -ss before -i is how that is asked: the trim is the
		// input's, so -t below still measures the slot and not the file.
		if c.sndAt > 0 {
			args = append(args, "-ss", fmt.Sprintf("%.3f", c.sndAt))
		}
		// the trim is in SOURCE seconds and the slot is in output ones, so a
		// clip played fast eats more of the file than it is long: without the
		// speed the sound would run out partway and apad would finish the slot
		// in silence. Slower than 1 takes less and the extra is simply
		// decoded and dropped, which costs nothing worth a branch.
		args = append(args, "-t", fmt.Sprintf("%.3f", c.length*math.Max(1, c.speed())),
			"-i", c.snd)
		game = "1:a"
	case !srcSound:
		args = append(args, "-f", "lavfi", "-t", fmt.Sprintf("%.3f", c.length),
			"-i", "anullsrc=channel_layout="+audLayout(st)+":sample_rate=48000")
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
	// the separate recordings last, so adding one cannot move a voice input out
	// from under the index the narration filters were written against. Seeked
	// and trimmed on the input rather than in the graph: this is one stretch of
	// a file that may be an hour long, and decoding all of it to throw away the
	// rest is the difference between a render and an afternoon.
	mixBase := voiceBase + len(spoken)
	for _, m := range c.mix {
		args = append(args, "-ss", fmt.Sprintf("%.3f", math.Max(0, m.ss)),
			"-t", fmt.Sprintf("%.3f", m.dur), "-i", m.path)
	}
	// the overlays, last, for the same reason the recordings are last: a new
	// one must not move an index an earlier filter was written against. A
	// title is a still SVG the size of the finished frame, written here
	// (fxtext.go); a drawing is the user's own file (fxsvg.go). Either way an
	// SVG looped for the clip's length -- ffmpeg reads SVG already, it is how
	// the cards get in.
	txtBase := mixBase + len(c.mix)
	svgFps := st.FPS
	if svgFps <= 0 {
		svgFps = 30
	}
	for _, cue := range c.texts {
		file := cue.fx.Src
		if cue.fx.Kind != "svg" {
			file = fmt.Sprintf("%s_t%02d.svg", strings.TrimSuffix(out, filepath.Ext(out)), cue.idx)
			if err := os.WriteFile(file, textSVG(cue.fx, c.boxW, c.boxH), 0o644); err != nil {
				return err
			}
		}
		// a hair longer than the slot, so the last frame of the clip still has
		// something to composite; the overlay's eof_action=pass covers the rest
		args = append(args, "-loop", "1", "-framerate", fmt.Sprintf("%g", svgFps))
		if cue.fx.Kind == "svg" {
			// a vector rendered at the size it is used at rather than at the
			// size its document happens to declare (fxsvg.go); a title's file
			// is already written the frame's size and wants none of this
			if _, _, bw, bh, ok := svgFitPx(cue.fx, c.boxW, c.boxH); ok {
				args = append(args, "-width", strconv.Itoa(bw), "-height", strconv.Itoa(bh),
					"-keep_ar", "1")
			}
		}
		args = append(args, "-t", fmt.Sprintf("%.3f", c.length+0.2), "-i", file)
	}
	// the stop stills, after everything else for the same index-stability
	// reason: each is one decoded frame of its recording, cut at the stop's own
	// second. The half-second input window is all trim=end_frame=1 below needs,
	// and it spares decoding the hour of recording behind it.
	stillBase := txtBase + len(c.texts)
	for _, sc := range c.stills {
		args = append(args, "-ss", fmt.Sprintf("%.3f", sc.at), "-t", "0.5", "-i", sc.path)
	}

	var vf []string
	// the clock first: setpts stretches or squeezes the timestamps, and
	// everything after it -- the fps grid, the camera -- works in that time,
	// which is the clip's own output time
	if c.ins == "" && !c.freeze && c.speed() != 1 {
		vf = append(vf, fmt.Sprintf("setpts=PTS/%g", c.speed()))
	}
	if c.freeze {
		// one decoded frame, held: everything after the first frame is cut and
		// the frame is cloned out a hair past the slot (the output -t below is
		// what makes the length exact)
		vf = append(vf, "trim=end_frame=1,setpts=PTS-STARTPTS",
			fmt.Sprintf("tpad=stop_mode=clone:stop_duration=%.3f", c.length+0.2))
	}
	// the fps FILTER is what pins a stream to a rate: it duplicates and drops
	// frames until they land on that grid, whether the source was faster or
	// slower. Under VFR the rate is a ceiling instead, which is the encoder's
	// job and not a filter's (see fpsArgs).
	if st.FPS > 0 && !st.VFR {
		vf = append(vf, fmt.Sprintf("fps=%g", st.FPS))
	} else if c.cam != nil && !c.cam.static() {
		// zoompan places its window per frame, so a moving camera needs a grid
		// even when the output is VFR; this clip gets one at the camera's rate
		vf = append(vf, fmt.Sprintf("fps=%g", c.cam.fps))
		a.logfIdle("%s: the moving camera needs a fixed frame rate — this clip is %g fps",
			c.name(), c.cam.fps)
	}
	// the stop stills go on between here and the camera: in the clip's own
	// time but in the SOURCE frame, so a zoom or a view crops the still
	// exactly as it crops the footage running under it (the chain is spliced
	// in where fc is assembled below, at this point of the filter list)
	clock := len(vf)
	// what the bare parts of the frame get instead of black. Emitted below,
	// at the same point in the graph the pad it replaces sat at: after the
	// stop stills (a still is the recording's own frame and is padded with
	// it) and before the crop the camera does.
	var bd bdrop
	switch {
	case c.ins != "":
		// An insert has to come out at exactly the frame size the footage clips
		// do, because the join is a stream copy: the concat demuxer refuses a
		// clip whose dimensions differ from the ones before it, and there is no
		// re-encode later to paper over it. So it is fitted rather than scaled --
		// scaled down until it fits, then centred on a black frame, which keeps a
		// square diagram square and a 4:3 card 4:3 inside a 16:9 video.
		if c.boxW > 0 && c.boxH > 0 {
			bd = bdrop{w: c.boxW, h: c.boxH, fit: true}
		}
		// a video insert shorter than its slot would end the clip early and take
		// the narration written over it with it; the last frame is held instead
		vf = append(vf, fmt.Sprintf("tpad=stop_mode=clone:stop_duration=%.3f", c.length))
	case c.cam != nil:
		// the camera looking past the edge of the recording: the same padded
		// frame it always measured itself on, with the border filled in
		if pw, ph, pl, pt, ok := c.cam.padBox(); ok {
			bd = bdrop{w: pw, h: ph, x: pl, y: pt}
		}
		vf = append(vf, c.cam.chainOn(bd.on())...)
	case c.boxW > 0 && c.boxH > 0:
		// Plain footage, and it comes out at exactly the finished frame -- not
		// at whatever this particular recording happens to be. The join is a
		// stream copy, so a clip whose size differs from the one before it is
		// not something the concat demuxer will take (the insert branch above
		// says the same thing for the same reason), and a second camera at
		// another size or another shape is exactly how that happens.
		//
		// Fitted rather than stretched, and so the same treatment the edges of
		// an insert get: a 4:3 webcam among 16:9 gameplay keeps its shape on a
		// blurred blow-up of itself instead of being pulled wide. Footage
		// already of the frame's shape -- which is every ordinary session --
		// takes the plain scale instead: the backdrop under it would be
		// covered to the last pixel, and a split, a blow-up and a gaussian
		// blur per frame is not a free way to render something nothing sees.
		if fitsFrame(c.video, c.boxW, c.boxH) {
			vf = append(vf, fmt.Sprintf("scale=%d:%d", c.boxW, c.boxH))
		} else {
			bd = bdrop{w: c.boxW, h: c.boxH, fit: true}
		}
	case st.Height > 0:
		// no box was worked out for this clip -- which the render always does
		// (clipBox), so this is a clip built by hand -- and the height that was
		// asked for is then the best answer available
		vf = append(vf, fmt.Sprintf("scale=-2:%d", st.Height))
	}
	bd.bare = st.Bare // black edges or a blurred blow-up: the region is the same
	vf = append(vf, "setsar=1")
	if cueFile != "" {
		vf = append(vf, "subtitles="+ffEscape(cueFile))
	}
	vlab := "v"
	head := "[0:v]"
	fc := ""
	if len(c.stills) > 0 || bd.on() {
		// the clock and the fps grid run on their own, so that the stills and
		// the backdrop can be spliced in between them and the camera
		pre := strings.Join(vf[:clock], ",")
		if pre == "" {
			pre = "null"
		}
		fc = head + pre + "[vpre];"
		head = "[vpre]"
		vf = vf[clock:]
	}
	if len(c.stills) > 0 {
		// each still is the same one-frame hold an audio insert's picture uses,
		// on an overlay input: cut to its first frame, cloned out past the
		// slot, faded on its alpha exactly as a title is (textChain), and laid
		// over the running footage only while its bar says so
		cur := strings.Trim(head, "[]")
		for k, sc := range c.stills {
			fc += fmt.Sprintf("[%d:v]trim=end_frame=1,setpts=PTS-STARTPTS,"+
				"tpad=stop_mode=clone:stop_duration=%.3f,format=rgba",
				stillBase+k, c.length+0.2)
			if sc.w > 0 && sc.h > 0 {
				// fitted and centred rather than stretched, and padded with
				// transparency (#00000000, not a black the toggle would owe an
				// answer for) so the running footage shows around a held frame
				// of another shape instead of a black border on a moving picture
				fc += fmt.Sprintf(",scale=%d:%d:force_original_aspect_ratio=decrease,"+
					"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=#00000000", sc.w, sc.h, sc.w, sc.h)
			}
			if sc.fin > 0 {
				fc += fmt.Sprintf(",fade=t=in:st=%.3f:d=%.3f:alpha=1", sc.s, sc.fin)
			}
			if sc.fout > 0 {
				fc += fmt.Sprintf(",fade=t=out:st=%.3f:d=%.3f:alpha=1", sc.e-sc.fout, sc.fout)
			}
			next := fmt.Sprintf("vfz%d", k)
			fc += fmt.Sprintf("[fz%d];[%s][fz%d]overlay=x=0:y=0:eof_action=pass:enable=between(t\\,%.3f\\,%.3f)[%s];",
				k, cur, k, sc.s, sc.e, next)
			cur = next
		}
		head = "[" + cur + "]"
	}
	if bd.on() {
		sub, lab := bd.chain(strings.Trim(head, "[]"), 0)
		fc += sub
		head = "[" + lab + "]"
	}
	fc += head + strings.Join(vf, ",")
	if len(c.texts) > 0 {
		// the words go on AFTER the camera and the subtitles: a title is put on
		// the finished frame, and it holds still while the camera moves under it
		fc += "[vcam];"
		chain, last := textChain(c.texts, "vcam", txtBase, c.boxW, c.boxH)
		fc += chain
		vlab = last
	} else {
		fc += "[v];"
	}
	// footage off its own clock: everything recorded with the picture goes with
	// it, pitch held (atempoChain) -- the capture's own sound here, each
	// separate recording where it is prepared below
	slow := ""
	if c.ins == "" && !c.freeze && c.speed() != 1 {
		slow = atempoChain(c.speed())
		if srcSound {
			fc += fmt.Sprintf("[%s]%s%s[slowgm];", game, audFmt(st), slow)
			game = "slowgm"
		}
	}
	// a stop told to take the sound with it. The window is in the clip's own
	// output seconds, which is what the still overlay above is enabled on and
	// what the sound reads as too once the atempo above has run, so the
	// silence lands exactly under the held frame.
	if mute := stillMute(c.stills); mute != "" {
		fc += fmt.Sprintf("[%s]%svolume=0:enable='%s'[hush];", game, audFmt(st), mute)
		game = "hush"
	}
	if c.snd != "" {
		// a file shorter than its slot must not end the clip's sound early --
		// the join is a stream copy, and a short audio track in one clip puts
		// every clip after it out of step with its picture. Padded with silence
		// out past the slot; the output -t below is what makes the length exact.
		fc += fmt.Sprintf("[%s]%s,apad[snd];", game, audFmt(st))
		game = "snd"
	}
	// The bed is everything that was there: the picture's own sound, plus every
	// separate recording that was running under it. They are one thing from
	// here on -- ducked together under the narration, or heard as they are when
	// there is none -- because they are the same moment recorded twice.
	//
	// duration=first pins the mix to the picture's sound, so a recording that
	// stops in the middle of the clip leaves the clip its full length instead of
	// ending it early, and normalize=0 keeps the game at the level it was
	// recorded at rather than halving it for the company.
	if len(c.mix) > 0 {
		seps := ""
		for k, m := range c.mix {
			fc += fmt.Sprintf("[%s]%s%s", trackOf(mixBase+k, m.track), audFmt(st), slow)
			// whole milliseconds, and only when there is a wait to honour: this
			// is the same integer adelay the narration learned to hand over
			if ms := int(math.Round(m.at * 1000)); ms > 0 {
				fc += fmt.Sprintf(",adelay=%d:all=1", ms)
			}
			fc += fmt.Sprintf("[sep%d];", k)
			seps += fmt.Sprintf("[sep%d]", k)
		}
		fc += fmt.Sprintf("[%s]%s[gm];", game, audFmt(st))
		fc += fmt.Sprintf("[gm]%samix=inputs=%d:duration=first:normalize=0[bed];",
			seps, 1+len(c.mix))
		game = "bed" // everything downstream ducks and mixes the bed, not the capture
	}
	// the volume effects, on everything that was actually there and nothing
	// that was added afterwards: after the bed, so a boost lifts the capture
	// and the separate recordings together the way one hand on one fader
	// would, and before the narration, which is written on this page and has
	// its own level (GameVol) rather than a cut effect's say over it.
	if fx, lab := gainChain(c.gains, game); fx != "" {
		fc += fx
		game = lab
	}
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
			fc += fmt.Sprintf("[%d:a]atempo=%.3f,aresample=48000,%s,adelay=%d:all=1[nr%d];",
				voiceBase+k, math.Max(0.5, c.tempo), voicePan(st), delayMS(ln.delay), k)
			nrs += fmt.Sprintf("[nr%d]", k)
		}
		fc += fmt.Sprintf("[%s]volume=%.3f,aresample=48000[bg];", game, st.GameVol)
		fc += fmt.Sprintf("[bg]%samix=inputs=%d:duration=first:normalize=0,", nrs, 1+len(spoken)) +
			clipCeil + "," + audFmt(st) + "[a]"
	} else {
		fc += fmt.Sprintf("[%s]%s,%s[a]", game, clipCeil, audFmt(st))
	}
	args = append(args, "-filter_complex", fc, "-map", "["+vlab+"]", "-map", "[a]")
	if c.ins != "" || c.snd != "" || c.freeze || c.speed() != 1 {
		// the slot is what decides these clips' length, not the input: a still
		// has no length of its own, a looped animation has too much, tpad gave
		// a short video (and every freeze) an endless tail, and a rated clip's
		// stretch only lands on the slot to rounding. This is what stops them.
		args = append(args, "-t", fmt.Sprintf("%.3f", c.length))
	}
	args = append(args, fpsArgs(st)...)
	args = append(args, codecArgs(st)...)
	args = append(args, audioArgs(st)...)
	args = append(args, out)
	return a.runCmd(ffTool("ffmpeg"), args...)
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

// voicePan spreads the narration -- which is one channel however it was
// recorded -- over the layout the video is being made in. Said out loud rather
// than left to the encoder, because a mono source dropped into a stereo mix
// without it lands in the left speaker only.
func voicePan(st prodSettings) string {
	if st.Mono {
		return "pan=mono|c0=c0"
	}
	return "pan=stereo|c0=c0|c1=c0"
}

func audioArgs(st prodSettings) []string {
	br := strconv.Itoa(st.AudioKbps) + "k"
	// -ac as well as the aformat above: the filter graph decides the layout for
	// every clip that goes through it, and this decides it for anything that
	// does not, so the two can never disagree inside one concat list.
	ac := []string{"-ac", "2"}
	if st.Mono {
		ac[1] = "1"
	}
	if st.Container == "webm" {
		return append([]string{"-c:a", "libopus", "-b:a", br}, ac...)
	}
	return append([]string{"-c:a", "aac", "-b:a", br}, ac...)
}

// insClip is the whole of what an insert becomes: its own picture, stretched to
// exactly the slot the cut gave it, and -- when it covers the picture alone --
// the sound of the recording it was laid over. The note is soundUnder's, to be
// logged by the caller, which is the only part of this that needs the App.
// copyClip is what a pasted stretch of footage becomes. Mute here is a copy
// taken at the picture-alone scope: the frames were copied WITHOUT the sound
// filmed with them, so neither the recording's own track nor the lanes that
// were running with it come along -- which is the paste going silent, not a
// failure. Both have to be said, because they are two different silences: mute
// stops the recording's own track reaching the mixer, noLanes stops every
// separate recording being mixed in under it.
func copyClip(i int, s cutSeg, from float64, v *tlVideo) prodClip {
	return prodClip{idx: i, video: v, local: v.at(from), tempo: 1, rate: 1,
		length: s.length(), mute: s.Mute, noLanes: s.Mute}
}

// sndClip is a sound laid over the session: the picture is the session's own --
// held on one frame when the file is spliced in, kept running when it covers a
// selection -- and the file takes the place of what the scope settled when it
// was laid down (cutSeg.Lane). Unnamed, it stands in for everything audible,
// which is what overwriting the sound has always meant. Named, it stands in for
// that one recording and the rest play on -- and the capture's own track is a
// lane on the cut page like any other, so naming it is simply not naming a
// separate recording, and laneRecorded is what tells the two apart.
func sndClip(i int, s cutSeg, path string, v *tlVideo, recs []tlAudio) prodClip {
	c := prodClip{idx: i, video: v, local: v.at(s.S), tempo: 1, rate: 1,
		length: s.length(), freeze: s.spliced(), snd: path, sndAt: s.Ss,
		noLanes: s.spliced() || s.Lane == ""}
	if !s.spliced() && laneRecorded(recs, s.Lane) {
		c.dropLane = s.Lane
	}
	return c
}

func insClip(i int, s cutSeg, file string, vids []tlVideo) (prodClip, string) {
	c := prodClip{idx: i, ins: file, tempo: 1, rate: 1, length: s.length(), mute: s.Mute,
		noLanes: !s.keepsSoundUnder()}
	var note string
	c.snd, c.sndAt, note = soundUnder(s, vids)
	return c, note
}

// laneRecorded says whether a named lane is one of the separately-recorded
// files. The other two answers a lane name can have are the capture's own
// track -- a lane on the cut page, drawn from the video and not a recording of
// its own (masterLanes) -- and a recording that has since left the session. Both
// come out false here, and both mean the same thing to the render: there is no
// mix input to take out, so an inserted sound stands in for the capture's slot.
func laneRecorded(recs []tlAudio, base string) bool {
	for _, au := range recs {
		if au.base == base {
			return true
		}
	}
	return false
}

// soundUnder is where an insert covering the picture ALONE gets its sound: the
// recording it is drawn over, at the second it covers. The picture is the
// file's and what is heard is the session's, which is the exact mirror of an
// audio insert -- so it rides the very same input slot, with the recording in
// the sound file's place (encodeClip, case c.snd != "").
//
// Everything else answers nothing, and for two different reasons worth keeping
// apart. An ordinary insert brings its own sound and has no use for this. A
// SPLICED muted one is the other reading of the same flag: the cut was opened
// for it, so there is nothing underneath to keep and the slot comes out silent
// -- which is not a failure and does not get a note.
//
// The note is for the two ways this can be asked for and not delivered: seconds
// that fall in no recording, and a recording with no sound in it. Both come out
// silent, both look like the flag was ignored, and neither is something the eye
// can find on the timeline.
func soundUnder(s cutSeg, vids []tlVideo) (path string, at float64, note string) {
	if !s.keepsSoundUnder() {
		return "", 0, ""
	}
	v := pickVideoOn(vids, s.Cam, s.S)
	switch {
	case v == nil:
		return "", 0, fmt.Sprintf("covers the picture at %.0f s and keeps what is heard, "+
			"but those seconds fall in no recording — it plays silent", s.S)
	case !hasAudioStream(v.path):
		return "", 0, fmt.Sprintf("covers the picture at %.0f s and keeps what is heard, "+
			"but %s has no sound — it plays silent", s.S, v.base)
	}
	return v.path, v.at(s.S), ""
}

// matchEntries finds the narration written for a segment -- every line of it,
// in placement order, since a clip may carry more than one. The entries carry
// the clip's own times, so they usually match to the decimal -- but a cut
// edited after narrating can shift underneath, and a line silently dropped
// from the render is the worst possible failure here. So: real overlap is
// enough, merely touching is not.
// spokenHere is what to add to the message about a clip being dropped, when
// there are narration lines written on it. The lines go with the clip -- they
// are attached further down, past the drop -- and a sentence disappearing out
// of the finished video is worth more than the half-second of footage it was
// written over. Empty for a clip nobody wrote on, which is the ordinary case.
func spokenHere(entries []narrEntry, s cutSeg) string {
	n := 0
	for _, e := range matchEntries(entries, s) {
		if strings.TrimSpace(e.Text) != "" {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return " — the narration line written on it is dropped with it"
	default:
		return fmt.Sprintf(" — the %d narration lines written on it are dropped with it", n)
	}
}

func matchEntries(entries []narrEntry, s cutSeg) []*narrEntry {
	var out []*narrEntry
	for i := range entries {
		e := &entries[i]
		if s.E <= s.S {
			// a card or a freeze occupies no span of the session, so overlap
			// cannot find its lines: they are the entries written exactly on it
			if math.Abs(e.S-s.S) <= 0.05 && math.Abs(e.E-s.E) <= 0.05 {
				out = append(out, e)
			}
			continue
		}
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

// subText is a caption as the .srt carries it: wrapped, and prefixed with its
// placement when that is not the bottom. {\an8} (top-center) and {\an5} (dead
// center) are ASS override tags in an srt file -- nonstandard but the one
// spelling everything honors: libass reads them when the burn happens
// (encodeClip's subtitles filter), and so do the usual players for a sidecar
// or muxed track. A bottom line carries no tag; the bottom is where subtitles
// already live.
func subText(ln prodLine) string {
	switch ln.pos {
	case "top":
		return `{\an8}` + wrapSub(ln.text)
	case "center":
		return `{\an5}` + wrapSub(ln.text)
	}
	return wrapSub(ln.text)
}

// ffEscape quotes a path for use inside a filtergraph argument, where ':' and
// ',' separate options and '\' escapes.
func ffEscape(p string) string {
	return strings.NewReplacer(
		`\`, `\\`, `:`, `\:`, `'`, `\'`, `[`, `\[`, `]`, `\]`, `,`, `\,`,
	).Replace(p)
}
