package main

// ---- a lane the cut put there -----------------------------------------------
//
// Every other row of the picture band is a recording. Prepare was pointed
// at a file, the file was transcribed and described and given a waveform, and
// the row is that work showing. This is the other kind: a shot copied from
// somewhere else in the session, or a video that was never a source at all, put
// on a row of its own so the green can cut to it and back.
//
// Cut-only, and that is the whole of the difference. It lives in cut.json and
// nowhere else: nothing transcribes it and nothing describes it. Prepare
// decides what the SOURCES are and this decides what the CUT has to work with,
// and those are not the same list -- reload builds the sources and then lays
// these over the top of them.
//
// It does get a waveform lane, because footage is picture and the sound filmed
// with it in one piece and a row of pictures with no sound under it is a row
// half of whose content the page will not show. Its sound is the same window
// its pictures are: the file from second Off, for Dur seconds (laneAudios).
//
// A row is a window on a file: which file, the second of it the row starts at,
// where that second lands in the session, and how long it runs. A recording is
// that same thing with the window wide open -- off nought, dur the whole file --
// which is why one tlVideo says both, and why every lookup from a session second
// to a file second goes through tlVideo.at rather than subtracting start by hand.
//
// The row it lands on is pinned, like a dragged one (cut_shift.go). It has to
// be: cutSeg.Cam is a row NUMBER, and a new row that pushed a real camera down
// one would silently repoint every scene that named it.

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

type cutLane struct {
	Name string  `json:"name"`          // the row's name, and its key everywhere else
	Src  string  `json:"src"`           // the file it shows, project-relative
	At   float64 `json:"at"`            // the session second the row starts at
	Off  float64 `json:"off,omitempty"` // the second of the file that lands there
	Dur  float64 `json:"dur"`           // and how long it runs
}

// laneVideos turns the saved lanes into rows of the picture band.
//
// The thumbnails come from the source's own frame folder when the file IS a
// source: a copied shot is one recording seen twice, and its frames were
// extracted the first time round. A file from outside the session has none, and
// its row draws its name and no pictures -- extracting frames for it would be
// exactly the Prepare pass this lane exists to do without.
func (a *App) laneVideos(lanes []cutLane, vids []tlVideo) []tlVideo {
	// the wall clock of session second nought, for a lane whose file has no
	// stamp of its own to read. Only the output clips are named off this.
	//
	// Read off the recording that STARTS earliest rather than the first in the
	// list: a corrected clock makes the two different answers, and the render
	// hands its sources over in the order they were chosen while the page holds
	// them in timeline order. Same lanes, same seconds, two names for the clip.
	zeroWall, first := 0.0, math.Inf(1)
	for _, v := range vids {
		if v.start < first {
			zeroWall, first = v.wall-v.start, v.start
		}
	}
	var out []tlVideo
	for _, l := range lanes {
		if l.Dur < minSegLn || l.Name == "" || l.Src == "" {
			// a row of no seconds is a row nobody can see or press, and one
			// with no file behind it would resolve to the project folder
			continue
		}
		path := a.fromRoot(l.Src)
		v := tlVideo{base: l.Name, path: path, start: l.At, off: l.Off, dur: l.Dur,
			wall: zeroWall + l.At - l.Off, fps: 30}
		if src := videoByPath(vids, path); src != nil {
			v.wall, v.fps, v.w, v.h = src.wall, src.fps, src.w, src.h
			v.interval, v.frames = src.interval, src.frames
		} else {
			// what the file itself says it is, when it says anything. wall is
			// the wall clock of the file's own second NOUGHT (produce names the
			// output clips off it), which is what a stamp in the name is -- so
			// off does not come into it, and a file naming no moment keeps the
			// place the row was put at instead.
			if st, ok := nameStamp(filepath.Base(path)); ok {
				v.wall = st
			}
			v.fps = ffprobeFPS(path)
			v.w, v.h, _ = ffprobeSize(path)
		}
		out = append(out, v)
	}
	return out
}

