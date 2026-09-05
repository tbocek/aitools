package main

// The cut in four passes.
//
// One reply used to choose the segments, do the length arithmetic and write
// every effect, from a request holding the whole timeline -- and the three jobs
// interfered until none of them finished. Now the cut is chosen and audited on
// its own, and the effects are asked for afterwards in the clips' own seconds:
// the captions clip by clip, the decorations over the kept clips. What is
// pinned here is that split and the two readers it needs.

import (
	"strings"
	"testing"
)

// A batch's captions come back in clip-relative seconds and land on the
// session clock inside their clip. Clip numbers are the brief's; a caption that
// runs past the clip is kept up to the end; an empty one is nothing.
func TestCaptionsLandInsideTheirClips(t *testing.T) {
	batch := []cutSeg{{S: 100, E: 130}, {S: 200, E: 260}}
	reply := `{"clips":[
	  {"i":6,"fx":[{"start":2,"end":5,"text":"first line"},{"start":28,"end":40,"text":"runs past the clip"},{"start":10,"end":12,"text":""}]},
	  {"i":7,"fx":[{"start":0,"end":4,"text":"second clip"}]}]}`
	fx, problem := captionsFromReply(batch, 5, reply) // the batch is clips 6 and 7
	if problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	if len(fx) != 3 {
		t.Fatalf("%d captions, want 3: %+v", len(fx), fx)
	}
	if fx[0].Kind != "text" || fx[0].T != 102 || fx[0].Dur != 3 || fx[0].Text != "first line" {
		t.Errorf("the first caption came out %+v", fx[0])
	}
	if fx[1].T != 128 || fx[1].Dur != 2 {
		t.Errorf("the caption past the clip's end was not held to it: %+v", fx[1])
	}
	if fx[2].T != 200 || fx[2].Text != "second clip" {
		t.Errorf("the second clip's caption came out %+v", fx[2])
	}
	// a clip the brief did not give is a wrong answer, not a silent drop
	if _, p := captionsFromReply(batch, 5, `{"clips":[{"i":9,"fx":[]}]}`); !strings.Contains(p, "clip 9 is not one of the clips given (6 to 7)") {
		t.Errorf("a wrong clip number was not named: %q", p)
	}
	// and the two nothings are named as themselves
	if _, p := captionsFromReply(batch, 5, ""); !strings.Contains(p, "no answer") {
		t.Errorf("an empty answer was not named: %q", p)
	}
	if _, p := captionsFromReply(batch, 5, `{"clips":[{"i":6,"fx":[{"start":2,"end":5,"te`); !strings.Contains(p, "stopped in the middle") {
		t.Errorf("a truncated answer was not named: %q", p)
	}
}

// The decorations name their clip and their seconds from its start; only the
// kinds this pass owns are read, and a gain of 0 is a mute, not a missing
// field.
func TestDecorationsAreReadPerClipAndOnlyTheirKinds(t *testing.T) {
	segs := []cutSeg{{S: 100, E: 130}, {S: 200, E: 260}}
	reply := `{"fx":[
	  {"clip":1,"kind":"zoom","start":2,"end":5},
	  {"clip":2,"kind":"stop","start":10,"end":12},
	  {"clip":2,"kind":"volume","start":0,"end":60,"gain":0},
	  {"clip":1,"kind":"text","start":0,"end":3},
	  {"clip":1,"kind":"speed","start":0,"end":30,"rate":4}]}`
	fx, problem := decorationsFromReply(segs, reply)
	if problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	// a stop is stored as a speed of nought (cutFx.frozenFx), so the kinds are
	// counted as the page reads them
	kinds := map[string]int{}
	for _, f := range fx {
		switch {
		case f.frozenFx():
			kinds["stop"]++
		default:
			kinds[f.Kind]++
		}
	}
	if kinds["zoom"] != 1 || kinds["stop"] != 1 || kinds["volume"] != 1 || kinds["text"] != 0 || kinds["speed"] != 0 {
		t.Errorf("the pass read %v, want one zoom, one stop, one volume and nothing it does not own", kinds)
	}
	for _, f := range fx {
		switch f.Kind {
		case "zoom":
			if f.T != 102 || f.Dur != 3 {
				t.Errorf("the zoom came out %+v", f)
			}
		case "volume":
			if f.T != 200 || f.Gain != 0 {
				t.Errorf("the mute came out %+v", f)
			}
		}
	}
	if _, p := decorationsFromReply(segs, `{"fx":[{"clip":3,"kind":"zoom","start":0,"end":2}]}`); !strings.Contains(p, "clip 3 is not one of the clips given (1 to 2)") {
		t.Errorf("a wrong clip number was not named: %q", p)
	}
}

