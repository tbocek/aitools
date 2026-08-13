package main

// DORMANT: content-based alignment, kept for projects where file metadata
// cannot be trusted (copied files, hand-set recorder clocks). Not wired to
// any page; timestamps are the contract now, alignment is a lookup.
//
// The recordings share no audio (verified early on: the mic is not in the
// video mix and the friends are not in the voice file), so the only bridge is
// CONTENT. The division of labor is strict:
//
//   LLM        finds correspondences -- moments where the commentary
//              demonstrably reacts to a described event or an NPC line.
//              That is a semantic task; nothing else can do it.
//   plain Go   does every number: per-anchor offsets, a robust Theil-Sen fit
//              of voice_t = OFFSET + RATE * video_t, outlier tolerance via
//              medians, and the accept/fallback decision. The LLM never
//              outputs the offset itself.
//
// RATE is the drift check. Honest resolution note: with anchors good to
// ±1-2 s over a ~25 min span, drift below ~0.1% is invisible -- which is fine,
// because real crystal drift is far smaller. What the fit DOES catch is the
// sample-rate class of error (mishandled 48000 vs 44100, variable-rate
// recordings, dropped samples), the drifts that actually ruin alignments.
//
// File metadata (filename timestamps, mtime minus duration) provides the
// pairing prior -- which pairs overlap at all, in filename order -- and the
// fallback offset when anchors disagree with each other.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const alignSystem = `Two recordings of the same gaming session must be aligned. Recording A is
the player's separate voice-only microphone track. Recording B is the game
video: you get its visual event log and its own audio transcript (game NPCs
and other players over voice chat). The recordings share no audio and their
clocks are unrelated.
Find moments where the commentary in A demonstrably reacts to or coincides
with something on B's timeline: counting along an on-screen countdown,
reacting to an NPC line, announcing an event that the log shows. Only clear,
specific correspondences count.
Return strict JSON, nothing else:
{"anchors":[{"voice_t":<sec in A>,"video_t":<sec in B>,"evidence":"..."}]}
Give 5 to 15 anchors, spread across the session if possible.`

// metadataOffset estimates where the video starts on the voice timeline from
// recorder metadata alone: start time from the filename (Quest convention) or
// mtime minus duration; the voice recorder's mtime marks its end of writing.
func metadataOffset(video, audio string) (float64, error) {
	vdur, err := ffprobeDur(video)
	if err != nil {
		return 0, err
	}
	adur, err := ffprobeDur(audio)
	if err != nil {
		return 0, err
	}
	var vstart float64
	if m := regexp.MustCompile(`(\d{8})-(\d{6})`).FindStringSubmatch(filepath.Base(video)); m != nil {
		t, err := time.ParseInLocation("20060102-150405", m[1]+"-"+m[2], time.Local)
		if err != nil {
			return 0, err
		}
		vstart = float64(t.Unix())
	} else {
		fi, err := os.Stat(video)
		if err != nil {
			return 0, err
		}
		vstart = float64(fi.ModTime().Unix()) - vdur
	}
	fi, err := os.Stat(audio)
	if err != nil {
		return 0, err
	}
	astart := float64(fi.ModTime().Unix()) - adur
	return vstart - astart, nil
}

// alignPair aligns one (video, voice) pair and writes
// step3/<video>/align_<audio>.env. Pairs that metadata says do not overlap
// are recorded as skipped rather than force-matched.
func (a *App) alignPair(video, audio, s3 string) error {
	vbase, abase := baseName(video), baseName(audio)
	dir := filepath.Join(s3, vbase)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(dir, "align_"+abase+".env")

	voice := loadTSVRows(filepath.Join(a.outDir, "step1", abase, "transcript.tsv"))
	if len(voice) == 0 {
		return fmt.Errorf("no voice transcript for %s -- run step 1", abase)
	}
	adur := voice[len(voice)-1].e
	vdur, _ := ffprobeDur(video)

	// pairing prior: skip pairs whose files do not even overlap in wall time
	metaOff := math.NaN()
	if off, err := metadataOffset(video, audio); err == nil {
		metaOff = off
		overlap := math.Min(adur, off+vdur) - math.Max(0, off)
		if overlap < 30 {
			summary := fmt.Sprintf("skipped: no overlap per metadata (video starts %.0f s on voice timeline)", off)
			a.logfIdle(">>> [%s↔%s] %s", vbase, abase, summary)
			return os.WriteFile(envPath,
				[]byte(fmt.Sprintf("SKIPPED=1\nMETA_OFFSET=%.2f\nSUMMARY=%q\n", off, summary)), 0o644)
		}
	}

	events, _ := os.ReadFile(filepath.Join(a.outDir, "step2", vbase, "events.tsv"))
	game := loadTSVRows(filepath.Join(a.outDir, "step1", vbase, "transcript.tsv"))

	var vb, gb strings.Builder
	for _, r := range voice {
		fmt.Fprintf(&vb, "%.1f-%.1f: %s\n", r.s, r.e, r.text)
	}
	for _, r := range game {
		fmt.Fprintf(&gb, "%.1f-%.1f: %s\n", r.s, r.e, r.text)
	}
	user := fmt.Sprintf(`RECORDING A -- the player's voice mic (its own timeline):