// laneAudios is the sound of those rows: the file's own track, windowed to the
// row exactly as its pictures are. masterLanes says the same thing about a
// recording and is not reused, because a recording's window is the whole file
// and is read off tlVideo -- here the window is the LANE's, and a row put on
// the band twice at two different seconds of one file is two lanes with two
// different offs and one path.
//
// A file with no track in it gets none, for masterLanes' reason: a strip of
// ground and a decode that can only fail.
//
// How many channels comes off the source's own lane when the file IS a source,
// the way laneVideos takes its thumbnails from the source's frames: a copied
// shot is one recording seen twice and the probe was paid for the first time
// round. src is the lanes that are not cut lanes, which is exactly the list
// that would hold it.
func laneAudios(rows []tlVideo, src []tlAudio) []tlAudio {
	var out []tlAudio
	for _, v := range rows {
		ch, known := 0, false
		for _, au := range src {
			// the MASTER lane of that file, because that is the sound a copied
			// shot carries: the file's first track, the one the render takes
			// off the footage input. A further track of a multi-track capture is
			// on the same path and is not it (cut_tracks.go).
			if au.path == v.path && au.master {
				ch, known = au.chans, true
				break
			}
		}
		// and a source whose first track this session was told to leave out is
		// a source with nothing to copy: only a file that is not in the session
		// at all -- an insert from outside it -- is worth a probe of its own
		if !known {
			if inSession(src, v.path) {
				continue
			}
			ch = ffprobeChannels(v.path)
		}
		if ch < 1 {
			continue
		}
		out = append(out, tlAudio{base: v.base, path: v.path, start: v.start,
			off: v.off, dur: v.dur, chans: ch, master: true})
	}
	return out
}

// inSession says whether a file is one the session's own lanes were built from,
// which is how "this source has no first track" is told apart from "this file
// was never a source": the first has an answer already and the second has none.
func inSession(src []tlAudio, path string) bool {
	for _, au := range src {
		if au.path == path {
			return true
		}
	}
	return false
}

// videoByPath is the recording a lane is a second look at, or nil when the file
// came from outside the session.
func videoByPath(vids []tlVideo, path string) *tlVideo {
	for i := range vids {
		if vids[i].path == path {
			return &vids[i]
		}
	}
	return nil
}

// laneName is what to call a new row: the file's name, and the file's name with
// a number after it when the session already has one. Names are the key the
// pinned rows and the hand-made corrections are held under, so two rows sharing
// one would move together and be drawn on top of each other.
func cutLaneName(vids []tlVideo, want string) string {
	taken := func(s string) bool {
		for _, v := range vids {
			if v.base == s {
				return true
			}
		}
		return false
	}
	if !taken(want) {
		return want
	}
	for n := 2; ; n++ {
		try := fmt.Sprintf("%s-%d", want, n)
		if !taken(try) {
			return try
		}
	}
}

// addLane puts a stretch of a file on a row of its own, and returns what the row
// is called. src is an absolute path; off is the second of it the row starts at,
// at is where that second lands in the session, and dur is how long it runs.
//
// The rows are frozen first, for the reason setShift freezes them: a row number
// is what a scene carries, and inserting a row is exactly the edit that would
// change what those numbers point at. Frozen, the new one is pinned to the
// bottom of the stack and every camera keeps the row it had.
func (ed *cutEditor) addLane(src string, off, at, dur float64) string {
	off, at = math.Max(0, off), math.Max(0, at)
	// a window can only be as long as there is file left past off. Zero dur
	// means "as much as there is", which is what choosing a file rather than
	// copying a stretch asks for; anything longer than that is a row drawing
	// band the green can be laid on over footage that stops partway.
	if d := ed.srcDur(src); d > off {
		if dur < minSegLn {
			dur = d - off
		}
		dur = math.Min(dur, d-off)
	}
	if dur < minSegLn {
		return ""
	}
	ed.pushUndo()
	ed.freezeRows()
	name := cutLaneName(ed.vids, baseName(src))
	ed.cutLanes = append(ed.cutLanes, cutLane{Name: name, Src: ed.a.relToRoot(src),
		At: at, Off: off, Dur: dur})
	// its own row, under everything already there -- read off the rows in use
	// rather than off laneN, which is floored at one and would leave an empty
	// row above the first lane on a project with no footage of its own yet
	row := 0
	for _, v := range ed.vids {
		row = max(row, v.lane+1)
	}
	ed.rows[name] = row
	ed.setLanes(ed.cutLanes)
	ed.relayout()
	ed.persist()
	return name
}

