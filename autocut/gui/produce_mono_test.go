package main

// The Mono toggle on the produce page. The choice has to hold in three places
// at once -- the filter graph that builds each clip, the encoder flags on the
// final pass, and the narration's pan -- because the clips are joined by a
// concat stream COPY: a layout decided per clip is the video's layout, and two
// clips that decided differently are one broken concat list. These tests pin
// each of the three to the one Mono flag, and the widget that sets it to the
// settings that carry it.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestTheLayoutFollowsTheOneToggle: every string the filter graph builds from
// the settings says the same layout, and it is the toggle that picks it.
func TestTheLayoutFollowsTheOneToggle(t *testing.T) {
	stereo, mono := prodSettings{}, prodSettings{Mono: true}

	if got := audLayout(stereo); got != "stereo" {
		t.Errorf("audLayout off = %q, want stereo", got)
	}
	if got := audLayout(mono); got != "mono" {
		t.Errorf("audLayout on = %q, want mono", got)
	}

	// aformat is the per-clip contract: every audio path in encodeClip runs
	// through it, so this string IS the clip's layout
	if got := audFmt(stereo); !strings.Contains(got, "channel_layouts=stereo") {
		t.Errorf("audFmt off = %q, want a stereo layout in it", got)
	}
	if got := audFmt(mono); !strings.Contains(got, "channel_layouts=mono") {
		t.Errorf("audFmt on = %q, want a mono layout in it", got)
	}

	// the narration arrives mono from the synthesizer either way; the pan is
	// what spreads it, and a mono video has nothing to spread it across
	if got := voicePan(stereo); got != "pan=stereo|c0=c0|c1=c0" {
		t.Errorf("voicePan off = %q", got)
	}
	if got := voicePan(mono); got != "pan=mono|c0=c0" {
		t.Errorf("voicePan on = %q", got)
	}
}

// TestTheEncoderAgreesWithTheGraph: -ac on the encode pass says the same number
// the graph's aformat said, for both containers, so a path that skips the graph
// cannot smuggle the other layout into the concat list.
func TestTheEncoderAgreesWithTheGraph(t *testing.T) {
	for _, c := range []struct {
		st   prodSettings
		want string
	}{
		{prodSettings{Container: "mp4", AudioKbps: 160}, "-ac 2"},
		{prodSettings{Container: "mp4", AudioKbps: 160, Mono: true}, "-ac 1"},
		{prodSettings{Container: "webm", AudioKbps: 160}, "-ac 2"},
		{prodSettings{Container: "webm", AudioKbps: 160, Mono: true}, "-ac 1"},
	} {
		got := strings.Join(audioArgs(c.st), " ")
		if !strings.Contains(got, c.want) {
			t.Errorf("audioArgs(%+v) = %q, want %q in it", c.st, got, c.want)
		}
	}
}

// TestMonoRidesInTheProject: the choice is part of the produce settings, so it
// comes back with the project -- and an untouched project's json is untouched,
// because off is the default and the tag says omitempty.
func TestMonoRidesInTheProject(t *testing.T) {
	b, err := json.Marshal(prodSettings{Mono: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"mono":true`) {
		t.Errorf("marshalled settings %s, want a mono field", b)
	}
	var st prodSettings
	if err := json.Unmarshal(b, &st); err != nil || !st.Mono {
		t.Errorf("mono did not survive the round trip: %v %+v", err, st)
	}
	if b, _ = json.Marshal(prodSettings{}); strings.Contains(string(b), "mono") {
		t.Errorf("default settings wrote %s — mono off should write nothing", b)
	}
}

// TestTheMonoToggleIsWired pins the widget to the settings: the check button
// exists on the grid, prodSettings() reads it, applyProdSettings writes it, and
// the summary line names the layout so the choice can be read back off the page.
func TestTheMonoToggleIsWired(t *testing.T) {
	b, err := os.ReadFile("produce.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`p.mono = gtk.NewCheckButtonWithLabel("Mono (one channel)")`,
		"check(2, 3, p.mono)", // a tick needs no leading label
		`Mono:      p.mono.Active(),`,
		`p.mono.SetActive(st.Mono)`,
		`audLayout(st)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("produce.go does not contain %q", want)
		}
	}
}

// The settings grid: three columns, one subject each, and a tick that says
// what it is on itself.
//
// It was two columns and seven rows -- the sound settings stacked under the
// picture settings, the page's whole right-hand half empty -- and every tick
// carried a leading label that said the same thing the tick did ("Frame
// timing: [x] Peak frame rate (VFR)"). VFR in particular sat two rows below
// the frame rate it qualifies, where it reads as a setting of its own rather
// than as what the number above it MEANS.
func TestTheProduceSettingsAreThreeColumns(t *testing.T) {
	body := funcBody(t, "produce.go", `func \(a \*App\) buildProduce\(\)`)
	for _, want := range []string{
		// the encoder column, the shape column, the sound-and-words column
		`at(0, 0, "Container:", p.container)`,
		`at(1, 0, "Resolution:", p.height)`,
		`p.subsLbl = at(2, 0, "Subtitles:", p.subs)`,
		// ...and the ticks, in the control column with no label of their own
		"check := func(col, row int, w gtk.Widgetter) { grid.Attach(w, col*2+1, row, 1, 1) }",
		"check(1, 3, p.vfr)", // beside the frame rate's own column, on the CRF row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the settings grid no longer contains %q", want)
		}
	}
	// the leading words are gone, not merely moved
	for _, gone := range []string{`"Frame timing:"`, `"Frame edges:"`, `"Audio channels:"`} {
		if strings.Contains(body, gone) {
			t.Errorf("a tick still carries %s, which is what the tick says", gone)
		}
	}
	// VFR shares the row with the CRF slider it was two rows under
	crf := strings.Index(body, `at(0, 3, "Quality (CRF):", p.crf)`)
	vfr := strings.Index(body, "check(1, 3, p.vfr)")
	if crf < 0 || vfr < 0 {
		t.Fatal("the CRF slider and the VFR tick are not both placed")
	}
	// a slider's own reading is drawn in the foreground colour: dimmed, it is
	// the app's own way of saying a control is dead
	if !strings.Contains(readSrc(t, "main.go"), "scale value, scale marks label { color: @theme_fg_color; }") {
		t.Error("a slider's value and marks are back in the dimmed colour")
	}
}
