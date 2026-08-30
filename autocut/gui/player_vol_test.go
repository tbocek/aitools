package main

// The preview's volume sliders: one gain for the whole preview, shown wherever
// a video can be played. The players themselves are GStreamer pipelines and
// stay out of a unit test, so the knob's arithmetic is tested straight and the
// wiring -- who is told, and when -- is pinned in the source.

import (
	"math"
	"strings"
	"testing"
)

func TestTheVolumeIsOneNumberAndStaysOnTheDial(t *testing.T) {
	was := previewVol
	defer SetPreviewVolume(was)

	// the slider hands over 0..1; anything outside is a bug upstream, and the
	// gain must not amplify (playbin would happily go to 10)
	SetPreviewVolume(0.35)
	if previewVol != 0.35 {
		t.Fatalf("previewVol = %v, want 0.35", previewVol)
	}
	SetPreviewVolume(1.7)
	if previewVol != 1 {
		t.Fatalf("previewVol = %v, want clamped to 1", previewVol)
	}
	SetPreviewVolume(-0.2)
	if previewVol != 0 {
		t.Fatalf("previewVol = %v, want clamped to 0", previewVol)
	}
}

func TestTheVolumeReachesEveryPipeline(t *testing.T) {
	src := readSrc(t, "player.go")
	for _, pin := range []string{
		// every pipeline is born at the loudness its player already runs at:
		// the master in NewPlayer, and every aux (a recording under the
		// footage, an insert's own sound) at newAux's vol argument
		"pb.SetObjectProperty(\"volume\", vol)",
		"newAux(fmt.Sprintf(\"mix%d\", i), t, p.vol())",
		"allPlayers = append(allPlayers, p)",
		// ...and a turn of the dial visits the live ones, mixes and card
		// included -- a slider that only reached the footage would rebalance
		// the preview instead of turning it down
		"for _, a := range p.mix {",
		"if p.card != nil {",
	} {
		if !strings.Contains(src, pin) {
			t.Errorf("player.go lost its pin %q", pin)
		}
	}
	if n := strings.Count(src, "p.applyVol()"); n < 3 {
		t.Errorf("applyVol is called %d times; the birth, the slider and the effect all need it", n)
	}

	// one builder, so the three sliders cannot drift into three behaviours
	for file, pin := range map[string]string{
		"main.go":    "a.volBox = volumeCtl()",
		"cut.go":     "bar.Append(volumeCtl())",
		"narrate.go": "transport.Append(volumeCtl())",
	} {
		if !strings.Contains(readSrc(t, file), pin) {
			t.Errorf("%s no longer shows a volume control (%q)", file, pin)
		}
	}
}

// The run bar spans every page, but only one page plays from it. Produce's
// preview has no transport of its own, so the bar's ▶ is its ▶ and the bar's
// slider is its slider; Cut and Narrate have both of their own on the page,
// and Prepare and Publish play nothing, so on those four the bar's slider is
// either a duplicate in reach of the original or a control over silence.
func TestTheRunBarsSliderIsOnlyOnThePageThatPlaysFromTheRunBar(t *testing.T) {
	if !barVolume("produce") {
		t.Error("Produce is watched from the run bar and has lost the bar's volume slider")
	}
	// ...which means there has to be one on the bar for it to keep
	if !strings.Contains(readSrc(t, "main.go"), "ctlRow.Append(a.volBox)") {
		t.Error("the slider is built but never put on the run bar, so Produce plays with no volume control at all")
	}
	for _, page := range []string{"prep", "cut", "narrate", "publish"} {
		if barVolume(page) {
			t.Errorf("the run bar still offers a volume slider on %s", page)
		}
	}
	// every page name barVolume is asked about is a page that exists, so a
	// renamed step cannot quietly turn the answer into "never"
	for _, name := range []string{"prep", "cut", "narrate", "produce", "publish"} {
		found := false
		for _, s := range steps {
			found = found || s.name == name
		}
		if !found {
			t.Errorf("this test asks about a page %q that the app no longer has", name)
		}
	}

	// showStep is the one place a page change happens, so it is the one place
	// that has to dress the bar for the page arriving
	if !strings.Contains(funcBody(t, "main.go", `func \(a \*App\) showStep\(name string\) \{`),
		"a.volBox.SetVisible(barVolume(name))") {
		t.Error("showStep no longer hides the bar's slider on the pages that do not play from it")
	}
	// ...including the first page, which is why the opening showStep has to
	// come after the bar it dresses is built
	build := funcBody(t, "main.go", `func \(a \*App\) build\(app \*gtk.Application\) \{`)
	made, opened := strings.Index(build, "a.volBox = volumeCtl()"), strings.Index(build, `a.showStep("prep")`)
	if made < 0 || opened < 0 {
		t.Fatalf("build no longer both makes the slider (%d) and opens the first page (%d)", made, opened)
	}
	if made > opened {
		t.Error("the first page is opened before the bar's slider exists, so showStep dereferences nil")
	}
}