// The seams: the cut's wordings ask for segments and speeds only, the two
// passes have wordings on the bench, and the flow runs them after the audit.
func TestTheCutIsThreePasses(t *testing.T) {
	src := readSrc(t, "cut_suggest.go")
	for _, want := range []string{
		"caps := a.captionCut(segs, rows)",
		"fx = append(fx, a.decorateCut(segs, rows)...)",
		`a.qJob(trackSTT, "suggest", 1, 4)`,
		`a.llmChatRetryOn("captions", msgs, false, nil)`,
		`a.llmChatRetryOn("effects", msgs, false, nil)`,
		"const captionBatch = 5",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut_suggest.go no longer contains %q", want)
		}
	}
	i, j := strings.Index(src, "a.suggestCut(session, target, span)"), strings.Index(src, "a.captionCut(segs, rows)")
	k, l := strings.Index(src, "a.speedCut(segs, caps, target)"), strings.Index(src, "a.decorateCut(segs, rows)")
	if !(i < j && j < k && k < l) {
		t.Error("the passes do not run in the order cut, captions, speed, effects")
	}
	// and the audit is gone: a second long call that moved boundaries on a
	// reply that is only boundaries
	if strings.Contains(src, "auditCut") {
		t.Error("the audit is back")
	}
	// the wordings: no cut style asks for effects any more, and the shared
	// speed rules say so in as many words
	if !strings.Contains(cutReply, "asked for afterwards, clip by clip, once the cut stands") {
		t.Error("the cut's wordings no longer say the rest is asked for afterwards")
	}
	if !strings.Contains(cutReply, "everything the user context names is in") ||
		strings.Contains(cutReply, "every effect lies inside") {
		t.Error("the cut's own check still names effects it no longer writes")
	}
	// both passes are prompts on the bench, edited like the others
	for _, key := range []string{"captions", "effects"} {
		if promptDefFor(key).def == "" {
			t.Errorf("%q has no wording registered", key)
		}
		found := false
		for _, r := range prepRows() {
			found = found || r.key == key
		}
		if !found {
			t.Errorf("%q has no row on the bench, so its wording cannot be edited", key)
		}
	}
	// and each is told its own reply shape and nothing about the other's
	a := &App{}
	if c := a.sysPrompt("captions"); !strings.Contains(c, `{"clips":[{"i":6,"fx":[`) || strings.Contains(c, `"kind":"zoom"`) {
		t.Error("the captions pass is not sent its own shape, or is sent the effects'")
	}
	if e := a.sysPrompt("effects"); !strings.Contains(e, `{"clip":4,"kind":"zoom"`) || strings.Contains(e, `{"clips":[{"i":6`) {
		t.Error("the effects pass is not sent its own shape, or is sent the captions'")
	}
}

// ✗ Clear takes the whole cut off the timeline and leaves the session alone.
// Undo brings it back, which is what makes it a button rather than a question.
func TestClearEmptiesTheCutAndNotTheSession(t *testing.T) {
	a, ed := splitEd(t)
	_ = a
	ed.segs = []cutSeg{{S: 0, E: 20}, {S: 30, E: 60}}
	ed.fx = []cutFx{{Kind: "text", T: 5, Dur: 3, Text: "a caption"}}
	ed.shift = map[string]float64{"a": 2.5}
	ed.sel.t0, ed.sel.t1, ed.sel.active = 10, 15, true
	ed.segOn, ed.segSel = true, 0
	vids := len(ed.vids)

	ed.clearCut()
	if len(ed.segs) != 0 || len(ed.fx) != 0 {
		t.Errorf("clear left %d segment(s) and %d effect(s)", len(ed.segs), len(ed.fx))
	}
	// what the session IS survives: the recordings and the hand-corrected clock
	if len(ed.vids) != vids || ed.shift["a"] != 2.5 {
		t.Errorf("clear touched the session: %d recording(s), shift %v", len(ed.vids), ed.shift)
	}
	// and nothing is left holding an index into a list that is now empty
	if ed.segOn || ed.sel.active {
		t.Errorf("clear left a hold: seg=%v sel=%v", ed.segOn, ed.sel.active)
	}
	if len(ed.undo) != 1 {
		t.Errorf("clear left %d undo step(s), want 1", len(ed.undo))
	}
	ed.undoLast()
	if len(ed.segs) != 2 || len(ed.fx) != 1 {
		t.Errorf("↶ after a clear left %d segment(s) and %d effect(s)", len(ed.segs), len(ed.fx))
	}
	// an empty timeline is not an edit
	ed.segs, ed.fx, ed.undo = nil, nil, nil
	ed.clearCut()
	if len(ed.undo) != 0 {
		t.Error("clearing an empty timeline left an undo step")
	}
	// the button is beside Undo and Revert, the other verbs that throw work away
	src := readSrc(t, "cut.go")
	for _, want := range []string{
		`ed.clearBtn = gtk.NewButtonFromIconName("edit-clear-all-symbolic")`,
		"ed.clearBtn.ConnectClicked(func() { ed.clearCut() })",
		"bar.Append(linked(ed.undoBtn, ed.redoBtn, ed.revertBtn, ed.clearBtn))",
		// ...and the aspect dropdown wears its caption, like every other
		// control on that bar
		"bar.Append(col(ed.aspectDD, aspLbl))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut.go no longer contains %q", want)
		}
	}
}

