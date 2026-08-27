package main

// Suggest: the Cut page's ▶.
//
// Everything else under cut_*.go is a hand doing something to the timeline --
// dragging an edge, dropping a card, muting a lane. This is the one path where
// the timeline arrives from outside: the session transcript goes out to a
// model, three prompts run over it in turn (cut, then audit, then effects), and
// what comes back is a set of segments and a set of effects the page then has
// to be talked into believing.
//
// Which is why it is its own file rather than a section of cut.go. It shares no
// state with the editing code beyond the two slices it hands over at the end,
// it is the only part of the page that can fail for reasons off this machine,
// and it is the only part with a progress bar -- a run here is minutes, and
// most of what these functions do is not choosing but explaining, clamping and
// double-checking what was chosen.
//
// The clamps are the point. A model asked for seconds will return seconds that
// overlap, run past the end of the footage, or hold a zoom for longer than the
// clip it is on; every reply is walked back onto the timeline it claims to
// describe (clampFxToSegs, keepFilmed, rowsInSegs) before the page sees it. A
// suggestion that cannot be edited by hand afterwards is worse than none.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

func (a *App) suggestClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	// re-suggesting over an untouched suggestion is fine -- there is no human
	// work in it to lose. Over hand edits it is not, so say what to press.
	if len(a.ed.segs) > 0 && !sameCut(a.ed.segs, a.ed.base.segs) {
		// ▶ is the only way to run this step now, so this line is where people meet
		// Revert -- it has to name the button as it looks, not as a glyph it lost
		a.setStatus("you have hand edits — ▶ will not throw them away; press Revert (beside Undo) " +
			"first if you want a fresh suggestion")
		return
	}
	rows := loadTSVRows(filepath.Join(a.transcriptDir(), "session.tsv"))
	if len(rows) == 0 {
		a.setStatus("run Describe first — the suggestion reads the session timeline, and there is none")
		return
	}
	session := sessionText(rows, a.narratorMic())
	target := 300.0
	fmt.Sscanf(a.ed.target.Text(), "%f", &target)
	// the Shorts style has a length of its own. The target box is usually
	// still set for the long cut -- minutes -- and a five-minute "Short" is a
	// mistake nobody means, so a target outside the format is read as the box
	// left over from other work rather than as a wish.
	shorts := a.promptPickName("cut") == shortsStyleName
	shortsClamped := shorts && (target < 15 || target > 45)
	if shortsClamped {
		target = 25
	}
	// how long the session runs, which is the denominator the choosing half of
	// the bar counts against (see suggestCut)
	span := 0.0
	for _, r := range rows {
		span = math.Max(span, r.e)
	}

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.qReset()
	a.updateRunControls()
	a.setStatus("suggesting a cut…")
	a.logExp.SetExpanded(true)
	a.logf(">>> suggest: target %.0f s, thinking over the session timeline — two long LLM calls "+
		"(choose, then audit what was chosen), expect a few minutes", target)
	if shortsClamped {
		a.logf(">>> a YouTube Short runs 20-30 s — the target box (%s s) is not one, aiming at 25 s instead",
			strings.TrimSpace(a.ed.target.Text()))
	}
	if shorts {
		a.logf(">>> the Shorts style makes a third, shorter call after those: effects are chosen " +
			"on the audited cut, so they land inside what is actually kept")
	}
	// Both calls are streamed, so the bar has real news to report -- but not
	// yet: the model thinks for minutes before it writes the first segment, and
	// there is nothing to measure in that. So it pulses until the first finished
	// segment arrives, and the thing that stops the pulse is that segment's own
	// fraction rather than a flag set from the goroutine. Pulse and SetFraction
	// drive the same needle, and the one that lasts has to be the one with real
	// news. (Same shape as publish; see there.)
	// the queue's first word, not the bar's: anything the queue says later
	// would otherwise land after this and wipe it (showProg runs on an idle)
	a.qJob(trackSTT, "suggest", 1, 2)
	a.prog(trackSTT, 0, "thinking over the whole session")
	glib.TimeoutAdd(150, func() bool {
		if !a.running {
			return false
		}
		a.progMu.Lock()
		moving := a.progParts[trackSTT] > 0
		a.progMu.Unlock()
		if moving {
			return false
		}
		a.progress.Pulse()
		return true
	})
	go func() {
		a.logCtx("suggest")
		segs, fx, err := a.suggestCut(session, target, span)
		if err == nil {
			a.qJob(trackSTT, "suggest", 2, 2)
			a.prog(trackSTT, suggestChooseShare, "reading the cut back")
			a.logfIdle(">>> audit: reading the %d proposed segments back against the brief — a second long call", len(segs))
			segs = a.auditCut(session, target, segs)
			// the effects, chosen LAST. Everything above this line may move a
			// segment; nothing below it does. Decorating any earlier is how
			// zooms ended up in the middle of stretches the audit had cut.
			if shorts {
				if got := a.suggestFx(rows, segs); len(got) > 0 {
					fx = got
				}
			}
		}
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				if !errors.Is(err, errStopped) {
					a.logf("suggest FAILED: %v", err)
				}
				a.progress.SetFraction(0)
				a.progress.SetText("suggest failed — see log")
				a.setStatus("suggest failed — see log")
				return
			}
			total := 0.0
			a.ed.pushUndo() // a suggestion is a proposal; Undo clears it again
			// A re-suggest replaces the old cut, never stacks on it -- but only
			// the footage half of it. The inserts were placed by hand, the model
			// was never told they exist, and an answer that does not mention them
			// is not an answer that dropped them.
			a.ed.segs = insertsOf(a.ed.segs)
			for _, s := range segs {
				a.ed.segs = append(a.ed.segs, cutSeg{
					S: a.ed.snapEdge(s.S, true), E: a.ed.snapEdge(s.E, false)})
				total += s.E - s.S
			}
			a.ed.coalesce()
			// Choosing the effects last is not enough on its own: snapEdge
			// and coalesce just moved the boundaries again, and a project's
			// edited cut prompt may still send fx in the cut reply, chosen
			// before the audit. The clamp is the guarantee -- whoever
			// proposed an effect, it lands inside the cut as applied, or
			// not at all.
			if len(fx) > 0 {
				kept := clampFxToSegs(fx, a.ed.segs)
				if n := len(fx) - len(kept); n > 0 {
					a.logf(">>> %d effect(s) pointed at footage the final cut does not keep — dropped", n)
				}
				fx = kept
			}
			// the effects the style proposed, on the page as if drawn by
			// hand. They REPLACE what was there: the moments they decorate
			// are the new suggestion's, and effects pinned to the old choice
			// would decorate stretches no longer in the video. Styles that
			// answer without fx (all but Shorts) change nothing here, and
			// the pushUndo above takes segments and effects back as one.
			if len(fx) > 0 {
				a.ed.fx = fx
				a.ed.dropFx()
			}
			a.ed.persist()
			a.ed.setBase() // from here on, Revert comes back to this suggestion
			a.progress.SetFraction(1)
			a.progress.SetText("cut suggested")
			a.logf(">>> suggested %d segments, %d:%02d total — edit away",
				len(segs), int(total)/60, int(total)%60)
			if len(fx) > 0 {
				a.logf(">>> ...and %d effect(s) from the style, on the fx lane like hand-drawn ones", len(fx))
			}
			a.setStatus(fmt.Sprintf("suggested %d segments", len(segs)))
		})
	}()
}

