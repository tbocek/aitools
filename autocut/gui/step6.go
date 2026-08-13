package main

// Step 6: Produce. Cuts every clip from ITS OWN source recording, lays the
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
// step6/clips/c000.<ext>   per-clip encodes
// step6/final.srt          subtitles on the produced timeline
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
	narrLead  = 0.3  // narration starts this far into a clip
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
	crf, gvol                                        *gtk.Scale
	outLbl                                           *gtk.Label
	outFile                                          string
	outAuto                                          bool // still the default -- follows the output folder
	info                                             *gtk.Label
	player                                           *Player
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
		return prodSettings{Container: "mp4", Codec: "h264", CRF: 20, Preset: "veryfast",
			FPS: 30, AudioKbps: 192, GameVol: 0.22, Subs: "sidecar"}
	}
	st := prodSettings{
		Container: pickText(p.container, prodContainers),
		Codec:     pickText(p.codec, prodCodecs),
		CRF:       int(math.Round(p.crf.Value())),
		Preset:    pickText(p.preset, prodPresets),
		Height:    atoiOr(pickText(p.height, prodHeights), 0),
		FPS:       float64(atoiOr(pickText(p.fps, prodFPS), 0)),
		AudioKbps: atoiOr(pickText(p.abr, prodABR), 192),
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

// ---- page -------------------------------------------------------------------

func (a *App) buildStep6() gtk.Widgetter {
	p := &producer{a: a}
	a.prod = p
	p.player = a.player

	expl := gtk.NewLabel("Renders the final video: every clip is cut from its own recording, the " +
		"narration is laid over ducked game audio, and the whole thing is loudness-normalized " +
		"to -14 LUFS for YouTube. Lines that have not been synthesized yet are spoken first.")
	expl.SetXAlign(0)
	expl.SetWrap(true)
	expl.AddCSSClass("dim-label")

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
				a.updateStep6Info()
			}
		})
		return d
	}

	p.container = dd(prodContainers, 0, "mp4 plays everywhere; mkv keeps subtitle tracks best; webm forces VP9 + Opus")
	p.container.Connect("notify::selected", func() { p.syncExt() })
	p.codec = dd(prodCodecs, 0, "h264 is the safe upload; h265 is smaller but slower; vp9 is for webm")
	p.preset = dd(prodPresets, 1, "how long the encoder may think — slower means smaller at the same quality")
	p.height = dd(prodHeights, 3, "output height; the width follows the source aspect")
	p.fps = dd(prodFPS, 2, "output frame rate")
	p.abr = dd(prodABR, 1, "audio bitrate in kbit/s")
	p.subs = dd(prodSubsLbl, 2, "what to do with the narration subtitles")

	p.crf = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 14, 34, 1)
	p.crf.SetValue(20)
	p.crf.SetDrawValue(true)
	p.crf.SetSizeRequest(200, -1)
	p.crf.SetTooltipText("quality: lower is better and bigger (18–23 is the usual range)")
	p.crf.AddMark(20, gtk.PosBottom, "20")
	p.crf.ConnectValueChanged(func() { a.updateStep6Info() })

	p.gvol = gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 1, 0.02)
	p.gvol.SetValue(0.22)
	p.gvol.SetDrawValue(true)
	p.gvol.SetSizeRequest(200, -1)
	p.gvol.SetTooltipText("how loud the original game audio sits under the narration")

	at(0, 0, "Container:", p.container)
	at(0, 1, "Video codec:", p.codec)
	at(0, 2, "Encoder preset:", p.preset)
	at(0, 3, "Quality (CRF):", p.crf)
	at(1, 0, "Resolution:", p.height)
	at(1, 1, "Frame rate:", p.fps)
	at(1, 2, "Audio bitrate:", p.abr)
	at(1, 3, "Game audio under voice:", p.gvol)
	at(0, 4, "Subtitles:", p.subs)

	// output row
	choose := gtk.NewButtonWithLabel("Choose…")
	choose.ConnectClicked(func() { a.chooseOutFileDialog() })
	open := gtk.NewButtonFromIconName("folder-open-symbolic")
	open.SetTooltipText("Open the folder holding the produced file")
	open.ConnectClicked(func() { a.openFolder(filepath.Dir(p.outFile)) })
	p.outLbl = gtk.NewLabel("")
	p.outLbl.SetXAlign(0)
	p.outLbl.SetHExpand(true)
	p.outLbl.SetEllipsize(pango.EllipsizeMiddle)
	p.outLbl.SetSelectable(true)
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	outRow.Append(choose)
	outRow.Append(open)
	outRow.Append(gtk.NewLabel("Output:"))
	outRow.Append(p.outLbl)
	p.setOut(filepath.Join(a.outDir, "final.mp4"))
	p.outAuto = true

	render := gtk.NewButtonWithLabel("Produce video")
	render.AddCSSClass("suggested-action")
	render.ConnectClicked(func() { a.produceClicked() })
	toggle := gtk.NewButtonWithLabel("⏯")
	toggle.SetTooltipText("play / pause the preview")
	toggle.ConnectClicked(func() {
		if p.player != nil {
			p.player.Toggle()
		}
	})
	load := gtk.NewButtonWithLabel("Preview result")
	load.ConnectClicked(func() {
		if !exists(p.outFile) {
			a.setStatus("nothing produced yet")
			return
		}
		p.player.PlaySegment(p.outFile, 0, -1, true)
	})
	ctl := gtk.NewBox(gtk.OrientationHorizontal, 8)
	ctl.Append(render)
	ctl.Append(load)
	ctl.Append(toggle)

	p.info = gtk.NewLabel("")
	p.info.SetXAlign(0)
	p.info.SetWrap(true)
	p.info.AddCSSClass("dim-label")

	vframe := gtk.NewFrame("")
	if p.player != nil {
		p.player.Picture.SetVExpand(true)
		p.player.Picture.SetSizeRequest(-1, 320)
		vframe.SetChild(p.player.Picture)
	}
	vframe.SetMarginTop(4)

	box := gtk.NewBox(gtk.OrientationVertical, 10)
	box.SetMarginTop(10)
	box.SetMarginBottom(8)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.Append(expl)
	box.Append(grid)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	box.Append(outRow)
	box.Append(ctl)
	box.Append(p.info)
	box.Append(vframe)

	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(box)
	scroll.SetVExpand(true)
	return scroll
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

