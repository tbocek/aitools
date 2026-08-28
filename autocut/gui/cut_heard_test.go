package main

// Which microphone the finished video is heard on.
//
// It is ONE answer for the whole cut, and deliberately not the one the picture
// gives. Two cameras filming the same room record the same voices twice, a few
// frames apart and from two different distances, in two different rooms' worth
// of tone; taking each scene's sound from the camera that shot it would put a
// seam in the audio at every change of picture. The picture may cut as often as
// it likes -- that is what cutting is -- and the sound runs through it.
//
// So: ↑ and ↓ walk the lanes, the choice is saved with the cut, and the render
// takes every scene's sound from there. Empty means each scene heard with its
// own camera, which is what a one-camera session has always done and is why a
// cut.json with no such field still renders exactly as it did.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// heardTracks is two cameras -- the first stopped and started again, so it is
// two recordings on one row -- and a recorder running under both.
func heardTracks() ([]tlVideo, []tlAudio) {
	vids := []tlVideo{
		{base: "a1", path: "/f/a1.mp4", start: 0, dur: 50},
		{base: "b", path: "/f/b.mp4", start: 10, dur: 90},
		{base: "a2", path: "/f/a2.mp4", start: 60, dur: 40},
	}
	assignLanes(vids, nil)
	auds := append(masterOf(vids), tlAudio{base: "mic", path: "/f/mic.wav", start: 5, dur: 120})
	return vids, auds
}

// masterOf is masterLanes without the probe: the real one reads each file for
// its channel count, and these files are not on disk.
func masterOf(vids []tlVideo) []tlAudio {
	var out []tlAudio
	for _, v := range vids {
		out = append(out, tlAudio{base: v.base, path: v.path, start: v.start,
			dur: v.dur, chans: 1, master: true})
	}
	return out
}

func TestNoChoiceMeansEverySceneKeepsItsOwnSound(t *testing.T) {
	vids, auds := heardTracks()
	// the default, and the one that has to cost nothing: soundOf answers "the
	// picture's own", and the render leaves the clip exactly as it built it
	for _, at := range []float64{0, 20, 70, 99} {
		if p, _ := soundOf(vids, auds, "", at); p != "" {
			t.Errorf("with no lane chosen the sound at %.0f s came from %s", at, p)
		}
	}
}

func TestAChosenCameraIsHeardAcrossAllItsRecordings(t *testing.T) {
	vids, auds := heardTracks()
	// "a1" names the ROW, not the file: the camera stopped and started again
	// and the sound carries across the seam exactly as the picture does
	for _, c := range []struct {
		at   float64
		path string
		off  float64
	}{
		{20, "/f/a1.mp4", 20}, // the recording the choice was written as
		{70, "/f/a2.mp4", 10}, // ...and the next one on the same row
	} {
		p, off := soundOf(vids, auds, "a1", c.at)
		if p != c.path || off != c.off {
			t.Errorf("at %.0f s the first camera is heard from %s at %.0f s, want %s at %.0f",
				c.at, p, off, c.path, c.off)
		}
	}
}

func TestACameraThatWasNotRollingIsNotHeard(t *testing.T) {
	vids, auds := heardTracks()
	// 55 s is the hole between the first camera's two recordings. There is no
	// sound to take, and the honest answer is to leave the scene with its own
	// rather than to reach onto the other camera behind the user's back
	if p, _ := soundOf(vids, auds, "a1", 55); p != "" {
		t.Errorf("a camera that was not rolling was heard anyway, from %s", p)
	}
	// and a lane that has left the session entirely
	if p, _ := soundOf(vids, auds, "gone", 20); p != "" {
		t.Errorf("a lane that is not in the session was heard from %s", p)
	}
}

func TestTheWholeCutCanBeHeardOnASeparateRecording(t *testing.T) {
	vids, auds := heardTracks()
	// the point of a lapel mic: one lane, running under everything, and the
	// pictures cut over it
	p, off := soundOf(vids, auds, "mic", 30)
	if p != "/f/mic.wav" || off != 25 {
		t.Errorf("the recorder is heard from %s at %.0f s, want /f/mic.wav at 25", p, off)
	}
	// its own clock, not the session's: it started five seconds in
	if _, off := soundOf(vids, auds, "mic", 5); off != 0 {
		t.Errorf("the recorder's own start is at %.0f s of it, want 0", off)
	}
}

