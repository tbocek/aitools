package main

// Hearing and seeing the same second.
//
// The cut page used to show one blue lane per separate recording and play the
// footage's file. So the thing you were cutting against -- the voices, which
// are on the other recorder -- was neither audible nor comparable to anything:
// a lane on its own is a smear, and whether it sits where it belongs is not a
// question one waveform can answer. What is pinned here is the pair that makes
// it answerable: the footage's own sound is a lane too, drawn from the same
// clock, and everything else in the session is played under it.

import (
	"path/filepath"
	"testing"
)

// ---- the picture ------------------------------------------------------------

// The same instant, recorded twice, has to be drawn in the same column.
//
// Two files are made that share one moment and nothing else: the footage
// carries a tone from 10:00:06 to 10:00:08, and a recording that started four
// seconds earlier carries a tone over exactly the same two seconds of the
// afternoon. Both go through the ordinary path -- sessionTracks places them,
// masterLanes makes the footage's track a lane, the real decoder builds both
// envelopes -- and then the lanes are painted and asked where their loud part
// is. Off-by-one-recording's-start is the whole failure mode of this page, and
// it is invisible in every unit that does not draw.
func TestTheSameInstantIsDrawnInTheSameColumn(t *testing.T) {
	a := insertApp(t)
	dir := t.TempDir()

	// footage from 10:00:04, eight seconds, tone over its own 2..4 s
	footage := filepath.Join(dir, "game_2026-01-01_10-00-04.mp4")
	mustFFmpeg(t, "-f", "lavfi", "-t", "8", "-i", "testsrc=size=160x120:rate=15",
		"-f", "lavfi", "-t", "2", "-i", "anullsrc=r=48000:cl=mono",
		"-f", "lavfi", "-t", "2", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-t", "4", "-i", "anullsrc=r=48000:cl=mono",
		"-filter_complex", "[1:a][2:a][3:a]concat=n=3:v=0:a=1[a]",
		"-map", "0:v", "-map", "[a]", "-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-c:a", "aac", footage)

	// the recording from 10:00:00, twelve seconds, tone over its own 6..8 s --
	// the same two seconds of the afternoon, told in the other recorder's clock
	mic := filepath.Join(dir, "mic_2026-01-01_10-00-00.wav")
	mustFFmpeg(t, "-f", "lavfi", "-t", "6", "-i", "anullsrc=r=48000:cl=mono",
		"-f", "lavfi", "-t", "2", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-t", "4", "-i", "anullsrc=r=48000:cl=mono",
		"-filter_complex", "[0:a][1:a][2:a]concat=n=3:v=0:a=1[a]", "-map", "[a]",
		"-c:a", "pcm_s16le", mic)

	vids, recs, err := a.sessionTracks([]string{footage}, []string{mic})
	if err != nil {
		t.Fatal(err)
	}
	for i := range recs {
		recs[i].chans = ffprobeChannels(recs[i].path)
	}
	ed := &cutEditor{a: a, vids: vids, pps: 40, waves: map[string]*waveform{}}
	ed.auds = append(masterLanes(vids), recs...)
	sortLanes(ed.auds)
	if len(ed.auds) != 2 || !ed.auds[0].master || ed.auds[1].master {
		t.Fatalf("the lanes came out as %+v, want the footage's own first", ed.auds)
	}
	ed.relayout()
	for i, au := range ed.auds {
		wf, err := loadWave(a.waveCache(), au.path, au.chans)
		if err != nil {
			t.Fatalf("no envelope for lane %d (%s): %v", i, au.base, err)
		}
		ed.waves[au.base] = wf
	}

	at := renderAudio(t, ed, 500, ed.audioHeight())
	// the loud part of each lane, in px. Read across the row just above the
	// lane's floor, which a column of any height at all covers -- the fill
	// stands up from there -- so what separates sound from silence on that row
	// is the ink and not the height. The quiet stretches have the baseline
	// under them, and the baseline is a third of the blue over dark ground,
	// well short of what isBlue will take.
	span := func(lane int) (int, int) {
		t.Helper()
		y := int(wavePad+float64(lane)*(waveLaneH+waveGap)+waveLaneH) - 2
		lo, hi := -1, -1
		for x := 0; x < 500; x++ {
			if r, g, b := at(x, y); isBlue(r, g, b) {
				if lo < 0 {
					lo = x
				}
				hi = x
			}
		}
		return lo, hi
	}
	m0, m1 := span(0) // the footage's own sound
	r0, r1 := span(1) // the separate recording
	t.Logf("footage tone at px %d..%d, recording tone at px %d..%d", m0, m1, r0, r1)

	// 40 px/s, and the footage's own tone starts 2 s into it
	if m0 < 0 || m1 < 0 {
		t.Fatal("the footage's own sound was not drawn at all — there is no lane to compare against")
	}
	if r0 < 0 || r1 < 0 {
		t.Fatal("the recording's lane is empty where it was recording a tone")
	}
	if m0 < 74 || m0 > 86 {
		t.Errorf("the footage's tone starts at px %d, want ~80 (2 s at 40 px/s)", m0)
	}
	// the point of the whole file: the two clocks put it in the same column
	if d := r0 - m0; d < -4 || d > 4 {
		t.Errorf("the recording's tone starts %d px from the footage's (%d vs %d) — "+
			"%.2f s out; the lanes are not on the footage's clock", d, r0, m0, float64(d)/ed.pps)
	}
	if d := r1 - m1; d < -4 || d > 4 {
		t.Errorf("the recording's tone ends %d px from the footage's (%d vs %d)", d, r1, m1)
	}
}