// The speed pass: a rate per clip, and a clip with captions on it runs at 1.
//
// Words on screen at 4 are gone before they are read, and which lines become
// captions is the pass before this one's decision -- so the rule is enforced
// here rather than only asked for. Everything else is arithmetic: the rates
// come back as speed effects over whole clips, folded where two touch.
func TestTheSpeedPassLeavesCaptionedClipsAlone(t *testing.T) {
	segs := []cutSeg{{S: 0, E: 60}, {S: 60, E: 180}, {S: 180, E: 240}, {S: 240, E: 300}}
	capped := []int{2, 0, 0, 1} // clips 1 and 4 carry captions
	fx, problem := speedsFromReply(segs, capped, `{"speeds":[
	  {"clip":1,"rate":4},{"clip":2,"rate":4},{"clip":3,"rate":2},{"clip":4,"rate":8}]}`)
	if problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	if len(fx) != 2 {
		t.Fatalf("%d speeds came back, want the two on the clips with no words: %+v", len(fx), fx)
	}
	if fx[0].T != 60 || fx[0].Dur != 120 || fx[0].Rate != 4 {
		t.Errorf("clip 2's speed came out %+v, want 60-180 at 4", fx[0])
	}
	if fx[1].T != 180 || fx[1].Rate != 2 {
		t.Errorf("clip 3's speed came out %+v, want 180-240 at 2", fx[1])
	}
	// a rate of 1 or less says nothing, and a clip nobody named runs at 1
	if got, _ := speedsFromReply(segs, []int{0, 0, 0, 0}, `{"speeds":[{"clip":2,"rate":1},{"clip":3,"rate":0.5}]}`); len(got) != 0 {
		t.Errorf("a rate of 1 and one below it produced %+v", got)
	}
	// two fast clips that touch are one stretch, not two badges side by side
	one, _ := speedsFromReply(segs, []int{0, 0, 0, 0}, `{"speeds":[{"clip":2,"rate":4},{"clip":3,"rate":4}]}`)
	if len(one) != 1 || one[0].T != 60 || one[0].Dur != 180 {
		t.Errorf("two touching clips at 4 came out %+v, want one 60-240 stretch", one)
	}
	// a clip the brief did not give is a wrong answer, not a silent drop
	if _, p := speedsFromReply(segs, capped, `{"speeds":[{"clip":9,"rate":4}]}`); !strings.Contains(p, "clip 9 is not one of the clips given (1 to 4)") {
		t.Errorf("a wrong clip number was not named: %q", p)
	}
	// the wording carries the arithmetic and the caption rule
	for _, want := range []string{
		"(F-T)*r/(r-1) seconds must run at r",
		"A clip with captions on it runs at 1",
		"A clip is one rate from end to end",
	} {
		if !strings.Contains(speedSystem, want) {
			t.Errorf("the speed pass's wording no longer says %q", want)
		}
	}
	// and the cut is judged on footage, because how fast it plays is this
	// pass's answer and does not exist when the cut is checked
	a := &App{}
	lo, hi := a.footageWindow(720)
	if wlo, whi := a.suggestWindow(720); lo != wlo || hi != whi*maxSpeedRate {
		t.Errorf("the footage window is %g-%g against a video window of %g-%g", lo, hi, wlo, whi)
	}
}