// ---- what the arrows walk ---------------------------------------------------

func TestTheArrowsWalkTheLanesThereAreToWalk(t *testing.T) {
	vids, auds := heardTracks()
	got := sndChoices(vids, auds)
	// "" first, then one entry per camera ROW named by its first recording,
	// then the separate recordings. Not one per file: a camera is picked whole
	want := []string{"", "a1", "b", "mic"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("↑/↓ walk %v, want %v", got, want)
	}

	// on one camera there is nothing to choose between: "" already means that
	// camera, and offering it twice reads as a key that does not work
	one := []tlVideo{{base: "a", start: 0, dur: 50}}
	assignLanes(one, nil)
	if got := sndChoices(one, masterOf(one)); len(got) != 1 || got[0] != "" {
		t.Errorf("a one-camera session offers %v, want nothing to choose", got)
	}
	// ...but one camera and a recorder is a real choice: the whole cut heard
	// on the lapel mic instead of the room
	withMic := append(masterOf(one), tlAudio{base: "mic", start: 0, dur: 50})
	if got := sndChoices(one, withMic); strings.Join(got, ",") != ",mic" {
		t.Errorf("one camera and a recorder offers %v", got)
	}
}

func TestTheLanePlateSaysWhichOneIsHeard(t *testing.T) {
	ed := newTestEd(t)
	ed.vids, ed.auds = heardTracks()
	if ed.heardOn("a1") || ed.heardOn("mic") {
		t.Error("a cut with no lane chosen marks one anyway")
	}
	ed.snd = "a1"
	// both of the first camera's recordings, because the choice was the row
	for _, base := range []string{"a1", "a2"} {
		if !ed.heardOn(base) {
			t.Errorf("%s is on the chosen camera's row and is not marked", base)
		}
	}
	for _, base := range []string{"b", "mic"} {
		if ed.heardOn(base) {
			t.Errorf("%s is marked as heard and is not the choice", base)
		}
	}
	ed.snd = "mic"
	if !ed.heardOn("mic") || ed.heardOn("a1") {
		t.Error("choosing the recorder did not move the mark off the camera")
	}
	// and the mark itself is on the lane's own plate, where the eye is
	if !strings.Contains(readSrc(t, "cut_audio.go"), `name += " · heard"`) {
		t.Error("nothing on the page says which lane the cut is heard on")
	}
}

func TestTheChoiceIsInASentence(t *testing.T) {
	vids, _ := heardTracks()
	for _, c := range []struct{ snd, want string }{
		{"", "each camera's own sound"},
		{"b", "camera 2 — b"},
		{"mic", "mic"},
	} {
		if got := sndLabel(vids, c.snd); got != c.want {
			t.Errorf("%q reads as %q, want %q", c.snd, got, c.want)
		}
	}
}

// ---- saved with the cut, and rendered from there ----------------------------

func TestTheChosenLaneIsSavedAndReadBack(t *testing.T) {
	ed := newTestEd(t)
	ed.segs = []cutSeg{{S: 0, E: 10}}
	ed.snd = "mic"
	ed.persist()

	b, err := os.ReadFile(ed.a.cutPath())
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Sound string }
	if json.Unmarshal(b, &f) != nil || f.Sound != "mic" {
		t.Fatalf("cut.json says %q, want mic:\n%s", f.Sound, b)
	}
	// and the render reads it from there: a.ed is nil, so this is the file
	// speaking, which is the case that matters -- a render started from a
	// project opened fresh
	if snd := ed.a.produceCut().Sound; snd != "mic" {
		t.Errorf("the render would use %q", snd)
	}
	// no choice writes no field, so a one-camera cut.json is the file it was
	ed.snd = ""
	ed.persist()
	b, _ = os.ReadFile(ed.a.cutPath())
	if strings.Contains(string(b), "sound") {
		t.Errorf("the default choice is written to the file:\n%s", b)
	}
}

