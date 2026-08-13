package main

// Step 3: Transcript. Turn raw ASR into publishable, grounded text.
//
// Timestamps are the contract: every source's absolute start comes from its
// filename (YYYYMMDD-HHMMSS) or mtime minus duration, so alignment is a
// lookup, not a step (see align.go for the dormant content-based fallback).
//
// The LLM fixes transcript lines in blocks, grounded in what the event log
// says was on screen and what the other sources heard at the same moment --
// that is how "pig access" becomes "pickaxes". Timestamps and speakers pass
// through byte-identical, enforced; a block that fails validation twice keeps
// its original lines and says so.
//
// step3/
//   <video>/transcript.fixed.tsv + subtitles.srt   per video, video timeline
//   <audio>/commentary.fixed.tsv                   per voice recording
//   offsets.tsv                                    video, audio, offset seconds
//   session.tsv / session.txt                      everything -- commentary,
//        game audio, events -- interleaved on one global timeline; this is
//        what the cut step reads.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const fixBlock = 25 // transcript lines per LLM request

const fixSystem = `You clean up ASR transcript lines from a gaming session for use as
subtitles and as source material for a video edit. You get the lines as TSV
(start, end, speaker, text), plus what was on screen and what other
recordings heard during them.
Rules:
- Return EXACTLY the same number of TSV lines, same order, identical
  start/end/speaker fields. Edit ONLY the text column.
- Every line is English or German. A line that looks like any other language
  is a misrecognition: reconstruct the intended English or German from sound
  and context. Do not translate between English and German.
- Fix mishearings using the on-screen context: a phrase that means nothing
  but sounds like a thing the event log says is on screen IS that thing.
- Remove pure stutter doubles ("I I" -> "I") and fix punctuation for
  readability, but keep the speaker's words and style -- these are
  subtitles, not a rewrite. Never invent content.
Output only the TSV lines, no commentary, no code fences.`

type seg4 struct {
	s, e      float64
	spk, text string
}

func loadSeg4(path string) []seg4 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []seg4
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		var r seg4
		fmt.Sscanf(f[0], "%f", &r.s)
		fmt.Sscanf(f[1], "%f", &r.e)
		r.spk = f[2]
		r.text = strings.Join(f[3:], " ")
		out = append(out, r)
	}
	return out
}

// events.tsv is 3 columns: start, end, event
func loadEvents(path string) []tsvRow {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []tsvRow
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		var r tsvRow
		fmt.Sscanf(f[0], "%f", &r.s)
		fmt.Sscanf(f[1], "%f", &r.e)
		r.text = f[2]
		out = append(out, r)
	}
	return out
}

