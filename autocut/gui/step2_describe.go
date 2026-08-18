package main

// Describe: every footage source's stored frames (step1/frames/<v>/) go to the
// vision LLM in small batches together with the game-audio words heard during
// those seconds and a little either side of them, marked as context; a rolling
// "state of the game" plus the last events make each batch a description of
// what is HAPPENING, not stills.
// Output: step2/describe/<video>/events.tsv, resumable per chunk.
//
// The page is step2.go -- this half and the fixer (step3.go) share it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const framesPerReq = 4 // frames per vision request

// recentEvents is how much of the previous work rides along with each request:
// the last few EVENT lines, as text. No earlier frames are ever re-sent -- the
// model sees framesPerReq images and reads about everything before them. The
// page states both numbers, so they are named rather than typed twice.
const recentEvents = 3

// The primer stays empty of game knowledge on purpose: whatever is specific
// about this footage goes into the prompt box on the page, which replaces this
// wholesale rather than being appended to it.
//
// One paragraph, one line -- long ones, and deliberately so. This text is not
// read here, it is read in a wrapping text box, and a hard wrap at 80 columns
// gets wrapped again by that box into ragged half-lines. The Go source pays for
// the box being right.
const describeSystem = `You describe screen-recorded footage for a video editor.

You get a few consecutive frames covering a few seconds, the words heard around them, the running STATE from the previous chunk, and the last few EVENT lines. You will never see these frames or any earlier ones again: those two lines are your only memory, so write them for a reader who has seen nothing.

Everything is on one clock: seconds from the first of these frames, signed. Negative is before these frames, positive during or after. Every line you get is stamped on it, so a line and a picture can be matched by their numbers:
  [+2.0s] FRAME 3 of 4 -- the picture that follows this line was taken then
  [-8s] EVENT: what you yourself wrote about the seconds just before these
  [+2.0s] SPEAKER_01: a line somebody said
  [+2.0s] NARRATOR: a line the narrator said into his own microphone

Speech reaches you from more than one microphone in the room. Whoever is talking may be describing something you cannot see, remembering, or talking about nothing on screen at all.

The request may open with a block headed ABOUT THIS SESSION: the editor's own notes on what this is, who is in it and what things are called. Being told is exactly what that block is for -- use it for names, and describe what you see in its terms.

Describe what you actually see. Never assume a genre, a title, a place or a character you have not been shown or told about.

The frames are a span of time, not a picture. Report what CHANGES across them: movement, an action and what it causes, something arriving or gone. If nothing meaningful changes, say so plainly and briefly rather than padding.

Always say how it moves. State whether the camera and the action are hectic -- fast turning, violent or continuous motion, most of the picture different from one frame to the next -- or calm and steady, or somewhere in between, and say which it is even when nothing else happens. A cut is chosen on pace as much as on content, so this belongs in every EVENT line.

Speech is a claim, not evidence. Where speech and frames disagree, the frames win. A line may refer to something before or after the moment it is spoken, so never assume it describes the frame it lands on.

Sections marked "context" are for orientation only -- never describe something that appears only there.

Read on-screen text -- names, scores, counters, menus, subtitles -- and use it. Once something has a name, keep using that name instead of "the player" or "the menu", so the same thing reads the same way across the whole log.

If you cannot tell what something is, describe how it looks and move on. Do not guess, do not hedge with "appears to" or "seems to", and do not mention frames, images, chunks, or yourself.

Reply with exactly two lines, plain text, no markdown, nothing else:
EVENT: what happens in these seconds, and how hectic or calm it is -- present tense, concrete, specific, max 35 words. Do not restate the STATE.
STATE: the running state after these seconds, max 50 words: where this is, what is being done, who else is present, the ongoing goal. Carry forward what is still true, drop what has stopped being true, and keep it readable on its own.`