func TestTheRenderTakesTheChosenLane(t *testing.T) {
	vids, auds := heardTracks()
	own := prodClip{video: &vids[1], local: 20, length: 5, tempo: 1, rate: 1}

	// the second camera's picture, heard on the first camera
	c := hearOn(own, vids, auds, "a1", 30)
	if c.snd != "/f/a1.mp4" || c.sndAt != 30 {
		t.Errorf("the clip is heard from %q at %.0f s, want /f/a1.mp4 at 30", c.snd, c.sndAt)
	}

	// ...and the cases that must change nothing, because a one-camera render
	// has to come out byte for byte the render it was
	for _, k := range []struct {
		what string
		c    prodClip
		snd  string
		at   float64
	}{
		{"no lane chosen", own, "", 30},
		{"the chosen lane IS this clip's own recording", own, "b", 30},
		{"the camera was not rolling", own, "a1", 55},
		{"a silent paste", prodClip{video: &vids[1], length: 5, mute: true}, "a1", 30},
	} {
		if got := hearOn(k.c, vids, auds, k.snd, k.at); got.snd != "" {
			t.Errorf("%s: the clip was given %q", k.what, got.snd)
		}
	}
}

func TestTheArrowsChangeTheChoiceAndSaveIt(t *testing.T) {
	ed := newTestEd(t)
	ed.vids, ed.auds = heardTracks()
	ed.segs = []cutSeg{{S: 0, E: 10}}

	// ↓ walks forward through what sndChoices offers, and wraps
	for _, want := range []string{"a1", "b", "mic", ""} {
		ed.cycleSound(1)
		if ed.snd != want {
			t.Fatalf("↓ landed on %q, want %q", ed.snd, want)
		}
	}
	// ↑ walks the other way
	ed.cycleSound(-1)
	if ed.snd != "mic" {
		t.Errorf("↑ landed on %q, want mic", ed.snd)
	}
	// and every step is saved: this is a choice about the finished video, and
	// a render started from a fresh window reads it off the file
	if snd := ed.a.produceCut().Sound; snd != "mic" {
		t.Errorf("the file says %q", snd)
	}
	// it is NOT an undo step, though. One keystroke puts it back, and a
	// history full of "changed my mind about the microphone" is a history that
	// has lost the edit before it
	if len(ed.undo) != 0 {
		t.Errorf("walking the lanes pushed %d undo steps", len(ed.undo))
	}

	// the keys themselves, which are a closure over a GTK controller: ↑ and ↓
	// unguarded, unlike ← and → beside them, which exist only while something
	// is in hand. Nothing else on the page wants them, and the choice is one
	// for the whole cut rather than something applied to a held thing
	keys := funcBody(t, "cut.go", `keys\.ConnectKeyPressed\(func\(keyval, keycode uint, state gdk\.ModifierType\) bool \{`)
	if !strings.Contains(keys, "case keyval == gdk.KEY_Up || keyval == gdk.KEY_Down:") ||
		!strings.Contains(keys, "ed.cycleSound(d)") {
		t.Errorf("↑ and ↓ no longer walk the lanes:\n%s", keys)
	}

	// nothing to walk on one camera: the key says so rather than appearing dead
	one := newTestEd(t)
	one.vids = []tlVideo{{base: "a", start: 0, dur: 50}}
	assignLanes(one.vids, nil)
	one.auds = masterOf(one.vids)
	one.cycleSound(1)
	if one.snd != "" {
		t.Errorf("a one-camera session was moved onto %q", one.snd)
	}
}

func TestTheSubstitutedSoundFillsASpedUpSlot(t *testing.T) {
	// the trim is in source seconds and the slot is in output ones, so a clip
	// played at double speed eats twice its length of the file. Without the
	// speed the lane would run out halfway and apad would finish the slot in
	// silence -- which sounds like the render dropping the audio
	src := readSrc(t, "produce.go")
	if !strings.Contains(src, `args = append(args, "-t", fmt.Sprintf("%.3f", c.length*math.Max(1, c.speed())),`) {
		t.Error("a substituted lane is trimmed to the slot rather than to what the slot eats")
	}
}