func srtStamp(t float64) string {
	if t < 0 {
		t = 0
	}
	h := int(t) / 3600
	m := (int(t) % 3600) / 60
	s := int(t) % 60
	ms := int((t-math.Floor(t))*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// Accepts both compact recorder stamps (20260808-195900) and human ones
// (2026-08-08_19-55-15) -- users add these by hand, formats vary.
var tsRe = regexp.MustCompile(`(\d{4})-?(\d{2})-?(\d{2})[-_T ](\d{2})[-:]?(\d{2})[-:]?(\d{2})`)

func parseStamp(m []string) (float64, error) {
	t, err := time.ParseInLocation("20060102-150405",
		m[1]+m[2]+m[3]+"-"+m[4]+m[5]+m[6], time.Local)
	if err != nil {
		return 0, err
	}
	return float64(t.Unix()), nil
}

// sourceStart puts a file on the wall clock: filename timestamp first (both
// videos and audio recorders may write one), else mtime minus duration.
func sourceStart(path string) (float64, error) {
	dur, err := ffprobeDur(path)
	if err != nil {
		return 0, err
	}
	if m := tsRe.FindStringSubmatch(filepath.Base(path)); m != nil {
		if t, err := parseStamp(m); err == nil {
			return t, nil
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return float64(fi.ModTime().Unix()) - dur, nil
}

// ---- page ------------------------------------------------------------------

func (a *App) buildStep3() gtk.Widgetter {
	expl := gtk.NewLabel("Fixes every transcript with the LLM, grounded in the event logs and in " +
		"what the other recordings heard at the same moment (offsets come from the file " +
		"timestamps). Produces per-video subtitles and one merged session timeline for the " +
		"cut step. Timestamps and speakers are never altered.")
	expl.SetXAlign(0)
	expl.SetWrap(true)
	expl.AddCSSClass("dim-label")

	hintLbl := gtk.NewLabel("Notes for the fixer (optional) — names, game vocabulary, corrections, " +
		"e.g. \"proper nouns are spelled …; SPEAKER_00 in the voice track is <name>\"")
	hintLbl.SetXAlign(0)
	hintLbl.SetWrap(true)
	a.s3hints = gtk.NewTextView()
	a.s3hints.SetWrapMode(gtk.WrapWord)
	a.s3hints.SetTopMargin(4)
	a.s3hints.SetLeftMargin(6)
	a.s3hints.SetRightMargin(6)
	hintScroll := gtk.NewScrolledWindow()
	hintScroll.SetChild(a.s3hints)
	hintScroll.SetSizeRequest(-1, 70)
	hintScroll.AddCSSClass("frame")

	a.s3info = gtk.NewLabel("")
	a.s3info.SetXAlign(0)
	a.s3info.SetWrap(true)

	a.s3out = gtk.NewLabel("")
	a.s3out.SetXAlign(0)
	a.s3out.SetYAlign(0)
	a.s3out.AddCSSClass("monospace")
	outScroll := gtk.NewScrolledWindow()
	outScroll.SetChild(a.s3out)
	outScroll.SetPropagateNaturalHeight(true)
	outScroll.SetMaxContentHeight(200)
	outScroll.SetVExpand(true)

	outLbl := gtk.NewLabel("Outputs")
	outLbl.SetXAlign(0)
	outLbl.AddCSSClass("heading")
	open := gtk.NewButtonWithLabel("Open step3 folder")
	open.ConnectClicked(func() { a.openFolder(filepath.Join(a.outDir, "step3")) })
	outHead := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outHead.Append(outLbl)
	outHead.Append(open)

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(expl)
	box.Append(hintLbl)
	box.Append(hintScroll)
	box.Append(a.s3info)
	box.Append(outHead)
	box.Append(outScroll)
	a.updateStep3Info()
	return box
}

func (a *App) transcriptHints() string {
	if a.s3hints == nil {
		return ""
	}
	buf := a.s3hints.Buffer()
	return strings.TrimSpace(buf.Text(buf.StartIter(), buf.EndIter(), false))
}

func (a *App) updateStep3Info() {
	if a.s3out == nil {
		return
	}
	s3 := filepath.Join(a.outDir, "step3")
	a.s3out.SetText(describeOutputs(s3))

	var lines []string
	if a.vidList != nil {
		for _, v := range a.vidList.selected() {
			base := baseName(v)
			if exists(filepath.Join(s3, base, "subtitles.srt")) {
				lines = append(lines, base+": subtitles ready")
			} else {
				lines = append(lines, base+": not fixed yet")
			}
		}
		for _, aud := range a.audList.selected() {
			base := baseName(aud)
			if exists(filepath.Join(s3, base, "commentary.fixed.tsv")) {
				lines = append(lines, base+": commentary fixed")
			} else {
				lines = append(lines, base+": not fixed yet")
			}
		}
	}
	if exists(filepath.Join(s3, "session.tsv")) {
		lines = append(lines, "session timeline ready")
	}
	if len(lines) == 0 {
		lines = append(lines, "step 3 has not run yet")
	}
	a.s3info.SetText(strings.Join(lines, "\n"))
}

// ---- run --------------------------------------------------------------------

func (a *App) step3Clicked() {
	if a.running {
		return
	}
	vids := a.vidList.selected()
	auds := a.audList.selected()
	if len(vids) == 0 || len(auds) == 0 {
		a.setStatus("select at least one video and one voice recording on step 1")
		return
	}
	abs := func(rels []string) []string {
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = filepath.Join(a.root, r)
		}
		return out
	}
	for _, v := range vids {
		if !exists(filepath.Join(a.outDir, "step2", baseName(v), "events.tsv")) {
			a.setStatus("run step 2 first — no event log for " + baseName(v))
			return
		}
	}
	a.saveProjectTo(filepath.Join(a.root, "project.json"))
	a.startStep3(abs(vids), abs(auds), a.transcriptHints())
}

func (a *App) startStep3(videos, audios []string, hints string) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	a.progMu.Unlock()
	a.updateRunControls()
	a.setStatus("step 3 running…")
	a.logExp.SetExpanded(true)
	go func() {
		err := a.step3(videos, audios, hints)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			switch {
			case errors.Is(err, errStopped):
				a.progress.SetText("stopped")
				a.setStatus("step 3 stopped")
			case err != nil:
				a.logf("step 3 FAILED: %v", err)
				a.progress.SetText("failed — see log")
				a.setStatus("step 3 failed")
			default:
				a.progress.SetFraction(1)
				a.setStatus("step 3 done")
			}
			a.updateStep3Info()
			a.updateGates()
		})
	}()
}