func (a *App) updateStep6Info() {
	p := a.prod
	if p == nil || p.info == nil {
		return
	}
	segs := a.produceSegs()
	entries := a.produceEntries()
	if len(segs) == 0 {
		p.info.SetText("No cut yet — build one on step 4.")
		return
	}
	total, voiced := 0.0, 0
	for _, s := range segs {
		total += s.E - s.S
	}
	missing := 0
	for _, e := range entries {
		voiced++
		if !exists(a.ttsWav(e)) {
			missing++
		}
	}
	st := a.prodSettings()
	res := "source resolution"
	if st.Height > 0 {
		res = fmt.Sprintf("%dp", st.Height)
	}
	fps := "source fps"
	if st.FPS > 0 {
		fps = fmt.Sprintf("%g fps", st.FPS)
	}
	msg := fmt.Sprintf("%d clips, %.0f s of source (the produced file grows a little where narration needs room) · %s, %s, %s crf %d · %d/%d lines synthesized",
		len(segs), total, res, fps, st.Codec, st.CRF, voiced-missing, voiced)
	if len(entries) == 0 {
		msg += " · no narration yet — the clips would carry only game audio"
	}
	p.info.SetText(msg)
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
	for _, r := range append(append([]string{}, vids...), auds...) {
		p := filepath.Join(a.root, r)
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
			start: s.start - zero, dur: dur, fps: ffprobeFPS(s.path)})
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

type prodClip struct {
	idx    int
	video  *tlVideo
	local  float64 // start inside that recording
	length float64 // slot length after growing for the narration
	tempo  float64
	voice  string // synthesized wav, "" = original audio only
	vdur   float64
	text   string
}

