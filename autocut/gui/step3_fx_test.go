package main

// The effects model: the camera function the preview and the render share, the
// clock arithmetic that turns speed effects into segments, and the wiring that
// puts all of it under the same fingers as the rest of the cut page.

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

func rectNear(a, b fxRect) bool {
	return math.Abs(a.cx-b.cx) < 1e-9 && math.Abs(a.cy-b.cy) < 1e-9 && math.Abs(a.hf-b.hf) < 1e-9
}

// The first region nobody has to place: the whole source frame inside the
// cut's aspect. For a short cut from widescreen that is a rect WIDER than the
// frame is tall (hf > 1) -- the letterboxed full picture -- because the
// alternative default, a full-height crop, silently throws away both sides of
// the frame before the user has chosen anything.
func TestTheCameraStartsOnTheWholeFrame(t *testing.T) {
	srcA := 16.0 / 9
	if got := fullFit(srcA, srcA); !rectNear(got, fxRect{0.5, 0.5, 1}) {
		t.Errorf("same aspect in and out gives %+v, want the exact frame", got)
	}
	got := fullFit(srcA, 9.0/16)
	if want := srcA / (9.0 / 16); math.Abs(got.hf-want) > 1e-9 || got.cx != 0.5 || got.cy != 0.5 {
		t.Errorf("widescreen into 9:16 gives %+v, want centred hf=%g", got, want)
	}
	// and with no effects at all, that region is the camera, forever
	if got := fxRectAt(nil, 123, srcA, 9.0/16); !rectNear(got, fullFit(srcA, 9.0/16)) {
		t.Errorf("an effect-free camera wanders: %+v", got)
	}
}

// Views chain through their glides: a view placed while the camera is still
// travelling starts from where the camera actually is, mid-flight, not from
// where the previous view was going to end up. Anything else makes the picture
// jump at the moment the second view begins.
func TestViewsChainFromMidGlide(t *testing.T) {
	srcA, outA := 16.0/9, 16.0/9 // same shape, so the base is the plain frame
	// z leads so the first-view rule frames the opening on z's rect -- which
	// IS the plain frame, keeping every departure below exactly fullFit
	z := cutFx{Kind: "view", T: 1, Cx: 0.5, Cy: 0.5, Hf: 1}
	a := cutFx{Kind: "view", T: 10, Trans: 2, Cx: 0.3, Cy: 0.3, Hf: 0.5}
	b := cutFx{Kind: "view", T: 11, Trans: 1, Cx: 0.7, Cy: 0.6, Hf: 0.4}
	fx := []cutFx{z, a, b}

	// a quarter into A's glide, B not yet begun
	got := fxRectAt(fx, 10.5, srcA, outA)
	want := lerpRect(fullFit(srcA, outA), fxRect{a.Cx, a.Cy, a.Hf}, 0.25)
	if !rectNear(got, want) {
		t.Errorf("mid-glide the camera is %+v, want %+v", got, want)
	}
	// B begins at 11, when A's glide is only half done: B must depart from that
	// half-way point
	depart := lerpRect(fullFit(srcA, outA), fxRect{a.Cx, a.Cy, a.Hf}, 0.5)
	got = fxRectAt(fx, 11.5, srcA, outA)
	want = lerpRect(depart, fxRect{b.Cx, b.Cy, b.Hf}, 0.5)
	if !rectNear(got, want) {
		t.Errorf("the second view departs from %+v, want the mid-flight camera %+v", got, want)
	}
	// and settles on its own rectangle
	if got := fxRectAt(fx, 30, srcA, outA); !rectNear(got, fxRect{b.Cx, b.Cy, b.Hf}) {
		t.Errorf("settled camera is %+v, want the last view", got)
	}
}

