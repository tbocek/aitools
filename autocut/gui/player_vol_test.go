package main

// The run bar's volume slider: one gain for the whole preview. The players
// themselves are GStreamer pipelines and stay out of a unit test, so the knob's
// arithmetic is tested straight and the wiring -- who is told, and when -- is
// pinned in the source.

import (
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
		// every pipeline is born at the dial's setting: the master in
		// NewPlayer, and every aux (a recording under the footage, an
		// insert's own sound) in newAux
		"pb.SetObjectProperty(\"volume\", previewVol)",
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
	if n := strings.Count(src, "\n\tpb.SetObjectProperty(\"volume\", previewVol)"); n != 2 {
		t.Errorf("born-at-the-dial appears %d times, want 2 (NewPlayer and newAux)", n)
	}

	src = readSrc(t, "main.go")
	for _, pin := range []string{
		// the slider lives on the shared run bar and speaks percent
		"vol := gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 100, 1)",
		"vol.ConnectValueChanged(func() { SetPreviewVolume(vol.Value() / 100) })",
	} {
		if !strings.Contains(src, pin) {
			t.Errorf("main.go lost its pin %q", pin)
		}
	}
}
