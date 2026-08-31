package main

// A file with more than one audio track.
//
// OBS writes the desktop and the microphone as two tracks of one .mkv; a camera
// with an on-board mic and a lav on the same card is another; a capture with the
// game on one track and a headset on the other is the everyday one. Until now the
// session read the first of them and the rest were not in the project at all --
// not on a lane, not in a waveform, not in the mix, and so not something the cut
// could say yes or no to. The sound was in the file the whole time and the only
// way to reach it was to demux it by hand outside the app and add the result as
// a second source.
//
// They are separate recordings that happen to share a container, so that is what
// they become here: one lane each, named for the file and the track, on the
// file's own clock, and from that point indistinguishable from a recorder that
// was running beside the camera. The same waveform, the same green/grey badge on
// a held scene (cut_hear.go), the same entry in a clip's mix (clipMixes).
//
// Which of them the session uses is a choice on the Prepare row, not in the cut,
// because it is a fact about the FILE: a project that only ever wanted the
// microphone should not carry the desktop track through every step in order to
// silence it in every scene. The default is the first track alone -- which is
// exactly what every project made before this already means, so an old one opens,
// cuts and renders unchanged.

import (
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// audTrack is one audio stream of a file as ffprobe lists them. The POSITION in
// the slice is the a:N index everything below uses -- ffprobe lists them in that
// order and ffmpeg counts them the same way, so no absolute stream index has to
// be carried around and no file's video streams can shift the numbering.
type audTrack struct {
	chans int    // 1 or 2, downmixed like ffprobeChannels: see lanes
	title string // the recorder's own name for it ("Mic/Aux"), or ""
}

// ffprobeTracks is every audio stream in a file, in a:0..a:N-1 order.
//
// Empty means the file has no sound, which is a real answer: a silent screen
// capture gets no lane rather than a strip of ground and a decode that can only
// fail. A probe that would not RUN is not that answer -- it is no answer -- and
// reports one mono track, which is the same guess this made when it was called
// ffprobeChannels and keeps a file the probe stumbled over in the session.
func ffprobeTracks(path string) []audTrack {
	out, err := exec.Command(ffTool("ffprobe"), "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=channels:stream_tags=title", "-of", "csv=p=0", path).Output()
	if err != nil {
		return []audTrack{{chans: 1}}
	}
	return parseTracks(string(out))
}

// parseTracks reads that listing, one track per line. Split out from the probe
// so the answers it gives to a line it did not expect can be asked for directly
// -- the file that would produce one is a file nobody can conveniently write.
func parseTracks(out string) []audTrack {
	var tr []audTrack
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); ln == "" {
			continue
		}
		// "2" for a track with no name, "2,Desktop Audio" for one with. The
		// title is cut off LAST because it is the field that can hold a comma,
		// and a recorder's own name for a track is exactly where one turns up.
		num, title, _ := strings.Cut(ln, ",")
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil || n < 1 {
			n = 1 // a stream ffprobe would not count is still a stream
		}
		tr = append(tr, audTrack{chans: min(2, n), title: strings.TrimSpace(title)})
	}
	return tr
}

// ffprobeChannels is how many lanes a recording gets: the first track's channel
// count, and zero when there is no audio at all. Kept as its own name because
// that is the question a separately recorded file asks -- one file, one track,
// how deep is its lane -- and asking it through the list is the same probe.
func ffprobeChannels(path string) int {
	tr := ffprobeTracks(path)
	if len(tr) == 0 {
		return 0
	}
	return tr[0].chans
}

// trackName is a lane's name: the file's own for the first track, and the file's
// with the track number after it for every other.
//
// The first is BARE, and that is the load-bearing half. A lane's name is the key
// in cutSeg.Quiet, cutFile.Shift, cutFile.Rows and the waveform cache, so
// numbering the first track would be a project that opens with its silences and
// its clock corrections pointing at lanes that no longer exist.
//
// Positional, and not the recorder's own title: the number cannot repeat and a
// title can -- two tracks both called "Track 1" is a real file -- and a repeat
// here is two lanes sharing a map key, which loses data quietly. The title is
// shown on the Prepare row instead, where a collision is something you look at.
func trackName(base string, n int) string {
	if n <= 0 {
		return base
	}
	return fmt.Sprintf("%s #%d", base, n+1) // 1-based, the way recorders count them
}