// The first view frames the video from the very beginning. The letterboxed
// full frame -- the shape where the source does not match the cut -- only
// stands while NO region has been chosen at all; the moment there is one, the
// opening seconds wear it too, and its glide has nowhere to travel from.
func TestTheFirstViewFramesTheVideoFromTheStart(t *testing.T) {
	srcA, outA := 16.0/9, 9.0/16
	v := cutFx{Kind: "view", T: 30, Trans: 2, Cx: 0.3, Cy: 0.5, Hf: 0.6}
	vr := fxRect{v.Cx, v.Cy, v.Hf}
	for _, tt := range []float64{0, 29.9, 31, 100} {
		if got := fxRectAt([]cutFx{v}, tt, srcA, outA); !rectNear(got, vr) {
			t.Errorf("with one view the camera at %.1f s is %+v, want the view's own %+v", tt, got, vr)
		}
	}
	// a second view still glides away from the first, exactly as before
	w := cutFx{Kind: "view", T: 50, Trans: 2, Cx: 0.7, Cy: 0.5, Hf: 0.4}
	got := fxRectAt([]cutFx{v, w}, 51, srcA, outA)
	want := lerpRect(vr, fxRect{w.Cx, w.Cy, w.Hf}, 0.5)
	if !rectNear(got, want) {
		t.Errorf("mid-glide to the second view the camera is %+v, want %+v", got, want)
	}
	// and no views at all still means the letterboxed full frame
	if got := fxRectAt(nil, 0, srcA, outA); !rectNear(got, fullFit(srcA, outA)) {
		t.Errorf("with no views the camera is %+v, want fullFit", got)
	}
}

// firstView is the predicate behind both refusals -- the placement dialog
// that stops asking for a glide, and the right-click on the rectangle that
// only explains: the earliest view is first, a later one is not, and a NEW
// view placed before every existing one takes the title with it.
func TestTheFirstViewIsKnown(t *testing.T) {
	ed := &cutEditor{fx: []cutFx{
		{Kind: "view", T: 20, Cx: 0.5, Cy: 0.5, Hf: 0.5},
		{Kind: "zoom", T: 5, Dur: 2, Cx: 0.5, Cy: 0.5, Hf: 0.3},
	}}
	if !ed.firstView(ed.fx[0]) {
		t.Error("the cut's only view does not count as first")
	}
	if ed.firstView(cutFx{Kind: "view", T: 30}) {
		t.Error("a view after an existing one claims to be first")
	}
	if !ed.firstView(cutFx{Kind: "view", T: 10}) {
		t.Error("a new view placed before every existing one is not first")
	}
	if ed.firstView(cutFx{Kind: "zoom", T: 0}) {
		t.Error("a zoom counts as a first view -- it always returns to the views")
	}
}

// A zoom borrows the camera and gives it back: in over Trans, hold, out over
// Tout -- each glide its own number -- and after Dur the camera is exactly
// where the views say it belongs; the zoom leaves no trace.
func TestAZoomBorrowsTheCameraAndGivesItBack(t *testing.T) {
	srcA, outA := 16.0/9, 16.0/9
	base := fullFit(srcA, outA)
	z := cutFx{Kind: "zoom", T: 20, Trans: 1, Tout: 1, Dur: 4, Cx: 0.6, Cy: 0.4, Hf: 0.3}
	fx := []cutFx{z}
	zr := fxRect{z.Cx, z.Cy, z.Hf}

	for _, c := range []struct {
		t    float64
		want fxRect
	}{
		{19.9, base},                   // not yet
		{20, base},                     // the very first instant is the departure
		{20.5, lerpRect(base, zr, .5)}, // half-way in
		{22, zr},                       // holding
		{23.5, lerpRect(base, zr, .5)}, // half-way back out
		{24, base},                     // given back, to the second
	} {
		if got := fxRectAt(fx, c.t, srcA, outA); !rectNear(got, c.want) {
			t.Errorf("at %.1f s the camera is %+v, want %+v", c.t, got, c.want)
		}
	}

	// the glides are independent: in over 1 s, out over 2, and a Tout of 0 is
	// a hard cut back -- still zoomed at the last covered instant, base after
	asym := cutFx{Kind: "zoom", T: 40, Trans: 1, Tout: 2, Dur: 6, Cx: 0.6, Cy: 0.4, Hf: 0.3}
	hard := cutFx{Kind: "zoom", T: 60, Trans: 1, Dur: 3, Cx: 0.6, Cy: 0.4, Hf: 0.3}
	fx = []cutFx{asym, hard}
	for _, c := range []struct {
		t    float64
		want fxRect
	}{
		{40.5, lerpRect(base, zr, .5)}, // half of the 1 s way in
		{43, zr},                       // holding: the out has not begun at 44
		{45, lerpRect(base, zr, .5)},   // half of the 2 s way out
		{46, base},                     // over
		{62.9, zr},                     // no out-glide: zoomed to the end...
		{63, base},                     // ...and cut straight back
	} {
		if got := fxRectAt(fx, c.t, srcA, outA); !rectNear(got, c.want) {
			t.Errorf("at %.1f s the camera is %+v, want %+v", c.t, got, c.want)
		}
	}
}