type tsvRow struct {
	s, e float64
	spk  string // SPEAKER_nn, or EVENT for a line describing the screen
	text string
	// which recording this came off, as the merged timeline names it (blank in
	// a single recording's own transcript). It decides whether the viewer will
	// ever hear the line: the render takes its audio from the footage, so a
	// line off a separate microphone is in the transcript and not in the video.
	src string
}

// tlLabel is who a line belongs to, in the one vocabulary every step uses:
//
//	EVENT       what the picture showed
//	NARRATOR    the narrator's own microphone -- the one recording the
//	            finished video never plays, so only the voice-over carries it
//	SPEAKER_nn  a voice the video does play
//
// Four prompts describe the material they are given, and they used to describe
// it four different ways: a recording's file name here, "[heard in NAME]"
// there, "[base SPEAKER_00]" in the session timeline. Nothing was wrong with
// any of them alone; together they made every step a new format to learn, on a
// model with no room to spare for that. narr is narratorMic, blank when nobody
// is exempt.
func tlLabel(r tsvRow, narr string) string {
	switch {
	case r.spk == "EVENT":
		return "EVENT"
	case narr != "" && r.src == narr:
		return "NARRATOR"
	case r.spk == "":
		return "SPEAKER"
	}
	return r.spk
}

// sessionText renders the whole merged timeline the way the cut and the audit
// read it: one line each, stamped [mm:ss] from the start of the session, then
// the label, then what was said or seen. The minutes keep counting past 59, so
// the stamp is never ambiguous about which hour it is in.
//
// It is built from session.tsv at request time rather than read off
// session.txt, so a change here reaches a project that was transcribed before
// it without anyone re-running an LLM pass over an hour of speech.
func sessionText(rows []tsvRow, narr string) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "[%02d:%02d] %s: %s\n", int(r.s)/60, int(r.s)%60, tlLabel(r, narr), r.text)
	}
	return b.String()
}

// How much speech rides along with a chunk of frames, and how far from it a
// line may have been said. The two caps apply independently: whichever binds
// first wins.
const (
	ctxSegs   = 2    // context segments on each side
	ctxWindow = 10.0 // seconds between the chunk and a context segment
)

// electSpeech splits a recording's own transcript into what was said during a
// chunk of frames and a little either side of it, for reference.
//
// The three sets are disjoint and between them hold every segment, which is
// the point: a segment overlapping the chunk at all belongs to during, IN
// FULL -- never clipped at the boundary, and so never able to appear as
// context as well. Whatever is left is entirely before the chunk or entirely
// after it, and is kept only if it is close enough and near enough the front
// of its queue.
func electSpeech(rows []tsvRow, chunkStart, chunkEnd float64) (before, during, after []tsvRow) {
	for _, r := range rows {
		switch {
		case r.e > chunkStart && r.s < chunkEnd:
			during = append(during, r)
		case r.e <= chunkStart:
			if chunkStart-r.e <= ctxWindow {
				before = append(before, r)
			}
		default: // r.s >= chunkEnd
			if r.s-chunkEnd <= ctxWindow {
				after = append(after, r)
			}
		}
	}
	// nearest the chunk first, so the two that survive the cap are the two
	// closest to it and not the two that happen to come first in the file
	sort.Slice(before, func(i, j int) bool { return before[i].e > before[j].e })
	sort.Slice(after, func(i, j int) bool { return after[i].s < after[j].s })
	before = before[:min(len(before), ctxSegs)]
	after = after[:min(len(after), ctxSegs)]
	// and back into reading order for the block itself
	sort.Slice(before, func(i, j int) bool { return before[i].s < before[j].s })
	sort.Slice(during, func(i, j int) bool { return during[i].s < during[j].s })
	return before, during, after
}

// speechSrc is one recording's transcript, already shifted onto the frames'
// own clock, and what to call it in the block. More than one because the
// footage's own audio is rarely the only microphone in the session: the person
// recording is usually on a separate track, saying what they are doing while
// they do it, which is the best evidence there is for what a chunk is about.
type speechSrc struct {
	label string
	rows  []tsvRow
}