// One paragraph or bullet per line, unwrapped: see describeSystem.
//
// The session belongs in the cut prompt, not here: this one is read the same
// way for every project, and it is handed the cut prompt as the brief, so notes
// about what mattered in a session reach the audit anyway. What is worth
// editing here is how suspicious the audit is -- how readily it drops, how far
// it will extend an end.
const auditSystem = `You are checking a proposed highlight cut against the brief it was made from, before anyone watches it. You did not choose these moments. Your job is to find where they are wrong.

You get the brief, the target length, the session timeline and the proposed segments. Timeline lines are [mm:ss] then who, then the line. The minutes keep counting past 59, so [72:30] is 4350 seconds.
  [12:04] EVENT: what was on screen then
  [12:04] SPEAKER_01: something said out loud, which the video plays
  [12:04] NARRATOR: something said on the narrator's own microphone, which the voice-over will say

Return strict JSON, nothing else:
{"checks":[{"i":<number>,"verdict":"ok","start":<sec>,"end":<sec>,"why":"<short>"}],"add":[{"start":<sec>,"end":<sec>,"why":"<short>"}]}

- One check per proposed segment, all of them, in order, under the numbers you were given.
- verdict "ok" leaves it exactly as it is: repeat the start and end you were given and leave why empty.
- verdict "fix" keeps the moment and corrects its boundaries. Give the new start and end, and say in a few words what was wrong.
- verdict "drop" takes it out of the video. Use it sparingly, for a stretch where nothing happens or one that shows what another segment already showed.
- add is for what is missing. This is where most of your value is. Say why in a few words.

What you are checking, hardest first.

- Does every moment the editor names appear in the cut? The request may open with a block headed ABOUT THIS SESSION, written by someone who was there, and anything it singles out has to be in the video. If it is missing, add it. If a segment covers it but stops short, fix it.
- Does each segment run to its payoff? Read the timeline past the end of the segment. If the thing it is about is still being argued about, still being opened, still being decided, or gets its reaction after the end, then the end is too early. Extend it past the last line that belongs to the moment. This is the commonest fault and the one worth looking hardest for.
- Does each segment start early enough to make sense on its own? A reaction whose cause was cut off, or a punchline without its setup, needs the start moved back to where the setup begins.
- Is a boundary in the middle of a sentence? Move it into the gap between two lines.
- Would someone who was not there follow the video from these segments alone? The first one has to establish what the session is.

Rules you may not break.

- Only times the timeline actually shows. Never invent one.
- Only stretches with footage. A span with no EVENT lines has nothing to show and will be thrown away.
- After your corrections the segments must still be in order and must not overlap. If extending one would run into the next, extend it anyway and drop the next, saying so.
- Keep the total near the target. If your fixes and additions make it much longer, pay for them by dropping the weakest segments.
- When a segment is right, say ok. A check that changes something for the sake of changing it is worse than no check at all.`