// srcDur is how long the file behind a lane runs, or zero when nobody knows.
// A recording on the band already answers it -- the row IS that file, whole --
// and shelling out for a number the page is drawing would be an ffprobe per
// press for nothing. Cut lanes are skipped: one of those is a WINDOW on a file
// and its dur is the window, which would clamp the next lane to the last one.
func (ed *cutEditor) srcDur(path string) float64 {
	for i := range ed.vids {
		if v := &ed.vids[i]; v.path == path && v.dur > 0 && !ed.isCutLane(v.base) {
			return v.dur
		}
	}
	if d, err := ffprobeDur(path); err == nil {
		return d
	}
	return 0
}

// killLane takes a row away again, with whatever the cut had put on it. The
// scenes go with it: they are frames of a file that is no longer on the page,
// and a scene left pointing at a row that is not there falls back to whatever
// was rolling at that second (pickVideoOn) -- which is a different picture
// arriving in the render for a reason nobody could see on this page.
//
// The rows below it come up one, and every scene on them comes with them.
func (ed *cutEditor) killLane(name string) {
	i := cutLaneIdx(ed.cutLanes, name)
	if i < 0 {
		return
	}
	// the row it is actually DRAWN on, which is what every scene's Cam is
	// measured against. The pin says the same thing whenever there is one, but
	// only the band is sure -- and a lane in cut.json that never made it onto
	// the band has no scenes to take with it and no rows to bring up, so
	// reading a missing pin as row nought would take the first camera's cut.
	row := -1
	for i := range ed.vids {
		if ed.vids[i].base == name {
			row = ed.vids[i].lane
			break
		}
	}
	ed.pushUndo()
	ed.cutLanes = append(ed.cutLanes[:i], ed.cutLanes[i+1:]...)
	delete(ed.rows, name)
	delete(ed.shift, name)
	var vids []tlVideo
	for _, v := range ed.vids {
		if v.base != name {
			vids = append(vids, v)
		}
	}
	ed.vids = vids
	// and its sound, which came with its pictures (laneAudios) and has no more
	// reason than they had to stay on a band the row has left. Taken off here
	// rather than by rebuilding through setLanes, because the rows below have
	// to come up one first and that is this function's own work.
	var auds []tlAudio
	for _, au := range ed.auds {
		if au.base != name {
			auds = append(auds, au)
		}
	}
	ed.auds = auds
	ed.fitAudio()
	ed.fitScope()
	if row < 0 {
		ed.relayout()
		ed.persist()
		return
	}
	ed.closeRow(row)
	ed.relayout()
	ed.persist()
	ed.a.setStatus(fmt.Sprintf("removed the %s lane and everything the cut took from it "+
		"— ↶ Undo takes it back", name))
	ed.redrawTracks()
}

func cutLaneIdx(lanes []cutLane, name string) int {
	for i, l := range lanes {
		if l.Name == name {
			return i
		}
	}
	return -1
}

// isCutLane says this row was put there by the cut rather than by Prepare.
func (ed *cutEditor) isCutLane(base string) bool { return cutLaneIdx(ed.cutLanes, base) >= 0 }

// ---- the ✕ that takes one away ----------------------------------------------
//
// A recording cannot be removed here -- it is a source, and the place to stop
// using a source is the page that chose it. A cut lane has no such page: it was
// made by a press on this one and it has to be unmakeable by a press on this
// one, or a lane put on by accident is a lane that can only be got rid of by
// editing cut.json by hand.
//
// So the same badge every remove on this page wears (drawKillBadge), at the
// row's own left edge beside its name. It is the only ✕ drawn on the pictures
// now -- the one that dropped a scene has moved to the green bar in the
// selection row -- so the press it has to be told apart from is the clip
// border it can sit on, and that one is asked after it (cut.go).

// laneKillCentre is where the ✕ for a cut lane sits: timeline x, area y.
func (ed *cutEditor) laneKillCentre(v *tlVideo) (float64, float64) {
	return v.pxOrigin + segKillIn, ed.laneTop(v.lane) + segKillTop
}

// laneKillAt is the cut lane whose ✕ is under a press, or "".
func (ed *cutEditor) laneKillAt(px, y float64) string {
	for i := range ed.vids {
		v := &ed.vids[i]
		if !ed.isCutLane(v.base) {
			continue
		}
		cx, cy := ed.laneKillCentre(v)
		if math.Abs(px-cx) <= segKillHit && math.Abs(y-cy) <= segKillHit {
			return v.base
		}
	}
	return ""
}