// A video with no sound gets no lane: the lane would be a strip of ground with
// nothing in it and a decode that can only fail, and the file is in the session
// for its pictures.
func TestASilentVideoHasNoLane(t *testing.T) {
	insertApp(t) // for ffmpeg; the App itself is not needed here
	dir := t.TempDir()
	silent := filepath.Join(dir, "silent_2026-01-01_10-00-00.mp4")
	mustFFmpeg(t, "-f", "lavfi", "-t", "1", "-i", "testsrc=size=160x120:rate=15",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", silent)
	if got := ffprobeChannels(silent); got != 0 {
		t.Errorf("a video with no audio stream probes as %d channel(s), want 0", got)
	}
	if lanes := masterLanes([]tlVideo{{base: "silent", path: silent, dur: 1}}); len(lanes) != 0 {
		t.Errorf("a silent video was given %d lane(s): %+v", len(lanes), lanes)
	}
}

// ---- the sound --------------------------------------------------------------

// What the preview plays under a piece of footage: every recording that was
// running while it ran, each with the offset that turns a time in the footage
// into a time in that recording. Not the footage's own track -- that is what
// the picture is already playing, and a second copy of it half a frame late is
// a broken speaker, not a mix.
func TestThePreviewPlaysTheRecordingsUnderTheFootage(t *testing.T) {
	v := tlVideo{base: "game", path: "/x/game.mp4", start: 100, dur: 60}
	ed := &cutEditor{
		vids: []tlVideo{v},
		auds: []tlAudio{
			// the footage's own sound: a lane, never a mix input
			{base: "game", path: "/x/game.mp4", start: 100, dur: 60, master: true},
			// running from long before: the footage's 0 is its own 90 s
			{base: "mic", path: "/x/mic.wav", start: 10, dur: 300},
			// started after the footage did, and still running at its end
			{base: "late", path: "/x/late.wav", start: 130, dur: 100},
			// a recording of another part of the day entirely
			{base: "other", path: "/x/other.wav", start: 4000, dur: 600},
		},
	}
	got := ed.mixUnder(&v)
	want := []mixTrack{
		{path: "/x/mic.wav", delta: 90, dur: 300},
		{path: "/x/late.wav", delta: -30, dur: 100},
	}
	if len(got) != len(want) {
		t.Fatalf("%d recording(s) go under the footage, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s comes out as %+v, want %+v", want[i].path, got[i], want[i])
		}
	}

	// and the offsets mean what they say: at the head of the footage the mic is
	// 90 s into itself, and the one that started 30 s later is not playing yet
	mic := &auxAudio{delta: 90, dur: 300}
	late := &auxAudio{delta: -30, dur: 100}
	if !mic.running(0) || !mic.running(60) {
		t.Error("the mic was recording throughout and the preview would not play it")
	}
	if late.running(0) {
		t.Error("a recording that started 30 s into the footage would be played at its head — " +
			"which is 30 s of the wrong sound, in sync with nothing")
	}
	if !late.running(30.5) {
		t.Error("the late recording is not played once it has started")
	}
	// past its own end it is silence, not its last second held or its first
	// second again
	if late.running(140) {
		t.Error("a recording that had already stopped would still be played")
	}
}