// auditCut is the second read of a suggestion, against the brief that produced
// it. The first call chooses moments from thousands of timeline lines at once;
// this one has far less to do -- it has the moments in front of it and only has
// to ask whether each one is right -- and that is what makes it worth a second
// long call. It is also the only check with any judgement in it: everything the
// code validates is arithmetic (does the JSON parse, is there footage, does the
// total land near the target), and none of that can see a segment that ends
// forty seconds before the chest is opened.
//
// It never fails the run. Anything wrong with the audit -- a refusal, bad JSON,
// a corrected cut that no longer passes the arithmetic -- leaves the original
// suggestion standing and says so in the log. A second opinion that can lose
// you the first one is not worth having.
func (a *App) auditCut(session string, target float64, segs []cutSeg) []cutSeg {
	var props strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&props, "#%d  [%s] to [%s]  (%.0f s)\n", i+1, mmss(s.S), mmss(s.E), s.E-s.S)
	}
	user := a.ctxBlock() + fmt.Sprintf("THE BRIEF THE CUT WAS MADE FROM:\n%s\n\nTARGET LENGTH: %.0f seconds.\n\n"+
		"PROPOSED SEGMENTS:\n%s\nSESSION TIMELINE:\n%s", a.prompt("cut"), target, props.String(), session)
	msgs := []map[string]any{msg("system", a.prompt("audit")), msg("user", user)}

	if err := a.checkpoint(); err != nil {
		return segs
	}
	// the rest of the bar, and this half has a denominator: one check per
	// proposed segment is what the audit was asked for
	best := 0
	onText := func(s string) {
		n, _ := jsonItemsDone(s, "checks")
		if len(segs) == 0 || n <= best {
			return
		}
		best = n
		a.prog(trackSTT, suggestChooseShare+(1-suggestChooseShare)*
			math.Min(1, float64(best)/float64(len(segs))),
			"checked %d/%d", best, len(segs))
	}
	reply, err := a.llmChatRetryOn(msgs, true, onText)
	if err != nil {
		a.logfIdle(">>> audit skipped: %v — keeping the suggestion as it is", err)
		return segs
	}
	clean := strings.TrimSpace(reply)
	if i := strings.Index(clean, "{"); i >= 0 {
		clean = clean[i:]
	}
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var out struct {
		Checks []struct {
			I          int
			Verdict    string
			Start, End float64
			Why        string
		} `json:"checks"`
		Add []struct {
			Start, End float64
			Why        string
		} `json:"add"`
	}
	if err := json.Unmarshal([]byte(clean), &out); err != nil {
		a.logfIdle(">>> audit answered with something that is not JSON — keeping the suggestion as it is")
		return segs
	}

	keep := make([]cutSeg, len(segs))
	copy(keep, segs)
	drop := make([]bool, len(segs))
	fixed, dropped := 0, 0
	for _, c := range out.Checks {
		i := c.I - 1
		if i < 0 || i >= len(segs) {
			continue // a number for a segment that was never proposed
		}
		switch strings.ToLower(strings.TrimSpace(c.Verdict)) {
		case "drop":
			drop[i] = true
			dropped++
			a.logfIdle("    audit − #%d [%s]–[%s]: %s", c.I, mmss(segs[i].S), mmss(segs[i].E), c.Why)
		case "fix":
			if c.End <= c.Start {
				continue
			}
			if c.Start == segs[i].S && c.End == segs[i].E {
				continue // "fix" with nothing changed is an ok
			}
			a.logfIdle("    audit ~ #%d [%s]–[%s] → [%s]–[%s] (%+.0f s): %s", c.I,
				mmss(segs[i].S), mmss(segs[i].E), mmss(c.Start), mmss(c.End),
				(c.End-c.Start)-(segs[i].E-segs[i].S), c.Why)
			keep[i] = cutSeg{S: c.Start, E: c.End}
			fixed++
		}
	}
	var res []cutSeg
	for i, s := range keep {
		if !drop[i] {
			res = append(res, s)
		}
	}
	for _, ad := range out.Add {
		if ad.End <= ad.Start {
			continue
		}
		a.logfIdle("    audit + [%s]–[%s] (%.0f s): %s", mmss(ad.Start), mmss(ad.End), ad.End-ad.Start, ad.Why)
		res = append(res, cutSeg{S: ad.Start, E: ad.End})
	}
	if fixed+dropped+len(out.Add) == 0 {
		a.logfIdle(">>> audit: all %d segments pass, nothing changed", len(segs))
		return segs
	}

	res = a.keepFilmed(res)
	sort.Slice(res, func(i, j int) bool { return res[i].S < res[j].S })
	// the audit is told not to overlap and mostly does not; where extending one
	// segment reached into the next, one longer segment is what was meant
	var merged []cutSeg
	for _, s := range res {
		if n := len(merged); n > 0 && s.S <= merged[n-1].E {
			if s.E > merged[n-1].E {
				merged[n-1].E = s.E
			}
			continue
		}
		merged = append(merged, s)
	}
	total := 0.0
	for _, s := range merged {
		total += s.E - s.S
	}
	if len(merged) < minSuggestSegs(target) || total < target*0.6 || total > target*1.5 {
		a.logfIdle(">>> audit result rejected (%d segments, %.0f s against a %.0f s target) — "+
			"keeping the suggestion as it is", len(merged), total, target)
		return segs
	}
	a.logfIdle(">>> audit: %d fixed, %d dropped, %d added — %d segments, %d:%02d total",
		fixed, dropped, len(out.Add), len(merged), int(total)/60, int(total)%60)
	return merged
}

