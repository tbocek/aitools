package main

// The narrate page's logic that survives without a display: where preview
// playback jumps, and where the narration is read from.
//
// Preview playback follows the CUT, not the source file -- the picture has to
// skip what the edit removed. The GTK and GStreamer halves need a display, but
// the arithmetic that decides where to jump does not, and that is where an
// off-by-one costs the user a wrong preview.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// TestNoteForFollowsTheClip: a ✎ note is standing -- it is honored the next
// time the whole narration is written, which may be after the cut moved under
// it. Matching by index would hand a note to whatever clip inherited its
// position; matching by overlap keeps it on the footage it was written about,
// and lets a clip that replaced it start clean.
func TestNoteForFollowsTheClip(t *testing.T) {
	n := &narrator{entries: []narrEntry{
		{S: 100, E: 130, Instr: "mention the countdown"},
		{S: 400, E: 460, Instr: "shorter"},
		{S: 900, E: 940}, // narrated, never annotated
	}}
	for _, c := range []struct {
		name string
		seg  cutSeg
		want string
	}{
		{"the same clip", cutSeg{S: 100, E: 130}, "mention the countdown"},
		{"trimmed by a second", cutSeg{S: 101, E: 128}, "mention the countdown"},
		{"grown at both ends", cutSeg{S: 96, E: 137}, "mention the countdown"},
		{"split, mostly the second note's clip", cutSeg{S: 380, E: 430}, "shorter"},
		{"a clip that was never annotated", cutSeg{S: 900, E: 940}, ""},
		{"footage added where nothing was", cutSeg{S: 600, E: 640}, ""},
		{"touching at the edge is not overlap", cutSeg{S: 130, E: 160}, ""},
	} {
		if got := n.noteFor(c.seg); got != c.want {
			t.Errorf("%s: noteFor(%.0f–%.0f) = %q, want %q", c.name, c.seg.S, c.seg.E, got, c.want)
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

// TestALinePlayedAloneOutlivesTheTick guards the rule that made a line's own ▶
// speak one word and stop. The preview and the per-line ▶ share one audio
// player, and the 100 ms tick's job is to hush that player whenever the picture
// is not running -- which is exactly the state a line auditioned over a still
// frame plays in. n.solo is what tells the two apart, and nothing at run time
// notices when a rewrite of this branch drops it.
func TestALinePlayedAloneOutlivesTheTick(t *testing.T) {
	src, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	hush := regexp.MustCompile(`(?s)if n\.player == nil \|\| !n\.player\.playing \{.*?\n\t\}`).Find(src)
	if hush == nil {
		t.Fatal("followPlayback's picture-stopped branch is gone")
	}
	if !strings.Contains(string(hush), "n.solo") {
		t.Error("the tick pauses the voice without asking who owns it — a line played " +
			"from its own ▶ will be cut off within 100 ms")
	}
	// and the other half: starting the preview has to take that player back,
	// or the line and the picture speak over each other
	tog := regexp.MustCompile(`(?s)func \(n \*narrator\) toggle\(\) \{.*?\n}\n`).Find(src)
	if !strings.Contains(string(tog), "claimVoice()") {
		t.Error("the preview starts without claiming the shared audio player")
	}
	claim := regexp.MustCompile(`(?s)func \(n \*narrator\) claimVoice\(\) \{.*?\n}\n`).Find(src)
	if claim == nil || !strings.Contains(string(claim), "n.solo = -1") {
		t.Error("claimVoice does not hand the players back to the preview")
	}
}

// TestALineAuditionRollsThePicture: a line's ▶ plays the clip it was written
// for, not the words alone over a frozen frame -- most of what there is to
// judge about a narration line is whether it lands on what is on screen. Source
// level, because the wiring is a GStreamer pipeline and a 100 ms tick: what a
// test can hold is that the button still reaches the picture, and that the
// audition still ends at its own clip instead of running the rest of the cut.
func TestALineAuditionRollsThePicture(t *testing.T) {
	src, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	speak := regexp.MustCompile(`(?s)func \(a \*App\) speakEntry\(i int\) \{.*?\n}\n`).Find(src)
	if speak == nil {
		t.Fatal("speakEntry is gone")
	}
	for _, want := range []string{"n.cue(e.S, true)", "n.solo, n.soloPic = i, true"} {
		if !strings.Contains(string(speak), want) {
			t.Errorf("a line's ▶ no longer rolls the picture with the voice (missing %s)", want)
		}
	}
	// the fallback is not a nicety: a clip whose recording is not on this
	// machine still has to be auditionable, and that is the only case left where
	// the line is spoken over whatever the frame happens to be showing
	if !strings.Contains(string(speak), "a.speakAlone(i, e)") {
		t.Error("a clip no recording covers can no longer be auditioned at all")
	}
	follow := regexp.MustCompile(`(?s)func \(n \*narrator\) followPlayback\(\) bool \{.*?\n}\n`).Find(src)
	if follow == nil {
		t.Fatal("followPlayback is gone")
	}
	if !strings.Contains(string(follow), "n.entries[n.solo].E") {
		t.Error("an audition no longer stops at the end of its clip — a line's ▶ and " +
			"the run bar's ▶ would be the same button")
	}
	// and the row that is sounding has to draw the ⏸ for it wherever the sound
	// came from, or the button the user just pressed goes on showing ▶
	if !strings.Contains(string(follow), "n.speaking = ei") {
		t.Error("the tick speaks a line without telling its row, so the row keeps showing ▶")
	}
}

// TestARescanReadsTheNarrationBack: delete step4/ and the page went on showing
// the narration it had in memory, with an Outputs line counting files that were
// no longer there. ⟳ is the button whose whole job is "the folder is the
// answer" -- and this was the one step it skipped.
func TestARescanReadsTheNarrationBack(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root, outDir: root}
	n := &narrator{a: a, entries: []narrEntry{{S: 0, E: 3, Text: "one"}}}
	n.save()

	n.load()
	if len(n.entries) != 1 {
		t.Fatalf("after saving and re-reading, entries = %+v", n.entries)
	}
	if err := os.RemoveAll(a.narrateDir()); err != nil {
		t.Fatal(err)
	}
	n.load()
	if len(n.entries) != 0 {
		t.Errorf("step4/ is gone and the page still holds %d line(s)", len(n.entries))
	}
	if got := summarizeOutputs(a.narrateDir()); got != "nothing yet" {
		t.Errorf("a deleted step4/ still summarizes as %q", got)
	}

	// the wiring that gets that re-read to happen at all
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	rescan := regexp.MustCompile(`(?s)func \(a \*App\) rescanAll\(\) \{.*?\n}\n`).Find(src)
	if rescan == nil || !strings.Contains(string(rescan), "a.updateStep4Info()") {
		t.Error("a rescan skips the narrate step, so its rows and its Outputs line go stale")
	}
	info := regexp.MustCompile(`(?s)func \(a \*App\) updateStep4Info\(\) \{.*?\n}\n`).Find(src)
	if info == nil {
		t.Fatal("updateStep4Info is gone")
	}
	for _, want := range []string{"n.load()", "n.rebuildRows()", "n.updateOut()"} {
		if !strings.Contains(string(info), want) {
			t.Errorf("the narrate rescan does not %s", want)
		}
	}
}