// One number, three sliders. A volume control is built once per place a video
// can be played from -- the run bar, the Cut transport, the Narrate transport
// -- and moving any of them has to move the others, or the second page a
// preview is played on shows 100% while playing at 40.
func TestEveryVolumeSliderShowsTheSameNumber(t *testing.T) {
	src := readSrc(t, "player.go")
	for _, pin := range []string{
		// every slider built goes on the roll...
		"volScales = append(volScales, sc)",
		// ...the one being dragged writes the shared number...
		"SetPreviewVolume(sc.Value() / 100)",
		// ...and then the others, without their own handlers writing back
		"volSyncing = true",
		"o.SetValue(previewVol * 100)",
		"volSyncing = false",
	} {
		if !strings.Contains(src, pin) {
			t.Errorf("player.go lost its pin %q", pin)
		}
	}
	if !strings.Contains(funcBody(t, "player.go", `func volumeCtl\(\) \*gtk.Box \{`),
		"sc.SetValue(previewVol * 100)") {
		t.Error("a slider built later starts at 100% instead of at what the others already say")
	}
}

// The volume EFFECT reaches the preview, and it reaches it multiplied by the
// slider rather than instead of it: turning the preview down still turns a
// boosted stretch down.
func TestTheVolumeEffectIsHeardInThePreview(t *testing.T) {
	wasVol := previewVol
	defer SetPreviewVolume(wasVol)

	p := &Player{fxGain: 1}
	SetPreviewVolume(0.5)
	if got := p.vol(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("with no effect the player runs at %v, want the slider's 0.5", got)
	}
	p.fxGain = 4
	if got := p.vol(); math.Abs(got-2) > 1e-9 {
		t.Fatalf("a x4 effect at half volume runs at %v, want 2", got)
	}
	// and neither end can push the property past what playbin takes
	SetPreviewVolume(1)
	p.fxGain = fxMaxGain
	if got := p.vol(); got != fxMaxGain {
		t.Fatalf("full slider under the loudest effect runs at %v, want %v", got, fxMaxGain)
	}

	// the cut's half: the tick and every seek tell the player where the line
	// is, so a scrub across a boosted stretch sounds like one
	cut := readSrc(t, "cut.go")
	for _, pin := range []string{
		"ed.player.SetFxGain(fxGainAt(ed.fx, ed.playhead))",
		"ed.syncPlayGain()",
	} {
		if !strings.Contains(cut, pin) {
			t.Errorf("cut.go lost its pin %q", pin)
		}
	}
	if n := strings.Count(cut, "ed.syncPlayGain()"); n != 2 {
		t.Errorf("syncPlayGain is called %d times, want 2: the tick, which follows a "+
			"playing line across a band, and setPlayhead, which is every seek and "+
			"every scrub", n)
	}
}