// Slow motion splits the footage exactly at its own boundaries: the stretch
// inside [T, T+Dur) carries the rate, the rest of the clip plays as itself,
// and the lengths say what the finished video will actually run.
func TestSlowMotionSplitsAtItsOwnBoundaries(t *testing.T) {
	segs := []cutSeg{{S: 0, E: 60}}
	fx := []cutFx{{Kind: "speed", T: 10, Dur: 5, Rate: 0.5}}
	got := applyFx(segs, fx)
	want := []cutSeg{{S: 0, E: 10}, {S: 10, E: 15, Rate: 0.5}, {S: 15, E: 60}}
	if len(got) != len(want) {
		t.Fatalf("applyFx gave %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seg %d is %+v, want %+v", i, got[i], want[i])
		}
	}
	// 5 s of footage at half speed runs 10 s on screen
	if l := got[1].length(); math.Abs(l-10) > 1e-9 {
		t.Errorf("the slowed stretch runs %.1f s, want 10", l)
	}
	// an effect whose scene was cut away does nothing, silently -- the segs
	// come through untouched and the effect waits for Undo to matter again
	elsewhere := applyFx([]cutSeg{{S: 30, E: 40}}, fx)
	if len(elsewhere) != 1 || elsewhere[0] != (cutSeg{S: 30, E: 40}) {
		t.Errorf("an orphaned slow rewrote the cut: %v", elsewhere)
	}
}

// The same split, the other way round the clock. A capture is mostly dead air,
// and 100x is what makes twenty minutes of it watchable: nothing on this side
// of the render changes except the rate the segment carries, and the length it
// gives the finished video. What needs guarding is the far end -- fast enough
// over few enough seconds and the clip comes out shorter than the frames it is
// made of, which the render drops, which is no effect at all.
func TestFastMotionRunsTheSameSplitTheOtherWay(t *testing.T) {
	segs := []cutSeg{{S: 0, E: 600}}
	got := applyFx(segs, []cutFx{{Kind: "speed", T: 100, Dur: 300, Rate: 100}})
	want := []cutSeg{{S: 0, E: 100}, {S: 100, E: 400, Rate: 100}, {S: 400, E: 600}}
	if len(got) != len(want) {
		t.Fatalf("applyFx gave %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seg %d is %+v, want %+v", i, got[i], want[i])
		}
	}
	// five minutes of footage at a hundred times: three seconds of video
	if l := got[1].length(); math.Abs(l-3) > 1e-9 {
		t.Errorf("the fast stretch runs %.2f s, want 3", l)
	}

	// what the dialog does with what was typed into it: the ceiling, the
	// floor, and the case where the two ends fight -- there it is the RATE
	// that gives way, because the seconds are the ones the user marked and
	// can see on the timeline
	for _, c := range []struct{ rate, dur, wantR, wantD float64 }{
		{0.5, 5, 0.5, 5},          // untouched
		{1000, 60, fxMaxRate, 60}, // past the ceiling
		{0, 5, fxMinRate, 5},      // an empty entry must not become a freeze
		{100, 10, 50, 10},         // 10 s at 100x is 0.1 s on screen: 50x instead
		{100, 0, 1, 0.2},          // no length at all: the shortest there is
	} {
		r, d := clampSpeed(c.rate, c.dur)
		if math.Abs(r-c.wantR) > 1e-9 || math.Abs(d-c.wantD) > 1e-9 {
			t.Errorf("clampSpeed(%g, %g) = %g over %gs, want %g over %gs",
				c.rate, c.dur, r, d, c.wantR, c.wantD)
		}
		if d/r < fxMinPlay-1e-9 {
			t.Errorf("clampSpeed(%g, %g) leaves %.3f s of video, under the %g floor",
				c.rate, c.dur, d/r, fxMinPlay)
		}
	}

	// the lane plate is two characters wide and the status line is read by a
	// human: %g spells a hundred "1e+02", and the one decimal every other
	// field uses spells a quarter speed "0.2", which is a different effect
	for _, c := range []struct {
		v    float64
		want string
	}{{100, "100"}, {0.25, "0.25"}, {1.5, "1.5"}, {fxMinRate, "0.05"}, {1, "1"}} {
		if got := fxNum(c.v); got != c.want {
			t.Errorf("fxNum(%g) = %q, want %q", c.v, got, c.want)
		}
	}
	fast := cutFx{Kind: "speed", T: 90, Dur: 300, Rate: 100}.fxLabel()
	if !strings.Contains(fast, "sped up ×100") {
		t.Errorf("a 100x effect reads %q", fast)
	}
	slow := cutFx{Kind: "speed", T: 90, Dur: 5, Rate: 0.25}.fxLabel()
	if !strings.Contains(slow, "slowed ×0.25") {
		t.Errorf("a quarter-speed effect reads %q", slow)
	}
}

