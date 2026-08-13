package main

// The narrate page's logic that survives without a display: where preview
// playback jumps, and where the narration is read from.
//
// Preview playback follows the CUT, not the source file -- the picture has to
// skip what the edit removed. The GTK and GStreamer halves need a display, but
// the arithmetic that decides where to jump does not, and that is where an
// off-by-one costs the user a wrong preview.

import (
	"path/filepath"
	"testing"
)

// realCut is the shape a session actually has: long holes between clips, the
// first clip starting minutes in, fractional bounds.
var realCut = []cutSeg{
	{S: 392.85, E: 419}, {S: 464, E: 491}, {S: 543, E: 575},
	{S: 639, E: 670}, {S: 2080, E: 2114.36},
}

func TestGapAt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		t         float64
		cur, next int
	}{
		{"before the cut starts", 0, -1, 0},
		{"first frame of a clip", 392.85, 0, -1},
		{"inside a clip", 400, 0, -1},
		// the end is exclusive: 419 is the first instant the edit removed, so
		// it must read as a gap or the jump would fire a tick late
		{"the instant a clip ends", 419, -1, 1},
		{"deep in a gap", 450, -1, 1},
		{"the long gap before the last clip", 1000, -1, 4},
		{"inside the last clip", 2100, 4, -1},
		{"past the whole cut", 2114.36, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cur, next := gapAt(realCut, tc.t)
			if cur != tc.cur || next != tc.next {
				t.Errorf("gapAt(%.2f) = (%d, %d), want (%d, %d)",
					tc.t, cur, next, tc.cur, tc.next)
			}
		})
	}

	// no cut at all must not read as "past the end", which would pause the
	// preview the moment it started
	if cur, next := gapAt(nil, 100); cur != -1 || next != -1 {
		t.Errorf("gapAt(nil) = (%d, %d), want (-1, -1)", cur, next)
	}
}

// TestClipsPrefersTheCut pins where the preview gets its clip list. Narration is
// written one entry per clip, but it is written LATER: following the entries
// alone means a project that has been cut but not yet narrated previews the
// uncut source, which is exactly not what step 4 asked for.
func TestClipsPrefersTheCut(t *testing.T) {
	n := &narrator{a: &App{}, entries: []narrEntry{{S: 10, E: 20}}}

	if got := n.clips(); len(got) != 1 || got[0].S != 10 {
		t.Fatalf("with no editor, clips() = %v, want the narration entries", got)
	}

	n.a.ed = &cutEditor{segs: realCut}
	got := n.clips()
	if len(got) != len(realCut) || got[0].S != realCut[0].S {
		t.Fatalf("with an editor, clips() = %v, want the cut", got)
	}

	// an emptied cut is not a cut: fall back rather than preview nothing
	n.a.ed = &cutEditor{}
	if got := n.clips(); len(got) != 1 || got[0].E != 20 {
		t.Fatalf("with an empty editor, clips() = %v, want the narration entries", got)
	}
}

// TestEntryAtIsByTime guards the voice lookup. Clip index and entry index drift
// apart the moment someone edits the cut after narrating; speaking by index
// would then play the wrong line over the wrong picture, confidently.
func TestEntryAtIsByTime(t *testing.T) {
	n := &narrator{entries: []narrEntry{
		{S: 392.85, E: 419, Text: "one"},
		{S: 2080, E: 2114.36, Text: "two"},
	}}
	for _, tc := range []struct {
		t    float64
		want int
	}{{392.85, 0}, {400, 0}, {419, -1}, {1000, -1}, {2100, 1}, {3000, -1}} {
		if got := n.entryAt(tc.t); got != tc.want {
			t.Errorf("entryAt(%.2f) = %d, want %d", tc.t, got, tc.want)
		}
	}
}

// TestNarrationFollowsOutDir is the regression behind "my narration is gone
// after a restart". It was always written; it was READ once, while building the
// page at startup, when outDir was still the root -- so a project whose output
// folder is out/<name> loaded an empty list and looked like it had never saved.
// Anything that moves outDir has to re-read it.
func TestNarrationFollowsOutDir(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "out", "test")

	// a project narrated with its output folder set
	saver := &narrator{a: &App{root: root, outDir: out}}
	saver.entries = []narrEntry{{S: 392.85, E: 419, Text: "one", Emotion: "calm"}}
	saver.save()

	// the restart: the page is built before the project moves outDir
	n := &narrator{a: &App{root: root, outDir: root}}
	n.entries = []narrEntry{{Text: "stale"}} // load must clear, not merge
	n.load()
	if len(n.entries) != 0 {
		t.Fatalf("loaded %d entries from the root, want none there", len(n.entries))
	}

	// ...and then the project points it at the real output folder
	n.a.outDir = out
	n.load()
	if len(n.entries) != 1 || n.entries[0].Text != "one" || n.entries[0].S != 392.85 {
		t.Fatalf("after outDir moved, entries = %+v, want the saved narration", n.entries)
	}
}
