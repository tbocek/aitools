package main

// Suggest: the Cut page's ▶.
//
// Everything else under cut_*.go is a hand doing something to the timeline --
// dragging an edge, dropping a card, muting a lane. This is the one path where
// the timeline arrives from outside: the session transcript goes out to a
// model, two prompts run over it in turn -- the cut, which chooses segments
// and (for the Shorts style) the effects that decorate them, then an audit
// that reads both back -- and what comes back is a set of segments and a set
// of effects the page then has to be talked into believing.
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
	rows := a.sessionRows()
	if len(rows) == 0 {
		a.setStatus("run Describe first — the suggestion reads the session timeline, and there is none")
		return
	}
	session := sessionText(rows, a.narratorMic())
	target := 300.0
	fmt.Sscanf(a.ed.target.Text(), "%f", &target)
	// the Shorts style has a length of its own. Picking the wording already
	// set the box (styleTarget), but the box stays editable, so the same
	// judgement -- a target outside the format is the box left over from other
	// work, not a wish -- is made again at the moment it counts.
	shorts := a.promptPickName("cut") == shortsStyleName
	shortsClamped := false
	if shorts {
		target, shortsClamped = shortsTargetFix(target)
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
	a.logExp.SetExpanded(true)
	a.logf(">>> suggest: target %.0f s — two LLM calls (choose, then audit), a few minutes", target)
	if shortsClamped {
		a.logf(">>> target %s s is not a Short (20-30 s) — aiming at %.0f s instead",
			strings.TrimSpace(a.ed.target.Text()), shortsLen)
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
			a.logfIdle(">>> audit: %d segments and %d effect(s) read back against the brief", len(segs), len(fx))
			segs, fx = a.auditCut(session, target, segs, fx)
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
				return
			}
			a.ed.pushUndo() // a suggestion is a proposal; Undo clears it again
			// A re-suggest replaces the old cut, never stacks on it -- but only
			// the footage half of it. The inserts were placed by hand, the model
			// was never told they exist, and an answer that does not mention them
			// is not an answer that dropped them.
			a.ed.segs = insertsOf(a.ed.segs)
			for _, s := range segs {
				a.ed.segs = append(a.ed.segs, cutSeg{
					S: a.ed.snapEdge(s.S, true), E: a.ed.snapEdge(s.E, false)})
			}
			a.ed.coalesce()
			// The audit checks the effects against the segments, but it is
			// a model being asked; snapEdge and coalesce also just moved the
			// boundaries again. The clamp is the guarantee -- whoever
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
			// would decorate stretches no longer in the video. A reply that
			// proposed none changes nothing here, and the pushUndo above
			// takes segments and effects back as one.
			if len(fx) > 0 {
				a.ed.fx = fx
				a.ed.dropFx()
			}
			a.ed.persist()
			a.ed.setBase() // from here on, Revert comes back to this suggestion
			a.progress.SetFraction(1)
			a.progress.SetText(fmt.Sprintf("suggested %d segments", len(segs)))
			// the length of the VIDEO this makes, effects included, which is
			// what the target was a target for: read straight off the
			// segments it would over-report every speed-up in the answer
			total := a.ed.cutLen()
			a.logf(">>> suggested %d segments, %d:%02d total",
				len(segs), int(total)/60, int(total)%60)
			if len(fx) > 0 {
				a.logf(">>> ...and %d effect(s) from the style", len(fx))
			}
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
const auditSystem = `You are checking a proposed cut against the brief it was made from, before anyone watches it. You did not choose these moments; your job is to find where they are wrong.

Answer in the audit's shape. drop is for a stretch where nothing happens or that repeats another segment, and sparingly; add is where most of your value is. An effect follows the segment you moved it with, and goes with a segment you dropped.

For each segment, ask in this order.

1. Does it run to its payoff? Read the timeline past its end: if the thing it is about is still being argued, opened or decided, or gets its reaction after the end, extend past the last line that belongs to the moment. This is the commonest fault.
2. Does it start early enough to make sense on its own? Move the start back to where the setup begins.
3. Is either boundary in the middle of a sentence? Move it into the gap between two lines.
4. Is the thing it is about on screen -- does an EVENT line inside it show it?

Then for the whole cut.

5. Every moment the ABOUT THIS SESSION notes name must be in the cut. Add it if missing; fix a segment that stops short of it.
6. The first segment must establish what the session is.
7. After your corrections the segments must still be in order and must not overlap. If extending one runs into the next, extend it and drop the next, saying so.
8. Keep the total near the target: pay for additions by dropping the weakest segments.

When a segment is right, say ok. A change for its own sake is worse than no check at all.`

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
func (a *App) auditCut(session string, target float64, segs []cutSeg, fx []cutFx) ([]cutSeg, []cutFx) {
	var props strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&props, "#%d  [%s] to [%s]  (%.0f s)\n", i+1, mmss(s.S), mmss(s.E), s.E-s.S)
	}
	// the effects under their own numbers, when there are any: the audit is
	// asked about them by number exactly as it is asked about the segments
	fxBlock := ""
	if len(fx) > 0 {
		var b strings.Builder
		for i, f := range fx {
			t0, t1 := f.fxSpan()
			extra := ""
			kind := f.Kind
			switch {
			case f.frozenFx():
				kind = "stop" // "speed rate 0" is the storage, not the effect
			case f.Kind == "speed":
				extra = fmt.Sprintf("  rate %g", f.Rate)
			case f.Kind == "volume":
				extra = fmt.Sprintf("  gain %g", f.Gain)
			case f.Kind == "text":
				extra = fmt.Sprintf("  %q", f.Text)
			}
			fmt.Fprintf(&b, "#%d  %s  [%s] to [%s]%s\n", i+1, kind, mmss(t0), mmss(t1), extra)
		}
		fxBlock = "PROPOSED EFFECTS:\n" + b.String() + "\n"
	}
	user := a.ctxBlock() + fmt.Sprintf("THE BRIEF THE CUT WAS MADE FROM:\n%s\n\nTARGET LENGTH: %.0f seconds.\n\n"+
		"PROPOSED SEGMENTS:\n%s\n%sSESSION TIMELINE:\n%s", a.prompt("cut"), target, props.String(), fxBlock, session)
	msgs := []map[string]any{msg("system", a.sysPrompt("audit")), msg("user", user)}

	if err := a.checkpoint(); err != nil {
		return segs, fx
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
	reply, err := a.llmChatRetryOn("audit", msgs, true, onText)
	if err != nil {
		a.logfIdle(">>> audit skipped (%v) — the suggestion stands", err)
		return segs, fx
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
		FxChecks []fxCheck `json:"fxchecks"`
	}
	if err := json.Unmarshal([]byte(clean), &out); err != nil {
		a.logfIdle(">>> audit did not answer in JSON — the suggestion stands")
		return segs, fx
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
	fxOut, fxChanged := a.applyFxChecks(fx, out.FxChecks)
	if fixed+dropped+len(out.Add)+fxChanged == 0 {
		a.logfIdle(">>> audit: all %d segments pass, nothing changed", len(segs))
		return segs, fx
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
	// with the effects the audit is returning laid over it: a check that adds
	// a ×4 and then measures the cut without it is checking a video nobody
	// will watch (applyFx, cut_speedmix.go)
	total := cutLen(applyFx(merged, fxOut))
	lo, hi := a.suggestWindow(target)
	if len(merged) < minSuggestSegs(target) || total < lo || total > hi {
		a.logfIdle(">>> audit rejected: %d segments, %.0f s against a %.0f s target — "+
			"the suggestion stands", len(merged), total, target)
		return segs, fx
	}
	a.logfIdle(">>> audit: %d fixed, %d dropped, %d added, %d effect(s) changed — %d segments, %d:%02d total",
		fixed, dropped, len(out.Add), fxChanged, len(merged), int(total)/60, int(total)%60)
	return merged, fxOut
}

// fxCheck is the audit's verdict on one proposed effect, by its number.
type fxCheck struct {
	I          int
	Verdict    string
	Start, End float64
	Why        string
}

// applyFxChecks reads the audit's effect verdicts back onto the proposal,
// with the segment checks' own discipline: a number never proposed is
// ignored, a fix that changes nothing is an ok, and a fix has to leave a
// playable band -- fades shrink with it (trimFades) and a speed keeps a rate
// the render can build (clampSpeed), exactly as clampFxToSegs trims. The
// count going back is how many verdicts changed something, which is what the
// caller folds into "did the audit change anything at all".
func (a *App) applyFxChecks(fx []cutFx, checks []fxCheck) ([]cutFx, int) {
	keep := make([]cutFx, len(fx))
	copy(keep, fx)
	drop := make([]bool, len(fx))
	changed := 0
	for _, c := range checks {
		i := c.I - 1
		if i < 0 || i >= len(fx) {
			continue
		}
		t0, t1 := fx[i].fxSpan()
		switch strings.ToLower(strings.TrimSpace(c.Verdict)) {
		case "drop":
			drop[i] = true
			changed++
			a.logfIdle("    audit − fx #%d %s [%s]–[%s]: %s", c.I, fx[i].Kind, mmss(t0), mmss(t1), c.Why)
		case "fix":
			if c.End <= c.Start {
				continue
			}
			if c.Start == t0 && c.End == t1 {
				continue // "fix" with nothing changed is an ok
			}
			f := keep[i]
			switch f.Kind {
			case "zoom", "text", "svg", "speed", "volume":
			default:
				continue // a kind with no span to move
			}
			was := t1 - t0
			f.T, f.Dur = c.Start, c.End-c.Start
			if f.Kind == "speed" && !f.frozenFx() {
				f.Rate, f.Dur = clampSpeed(f.Rate, f.Dur)
			}
			trimFades(&f, was)
			keep[i] = f
			changed++
			a.logfIdle("    audit ~ fx #%d %s [%s]–[%s] → [%s]–[%s]: %s", c.I, fx[i].Kind,
				mmss(t0), mmss(t1), mmss(c.Start), mmss(c.End), c.Why)
		}
	}
	var out []cutFx
	for i, f := range keep {
		if !drop[i] {
			out = append(out, f)
		}
	}
	return out, changed
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
// was minutes long; a Short's count is however many beats its notes name, and
// rejecting a two-segment answer to a 25 s target burns attempts on a rule
// the target itself contradicts. The audit accepts by the same count.
func minSuggestSegs(target float64) int {
	if n := 1 + int(target/30); n < 4 {
		return n
	}
	return 4
}

// suggestWindow is how far a suggestion's total may drift from the target
// before it is rejected -- by the suggest loop and by the audit alike. The
// wide band exists because a highlight cut is a wish, not a contract:
// minutes-long cuts land where the material lets them. A Short is the
// opposite -- 20 to 30 seconds is a promise to the viewer -- so its ceiling
// is a fifth over instead of half over: a 25-second target must not ship as a
// 37-second "Short", and being told so is what makes the next attempt trim
// inside its beats (the budget in shortsSystem) rather than keep the length.
// The floor stays shared: too little footage is the same failure everywhere.
func (a *App) suggestWindow(target float64) (lo, hi float64) {
	if a.promptPickName("cut") == shortsStyleName {
		return target * 0.6, target * 1.2
	}
	return target * 0.6, target * 1.5
}

// sugFx is one effect as a cut style's reply spells it: a kind, the stretch of
// session seconds it covers, and the one number that kind needs. Every style
// asks for these; a reply without the list parses to none.
type sugFx struct {
	Kind       string
	Start, End float64
	Rate       float64
	// a pointer because 0 is a gain somebody can mean. Silence is the whole
	// reason a session says "do not use this audio", and as a plain float64
	// that instruction and a volume effect with no gain field at all arrive
	// here as the same 0 -- so obeying one meant obeying the other, and the
	// parser chose to obey neither. Absent is nil; explicit is a number.
	Gain *float64
	Text string
}

// fxFromReply turns proposed effects into the page's own cutFx. The model is
// trusted with WHEN and WHICH KIND; everything about HOW -- where a zoom
// centres, how a caption is boxed, how long a fade runs -- is this app's own
// defaults, the same ones the fx dialogs open with (a centre punch-in; the
// caption box is left empty for textBox() to fill in). Entries that make no
// sense are dropped rather than failing the run: the segments are the work,
// the effects are seasoning.
//
// Four of the app's five kinds can be asked for. The fifth, svg, cannot: a
// drawing is a file on this machine and nothing in a reply can name one, so
// the overlay stays a thing a hand places.
// fxMaxProposed is how many effects a reply may place. It was a handful when
// the only style asking for them cut a 25-second Short; the long-form styles
// ask now too, and their wording budgets three or four per five minutes of
// finished video -- so the ceiling is what that rate reaches on a cut far
// longer than anyone makes, and a reply past it is a model decorating instead
// of editing.
const fxMaxProposed = 24

func fxFromReply(in []sugFx) []cutFx {
	var out []cutFx
	for _, f := range in {
		if len(out) == fxMaxProposed {
			break
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
		case "stop":
			// the held frame: the picture stands still at f.Start while the
			// footage runs on under it, which is a speed of 0 (frozenFx). It
			// is a kind of its own in the reply because a rate cannot say it
			// -- an omitted rate and a rate of 0 are the same JSON number,
			// and reading that as a freeze would hand a still to every reply
			// that forgot to name a speed
			if d <= 0 {
				d = 2 // long enough to read as a hold, short enough not to strand the viewer
			}
			fade := math.Min(0.3, d/4)
			out = append(out, cutFx{Kind: "speed", T: f.Start, Dur: d, Rate: 0,
				Trans: fade, Tout: fade})
		case "volume":
			if d <= 0 {
				continue
			}
			// a gain nobody named is a field left out, and there is nothing
			// to do about it; 1 is dropped for the plainer reason that it
			// does nothing. An explicit 0 is kept -- that is a stretch the
			// session wanted silent.
			if f.Gain == nil || *f.Gain == 1 {
				continue
			}
			// the same second either way that a suggested zoom and caption get
			ramp := math.Min(1, d/4)
			out = append(out, cutFx{Kind: "volume", T: f.Start, Dur: d,
				Gain: clampGain(*f.Gain), Trans: ramp, Tout: ramp})
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
	system := a.sysPrompt("cut")
	user := a.ctxBlock() + fmt.Sprintf("TARGET LENGTH: %.0f seconds.\n\nSESSION TIMELINE:\n%s", target, session)
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	// the web, for a caption that names a thing the timeline does not explain
	tools, ffx := a.webToolsFor("suggest")
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
	// thinking, until an attempt comes back with nothing: see thinkAgain
	think := true
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, nil, err
		}
		reply, err := a.llmChatRetryTools("suggest", msgs, think, tools, a.webRunner("suggest", ffx), onText)
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
		problem := noAnswer(reply)
		if problem != "" {
			// nothing to parse: say so rather than reporting the parser's
			// bafflement at an empty string (llm.go)
		} else if err := json.Unmarshal([]byte(clean), &out); err != nil {
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
			// against the target as the video will run, not as the segments
			// read: an answer that spends a minute of footage at ×4 has
			// proposed fifteen seconds of video, and telling it the minute is
			// telling it something it can do nothing with
			fx := fxFromReply(out.Fx)
			total := cutLen(applyFx(segs, fx))
			if problem == "" {
				if lo, hi := a.suggestWindow(target); total < lo || total > hi {
					problem = fmt.Sprintf("total %.0fs, target %.0fs", total, target)
				} else {
					return segs, fx, nil
				}
			}
		}
		a.logfIdle(">>> suggest attempt %d rejected: %s", try+1, problem)
		if next := thinkAgain(think, reply); next != think {
			think = next
			a.logfIdle(">>> suggest: the model spent the whole call thinking and wrote " +
				"nothing — asking again with thinking off")
		}
		msgs = retryTurn(msgs, reply, problem)
	}
	return nil, nil, fmt.Errorf("no valid cut after 3 attempts")
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
		case "zoom", "text", "svg", "volume":
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