// The frame buttons with an effect held. Two things went wrong at once here:
// the nudge moved the effect and nothing on screen moved with it -- one frame
// is a fraction of a pixel on the lane and the label reads the same to the
// second, so ‹‹f looked like a dead button -- and a hold left over an Undo
// swallowed the press instead of giving it back to the playhead.
func TestTheFrameButtonsWithAnEffectHeldMoveTheLineWithIt(t *testing.T) {
	ed := newTestEd(t)
	ed.vids = []tlVideo{{base: "v", path: "v.mkv", start: 0, dur: 600, interval: 5, fps: 30}}
	ed.relayout()
	ed.segs = []cutSeg{{S: 0, E: 600}}
	ed.addFx(cutFx{Kind: "speed", T: 100, Dur: 20, Rate: 8})
	if ed.heldFx() == nil {
		t.Fatal("a placed effect is not the one in hand")
	}

	ed.frameStep(-15) // 30 fps: half a second back
	if got := ed.fx[0].T; math.Abs(got-99.5) > 1e-9 {
		t.Errorf("‹‹f moved the effect to %g, want 99.5", got)
	}
	// and the red line came with it, which is the only visible answer the
	// press has: the effect itself moved half a pixel
	if !ed.hasPlay || math.Abs(ed.playhead-99.5) > 1e-9 {
		t.Errorf("the playhead is at %g (set=%v), want it on the effect at 99.5",
			ed.playhead, ed.hasPlay)
	}
	// two nudges of one held effect are one entry in the history, the same
	// bargain the edges and clips make
	ed.frameStep(+15)
	if len(ed.undo) != 2 { // the place, then the move
		t.Errorf("placing and nudging left %d undo entries, want 2", len(ed.undo))
	}

	// an Undo (or a re-cut, or another project) can leave the hold pointing at
	// an effect that is no longer there. The nudge has to SAY it moved nothing,
	// which is what lets frameStep give the press back to the playhead instead
	// of dropping it -- and it has to let the stale hold go.
	ed.fx = nil
	if ed.nudgeFx(+1) {
		t.Error("nudging a vanished effect claimed to have moved it")
	}
	if ed.fxOn {
		t.Error("a hold on a vanished effect survived the press")
	}
}

// A freeze is a spliced card without the card: a zero-span, Dur-long segment
// standing in the clip it cut open, with the frame at T for a picture. Same
// shape, so everything that already knows cards -- the render, the narration
// matcher, the seek bar -- knows freezes.
func TestAFreezeStandsInTheClipLikeACard(t *testing.T) {
	got := applyFx([]cutSeg{{S: 0, E: 60}}, []cutFx{{Kind: "speed", T: 20, Dur: 2}})
	want := []cutSeg{{S: 0, E: 20}, {S: 20, E: 20, Dur: 2}, {S: 20, E: 60}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("freeze gave %v, want %v", got, want)
		}
	}
	if !got[1].frozen() || got[1].spliced() || got[1].isInsert() {
		t.Errorf("the held frame answers frozen=%v spliced=%v insert=%v, want only frozen",
			got[1].frozen(), got[1].spliced(), got[1].isInsert())
	}
	// a freeze inside a slowed stretch: the rate survives on both sides of it
	got = applyFx([]cutSeg{{S: 0, E: 60}}, []cutFx{
		{Kind: "speed", T: 10, Dur: 10, Rate: 0.5},
		{Kind: "speed", T: 15, Dur: 2},
	})
	want = []cutSeg{{S: 0, E: 10}, {S: 10, E: 15, Rate: 0.5},
		{S: 15, E: 15, Dur: 2}, {S: 15, E: 20, Rate: 0.5}, {S: 20, E: 60}}
	if len(got) != len(want) {
		t.Fatalf("freeze-in-slow gave %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seg %d is %+v, want %+v", i, got[i], want[i])
		}
	}

	// the seek bar knows both: a freeze is a point of the session however far
	// into its bar-time the handle lands, and slowed footage maps back through
	// its rate -- x bar seconds cover x*rate session seconds
	segs := want
	if at := cutAt(segs, 10+3); math.Abs(at-(10+3*0.5)) > 1e-9 {
		t.Errorf("3 s into the slowed stretch is session %.2f, want %.2f", at, 10+1.5)
	}
	if at := cutAt(segs, 20+1); at != 15 { // inside the freeze
		t.Errorf("inside the freeze the session time is %.2f, want the frozen moment 15", at)
	}
	// session 17: 10 s kept + 10 s of slowed footage + the 2 s freeze + 2 s
	// more at half speed = 26 on the bar
	if pos := cutPos(segs, 17); math.Abs(pos-26) > 1e-9 {
		t.Errorf("session 17 is %.2f on the bar, want 26", pos)
	}
}

