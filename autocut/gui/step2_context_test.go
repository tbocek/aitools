package main

// What the describe model is told was said around a chunk of frames. The
// election has two caps that can each bind first, a boundary rule that decides
// whether a line is evidence or merely context, and a rendering that has to
// survive an empty transcript without looking like a broken pipeline.

import (
	"strings"
	"testing"
)

// rows named for what they are, so a failure message says which line moved
func ctxRows() []tsvRow {
	return []tsvRow{
		{0.5, 1.5, "way before"},     // 8.5 s before the chunk
		{2.0, 3.0, "far before"},     // 7.0 s before
		{6.0, 7.0, "near before"},    // 3.0 s before
		{9.5, 10.5, "straddles in"},  // starts before the chunk, ends inside
		{11.4, 12.0, "during one"},   //
		{14.8, 15.6, "during two"},   //
		{17.1, 18.0, "near after"},   // 1.1 s after the last frame
		{21.0, 22.0, "far after"},    // 5.0 s after
		{25.5, 26.0, "way after"},    // 9.5 s after
		{26.5, 27.0, "just too far"}, // 10.5 s after -- outside the window
	}
}

func texts(rows []tsvRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.text)
	}
	return strings.Join(out, "|")
}

func TestElectSpeechPicksTheNearestFewEitherSide(t *testing.T) {
	before, during, after := electSpeech(ctxRows(), 10, 16)
	// two a side, nearest first while choosing but chronological when emitted
	if got := texts(before); got != "far before|near before" {
		t.Errorf("before = %q, want the two nearest in reading order", got)
	}
	if got := texts(after); got != "near after|far after" {
		t.Errorf("after = %q, want the two nearest in reading order", got)
	}
	// the straddling line is evidence, whole and in the middle section
	if got := texts(during); got != "straddles in|during one|during two" {
		t.Errorf("during = %q, want the straddling line and both inside ones", got)
	}
}

// Both caps apply on their own, and either can be the one that binds.
func TestElectSpeechAppliesBothCapsIndependently(t *testing.T) {
	// count binds: five lines within the window, two survive
	dense := []tsvRow{
		{5.0, 5.5, "a"}, {6.0, 6.5, "b"}, {7.0, 7.5, "c"},
		{8.0, 8.5, "d"}, {9.0, 9.5, "e"},
	}
	before, _, _ := electSpeech(dense, 10, 16)
	if got := texts(before); got != "d|e" {
		t.Errorf("count cap: before = %q, want the two nearest", got)
	}
	// window binds: only one line is close enough, so only one comes along
	sparse := []tsvRow{{10.0, 11.0, "ancient"}, {80.0, 81.0, "old"}, {95.0, 97.0, "recent"}}
	before, _, _ = electSpeech(sparse, 100, 106)
	if got := texts(before); got != "recent" {
		t.Errorf("window cap: before = %q, want only the one inside 10 s", got)
	}
	// and the window is inclusive at exactly 10 s, exclusive past it
	edge := []tsvRow{{89.0, 90.0, "exactly ten"}, {88.0, 89.5, "ten and a half"}}
	before, _, _ = electSpeech(edge, 100, 106)
	if got := texts(before); got != "exactly ten" {
		t.Errorf("edge: before = %q, want 10.0 s kept and 10.5 s dropped", got)
	}
}

// The three sets partition the transcript. That is what keeps a line from
// being shown twice -- as evidence and again as context -- without any
// bookkeeping, and what keeps a line from being dropped in the gap between
// them. Both failures are silent in a prompt, so they are checked here.
func TestElectSpeechIsDisjointAndTotal(t *testing.T) {
	// every chunk placement against the same rows, including ones that start
	// or end mid-segment and ones the transcript does not reach
	for _, c := range []struct{ s, e float64 }{
		{0, 6}, {10, 16}, {10.5, 10.5}, {9.9, 10.1}, {15, 30}, {60, 66}, {-5, 0},
	} {
		rows := ctxRows()
		before, during, after := electSpeech(rows, c.s, c.e)
		seen := map[string]int{}
		for _, set := range [][]tsvRow{before, during, after} {
			for _, r := range set {
				seen[r.text]++
			}
		}
		for text, n := range seen {
			if n > 1 {
				t.Errorf("chunk %g-%g: %q appears %d times", c.s, c.e, text, n)
			}
		}
		// nothing may fall through a crack: a row that is in none of the three
		// sets must have been dropped by the window or the count, never by an
		// interval test that misses it
		for _, r := range rows {
			if seen[r.text] > 0 {
				continue
			}
			overlaps := r.e > c.s && r.s < c.e
			if overlaps {
				t.Errorf("chunk %g-%g: %q overlaps but was not emitted", c.s, c.e, r.text)
			}
		}
	}
}

// one source, named the way describeVideo names the footage's own audio
func ownAudio(rows []tsvRow) []speechSrc {
	return []speechSrc{{label: "clip.mkv", rows: rows}}
}