func (a *App) produceClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	segs := a.produceSegs()
	if len(segs) == 0 {
		a.setStatus("no cut yet — build one on step 4 first")
		return
	}
	entries := a.produceEntries()
	st := a.prodSettings()
	vids, auds := a.snapSources()
	a.saveProjectTo(filepath.Join(a.root, "project.json"))

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
			a.updateStep6Info()
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
	dir := filepath.Join(a.outDir, "step6")
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
		switch e := matchEntry(entries, s); {
		case e == nil:
			if len(entries) > 0 {
				a.logfIdle("clip %d at %.0f s has no narration entry — it keeps its own audio", i+1, s.S)
			}
		default:
			c.text = strings.TrimSpace(e.Text)
			if c.text != "" {
				wav := a.pitchedWav(a.ttsWav(*e))
				if !exists(wav) {
					a.logfIdle("clip %d: no synthesis for its line — it keeps its own audio", i+1)
				} else {
					c.voice = wav
					c.vdur, _ = ffprobeDur(wav)
				}
			}
		}
		// grow the slot for the line, then speed the line up if it still spills
		if c.vdur > 0 {
			need := c.vdur + narrLead + 0.2
			if need > c.length {
				c.length += math.Min(need-c.length, maxExtend)
				if end := v.start + v.dur; s.S+c.length > end {
					c.length = end - s.S
				}
			}
			if need > c.length {
				c.tempo = math.Min(c.vdur/math.Max(0.1, c.length-narrLead-0.2), maxTempo)
				a.logfIdle("clip %d: narration %.1f s does not fit %.1f s — sped up %.2fx",
					i+1, c.vdur, c.length, c.tempo)
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
		if c.text != "" {
			end := cum + narrLead + c.vdur/c.tempo
			if c.vdur == 0 { // unsynthesized: hold the caption for the whole clip
				end = cum + c.length
			}
			cue++
			srt += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", cue,
				srtTime(cum+narrLead), srtTime(math.Min(end, cum+c.length)), wrapSub(c.text))
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
		name := fmt.Sprintf("c%03d%s", i, ext)
		var cueFile string
		if st.Subs == "burn" && c.text != "" {
			cueFile = filepath.Join(clipDir, fmt.Sprintf("c%03d.srt", i))
			end := narrLead + c.vdur/c.tempo
			if c.vdur == 0 {
				end = c.length
			}
			one := fmt.Sprintf("1\n%s --> %s\n%s\n\n", srtTime(narrLead),
				srtTime(math.Min(end, c.length)), wrapSub(c.text))
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
	if c.voice != "" {
		args = append(args, "-i", c.voice)
	}
	voiceIn := "2:a"
	if game == "0:a" {
		voiceIn = "1:a"
	}

	var vf []string
	if st.FPS > 0 {
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
	if c.voice != "" {
		fc += fmt.Sprintf("[%s]atempo=%.3f,aresample=48000,pan=stereo|c0=c0|c1=c0,adelay=%gs:all=1[nr];",
			voiceIn, math.Max(0.5, c.tempo), narrLead)
		fc += fmt.Sprintf("[%s]volume=%.3f,aresample=48000[bg];", game, st.GameVol)
		fc += "[bg][nr]amix=inputs=2:duration=first:normalize=0," + audFmt + "[a]"
	} else {
		fc += fmt.Sprintf("[%s]%s[a]", game, audFmt)
	}
	args = append(args, "-filter_complex", fc, "-map", "[v]", "-map", "[a]")
	args = append(args, codecArgs(st)...)
	args = append(args, audioArgs(st)...)
	args = append(args, out)
	return a.runCmd("ffmpeg", args...)
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

// matchEntry finds the narration written for a segment. The entries carry the
// clip's own times, so the start usually matches to the decimal -- but the
// model does echo them back and can round or nudge, and a line silently
// dropped from the render is the worst possible failure here. So: best
// overlap wins, and merely touching is not enough.
func matchEntry(entries []narrEntry, s cutSeg) *narrEntry {
	best, bestOv := -1, 0.0
	for i, e := range entries {
		ov := math.Min(e.E, s.E) - math.Max(e.S, s.S)
		if ov > bestOv {
			best, bestOv = i, ov
		}
	}
	if best < 0 {
		return nil
	}
	shorter := math.Min(entries[best].E-entries[best].S, s.E-s.S)
	if shorter > 0 && bestOv < shorter/2 {
		return nil
	}
	return &entries[best]
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