// hoverLaneKill lights the badge under the pointer. x below zero means the
// pointer has left the band.
func (ed *cutEditor) hoverLaneKill(x, y float64) {
	name, row := "", -1
	if x >= 0 && ed.hitPics(y) {
		name = ed.laneKillAt(x+ed.viewX, y)
		row = ed.rowKillAt(x+ed.viewX, y)
	}
	if name != ed.laneHov || row != ed.rowHov {
		ed.laneHov, ed.rowHov = name, row
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
		}
	}
}

// drawLaneKill paints them, in drawTrack's own translation so x is timeline px.
func (ed *cutEditor) drawLaneKill(cr *cairo.Context, vx0, vx1 float64) {
	for i := range ed.vids {
		v := &ed.vids[i]
		if !ed.isCutLane(v.base) {
			continue
		}
		cx, cy := ed.laneKillCentre(v)
		if cx < vx0-segKillHit || cx > vx1+segKillHit {
			continue
		}
		drawKillBadge(cr, cx, cy, ed.laneHov == v.base)
	}
}

// closeRow brings the rows above `row` down one and renumbers everything that
// names a row: the pins, the scenes, the selection, a copy in hand, and the
// watch. The one piece of row-removal killLane and killRow share -- and it is
// shared rather than copied because a scene whose Cam came down while its
// footage stayed put would silently show the wrong camera, which is the exact
// lie the pins exist to prevent.
//
// Every source is pinned first. The decrement below is only TRUE of a source
// whose row is written down: an unpinned one is re-coloured greedily at the
// next relayout and could land in the closed gap, desynced from the very Cam
// numbers this function just adjusted to match it.
func (ed *cutEditor) closeRow(row int) {
	if ed.rows == nil {
		ed.rows = map[string]int{}
	}
	for _, v := range ed.vids {
		if _, ok := ed.rows[v.base]; !ok {
			ed.rows[v.base] = v.lane
		}
	}
	for b, r := range ed.rows {
		if r > row {
			ed.rows[b] = r - 1
		}
	}
	var segs []cutSeg
	for _, s := range ed.segs {
		switch {
		case s.Cam == row:
			continue // it showed that row, and that row has gone
		case s.Cam > row:
			s.Cam--
		}
		segs = append(segs, s)
	}
	// the live row numbers that are not written in the cut. A selection, a copy
	// in hand and the watch each name a ROW, and a row number that quietly came
	// to mean the camera below is the same silent repointing the pins exist to
	// prevent -- except here it would put the green, the next paste, or the
	// preview on footage nobody chose. What named the row that has gone lets go
	// instead.
	switch {
	case ed.sel.lane == row:
		ed.sel.active = false
	case ed.sel.lane > row:
		ed.sel.lane--
	}
	switch {
	case !ed.copyOn || ed.copyAud != "": // a copied SOUND names a lane, not a row
	case ed.copyCam == row:
		ed.copyOn = false
	case ed.copyCam > row:
		ed.copyCam--
	}
	switch {
	case ed.monRow-1 == row: // monRow is 1-based, 0 for off
		ed.monRow = 0
	case ed.monRow-1 > row:
		ed.monRow--
	}
	if ed.nRows > 0 {
		ed.nRows-- // one row fewer is one row less of floor to hold
	}
	ed.laneHov, ed.rowHov = "", -1 // the badge under the pointer went with it
	ed.segs = segs
	ed.dropSeg()
	ed.dropEdge()
	ed.syncSelBtns()
	ed.syncInsertBtn() // ⇲ Lane goes off the bar with the copy it would have used
}

// ---- the ✕ on a row with nothing on it ---------------------------------------
//
// A right-drag that moves a part onto another row leaves the old row standing
// empty on purpose (moveRow): closing it there would renumber every scene's
// camera as a side effect of a drag that meant something else. But an empty
// row is still a row of space on the band, and the only way to be rid of one
// was to know that truth about moveRow. So it wears the same ✕ a cut lane
// does, at its left edge -- there is no footage to put it beside, so it sits at
// the edge of the VIEW and rides the scroll, where a lane's sits at the lane's
// own start.

// rowEmpty says nothing is drawn on row r.
func (ed *cutEditor) rowEmpty(r int) bool {
	for i := range ed.vids {
		if ed.vids[i].lane == r {
			return false
		}
	}
	return true
}

