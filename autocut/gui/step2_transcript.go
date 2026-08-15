package main

// Transcript: turn raw ASR into publishable, grounded text.
//
// Timestamps are the contract: every source's absolute start comes from its
// filename (YYYYMMDD-HHMMSS) or mtime minus duration, so alignment is a
// lookup, not a step. A content-based LLM aligner used to sit in align.go for
// the case where metadata lies; it was never wired to anything and is gone --
// git has it if copied files or hand-set recorder clocks ever make it worth
// reviving.
//
// The LLM fixes transcript lines in blocks, grounded in what the event log
// says was on screen and what the other sources heard at the same moment --
// that is how "pig access" becomes "pickaxes". Timestamps and speakers pass
// through byte-identical, enforced; a block that fails validation twice keeps
// its original lines and says so.
//
// step2/transcript/
//   <video>/transcript.fixed.tsv + subtitles.srt   per video, video timeline
//   <audio>/commentary.fixed.tsv                   per voice recording
//   offsets.tsv                                    video, audio, offset seconds
//   session.tsv / session.txt                      everything -- commentary,
//        game audio, events -- interleaved on one global timeline; this is
//        what the cut step reads.
//
// The page is step2.go -- the describer (step2_describe.go) and this share it.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const fixBlock = 25 // transcript lines per fixer request

// One paragraph or bullet per line, unwrapped: this is read in a wrapping text
// box, where a hard wrap at 80 columns only becomes a ragged second wrap. See
// describeSystem.
const fixSystem = `You clean up ASR transcript lines from a gaming session. They become subtitles, and they are the material the video edit is chosen from, so they have to stay faithful to what was actually said.

A request may open with a block headed ABOUT THIS SESSION: the editor's notes on who is in it and what things are called. That is where the spelling of a name comes from -- ASR gets names wrong more than anything else, and this block outranks what it heard.

Each request then gives you a context block, then the lines to clean as TSV: start, end, speaker, text, tab separated, headed by how many there are.

The context block is what was on screen and what the other microphones picked up in the same seconds:
  EVENT: what this recording's own video showed
  EVENT (NAME): what another recording's video showed
  SPEAKER_01 (NAME): a line off another recording, often the same room heard from a different seat
  NARRATOR (NAME): a line off the narrator's own microphone
Use it only to work out what a garbled line was. Never copy context into a line, and never let it put words in someone's mouth. It is frequently empty, which is normal: then clean the lines on their own.

Shape of your reply. It is checked line by line against what you were given. If the count, the order, a timestamp or a speaker differs, the whole block is discarded and the original uncleaned lines are kept, so every fix in it is lost.

- Return exactly the number of lines you were given, in the same order.
- Copy start, end and speaker character for character. Change only the fourth column. The speaker labels come from automatic diarisation and are sometimes plainly wrong. That is not yours to fix.
- Never merge, split, drop or add a line. A line stays one line even when it ends mid sentence: the next line continues it.
- Four tab separated columns. No tabs inside the text, no line numbers, no speaker name inside the text.
- Never leave the text empty. A line you cannot make sense of keeps its original text.
- Output only the TSV lines. No commentary, no code fences, no header, no blank lines.

What to fix.

- Every line is English or German. A line that looks like another language is a misrecognition: reconstruct the intended English or German from how it sounds and from what was happening. Never translate between English and German.
- Mixing the two is normal here: English game terms inside a German sentence, and the other way round. Keep the mix as spoken. It is not a mistake to tidy up.
- Repair mishearings from the context. A phrase that means nothing by itself but sounds like something the context says is on screen, or was just said, IS that thing. Names of games, items, places and players are what ASR gets wrong most, and the context is where their spelling comes from.
- Remove stutter doubles ("I I" becomes "I") and bare fillers ("uh", "ähm") that are clearly disfluency. Keep repetition that is meant: "go go go" stays.
- ASR sometimes loops one phrase for a whole line, or invents subtitle credits ("Untertitel von ...", "Amara.org", "thanks for watching") over silence. Collapse a loop to one occurrence. Leave an invented credit alone unless the context shows what was really said.
- Punctuate and capitalise for readability: sentence case, commas and full stops where they help, a question mark where the voice is asking.
- Keep the speaker's words, register and swearing. Do not soften, censor, condense or improve anyone's phrasing. These are subtitles, not a rewrite. Never invent content.`

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