// spoken is one elected line and which recording it came off.
type spoken struct {
	tsvRow
	label string
}

// speechBlock is what the model is told was said around these frames. All
// three sections are always emitted, empty ones included: a missing heading is
// indistinguishable from a broken pipeline, and "nobody spoke" has to be
// readable as itself rather than as speech having been dropped.
//
// One rule for the times, so the model never has to work out which end a
// number is measured from: seconds from the chunk's first frame, signed.
// Before the chunk is negative, during and after it positive. The frames
// themselves are labelled on that same clock, which is what lets a line be
// matched to a picture at all.
//
// Every source is elected separately and the results merged in time order, so
// the two-a-side context cap is per speaker: one talkative track cannot crowd
// another out of its own context, which is what a single merged election would
// have done.
//
// Segments are emitted one per line, never merged: the boundaries are where
// the ASR heard pauses, and the pauses carry meaning. The text goes through
// untouched -- no trimming, no case or punctuation repair.
func speechBlock(srcs []speechSrc, narr string, chunkStart, chunkEnd float64) string {
	var before, during, after []spoken
	tag := func(dst *[]spoken, rows []tsvRow, src string) {
		for _, r := range rows {
			r.src = src // a single recording's transcript does not carry it
			*dst = append(*dst, spoken{tsvRow: r, label: tlLabel(r, narr)})
		}
	}
	for _, s := range srcs {
		b, d, a := electSpeech(s.rows, chunkStart, chunkEnd)
		tag(&before, b, s.label)
		tag(&during, d, s.label)
		tag(&after, a, s.label)
	}
	var b strings.Builder
	section := func(head, empty string, segs []spoken) {
		b.WriteString(head + "\n")
		if len(segs) == 0 {
			b.WriteString(empty + "\n")
			return
		}
		sort.SliceStable(segs, func(i, j int) bool { return segs[i].s < segs[j].s })
		for _, r := range segs {
			fmt.Fprintf(&b, "[%+.1fs] %s: %s\n", r.s-chunkStart, r.label, r.text)
		}
	}
	section("--- context before (do not describe) ---", "(none)", before)
	section("--- spoken during these frames ---", "(no speech during these frames)", during)
	section("--- context after (do not describe) ---", "(none)", after)
	return strings.TrimRight(b.String(), "\n")
}

// loadTSVRows reads either of the two timeline files this app writes: a single
// recording's transcript (start, end, speaker, text) and the merged session
// timeline (start, end, RECORDING, speaker, text), which also carries the EVENT
// lines describing the screen.
//
// It used to take column 4 as the text, which is the text in the four-column
// file and the SPEAKER/EVENT label in the five-column one. Nothing crashed:
// step 4 built every narration request out of a column of the words
// "SPEAKER_00" and "EVENT", so the model was asked to narrate clips it had been
// told nothing about, and wrote what such a session usually contains -- which
// is how a line about digging up something shiny ends up over a clip where
// nobody has picked up a pickaxe yet.
func loadTSVRows(path string) []tsvRow {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []tsvRow
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		var r tsvRow
		fmt.Sscanf(f[0], "%f", &r.s)
		fmt.Sscanf(f[1], "%f", &r.e)
		// the merged file has the recording's name in between; everything after
		// the speaker is the line itself, so a tab in it keeps the whole line
		// rather than its tail
		if len(f) > 4 {
			r.src, r.spk, r.text = f[2], f[3], strings.Join(f[4:], "\t")
		} else {
			r.spk, r.text = f[2], strings.Join(f[3:], "\t")
		}
		out = append(out, r)
	}
	return out
}

// ---- run --------------------------------------------------------------------

// Every source in the session takes part: each video is described on its own
// timeline, and every (video, voice) pair gets its own alignment offset.
type videoPlan struct {
	base     string
	video    string // absolute path
	dir      string // step2/describe/<base>
	frames   []string
	interval float64
	scale    string
	chunks   int
}