// keepFilmed drops what cannot be shown: a segment with no recording at either
// end is time nobody has footage of. Both the suggestion and the audit go
// through it, so neither can put a hole in the video.
func (a *App) keepFilmed(segs []cutSeg) []cutSeg {
	var out []cutSeg
	for _, s := range segs {
		// an insert brings its own picture, which is the entire point of it --
		// "no recording under it" is its normal state, not a fault
		if !s.isInsert() && a.ed.videoAt(s.S) == nil && a.ed.videoAt(s.E) == nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// minSuggestSegs is how few segments a suggestion may come back with before it
// is arithmetic nonsense rather than a choice. Four made sense when every cut
// was minutes long; a 25-second Short holds one to three beats, and rejecting
// a two-segment answer to a 25 s target burns attempts on a rule the target
// itself contradicts. The audit accepts by the same count.
func minSuggestSegs(target float64) int {
	if n := 1 + int(target/30); n < 4 {
		return n
	}
	return 4
}

// sugFx is one effect as a cut style's reply spells it: a kind, the stretch of
// session seconds it covers, and the one number that kind needs. Only the
// Shorts style asks for these; a reply without the list parses to none.
type sugFx struct {
	Kind       string
	Start, End float64
	Rate       float64
	Text       string
}

// fxFromReply turns proposed effects into the page's own cutFx. The model is
// trusted with WHEN and WHICH KIND; everything about HOW -- where a zoom
// centres, how a caption is boxed, how long a fade runs -- is this app's own
// defaults, the same ones the fx dialogs open with (a centre punch-in; the
// caption box is left empty for textBox() to fill in). Entries that make no
// sense are dropped rather than failing the run: the segments are the work,
// the effects are seasoning.
func fxFromReply(in []sugFx) []cutFx {
	var out []cutFx
	for _, f := range in {
		if len(out) == 8 {
			break // seasoning: past a handful it is noise, not a cut
		}
		d := f.End - f.Start
		switch strings.ToLower(strings.TrimSpace(f.Kind)) {
		case "zoom":
			if d <= 0 {
				continue
			}
			g := math.Min(1, d/3) // the dialog's second of glide, shrunk to fit a short zoom
			out = append(out, cutFx{Kind: "zoom", T: f.Start, Dur: d,
				Cx: 0.5, Cy: 0.5, Hf: 0.6, Trans: g, Tout: g})
		case "speed":
			if d <= 0 {
				continue
			}
			rate := f.Rate
			if rate <= 0 {
				rate = 0.5 // an unnamed rate means slow-mo; 0 would be a freeze nobody asked for
			}
			rate, d = clampSpeed(rate, d)
			// the same fades a suggested zoom and caption get, on the same
			// terms: a second either side where the stretch is long enough to
			// spend it, and nothing where the render could not build the
			// stairs anyway -- which at a fast rate wants more footage than a
			// second, because a stair is measured on the finished video
			ramp := math.Min(1, d/4)
			if ramp < rampStep*math.Max(1, rate) {
				ramp = 0
			}
			out = append(out, cutFx{Kind: "speed", T: f.Start, Dur: d, Rate: rate,
				Trans: ramp, Tout: ramp})
		case "text":
			txt := strings.TrimSpace(f.Text)
			if txt == "" {
				continue // a caption with no words is nothing to draw
			}
			if d <= 0 {
				d = 3 // the dialog's own opening duration
			}
			fade := math.Min(0.3, d/4)
			out = append(out, cutFx{Kind: "text", T: f.Start, Dur: d,
				Trans: fade, Tout: fade, Text: txt})
		}
	}
	return out
}

func (a *App) suggestCut(session string, target, span float64) ([]cutSeg, []cutFx, error) {
	system := a.prompt("cut")
	user := a.ctxBlock() + fmt.Sprintf("TARGET LENGTH: %.0f seconds.\n\nSESSION TIMELINE:\n%s", target, session)
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	// The bar, while a call that takes minutes runs: segments counted as they
	// close, placed by where in the session they land (see jsonItemsDone).
	// Only ever forward -- a rejected attempt starts the count again, and a bar
	// that fell back to the first minute would read as work being undone rather
	// than redone.
	best := 0.0
	onText := func(s string) {
		n, through := jsonItemsDone(s, "segments")
		if n == 0 {
			return // still thinking, and the pulse says that better than a 0 does
		}
		f := float64(n) / suggestMaxSegs // no timeline to place them on: count
		if span > 0 && through > 0 {
			f = through / span
		}
		// never quite 0: a first segment two minutes into a two-hour session is
		// news, and reporting it as nothing would leave the pulse running
		f = math.Max(0.02, math.Min(1, f))
		if f <= best {
			return
		}
		best = f
		if span > 0 {
			a.prog(trackSTT, suggestChooseShare*f, "%d moments, at %s of %s",
				n, mmss(through), mmss(span))
		} else {
			a.prog(trackSTT, suggestChooseShare*f, "%d moments", n)
		}
	}
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, nil, err
		}
		reply, err := a.llmChatRetryOn(msgs, true, onText)
		if err != nil {
			return nil, nil, err
		}
		clean := strings.TrimSpace(reply)
		if i := strings.Index(clean, "{"); i >= 0 {
			clean = clean[i:]
		}
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
		var out struct {
			Segments []struct{ Start, End float64 } `json:"segments"`
			Fx       []sugFx                        `json:"fx"`
		}
		problem := ""
		if err := json.Unmarshal([]byte(clean), &out); err != nil {
			problem = "not valid JSON: " + err.Error()
		} else if len(out.Segments) < minSuggestSegs(target) {
			problem = fmt.Sprintf("fewer than %d segments", minSuggestSegs(target))
		} else {
			var segs []cutSeg
			for _, s := range out.Segments {
				if s.End <= s.Start {
					problem = "segment with end before start"
					break
				}
				segs = append(segs, cutSeg{S: s.Start, E: s.End})
			}
			// only video-backed time counts, and it is counted after the drop:
			// a suggestion that spent half its length on stretches nobody
			// filmed is short, and being told the number it actually landed on
			// is what makes the next attempt aim elsewhere
			asked := len(segs)
			segs = a.keepFilmed(segs)
			if n := asked - len(segs); n > 0 {
				a.logfIdle(">>> suggest attempt %d: %d segment(s) dropped for having no footage", try+1, n)
			}
			total := 0.0
			for _, s := range segs {
				total += s.E - s.S
			}
			if problem == "" {
				if total < target*0.6 || total > target*1.5 {
					problem = fmt.Sprintf("total %.0fs, target %.0fs", total, target)
				} else {
					return segs, fxFromReply(out.Fx), nil
				}
			}
		}
		a.logfIdle(">>> suggest attempt %d rejected: %s", try+1, problem)
		msgs = append(msgs, msg("assistant", reply),
			msg("user", "Your answer failed validation: "+problem+". Return corrected strict JSON only."))
	}
	return nil, nil, fmt.Errorf("no valid cut after 3 attempts")
}