// Accepts the stamps recorders actually write: OBS's 2026-08-08 19-55-15, a
// Quest's com.Maker.Game-20260808-195900-0, a phone's VID_20250814_213311, a
// dashcam's 20250814213311 with no separator at all, ShadowPlay's dotted
// 2025.08.14 - 21.33.11.03, QuickTime's "2026-08-08 at 7.55.15 PM", ISO
// 2026-08-08T19:55:15 -- and whatever users add by hand, which mixes them.
// Always year first: a day-first date cannot be told from a month-first one,
// and a guessed order silently misplaces the file by weeks. The century is
// pinned to 19/20 so a bare digit run has to look like a date before it counts
// as one; parseStamp then rejects the ones that only look like it (month 13,
// hour 25), which is what keeps the loose separators honest. A single-digit
// hour must bring its own separators -- freed of that, it would read a bad
// six-digit time as a good five-digit one.
var tsRe = regexp.MustCompile(`((?:19|20)\d{2})[-._]?(\d{2})[-._]?(\d{2})` +
	`(?:\s?[aA][tT]\s|[-_T. ]{0,3})` +
	`(?:(\d{2})[-.:_]?(\d{2})[-.:_]?(\d{2})|(\d)[-.:_](\d{2})[-.:_](\d{2}))` +
	`(?:\s?([aApP][mM]))?`)

// epochRe is the last resort: bare unix seconds, which is what an iPhone
// screen recording (RPReplay_Final1723456789.mp4) carries. Ten digits bounded
// by non-digits, pinned to 2017..2033 so an arbitrary number has to look like
// the present decade before it counts.
var epochRe = regexp.MustCompile(`(?:\A|\D)(1[5-9]\d{8})(?:\D|\z)`)

func parseStamp(m []string) (float64, error) {
	h, mn, sc := m[4], m[5], m[6]
	if h == "" { // the single-digit-hour layout matched instead
		h, mn, sc = "0"+m[7], m[8], m[9]
	}
	t, err := time.ParseInLocation("20060102-150405",
		m[1]+m[2]+m[3]+"-"+h+mn+sc, time.Local)
	if err != nil {
		return 0, err
	}
	// a 12-hour stamp names its half of the day
	if ap := m[10]; ap != "" {
		switch hr := t.Hour(); {
		case (ap[0] == 'p' || ap[0] == 'P') && hr < 12:
			t = t.Add(12 * time.Hour)
		case (ap[0] == 'a' || ap[0] == 'A') && hr == 12:
			t = t.Add(-12 * time.Hour)
		}
	}
	return float64(t.Unix()), nil
}

// nameStamp reads the wall clock out of a file name, if it carries one. The
// Inputs page asks it to warn about names that carry none; sourceStart asks it
// to place the file. Every candidate the pattern finds gets a chance, so a
// digit run that only resembles a date (a resolution, a serial) cannot mask a
// real stamp sitting after it.
func nameStamp(name string) (float64, bool) {
	for _, m := range tsRe.FindAllStringSubmatch(name, -1) {
		if t, err := parseStamp(m); err == nil {
			return t, true
		}
	}
	if m := epochRe.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return float64(n), true
		}
	}
	return 0, false
}

// stampFmt is the human half of what tsRe reads back, and it is what an output
// file naming a moment is called: 2026-08-08_19-59-00. Seconds are the whole
// resolution -- a frame every three seconds or thirty a second, the name says
// which second it belongs to and stampSeq separates the ones that share one.
const stampFmt = "2006-01-02_15-04-05"

// stampName renders a wall-clock instant, floored to the second it falls in.
// Local time, because that is what the recorders put in their own file names
// and what parseStamp reads them back as.
func stampName(unix float64) string {
	return time.Unix(int64(math.Floor(unix)), 0).Format(stampFmt)
}

// stampSeq names a series of instants, numbering the ones landing in the same
// second: 19-59-00, then 19-59-00-1, 19-59-00-2. The first frame of a second
// keeps the bare name, which plain string sort then gets backwards ('-' sorts
// before '.') -- read such a folder back with sortStamped, not sort.Strings.
type stampSeq struct{ used map[string]int }

func (s *stampSeq) name(unix float64, ext string) string {
	if s.used == nil {
		s.used = map[string]int{}
	}
	base := stampName(unix)
	n := s.used[base]
	s.used[base]++
	if n > 0 {
		base = fmt.Sprintf("%s-%d", base, n)
	}
	return base + ext
}

