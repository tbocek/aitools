package main

// The session's sources: one list, and what each file is FOR.
//
// There were two lists, one per folder -- footage on the left, voice
// recordings on the right -- and a file could only be whatever its folder said
// it was. That is wrong for the sessions this is built for: a screen recording
// carries the game AND everyone talking over it, so the same file is both the
// footage and a voice. Two lists could not say that, and pointing them at the
// same file said it twice: two transcripts of one recording, two copies of
// every line in the session timeline.
//
// So: one list, and two roles a row can carry.
//
//	footage   -- frames come out of it; it is a video to describe, cut and render
//	narrator  -- slot 1..4: whose voice this recording is
//
// The roles are independent, and a row can carry neither. A row also carries
// one wish -- split the voice off -- which is not a role but a job for ▶, and
// which turns one row into two: the recording without its voice, and the voice. Everything in the
// list is transcribed either way -- that is the point of having the other
// players' chatter in here: it belongs in the timeline without belonging to
// anyone we narrate as. What a narrator slot adds is identity: narrator 1 is
// the voice the narration is spoken in, and 2..4 are the rest of the group,
// each cloneable in Narrate.
//
// Membership is the list itself, not a folder scan and a checkbox. A file is
// in because it was added and stays in until it is thrown out, which is what
// the trash button on each row does -- to the list, never to the file.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// narratorSlots is how many people a session can name. Four, because that is
// what fits on a row as a digit and what a group recording tends to hold; the
// rest of the voices stay untagged and are still transcribed.
const narratorSlots = 4

// audioExt and videoExt decide two different things: what may be added at all,
// and what a new row defaults to. A .mkv arrives as footage because that is
// what a video is for here -- the toggle is there for the case where it is
// only wanted for the sound.
var (
	audioExt = map[string]bool{
		".flac": true, ".wav": true, ".mp3": true, ".m4a": true, ".aac": true,
		".ogg": true, ".opus": true, ".wma": true,
	}
	videoExt = map[string]bool{
		".mp4": true, ".mkv": true, ".mov": true, ".webm": true, ".avi": true, ".ts": true,
	}
	mediaExt = map[string]bool{}
)

func init() {
	for e := range audioExt {
		mediaExt[e] = true
	}
	for e := range videoExt {
		mediaExt[e] = true
	}
}

func isMedia(name string) bool { return mediaExt[strings.ToLower(filepath.Ext(name))] }
func isVideo(name string) bool { return videoExt[strings.ToLower(filepath.Ext(name))] }