// This guidance used to be the Effects section of shortsSystem, asked for in
// the same reply as the segments -- see the note above suggestFx for why that
// could never line up. Here the segments are final before a word of this is
// sent, and the model is shown nothing outside them to get attached to.
//
// One paragraph or bullet per line, unwrapped: see describeSystem.
const effectsSystem = `You decorate a YouTube Short that is already cut: a few zooms, speed changes and captions on footage someone else chose. The segments are final. You cannot move them, and an effect outside them decorates footage the viewer never sees.

You get the segments of the cut, then the session timeline of only those stretches. Timeline lines are [mm:ss] then who, then the line. The minutes keep counting past 59, so [72:30] is 4350 seconds.
  [12:04] EVENT: what was on screen then, and whether it was hectic or calm
  [12:04] SPEAKER_01: something said out loud, which the video plays
  [12:04] NARRATOR: something said on the narrator's own microphone, which the voice-over will say

Return strict JSON, nothing else:
{"fx":[{"kind":"zoom","start":<sec>,"end":<sec>},{"kind":"speed","start":<sec>,"end":<sec>,"rate":<number>},{"kind":"text","start":<sec>,"end":<sec>,"text":"<words>"}]}

- start and end are session seconds: mm*60+ss from the stamps. Every effect lies inside one of the segments you were given -- one that hangs over an edge is trimmed to fit, and one outside every segment is thrown away.
- The request may open with a block headed ABOUT THIS SESSION: the editor's notes on what this Short is about. Let it steer what deserves the eye and what a caption says.
- Use effects where they earn their place, not everywhere. Two or three across the whole Short is plenty; a Short that is all effects reads as noise. An empty list is a fine answer for a clip that carries itself.
- zoom is a centre punch-in. Use it to put the eye on the thing that matters at the moment it matters -- the hit, the reveal, the reaction. Two to four seconds.
- speed rescales the clock: rate 0.5 for the one impact worth savouring at half speed, 2 or more to rush a stretch the moment needs but the viewer does not. rate is the footage's clock times that number.
- text is a caption on screen. Phones are watched with the sound off more often than not, so a caption over the key line is never wasted: the one line of context that lets the clip open mid-action, or the punchline written out. Under about eight words.`