// one source with everything known about its place in the world
type src struct {
	path, base string
	start      float64 // wall clock, seconds since epoch
	dur        float64
	isVideo    bool
	rows       []seg4   // fixed transcript, its own timeline
	events     []tsvRow // videos only
}

func (a *App) step3(videos, audios []string, hints string) error {
	s3 := filepath.Join(a.outDir, "step3")
	if err := os.MkdirAll(s3, 0o755); err != nil {
		return err
	}

	// place every source on the wall clock
	var srcs []*src
	for _, p := range append(append([]string{}, videos...), audios...) {
		st, err := sourceStart(p)
		if err != nil {
			return fmt.Errorf("cannot place %s in time: %w", baseName(p), err)
		}
		dur, _ := ffprobeDur(p)
		srcs = append(srcs, &src{path: p, base: baseName(p), start: st, dur: dur,
			isVideo: len(videos) > 0 && contains(videos, p)})
	}
	var offTsv strings.Builder
	for _, v := range srcs {
		if !v.isVideo {
			continue
		}
		for _, au := range srcs {
			if au.isVideo {
				continue
			}
			off := v.start - au.start
			fmt.Fprintf(&offTsv, "%s\t%s\t%.2f\n", v.base, au.base, off)
			a.logfIdle(">>> offset: %s starts %.1f s into %s", v.base, off, au.base)
		}
	}
	if err := os.WriteFile(filepath.Join(s3, "offsets.tsv"), []byte(offTsv.String()), 0o644); err != nil {
		return err
	}

	// load raw material
	for _, s := range srcs {
		s.rows = loadSeg4(filepath.Join(a.outDir, "step1", s.base, "transcript.tsv"))
		if s.isVideo {
			s.events = loadEvents(filepath.Join(a.outDir, "step2", s.base, "events.tsv"))
		}
	}

	// context provider: everything any source shows or says inside a window
	// of one source's timeline, mapped through the wall clock
	ctxFor := func(of *src, t0, t1 float64) string {
		var b strings.Builder
		w0 := of.start + t0 - 5
		w1 := of.start + t1 + 5
		for _, s := range srcs {
			if s == of {
				continue
			}
			for _, ev := range s.events {
				if s.start+ev.e > w0 && s.start+ev.s < w1 {
					fmt.Fprintf(&b, "[on screen, %s] %s\n", s.base, ev.text)
				}
			}
			for _, r := range s.rows {
				if s.start+r.e > w0 && s.start+r.s < w1 {
					fmt.Fprintf(&b, "[heard in %s] %s\n", s.base, r.text)
				}
			}
		}
		if of.isVideo { // its own events ground its own audio too
			for _, ev := range of.events {
				if ev.e > t0-5 && ev.s < t1+5 {
					fmt.Fprintf(&b, "[on screen] %s\n", ev.text)
				}
			}
		}
		return b.String()
	}

	// fix everything, block by block
	total := 0
	for _, s := range srcs {
		total += (len(s.rows) + fixBlock - 1) / fixBlock
	}
	done := 0
	for _, s := range srcs {
		fixed, err := a.fixRows(s, ctxFor, hints, &done, total)
		if err != nil {
			return err
		}
		s.rows = fixed
		dir := filepath.Join(s3, s.base)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		var tsv, srt strings.Builder
		for i, r := range fixed {
			fmt.Fprintf(&tsv, "%.2f\t%.2f\t%s\t%s\n", r.s, r.e, r.spk, r.text)
			fmt.Fprintf(&srt, "%d\n%s --> %s\n[%s] %s\n\n", i+1, srtStamp(r.s), srtStamp(r.e), r.spk, r.text)
		}
		name := "commentary.fixed.tsv"
		if s.isVideo {
			name = "transcript.fixed.tsv"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(tsv.String()), 0o644); err != nil {
			return err
		}
		if s.isVideo {
			if err := os.WriteFile(filepath.Join(dir, "subtitles.srt"), []byte(srt.String()), 0o644); err != nil {
				return err
			}
		}
	}

	// merged session timeline: the cut step's single source of truth
	zero := math.MaxFloat64
	for _, s := range srcs {
		zero = math.Min(zero, s.start)
	}
	type row struct {
		g, ge    float64
		src, spk string
		text     string
	}
	var rows []row
	for _, s := range srcs {
		for _, r := range s.rows {
			rows = append(rows, row{s.start - zero + r.s, s.start - zero + r.e, s.base, r.spk, r.text})
		}
		for _, ev := range s.events {
			rows = append(rows, row{s.start - zero + ev.s, s.start - zero + ev.e, s.base, "EVENT", ev.text})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].g < rows[j].g })
	var stv, stx strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&stv, "%.2f\t%.2f\t%s\t%s\t%s\n", r.g, r.ge, r.src, r.spk, r.text)
		fmt.Fprintf(&stx, "[%02d:%02d] [%s %s] %s\n", int(r.g)/60, int(r.g)%60, r.src, r.spk, r.text)
	}
	if err := os.WriteFile(filepath.Join(s3, "session.tsv"), []byte(stv.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s3, "session.txt"), []byte(stx.String()), 0o644); err != nil {
		return err
	}
	a.logfIdle(">>> session timeline: %d rows across %d source(s)", len(rows), len(srcs))
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fixRows runs one source's lines through the LLM in blocks. Validation is
// strict: same line count, byte-identical start/end/speaker; a block failing
// twice keeps its original lines, loudly.
func (a *App) fixRows(s *src, ctxFor func(*src, float64, float64) string,
	hints string, done *int, total int) ([]seg4, error) {

	system := fixSystem
	if hints != "" {
		system += "\nEditor's notes -- trust them:\n" + hints
	}
	var out []seg4
	nblocks := (len(s.rows) + fixBlock - 1) / fixBlock
	for b := 0; b < nblocks; b++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		a.prog(trackSTT, float64(*done)/float64(total),
			"[%s] fixing block %d/%d", s.base, b+1, nblocks)
		*done++

		lo := b * fixBlock
		hi := min(lo+fixBlock, len(s.rows))
		blk := s.rows[lo:hi]
		var lines []string
		for _, r := range blk {
			lines = append(lines, fmt.Sprintf("%.2f\t%.2f\t%s\t%s", r.s, r.e, r.spk, r.text))
		}
		user := fmt.Sprintf(`Context around these lines:
%s
Transcript lines to clean (%d lines -- return exactly %d):
%s`, ctxFor(s, blk[0].s, blk[len(blk)-1].e), len(blk), len(blk), strings.Join(lines, "\n"))

		ok := false
		for try := 0; try < 2 && !ok; try++ {
			reply, err := a.llmChatRetry([]map[string]any{
				msg("system", system), msg("user", user),
			}, false)
			if err != nil {
				if errors.Is(err, errStopped) {
					return nil, errStopped
				}
				return nil, fmt.Errorf("fix %s block %d: %w", s.base, b+1, err)
			}
			var got []seg4
			for _, l := range strings.Split(reply, "\n") {
				f := strings.Split(l, "\t")
				if len(f) < 4 {
					continue
				}
				var r seg4
				fmt.Sscanf(f[0], "%f", &r.s)
				fmt.Sscanf(f[1], "%f", &r.e)
				r.spk = f[2]
				r.text = strings.Join(f[3:], " ")
				got = append(got, r)
			}
			if len(got) == len(blk) {
				match := true
				for i := range got {
					if math.Abs(got[i].s-blk[i].s) > 0.01 || math.Abs(got[i].e-blk[i].e) > 0.01 ||
						got[i].spk != blk[i].spk {
						match = false
						break
					}
				}
				if match {
					out = append(out, got...)
					ok = true
				}
			}
		}
		if !ok {
			a.logfIdle(">>> [%s] block %d/%d failed validation, keeping original lines", s.base, b+1, nblocks)
			out = append(out, blk...)
		}
	}
	return out, nil
}