// rowKillAt is the empty row whose ✕ is under a press, or -1. Timeline px, so
// the centre is the view's left edge plus the badge's inset. Not offered on the
// last row standing: with no footage at all the one row IS the band, and an ✕
// there would remove nothing.
func (ed *cutEditor) rowKillAt(px, y float64) int {
	if ed.laneN <= 1 {
		return -1
	}
	for r := 0; r < ed.laneN; r++ {
		if !ed.rowEmpty(r) {
			continue
		}
		cx, cy := ed.viewX+segKillIn, ed.laneTop(r)+segKillTop
		if math.Abs(px-cx) <= segKillHit && math.Abs(y-cy) <= segKillHit {
			return r
		}
	}
	return -1
}

// drawRowKill paints them, in drawTrack's own translation like drawLaneKill.
// vx is the VIEW's left edge in timeline px, which is what these ride: the
// badge stays at the left of the widget while the tape scrolls under it, so it
// is the same number rowKillAt tests a press against.
func (ed *cutEditor) drawRowKill(cr *cairo.Context, vx float64) {
	if ed.laneN <= 1 {
		return
	}
	for r := 0; r < ed.laneN; r++ {
		if !ed.rowEmpty(r) {
			continue
		}
		cx, cy := vx+segKillIn, ed.laneTop(r)+segKillTop
		drawKillBadge(cr, cx, cy, ed.rowHov == r)
	}
}

// killRow takes an empty row away: the rows below come up one and every scene
// keeps the camera it was showing. Only an EMPTY row -- a row with footage has
// its own ways off the band (a cut lane's ✕, Prepare) and each of those
// says what happens to the footage, which this deliberately has no answer for.
func (ed *cutEditor) killRow(row int) {
	if row < 0 || row >= ed.laneN || ed.laneN <= 1 || !ed.rowEmpty(row) {
		return
	}
	ed.pushUndo()
	ed.closeRow(row)
	ed.relayout()
	ed.persist()
	ed.a.setStatus(fmt.Sprintf("removed the empty row %d — the rows below it came up one; "+
		"↶ Undo takes it back", row+1))
	ed.redrawTracks()
}

// setLanes puts the cut's own rows on the band, replacing whatever was there.
// Rebuilt rather than patched: a load is putting them there for the first time,
// and an undo may be putting one back, taking one away, or doing both at once.
//
// They land where the corrections say, not where cut.json says: a lane can be
// right-dragged like any other row (cut_shift.go), and its correction is held in
// the same map under the same key as a recording's.
func (ed *cutEditor) setLanes(lanes []cutLane) {
	// what is on the band because Prepare put it there, picture and
	// sound. Asked before ed.cutLanes is replaced, so a lane being TAKEN away
	// is still known to be one: read after, its name is in neither list and it
	// would survive as a row of a file nothing on the page mentions.
	var vids []tlVideo
	for _, v := range ed.vids {
		if !ed.isCutLane(v.base) {
			vids = append(vids, v)
		}
	}
	var auds []tlAudio
	for _, au := range ed.auds {
		if !ed.isCutLane(au.base) {
			auds = append(auds, au)
		}
	}
	ed.cutLanes = append([]cutLane(nil), lanes...)
	rows := ed.a.laneVideos(ed.cutLanes, vids)
	for i := range rows {
		rows[i].start += ed.shift[rows[i].base]
	}
	ed.vids = append(vids, rows...)
	ed.sortVids()
	// and their sound with them, from the rows as they now stand so a lane
	// dragged in time takes its waveform along
	ed.auds = append(auds, laneAudios(rows, auds)...)
	sortLanes(ed.auds)
	ed.loadWaves() // a row added now would otherwise draw ground until a reload
	ed.fitAudio()  // one more lane to make room for, or one fewer
	ed.fitScope()  // and a selection that was on a lane just taken away
}

// sortVids puts the picture band back in timeline order. Stable, so two rows
// starting on the same second keep the order the list already had rather than
// swapping places on every redraw.
func (ed *cutEditor) sortVids() {
	sort.SliceStable(ed.vids, func(i, j int) bool { return ed.vids[i].start < ed.vids[j].start })
}

// sameLanes compares two lists of the cut's own rows, in order: reordering them
// changes which row each is drawn on, which is an edit like any other.
func sameCutLanes(a, b []cutLane) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