// rowsInSegs is the timeline the cut kept: only the lines that overlap a
// footage segment. It is what suggestFx shows the model -- an effect can only
// be pinned to a line it was shown, so a timeline with no outside lines is a
// prompt whose whole vocabulary lies inside the cut.
func rowsInSegs(rows []tsvRow, segs []cutSeg) []tsvRow {
	var out []tsvRow
	for _, r := range rows {
		for _, s := range segs {
			if !s.isInsert() && r.e > s.S && r.s < s.E {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// suggestFx is the Shorts style's third call, made only once the audit has
// settled the segments. The effects used to ride along in the cut reply, and
// the audit then fixed, dropped and added segments under them -- so a zoom
// chosen for the proposed cut sat in the middle of nothing on the final one.
// Ordering is the fix: decorate LAST, and show the model only what survived.
//
// It never fails the run and cannot lose the cut: a refusal or bad JSON is a
// log line and no effects. The segments are the work, the effects are
// seasoning. The caller keeps whatever inline fx the cut reply carried (none
// from the shipped prompt; a project's edited copy may still send some) when
// this returns nothing.
func (a *App) suggestFx(rows []tsvRow, segs []cutSeg) []cutFx {
	if err := a.checkpoint(); err != nil {
		return nil
	}
	a.logfIdle(">>> effects: choosing zooms, speeds and captions for the final %d segment(s) — "+
		"a third, shorter call", len(segs))
	var props strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&props, "#%d  [%s] to [%s]  (%.0f s)\n", i+1, mmss(s.S), mmss(s.E), s.E-s.S)
	}
	user := a.ctxBlock() + fmt.Sprintf("THE SEGMENTS OF THE CUT:\n%s\nSESSION TIMELINE (only the stretches the cut kept):\n%s",
		props.String(), sessionText(rowsInSegs(rows, segs), a.narratorMic()))
	msgs := []map[string]any{msg("system", a.prompt("effects")), msg("user", user)}
	reply, err := a.llmChatRetryOn(msgs, true, nil)
	if err != nil {
		a.logfIdle(">>> effects skipped: %v — the cut stands, undecorated", err)
		return nil
	}
	clean := strings.TrimSpace(reply)
	if i := strings.Index(clean, "{"); i >= 0 {
		clean = clean[i:]
	}
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var out struct {
		Fx []sugFx `json:"fx"`
	}
	if err := json.Unmarshal([]byte(clean), &out); err != nil {
		a.logfIdle(">>> effects answered with something that is not JSON — the cut stands, undecorated")
		return nil
	}
	return fxFromReply(out.Fx)
}

// clampFxToSegs holds a proposed effect to the cut as it will actually play:
// inside a footage segment, or gone. It has to run against the segments AS
// APPLIED -- after the audit, after snapEdge, after coalesce -- because every
// one of those moves boundaries after the effects were chosen, and a rule
// enforced any earlier is enforced against a cut that no longer exists.
// Inserts do not count as home: they bring their own picture, and an effect
// proposed off the timeline was never about one.
func clampFxToSegs(fx []cutFx, segs []cutSeg) []cutFx {
	var out []cutFx
	for _, f := range fx {
		t0, t1 := f.fxSpan()
		// a point -- a bare view -- has no width to overlap with; being
		// inside a segment is the whole question
		if t1 <= t0 {
			for _, s := range segs {
				if !s.isInsert() && s.S <= t0 && t0 <= s.E {
					out = append(out, f)
					break
				}
			}
			continue
		}
		// the footage segment this effect shares the most time with
		best, overlap := -1, 0.0
		for i, s := range segs {
			if s.isInsert() {
				continue
			}
			if o := math.Min(t1, s.E) - math.Max(t0, s.S); o > overlap {
				best, overlap = i, o
			}
		}
		if best < 0 {
			continue // no kept footage under it at all
		}
		s := segs[best]
		if t0 >= s.S && t1 <= s.E {
			out = append(out, f) // already inside: exactly as proposed
			continue
		}
		was := t1 - t0 // the band before the cut took a piece off it
		nt0, nt1 := math.Max(t0, s.S), math.Min(t1, s.E)
		if nt1-nt0 < 1 {
			// under a second left is not the effect that was meant: most of
			// the moment it decorated is not in the video
			continue
		}
		f.T, f.Dur = nt0, nt1-nt0
		switch f.Kind {
		case "zoom", "text", "svg":
			// nothing kind-specific: the band is the band, and the fades
			// inside it are shrunk with it below
		case "speed":
			// a rate has to stay playable over the band it was trimmed to.
			// A stop is not a rate and gets none: clampSpeed would hand the
			// frozen frame one, which is a stop that quietly became slow
			// motion because the cut moved under it.
			if !f.frozenFx() {
				f.Rate, f.Dur = clampSpeed(f.Rate, f.Dur)
			}
		default:
			continue // a kind with no span to trim
		}
		trimFades(&f, was)
		out = append(out, f)
	}
	return out
}