// The rendered block, exactly. The headings are load-bearing -- the prompt
// tells the model to trust one section and not describe the others -- so the
// wording is pinned rather than described. So is the label on every line: the
// model is told to weigh a line by which microphone it came off, which it can
// only do if the line says.
func TestSpeechBlockRendersAllThreeSections(t *testing.T) {
	rows := []tsvRow{
		{3.8, 5.0, "so the trick with these panels is"},
		{11.4, 12.0, "you pull the left one first"},
		{14.8, 15.6, "and the latch releases"},
		{17.1, 18.0, "now the same on the other side"},
	}
	want := `--- context before (do not describe) ---
[-6.2s] clip.mkv: "so the trick with these panels is"
--- spoken during these frames ---
[+1.4s] clip.mkv: "you pull the left one first"
[+4.8s] clip.mkv: "and the latch releases"
--- context after (do not describe) ---
[+7.1s] clip.mkv: "now the same on the other side"`
	if got := speechBlock(ownAudio(rows), 10, 16); got != want {
		t.Errorf("block is\n%s\nwant\n%s", got, want)
	}
}

// Two microphones on one chunk. Each is elected on its own -- so the cap is per
// speaker and a talkative track cannot crowd the other out of its own context
// -- and the survivors are merged into one run in time order, because the block
// is a record of a conversation and reading it out of order misreads who
// answered whom.
func TestSpeechBlockMergesEverySourceInTimeOrder(t *testing.T) {
	srcs := []speechSrc{
		{label: "clip.mkv", rows: []tsvRow{
			{11.0, 11.4, "game one"},
			{14.0, 14.4, "game two"},
			{4.0, 4.5, "game before A"}, // both inside the window: the two nearest
			{6.0, 6.5, "game before B"}, // survive, and they are these two
			{1.0, 1.5, "game before C"}, // furthest, so dropped by the count cap
		}},
		{label: "mic.flac", rows: []tsvRow{
			{12.0, 12.4, "mic one"},
			{15.0, 15.4, "mic two"},
			{5.0, 5.5, "mic before"}, // its own budget, untouched by clip.mkv's
		}},
	}
	got := speechBlock(srcs, 10, 16)
	want := `--- context before (do not describe) ---
[-6.0s] clip.mkv: "game before A"
[-5.0s] mic.flac: "mic before"
[-4.0s] clip.mkv: "game before B"
--- spoken during these frames ---
[+1.0s] clip.mkv: "game one"
[+2.0s] mic.flac: "mic one"
[+4.0s] clip.mkv: "game two"
[+5.0s] mic.flac: "mic two"
--- context after (do not describe) ---
(none)`
	if got != want {
		t.Errorf("merged block is\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "game before C") {
		t.Error("the count cap did not bind per source")
	}
}

// A silent stretch still gets all three headings. An omitted section is
// indistinguishable from a pipeline failure, and the model cannot tell "nobody
// spoke" from "the speech was lost on the way here".
func TestSpeechBlockSaysNothingRatherThanSayingNothing(t *testing.T) {
	want := `--- context before (do not describe) ---
(none)
--- spoken during these frames ---
(no speech during these frames)
--- context after (do not describe) ---
(none)`
	if got := speechBlock(nil, 10, 16); got != want {
		t.Errorf("empty block is\n%s\nwant\n%s", got, want)
	}
	// and a transcript that exists but is nowhere near this chunk reads the same
	far := []tsvRow{{0, 1, "long gone"}, {600, 601, "much later"}}
	if got := speechBlock(ownAudio(far), 300, 306); got != want {
		t.Errorf("out-of-range block is\n%s\nwant\n%s", got, want)
	}
	// so does a session whose second microphone recorded nothing at all
	quiet := []speechSrc{{label: "clip.mkv"}, {label: "mic.flac"}}
	if got := speechBlock(quiet, 10, 16); got != want {
		t.Errorf("silent-sources block is\n%s\nwant\n%s", got, want)
	}
}

// Whatever the ASR produced goes through untouched: the wording is the
// evidence, and a line that reads oddly is a line the model should see reading
// oddly. Segments stay one per line for the same reason -- the boundaries are
// where the pauses were.
func TestSpeechBlockPassesTextThroughUnchanged(t *testing.T) {
	rows := []tsvRow{
		{11.0, 11.5, "  no wait  "},
		{11.5, 12.0, `he said "left" i think`},
		{12.0, 12.5, "uh"},
	}
	got := speechBlock(ownAudio(rows), 10, 16)
	for _, want := range []string{
		`[+1.0s] clip.mkv: "  no wait  "`,
		`[+1.5s] clip.mkv: "he said "left" i think"`,
		`[+2.0s] clip.mkv: "uh"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block is missing the line %s:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "[+"); n != 3 {
		t.Errorf("%d lines emitted for 3 segments -- adjacent ones were merged:\n%s", n, got)
	}
}