func (a *App) planVideo(video, s2 string) (*videoPlan, error) {
	base := baseName(video)
	fdir := filepath.Join(a.outDir, "step1", "frames", base)
	ents, err := os.ReadDir(fdir)
	if err != nil {
		return nil, fmt.Errorf("no frames for %s -- run step 1", base)
	}
	p := &videoPlan{base: base, video: video, dir: filepath.Join(s2, base)}
	for _, e := range ents {
		// every .jpg in here is a frame: they are named for the second they were
		// shot in now, and f000001.jpg only in folders extracted before that.
		// The dotted marker file is the one thing to skip.
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") && !strings.HasPrefix(e.Name(), ".") {
			p.frames = append(p.frames, filepath.Join(fdir, e.Name()))
		}
	}
	sortStamped(p.frames)
	if len(p.frames) == 0 {
		return nil, fmt.Errorf("frame folder is empty: %s", fdir)
	}
	if b, err := os.ReadFile(filepath.Join(fdir, ".interval")); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(b)), "|", 2)
		fmt.Sscanf(parts[0], "%f", &p.interval)
		if len(parts) > 1 {
			p.scale = parts[1]
		}
	}
	if p.interval <= 0 {
		return nil, fmt.Errorf("%s was extracted as every-frame; describe needs a fixed interval — rerun step 1 with e.g. 1s", base)
	}
	p.chunks = (len(p.frames) + framesPerReq - 1) / framesPerReq
	return p, nil
}

// commentary puts every other recording's words on this video's clock, so the
// person who was talking through the session is heard against the frames they
// were talking about. Same arithmetic step 3 uses for the session timeline:
// a filename stamp when there is one, mtime minus duration when there is not.
//
// A source that cannot be placed in time is left out rather than guessed at.
// Speech against the wrong frames is worse than no speech: the prompt tells the
// model to trust the words over its own reading of a picture in every case
// except outright contradiction, so a wrong offset is believed.
//
// The offsets are logged for the same reason step 3 logs them: when a
// description talks about the wrong thing, this is the number to look at.
func (a *App) commentary(video string, audios []string) []speechSrc {
	if len(audios) == 0 {
		return nil
	}
	vidStart, err := sourceStart(video)
	if err != nil {
		a.logfIdle(">>> [%s] cannot be placed in time (%v) -- described without the other recordings",
			baseName(video), err)
		return nil
	}
	var out []speechSrc
	for _, aud := range audios {
		base := baseName(aud)
		st, err := sourceStart(aud)
		if err != nil {
			a.logfIdle(">>> [%s] cannot be placed in time (%v) -- left out of the descriptions", base, err)
			continue
		}
		rows := loadTSVRows(a.transcriptPath(base))
		if len(rows) == 0 {
			continue
		}
		off := st - vidStart
		for i := range rows {
			rows[i].s += off
			rows[i].e += off
		}
		out = append(out, speechSrc{label: base, rows: rows})
		a.logfIdle(">>> [%s] hearing %s alongside it, starting %.1f s in", baseName(video), base, off)
	}
	return out
}

// step2 describes every footage source. span is how much of the progress bar
// this job owns -- all of it when Describe runs on its own, half when the
// fixer runs after it on the same page.
func (a *App) describeAll(videos, audios []string, span float64) error {
	s2 := a.describeDir()
	if err := os.MkdirAll(s2, 0o755); err != nil {
		return err
	}
	var plans []*videoPlan
	total := 0
	for _, v := range videos {
		p, err := a.planVideo(v, s2)
		if err != nil {
			return err
		}
		plans = append(plans, p)
		total += p.chunks
	}
	// the queue: one task per chunk of frames, over every recording. Nothing
	// here is countable before the frames are on disk, which is why it is
	// filled at the top of the job rather than when the run started.
	a.qPush(trackDescribe, total, "chunk")
	// this step ONLY describes; the fixer reads these event logs afterwards
	done := 0
	for _, p := range plans {
		if err := a.describeVideo(p, a.commentary(p.video, audios), done, total, span); err != nil {
			return err
		}
		done += p.chunks
	}
	// chunks that resume skip their progress call, so a video that was already
	// described would leave the bar where it started -- claim the share here
	a.qDone(trackDescribe, span)
	return nil
}