%s
RECORDING B -- visual event log of the video (video timeline, start<TAB>end<TAB>event):
%s
RECORDING B -- the video's own audio (video timeline):
%s`, vb.String(), string(events), gb.String())

	type anchor struct {
		VoiceT   float64 `json:"voice_t"`
		VideoT   float64 `json:"video_t"`
		Evidence string  `json:"evidence"`
	}
	var anchors []anchor
	msgs := []map[string]any{msg("system", alignSystem), msg("user", user)}
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return err
		}
		reply, err := a.llmChatRetry(msgs, true)
		if err != nil {
			return err
		}
		var out struct {
			Anchors []anchor `json:"anchors"`
		}
		clean := strings.TrimSpace(reply)
		if i := strings.Index(clean, "{"); i >= 0 {
			clean = clean[i:]
		}
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
		problem := ""
		if err := json.Unmarshal([]byte(clean), &out); err != nil {
			problem = "not valid JSON: " + err.Error()
		} else if len(out.Anchors) < 3 {
			problem = "fewer than 3 anchors"
		} else {
			for _, an := range out.Anchors {
				if an.VoiceT < 0 || an.VoiceT > adur+60 || an.VideoT < 0 || an.VideoT > vdur+60 {
					problem = "anchor outside the recordings' time ranges"
					break
				}
			}
		}
		if problem == "" {
			anchors = out.Anchors
			break
		}
		a.logfIdle(">>> [%s↔%s] align attempt %d rejected: %s", vbase, abase, try+1, problem)
		msgs = append(msgs, msg("assistant", reply),
			msg("user", "Your answer failed validation: "+problem+". Return corrected strict JSON only."))
	}
	for _, an := range anchors {
		a.logfIdle(">>> [%s↔%s] anchor: voice %.1fs ↔ video %.1fs (Δ%.1f) -- %s",
			vbase, abase, an.VoiceT, an.VideoT, an.VoiceT-an.VideoT, an.Evidence)
	}

	// ---- the numbers: robust fit voice_t = offset + rate * video_t ---------
	median := func(v []float64) float64 {
		if len(v) == 0 {
			return math.NaN()
		}
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		return s[len(s)/2]
	}

	// Theil-Sen rate: median slope over well-separated anchor pairs
	rate := 1.0
	rateEstimable := false
	if len(anchors) >= 6 {
		var slopes []float64
		span := 0.0
		for i := 0; i < len(anchors); i++ {
			for j := i + 1; j < len(anchors); j++ {
				db := anchors[j].VideoT - anchors[i].VideoT
				if math.Abs(db) < 60 { // too close: slope would be noise
					continue
				}
				slopes = append(slopes, (anchors[j].VoiceT-anchors[i].VoiceT)/db)
				span = math.Max(span, math.Abs(db))
			}
		}
		if len(slopes) >= 5 && span >= 300 {
			r := median(slopes)
			// beyond 2% is not clock drift, it is bad anchors
			if math.Abs(r-1) <= 0.02 {
				rate = r
				rateEstimable = true
			}
		}
	}

	var offs []float64
	for _, an := range anchors {
		offs = append(offs, an.VoiceT-rate*an.VideoT)
	}
	off := median(offs)
	var devs []float64
	for _, o := range offs {
		devs = append(devs, math.Abs(o-off))
	}
	mad := median(devs)

	// decision: agreeing anchors win; scattered anchors mean the model
	// guessed, and the metadata prior is then the safer number
	offset, source := metaOff, "metadata"
	if len(offs) >= 4 && mad <= 1.5 {
		offset, source = off, fmt.Sprintf("%d anchors (±%.1fs)", len(offs), mad)
	} else {
		rate = 1.0 // a fallback offset never carries a fitted rate
	}
	if math.IsNaN(offset) {
		return fmt.Errorf("[%s↔%s] no usable alignment: %d anchors with ±%.1fs spread, and no metadata",
			vbase, abase, len(offs), mad)
	}
	if !math.IsNaN(metaOff) && math.Abs(offset-metaOff) > 5 {
		a.logfIdle(">>> [%s↔%s] WARNING: anchors and metadata disagree by %.1f s -- trusting %s",
			vbase, abase, math.Abs(offset-metaOff), source)
	}

	drift := "no drift detected (below anchor resolution)"
	if rateEstimable && math.Abs(rate-1) > 0.001 {
		drift = fmt.Sprintf("drift %.2f s/min -- linear correction applied", (rate-1)*60)
	}
	summary := fmt.Sprintf("offset %.2f s, rate %.5f via %s; metadata %.2f s; %s",
		offset, rate, source, metaOff, drift)
	a.logfIdle(">>> [%s↔%s] %s", vbase, abase, summary)

	env := fmt.Sprintf("OFFSET=%.2f\nRATE=%.6f\nDURATION=%.2f\nAUDIO_BASE=%s\nSOURCE=%q\nMETA_OFFSET=%.2f\nANCHORS=%d\nMAD=%.2f\nSUMMARY=%q\n",
		offset, rate, vdur, abase, source, metaOff, len(offs), mad, summary)
	return os.WriteFile(envPath, []byte(env), 0o644)
}