// wantTracks is which of a file's audio streams this session uses: the choice
// stored on its Prepare row, made safe against the file in front of it.
//
// Empty means the first track alone. That is both what a project written before
// this choice existed means and what an ordinary single-track file means, which
// is why the stored field can be omitted and why nothing had to be migrated.
//
// Sorted, deduplicated, and dropped where it names a track the file has not got:
// a session re-recorded with fewer tracks would otherwise open with a lane whose
// every decode fails, and the failure would arrive later, in a render, as sound
// that is simply missing.
func wantTracks(sel []int, have int) []int {
	if have <= 0 {
		return nil
	}
	if len(sel) == 0 {
		return []int{0}
	}
	seen := map[int]bool{}
	var out []int
	for _, n := range sel {
		if n >= 0 && n < have && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// srcLanes is the footage's own sound as lanes -- one per audio track its
// Prepare row asked for.
//
// The FIRST track is the master: the sound the preview is already playing, drawn
// as a paired strip under the video's pictures and taken by the render off the
// footage input itself. Every other chosen track is a lane of its own down in
// the recorders' band, because that is what it is, and nothing downstream is
// meant to be able to tell it from a recorder that was running beside the
// camera.
//
// A video with no sound gets nothing, and so does one whose row asked for the
// second track and not the first: no master strip rather than a strip of ground
// and a decode of a track the session was told to leave alone.
//
// want is the per-file choice keyed on path (App.snappedTracks); a file it says
// nothing about uses its first track alone.
func srcLanes(vids []tlVideo, want map[string][]int) []tlAudio {
	var out []tlAudio
	for _, v := range vids {
		tr := ffprobeTracks(v.path)
		for _, n := range wantTracks(want[v.path], len(tr)) {
			out = append(out, tlAudio{base: trackName(v.base, n), path: v.path,
				start: v.start, off: v.off, dur: v.dur, track: n,
				chans: tr[n].chans, master: n == 0})
		}
	}
	return out
}

// ownTrack says whether a video's own first track is one of this session's
// lanes, which is the only thing a clip's base audio can come from: it is read
// off the input the picture is read off ([0:a] in encodeClip), so a row that
// asked for the second track and not the first has no way to put it there --
// that track arrives as a lane in the mix, like every other recording.
//
// True for a file nobody said anything about, so an ordinary render is unchanged
// by any of this.
func ownTrack(want map[string][]int, path string) bool {
	sel := want[path]
	if len(sel) == 0 {
		return true
	}
	for _, n := range sel {
		if n == 0 {
			return true
		}
	}
	return false
}

// trackOf is a chosen track as ffmpeg names it in a filtergraph or a -map: bare
// for the first, indexed for the rest.
//
// Bare and not "a:0" on purpose. ffmpeg picks the first matching stream for a
// bare specifier, so the two say the same thing to it -- but they do not say the
// same thing to the command line a run logs, and every render made before this
// existed logged the bare one. Written out, a multi-track file that nobody chose
// anything for renders the identical command it always did.
func trackOf(input int, track int) string {
	if track <= 0 {
		return fmt.Sprintf("%d:a", input)
	}
	return fmt.Sprintf("%d:a:%d", input, track)
}

// ---- the choice, on the Prepare row -----------------------------------------

// srcTracks is one source's audio streams, probed once per session and per
// path.
//
// Once, because the list is rebuilt whole on every change -- a tag toggled on
// the first row redraws the ninth -- and an ffprobe per row per rebuild is a
// page that stalls every time it is touched. A file cannot grow a track while
// it sits in the list, and the one thing that does change a row's audio hands
// the row a different file to point at (separate.go), which is a new key here.
func (s *sourceList) srcTracks(path string) []audTrack {
	if s.probed == nil {
		s.probed = map[string][]audTrack{}
	}
	tr, ok := s.probed[path]
	if !ok {
		tr = ffprobeTracks(path)
		s.probed[path] = tr
	}
	return tr
}

// setTrack turns one of a file's audio tracks on or off for this session.
//
// The last one on cannot be turned off. A source in the list is a source the
// session uses, and "this file, none of it" is not a third state worth carrying
// through every step -- it is the ✕ at the end of the row, which is already
// there. Refused here rather than in the widget so the stored form keeps one
// meaning: what is listed is what is used, and nothing listed is the first
// track alone (wantTracks).
func (s *sourceList) setTrack(i, n int, on bool) {
	if i < 0 || i >= len(s.items) {
		return
	}
	it := s.items[i]
	var next []int
	for _, t := range wantTracks(it.tracks, len(s.srcTracks(it.path))) {
		if t != n {
			next = append(next, t)
		}
	}
	if on {
		next = append(next, n)
		sort.Ints(next)
	}
	if len(next) == 0 {
		return // the last one standing; see above
	}
	s.items[i].tracks = next
	s.changed()
}

// trackLabel names one track the way the row shows it: its number, and the name
// the recorder gave it when it gave one -- OBS writes "Desktop Audio" and "Mic/
// Aux" into the container, and those are the words the person choosing is
// thinking in. The number is always there because the title is the part that
// can be missing, wrong, or the same on two tracks.
func trackLabel(n int, t audTrack) string {
	name := fmt.Sprintf("Track %d", n+1)
	if t.title != "" {
		name += " — " + t.title
	}
	if t.chans > 1 {
		return name + " (stereo)"
	}
	return name + " (mono)"
}

// trackButton is the row's control for a multi-track file: a menu holding one
// check per audio stream, and nil for the ordinary file with one, which has
// nothing to choose and would only get a button that cannot be wrong.
//
// It says how many are on over how many there are, because that is the fact a
// glance up the list is after -- which of these files is only half in the
// session -- and it is the fact the rest of the app is about to act on.
func (s *sourceList) trackButton(i int) *gtk.MenuButton {
	it := s.items[i]
	tr := s.srcTracks(it.path)
	if len(tr) < 2 {
		return nil
	}
	on := wantTracks(it.tracks, len(tr))
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	box.SetMarginTop(6)
	box.SetMarginBottom(6)
	box.SetMarginStart(6)
	box.SetMarginEnd(6)
	head := gtk.NewLabel("Audio tracks in this file")
	head.SetXAlign(0)
	head.AddCSSClass("heading")
	box.Append(head)
	for n := range tr {
		n := n
		c := gtk.NewCheckButtonWithLabel(trackLabel(n, tr[n]))
		c.SetActive(slices.Contains(on, n))
		c.ConnectToggled(func() { s.setTrack(i, n, c.Active()) })
		box.Append(c)
	}
	why := gtk.NewLabel("Each track chosen gets a lane of its own in Cut, and is mixed\n" +
		"into the render like a separate recording. Untick one to leave it\n" +
		"out of the session entirely.")
	why.SetXAlign(0)
	why.SetMarginTop(4)
	why.AddCSSClass("dim-label")
	box.Append(why)
	pop := gtk.NewPopover()
	pop.SetChild(box)

	b := gtk.NewMenuButton()
	b.AddCSSClass("flat")
	b.SetPopover(pop)
	inner := gtk.NewBox(gtk.OrientationHorizontal, 2)
	inner.Append(gtk.NewImageFromIconName("media-playlist-consecutive-symbolic"))
	inner.Append(gtk.NewLabel(fmt.Sprintf("%d/%d", len(on), len(tr))))
	b.SetChild(inner)
	if len(on) < len(tr) {
		b.AddCSSClass("dim-label") // some of this file is not in the session
	}
	b.SetTooltipText(fmt.Sprintf("Audio tracks — this file holds %d, and %d of them are in\n"+
		"the session. Each one is a lane of its own in Cut and is mixed like a\n"+
		"separate recording.", len(tr), len(on)))
	return b
}