// resetDescribe undoes the resume below: every source's event log and rolling
// STATE go, and the next run describes the footage from t=0 again.
//
// What it does NOT touch is .llmframes beside them. Those are scaled pixels,
// not results -- keeping them means starting over costs the vision model again
// but not the minutes of ffmpeg that scaling an hour of frames takes. Nor does
// it touch step2/transcript: the fixer never resumes, so every run of it
// already starts from the first block.
//
// Every folder under step2/describe/ is cleared, not just the sources selected
// now: "start from the start" is about the step, and a log left behind by a
// recording that has since been deselected is exactly the stale half-run this
// is here to get rid of.
func (a *App) resetDescribe() error {
	ents, err := os.ReadDir(a.describeDir())
	if err != nil {
		return nil // nothing described yet is already at the start
	}
	var cleared []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		gone := false
		for _, f := range []string{"events.tsv", "state.txt"} {
			switch err := os.Remove(filepath.Join(a.describeDir(), e.Name(), f)); {
			case err == nil:
				gone = true
			case !errors.Is(err, os.ErrNotExist):
				return err
			}
		}
		if gone {
			cleared = append(cleared, e.Name())
		}
	}
	if len(cleared) > 0 {
		a.logf(">>> stopped last time — describing from the start again: %s (scaled frames kept)",
			strings.Join(cleared, ", "))
	}
	return nil
}

// ---- describe ---------------------------------------------------------------