// The cut file: effects and aspect go to disk with the cut and come back
// through the same door the render reads (produceCut) -- and a cut with no
// effects writes byte-for-byte the file it always wrote, so nothing that
// diffs, hashes or hand-reads cut.json sees this feature until it is used.
func TestEffectsGoToDiskAndAPlainCutDoesNotChange(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{S: 5, E: 25}}
	ed.persist()
	b, err := os.ReadFile(ed.a.cutPath())
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.MarshalIndent(struct {
		Segs []cutSeg `json:"segs"`
	}{ed.segs}, "", "  ")
	if string(b) != string(plain)+"\n" {
		t.Errorf("an effect-free cut writes:\n%s\nwant the format it always had:\n%s", b, plain)
	}

	ed.aspect = "9:16"
	ed.fx = []cutFx{
		{Kind: "view", T: 8, Trans: 1, Cx: 0.4, Cy: 0.5, Hf: 0.6},
		{Kind: "speed", T: 12, Dur: 3, Rate: 0.5},
	}
	ed.persist()
	segs, fx, aspect := ed.a.produceCut() // a.ed is nil: this is the file speaking
	if len(segs) != 1 || segs[0] != ed.segs[0] {
		t.Errorf("the cut came back as %v", segs)
	}
	if aspect != "9:16" || len(fx) != 2 || fx[0] != ed.fx[0] || fx[1] != ed.fx[1] {
		t.Errorf("the effects came back as aspect=%q fx=%v", aspect, fx)
	}
}

// The new nouns answer to the old verbs: the lane picks with the right button
// through the same handler as clips and edges, a held effect is moved by the
// left drag and nudged by ‹f f›, Remove removes it, the Insert button turns
// into ✎ Edit for it, and the overlay hangs over the player's own picture.
// Pinned as source, like the edge tool before it, because a regression here is
// a gesture that silently stops working.
func TestTheEffectsLaneIsWired(t *testing.T) {
	b, err := os.ReadFile("step3.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// the right button picks up an effect where it picks up everything else
		"if area == ed.srcArea && ed.fxHitLane(y) {",
		"ed.pickFxAt(x + ed.viewX)",
		// the left drag picks the effect under it up and slides it, snapped
		"if !ed.fxOn || ed.fxSel != i {",
		"ed.moveFxTo(ed.snapFx(ed.tAtView(dragStartX+ox)-grabAt), true)",
		// ‹f f› nudge it, Del removes it, Esc puts it down
		"ed.nudgeFx(n)",
		"case ed.heldFx() != nil:",
		"case (ed.edgeOn || ed.segOn || ed.fxOn || ed.fxArm != \"\") && keyval == gdk.KEY_Escape:",
		// the one button that edits whatever is held edits effects too
		"a.editFx()",
		// the lane is drawn inside the source track, the overlay over the picture
		"ed.drawFxLane(cr, vx0, vx1)",
		"vframe := videoFrame(ed.buildFxOverlay())",
		// and the aspect is a dropdown on the toolbar, not a dialog
		"ed.aspectDD = gtk.NewDropDownFromStrings(fxAspects)",
		// the four effects are one dropdown of verbs, the stop frame its own
		"fxDD := gtk.NewDropDownFromStrings(fxKinds)",
		"a.freezeClicked()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
	// undo is one stack for everything on the page: segments, effects and
	// aspect travel together or Undo tears them apart
	for _, f := range []string{"step3.go"} {
		b, _ := os.ReadFile(f)
		if !strings.Contains(string(b), "type cutState struct") {
			t.Errorf("%s no longer snapshots segs, fx and aspect as one undo state", f)
		}
	}
}