func listMedia(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if !e.IsDir() && isMedia(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// sourceItem is one file and what it is for. The path is absolute: sources come
// from wherever the capture put them now, so there is no folder left for a name
// to be relative to.
type sourceItem struct {
	path     string
	footage  bool
	narrator int // 0 = untagged, 1..narratorSlots
	// sepVoice asks for the voice to be split off this recording. It is a
	// WISH, not a state: nothing has happened until Prepare runs, and once it
	// has, the wish is spent -- the row then points at the voiceless file and
	// the voice is a row of its own, so there is nothing left to ask for.
	sepVoice bool
	// which of the file's audio tracks the session uses, as a:N indices. Empty
	// is the first alone, which is what an ordinary file means and what every
	// project written before the choice existed means (wantTracks). Only a file
	// with more than one track can be given a different answer, and only from
	// the menu on its row -- see trackButton (cut_tracks.go).
	tracks []int
}

func (it sourceItem) name() string { return filepath.Base(it.path) }

// sameSource is what == used to be, before a row could name a set of audio
// tracks and stopped being a comparable struct.
//
// Every field is spelled out by hand because Go will not compare a struct
// holding a slice. That is a standing hazard -- a field added later and
// forgotten here would be a change the project quietly does not notice it has
// to save -- which is why TestEverySourceFieldCountsAsAChange walks the type by
// reflection and fails on any field this does not read.
//
// The tracks compare in ORDER, unlike a scene's silenced lanes: they are the
// stored form of a sorted, deduplicated answer (wantTracks), so two orders here
// is a row that was written twice and only one of them the way this saves it.
func sameSource(a, b sourceItem) bool {
	if a.path != b.path || a.footage != b.footage || a.narrator != b.narrator ||
		a.sepVoice != b.sepVoice || len(a.tracks) != len(b.tracks) {
		return false
	}
	for i := range a.tracks {
		if a.tracks[i] != b.tracks[i] {
			return false
		}
	}
	return true
}

// sourceList is the session's sources. The order is arrival order and nothing
// hangs on it: sources are placed on the wall clock by the timestamp in their
// name (sourceStart), which is why a row whose name has no stamp gets a
// warning rather than a pair of reorder arrows. The whole list is rebuilt from
// the slice on every change -- at a handful of files that is simpler and safer
// than in-place row surgery.
type sourceList struct {
	items    []sourceItem
	box      *gtk.ListBox
	onChange func()
	// how many audio tracks each file holds, so the rows can be rebuilt without
	// an ffprobe apiece every time one of them is touched (srcTracks)
	probed map[string][]audTrack
}

func newSourceList(onChange func()) *sourceList {
	s := &sourceList{onChange: onChange}
	s.box = gtk.NewListBox()
	s.box.SetSelectionMode(gtk.SelectionNone)
	s.box.AddCSSClass("boxed-list")
	return s
}

// add appends the files it does not already hold and returns how many that was.
// Adding the same file twice is a no-op rather than an error: the two ways in
// -- a folder and a file chooser -- overlap, and the pipeline transcribes one
// row once.
func (s *sourceList) add(paths ...string) int {
	have := map[string]bool{}
	for _, it := range s.items {
		have[it.path] = true
	}
	n := 0
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if have[p] || !isMedia(p) {
			continue
		}
		have[p] = true
		s.items = append(s.items, sourceItem{path: p, footage: isVideo(p)})
		n++
	}
	if n > 0 {
		s.autoTag()
		s.changed()
	}
	return n
}

// addDir adds every media file in a folder, by name. This is how a session
// arrives: a card is dumped somewhere and all of it is the session.
func (s *sourceList) addDir(dir string) int {
	var paths []string
	for _, n := range listMedia(dir) {
		paths = append(paths, filepath.Join(dir, n))
	}
	return s.add(paths...)
}

// autoTag gives narrator 1 away when nobody holds it. Somebody has to be the
// narrator -- it is the voice Narrate speaks in -- and a first session should
// not have to be told that the one recording in it is the one to clone.
// Recordings come before footage: a screen capture holds everyone at once, so
// a dedicated mic track is the better guess at "you".
func (s *sourceList) autoTag() {
	for _, it := range s.items {
		if it.narrator == 1 {
			return
		}
	}
	// only an untagged row: a row that already holds a slot is somebody the user
	// named, and moving them to slot 1 would both re-cast the narration and
	// silently free the slot they were in
	for _, want := range []bool{false, true} { // recordings first, then footage
		for i := range s.items {
			if s.items[i].footage == want && s.items[i].narrator == 0 {
				s.items[i].narrator = 1
				return
			}
		}
	}
}

// remove throws a row out of the session. The file is not touched: this list is
// what a project is made of, and a project is not a folder.
func (s *sourceList) remove(i int) {
	if i < 0 || i >= len(s.items) {
		return
	}
	s.items = append(s.items[:i], s.items[i+1:]...)
	s.autoTag() // the narrator may have just left
	s.changed()
}

// setFootage turns the frames on or off for one row. Only a video can be on:
// footage means frames come out of it, and an audio file has none to give --
// the toggle exists for the one direction that is a choice, a video kept only
// for its sound.
func (s *sourceList) setFootage(i int, on bool) {
	if i < 0 || i >= len(s.items) || s.items[i].footage == on {
		return
	}
	if on && !isVideo(s.items[i].path) {
		return
	}
	s.items[i].footage = on
	s.changed()
}

// setSepVoice flags a row for voice separation, or unflags it. Only the wish
// is stored: the models are the server's and the work is minutes long, so the
// button cannot do it -- ▶ does, and until then this is reversible with a
// second click, which is the whole reason it is a flag and not an action.
func (s *sourceList) setSepVoice(i int, on bool) {
	if i < 0 || i >= len(s.items) || s.items[i].sepVoice == on {
		return
	}
	s.items[i].sepVoice = on
	s.changed()
}

// sepVoiceWanted is the list as the separator reads it: every path still
// waiting for its voice to be lifted off.
func (s *sourceList) sepVoiceWanted() []string {
	var out []string
	for _, it := range s.items {
		if it.sepVoice {
			out = append(out, it.path)
		}
	}
	return out
}

// cycleNarrator is what the microphone button does: step to the next free slot,
// and off the end back to untagged. Slots are exclusive -- a slot is a person,
// and two rows claiming to be narrator 1 would leave Narrate picking one of them
// silently -- so the ones other rows hold are stepped over rather than stolen.
func (s *sourceList) cycleNarrator(i int) {
	if i < 0 || i >= len(s.items) {
		return
	}
	next := 0
	for n := s.items[i].narrator + 1; n <= narratorSlots; n++ {
		if s.slotHolder(n) < 0 {
			next = n
			break
		}
	}
	s.items[i].narrator = next
	s.changed()
}

// slotHolder is the row holding slot n, or -1.
func (s *sourceList) slotHolder(n int) int {
	for i, it := range s.items {
		if it.narrator == n {
			return i
		}
	}
	return -1
}

// narratorPath is the recording tagged with slot n, or "".
func (s *sourceList) narratorPath(n int) string {
	if i := s.slotHolder(n); i >= 0 {
		return s.items[i].path
	}
	return ""
}

// split is the list as the pipeline reads it: the footage, then everything
// else, each in list order. Every source appears in exactly one of the two, so
// appending them is the whole session and nothing is transcribed twice.
func (s *sourceList) split() (footage, rest []string) {
	for _, it := range s.items {
		if it.footage {
			footage = append(footage, it.path)
		} else {
			rest = append(rest, it.path)
		}
	}
	return
}

// clash names two sources that would be written to the same place. Every step
// keys a source's output folder on its file name without the extension, so
// clip.mkv and clip.flac -- a camera and its separate sound take, which is a
// normal way to record -- are both inputs/clip, and the second run would find
// the first's words.json and skip itself. The list can hold them (they are
// different files, and dedupe is by path), so the run has to refuse: one clear
// message beats a transcript that is quietly the wrong file's. "" when the
// session is clean.
func (s *sourceList) clash() (a, b string) {
	seen := map[string]string{}
	for _, it := range s.items {
		k := baseName(it.path)
		if p, ok := seen[k]; ok {
			return p, it.path
		}
		seen[k] = it.path
	}
	return "", ""
}

func (s *sourceList) paths() []string {
	out := make([]string, len(s.items))
	for i, it := range s.items {
		out[i] = it.path
	}
	return out
}

// prune drops rows whose file is gone and names them, for the log. A source
// that quietly stopped being in the list is how a render comes out missing a
// camera angle nobody can account for.
func (s *sourceList) prune() []string {
	var gone []string
	var kept []sourceItem
	for _, it := range s.items {
		if exists(it.path) {
			kept = append(kept, it)
			continue
		}
		gone = append(gone, it.path)
	}
	if len(gone) == 0 {
		return nil
	}
	s.items = kept
	s.autoTag()
	s.changed()
	return gone
}

// load replaces the list wholesale -- a project being opened. Roles come from
// the project as stored: a list with nobody in slot 1 is one the user untagged,
// and it must come back untagged. autoTag runs only when a broken tag had to
// be stripped -- filling the one kind of gap load itself leaves.
func (s *sourceList) load(items []sourceItem) {
	s.items = nil
	stripped := false
	for _, it := range items {
		// a hand-edited project must not seat two people in one slot,
		// or promise frames out of a file that has none
		if it.narrator != 0 && (it.narrator < 1 || it.narrator > narratorSlots || s.slotHolder(it.narrator) >= 0) {
			it.narrator = 0
			stripped = true
		}
		it.footage = it.footage && isVideo(it.path)
		s.items = append(s.items, it)
	}
	if stripped {
		s.autoTag()
	}
	s.changed()
}

func (s *sourceList) changed() {
	// before anything reads the list: placing a file is the first thing every
	// step does with one, and it has to place it where this list says
	s.render()
	if s.onChange != nil {
		s.onChange()
	}
}

// ---- the rows ---------------------------------------------------------------

func (s *sourceList) render() {
	if s.box == nil {
		return // headless, in tests
	}
	for {
		row := s.box.RowAtIndex(0)
		if row == nil {
			break
		}
		s.box.Remove(row)
	}
	for i := range s.items {
		s.box.Append(s.row(i))
	}
}

func (s *sourceList) row(i int) *gtk.Box {
	it := s.items[i]

	// footage: what frames are taken from, and what the cut and the render are
	// made of. On by default for a video file, off for a recording, and a
	// toggle either way -- a capture wanted only for its sound is a video with
	// this off, which is the case that used to need a second copy of the file
	// in the other folder.
	foot := gtk.NewToggleButton()
	foot.SetIconName("camera-video-symbolic")
	foot.AddCSSClass("flat")
	foot.SetActive(it.footage)
	// only a video can be footage, so on an audio file the toggle is dead
	// rather than a button that silently never sticks
	foot.SetSensitive(isVideo(it.path))
	foot.SetTooltipText("Footage — frames come out of this file and it can be cut.\n" +
		"Off: it is only listened to, which is what a video kept for its audio wants.")
	foot.ConnectToggled(func() { s.setFootage(i, foot.Active()) })

	// the narrator slot: a microphone, and the slot number once it holds one --
	// which of the four voices this recording is. Untagged is the mic alone,
	// dimmed; a placeholder dash was just one more thing to decode. Click cycles.
	narr := gtk.NewButton()
	narr.AddCSSClass("flat")
	nb := gtk.NewBox(gtk.OrientationHorizontal, 2)
	nb.Append(gtk.NewImageFromIconName("audio-input-microphone-symbolic"))
	if it.narrator > 0 {
		nb.Append(gtk.NewLabel(strconv.Itoa(it.narrator)))
	} else {
		nb.AddCSSClass("dim-label")
	}
	narr.SetChild(nb)
	if it.narrator == 1 {
		narr.AddCSSClass("suggested-action") // the voice the narration is spoken in
	}
	narr.SetTooltipText("Narrator — click to cycle through the free slots and back to none.\n" +
		"1 is the voice the narration is spoken in; 2–4 are the rest of the group.")
	narr.ConnectClicked(func() { s.cycleNarrator(i) })

	// an ellipsizing label: a recorder filename is 50 characters, and a row
	// that insists on all of them sets a floor under the whole window
	lbl := gtk.NewLabel(it.name())
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	lbl.SetEllipsize(pango.EllipsizeMiddle)
	lbl.SetTooltipText(it.path) // sources come from anywhere; the name alone can repeat

	// split the voice off: the recording becomes two rows -- itself without the
	// voice, and the voice on its own. A flag, because the separation is a
	// model on the server and minutes of it, so the press that starts work is
	// ▶ like every other minute this page spends. Toggled on it is loud, so a
	// row waiting for it is visible from across the list.
	sep := gtk.NewToggleButton()
	sep.SetIconName("edit-cut-symbolic")
	sep.AddCSSClass("flat")
	sep.SetActive(it.sepVoice)
	sep.SetTooltipText("Split the voice off — on ▶ this recording is separated into the\n" +
		"voice and everything else. This row keeps everything else; the voice\n" +
		"is added as a track of its own, so it can be cut and mixed apart.")
	sep.ConnectToggled(func() { s.setSepVoice(i, sep.Active()) })
	// a row that IS a half of a split offers no scissors: there is no voice
	// left to take off a voice, and the name says which rows those are
	sep.SetVisible(!splitProduct(it.path))

	del := gtk.NewButtonFromIconName("user-trash-symbolic")
	del.AddCSSClass("flat")
	del.SetTooltipText("Remove from this session — the file itself is left alone")
	del.ConnectClicked(func() { s.remove(i) })

	row := gtk.NewBox(gtk.OrientationHorizontal, 4)
	row.SetMarginStart(6)
	row.SetMarginEnd(2)
	row.Append(foot)
	row.Append(narr)
	row.Append(lbl)
	// only for a file that holds more than one audio track: an ordinary
	// recording has nothing to choose and gets no button (trackButton)
	if tb := s.trackButton(i); tb != nil {
		row.Append(tb)
	}
	// sources align on the wall clock, and the clock comes out of the name --
	// a file without one is stacked at the session's start and has to be lined
	// up by hand. That deserves a flag on the row, not a failed run.
	if _, ok := nameStamp(it.name()); !ok {
		warn := gtk.NewImageFromIconName("dialog-warning-symbolic")
		warn.AddCSSClass("stamp-warn")
		warn.SetTooltipText("No timestamp in the file name — this file starts where the\n" +
			"session does, which is only right if it was rolling from the start.\n" +
			"Rename it with when the recording STARTED, like\n" +
			"clip_2026-08-08_19-55-15.mkv — most recorders already do — or drag it\n" +
			"into place with the right mouse button on the Cut page.")
		row.Append(warn)
	}
	row.Append(sep)
	row.Append(del)
	return row
}