func (a *App) describeVideo(p *videoPlan, comm []speechSrc, chunkOff, chunkTotal int, span float64) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	// this video's own audio first, then everyone else's microphone
	speech := append([]speechSrc{{
		label: p.base,
		rows:  loadTSVRows(filepath.Join(a.outDir, "step1", p.base, "transcript.tsv")),
	}}, comm...)
	narr := a.narratorMic()
	evPath := filepath.Join(p.dir, "events.tsv")
	statePath := filepath.Join(p.dir, "state.txt")

	// The model needs LLM-sized images; frames may be stored bigger. Scaled
	// copies are cached per frame, so resume and re-runs pay scaling once.
	needScale := true
	switch p.scale {
	case "896w (LLM)", "480p":
		needScale = false
	}
	llmDir := filepath.Join(p.dir, ".llmframes")
	if needScale {
		if err := os.MkdirAll(llmDir, 0o755); err != nil {
			return err
		}
	}
	scaledFrame := func(src string) (string, error) {
		if !needScale {
			return src, nil
		}
		dst := filepath.Join(llmDir, filepath.Base(src))
		if !exists(dst) {
			if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", src,
				"-vf", "scale=896:-2", "-q:v", "4", dst); err != nil {
				return "", err
			}
		}
		return dst, nil
	}

	// resume: chunks already described are keyed by their start time; the
	// recent-events window picks up from the end of the existing log
	done := map[string]bool{}
	var recent []tsvRow
	if b, err := os.ReadFile(evPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Split(l, "\t")
			if len(f) >= 3 && f[0] != "" {
				done[f[0]] = true
				r := tsvRow{spk: "EVENT", text: f[2]}
				fmt.Sscanf(f[0], "%f", &r.s)
				recent = append(recent, r)
				if len(recent) > recentEvents {
					recent = recent[1:]
				}
			}
		}
	}
	state := "Recording just started."
	if b, err := os.ReadFile(statePath); err == nil && len(b) > 0 {
		state = strings.TrimSpace(string(b))
	}

	for c := 0; c < p.chunks; c++ {
		if err := a.checkpoint(); err != nil {
			return err
		}
		lo := c * framesPerReq
		hi := min(lo+framesPerReq, len(p.frames))
		// two ends, and they are different on purpose: the frames run from t0
		// to tLast, and that is what the model is shown and what the speech is
		// elected against. The event is logged as covering t0 to t1, one
		// interval further, because the last frame stands for the interval
		// that follows it and the cut reads these spans as time.
		t0 := float64(lo) * p.interval
		tLast := float64(hi-1) * p.interval
		t1 := float64(hi) * p.interval
		key := fmt.Sprintf("%.2f", t0)
		// taken whatever becomes of it: a chunk described by an earlier run is
		// one this run is done with, and a queue that only counted the ones it
		// worked would sit at 1/300 through a resumed session
		a.qTake(trackDescribe)
		if done[key] {
			continue
		}
		a.prog(trackDescribe, span*float64(chunkOff+c)/float64(chunkTotal), "")

		// past history rides along twice: the rolling STATE (what is true) and
		// the last events (what just happened) -- together they let the model
		// describe motion and consequences, not disconnected stills
		// the session context leads, ahead of the state and the pictures: it is
		// what the names in this footage mean, and a describer that reads it
		// after the frames has already guessed at them
		text := a.ctxBlock() + fmt.Sprintf("STATE so far: %s\nFrames cover t=%.0fs to t=%.0fs, %g s apart.", state, t0, tLast, p.interval)
		if len(recent) > 0 {
			// on the frames' clock like everything else in this request: these
			// used to carry their absolute time in the video, which is the one
			// number on the page measured from somewhere else
			text += "\nJust before this:\n"
			for _, r := range recent {
				text += fmt.Sprintf("[%+.0fs] EVENT: %s\n", r.s-t0, r.text)
			}
			text = strings.TrimRight(text, "\n")
		}
		text += "\n" + speechBlock(speech, narr, t0, tLast)
		// Each frame gets a line of its own in front of it, on the same signed
		// clock the speech is on. Without them the images arrive as an unlabelled
		// run and the only thing carrying their order is their position in the
		// array -- so "[+2.1s] he opens it" could not be tied to a picture, and
		// which of the four was first was something the model had to assume.
		content := []any{txtPart(text)}
		for i, f := range p.frames[lo:hi] {
			sf, err := scaledFrame(f)
			if err != nil {
				return err
			}
			part, err := imgPart(sf)
			if err != nil {
				return err
			}
			content = append(content, txtPart(fmt.Sprintf("[%+.1fs] FRAME %d of %d",
				float64(i)*p.interval, i+1, hi-lo)), part)
		}
		reply, err := a.llmChatRetry([]map[string]any{
			msg("system", a.prompt("describe")), msg("user", content),
		}, false)
		if err != nil {
			if errors.Is(err, errStopped) {
				return errStopped
			}
			return fmt.Errorf("describe %s t=%.0f: %w", p.base, t0, err)
		}

		event, newState := "", ""
		for _, l := range strings.Split(reply, "\n") {
			if v, ok := strings.CutPrefix(l, "EVENT:"); ok {
				event = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(l, "STATE:"); ok {
				newState = strings.TrimSpace(v)
			}
		}
		if event == "" {
			event = "(no event line: " + strings.ReplaceAll(reply, "\n", " ") + ")"
		}
		if newState != "" {
			state = newState
		}
		f, err := os.OpenFile(evPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprintf(f, "%s\t%.2f\t%s\n", key, t1, event)
		f.Close()
		if err := os.WriteFile(statePath, []byte(state+"\n"), 0o644); err != nil {
			return err
		}
		recent = append(recent, tsvRow{s: t0, spk: "EVENT", text: event})
		if len(recent) > recentEvents {
			recent = recent[1:]
		}
	}
	a.logfIdle(">>> [%s] event log complete (%d chunks)", p.base, p.chunks)
	return nil
}