// readStamp is the inverse: the second a name claims, and which frame of that
// second it is. Names carrying no stamp -- f000001.jpg from before frames were
// timestamped, a stray file -- report !ok and are left to sort by name.
func readStamp(path string) (sec float64, sub int, ok bool) {
	base := filepath.Base(path)
	m := tsRe.FindStringSubmatch(base)
	loc := tsRe.FindStringIndex(base)
	if m == nil || loc == nil {
		return 0, 0, false
	}
	t, err := parseStamp(m)
	if err != nil {
		return 0, 0, false
	}
	rest := strings.TrimSuffix(base[loc[1]:], filepath.Ext(base))
	if rest == "" {
		return t, 0, true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest, "-"))
	if err != nil || !strings.HasPrefix(rest, "-") {
		return 0, 0, false
	}
	return t, n, true
}

// sortStamped puts timestamped output files back in the order they were made.
func sortStamped(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		as, an, aok := readStamp(paths[i])
		bs, bn, bok := readStamp(paths[j])
		switch {
		case !aok || !bok:
			return paths[i] < paths[j]
		case as != bs:
			return as < bs
		default:
			return an < bn
		}
	})
}

// sourceStart puts a file on the wall clock: filename timestamp first (both
// videos and audio recorders may write one), else mtime minus duration.
func sourceStart(path string) (float64, error) {
	dur, err := ffprobeDur(path)
	if err != nil {
		return 0, err
	}
	if t, ok := nameStamp(filepath.Base(path)); ok {
		return t, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return float64(fi.ModTime().Unix()) - dur, nil
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

// step3 fixes every source's transcript and writes the merged session
// timeline. span is this job's share of the progress bar; see step2.
func (a *App) fixTranscripts(videos, audios []string, span float64) error {
	s3 := a.transcriptDir()
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
			s.events = loadEvents(filepath.Join(a.describeDir(), s.base, "events.tsv"))
		}
	}

	// context provider: everything any source shows or says inside a window
	// of one source's timeline, mapped through the wall clock
	// ...labelled the way every other step labels a timeline line (tlLabel),
	// with the recording named after it when it is not the one being cleaned
	narr := a.narratorMic()
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
					fmt.Fprintf(&b, "EVENT (%s): %s\n", s.base, ev.text)
				}
			}
			for _, r := range s.rows {
				if s.start+r.e > w0 && s.start+r.s < w1 {
					who := tlLabel(tsvRow{spk: r.spk, src: s.base}, narr)
					fmt.Fprintf(&b, "%s (%s): %s\n", who, s.base, r.text)
				}
			}
		}
		if of.isVideo { // its own events ground its own audio too
			for _, ev := range of.events {
				if ev.e > t0-5 && ev.s < t1+5 {
					fmt.Fprintf(&b, "EVENT: %s\n", ev.text)
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
		fixed, err := a.fixRows(s, ctxFor, &done, total, span)
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
	// session.tsv is the machine copy and keeps every column, including which
	// recording each line came off. session.txt is the readable one, and it is
	// written exactly as the cut step will hand it to the model (sessionText),
	// so what you read is what it reads.
	var stv strings.Builder
	var tl []tsvRow
	for _, r := range rows {
		fmt.Fprintf(&stv, "%.2f\t%.2f\t%s\t%s\t%s\n", r.g, r.ge, r.src, r.spk, r.text)
		tl = append(tl, tsvRow{s: r.g, e: r.ge, src: r.src, spk: r.spk, text: r.text})
	}
	stx := sessionText(tl, a.narratorMic())
	if err := os.WriteFile(filepath.Join(s3, "session.tsv"), []byte(stv.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s3, "session.txt"), []byte(stx), 0o644); err != nil {
		return err
	}
	a.prog(trackFix, span, "transcript done")
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
	done *int, total int, span float64) ([]seg4, error) {

	system := a.prompt("fix")
	var out []seg4
	nblocks := (len(s.rows) + fixBlock - 1) / fixBlock
	for b := 0; b < nblocks; b++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		a.prog(trackFix, span*float64(*done)/float64(total),
			"[%s] fixing block %d/%d", s.base, b+1, nblocks)
		*done++

		lo := b * fixBlock
		hi := min(lo+fixBlock, len(s.rows))
		blk := s.rows[lo:hi]
		var lines []string
		for _, r := range blk {
			lines = append(lines, fmt.Sprintf("%.2f\t%.2f\t%s\t%s", r.s, r.e, r.spk, r.text))
		}
		user := a.ctxBlock() + fmt.Sprintf(`Context around these lines:
%s
Transcript lines to clean (%d lines, return exactly %d):
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
