package main

// The audio lanes under the cut: one pair for the footage's own sound, and one
// per separate recording.
//
// The video track is the master and stays the master: the timeline is the
// footage's, x is still the footage's x, and a lane below is a slave drawing of
// something that happened at the same time. This matters because the audio was
// recorded by a different machine -- a headset recorder, OBS's second track, a
// phone on the table -- which started when it started and knows nothing about
// when the capture card did. What lines them up is the wall clock (sourceStart:
// a timestamp in the name, else mtime minus length), the same zero every other
// part of this app places sources by, so a lane is drawn where the recording
// actually was rather than from the left edge.
//
// The consequence is that a lane is usually shorter than the timeline, and that
// is the point: only the part of the recording that overlaps the footage is
// drawn, over its own lighter ground, so the ends that hang off -- the minutes
// before you hit record on the capture card, the hour after you stopped -- are
// visibly not there instead of being stretched to fit.
//
// The footage's own track is drawn as a lane too, first, even though it is
// coming out of the speakers anyway. A lane on its own cannot be checked
// against anything -- it is a blue smear, and whether it is a second early is
// not a question a picture of one waveform can answer. Under the sound the
// footage itself carries, a laugh in both lanes lines up in the same column or
// it does not, and the answer is the page rather than a claim about it.
//
// Left and right are separate lanes. A stereo recording of a group is often not
// a stereo picture at all -- one player per side, or a mic on one channel and
// the game on the other -- and a merged waveform hides exactly that. Blue,
// because every other ink on this page already means something: green is kept,
// red is removed and the playhead, yellow is a file boundary, violet is an
// insert, white is the held edge.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	// The envelope's resolution. 10 ms is a hair under one pixel at the top
	// zoom (120 px/s), so the drawing never runs out of detail, and an hour of
	// stereo is 720 kB of it -- small enough to keep in memory and to cache on
	// disk without thinking about it.
	waveHz = 100.0
	// What the decode asks ffmpeg for. Peaks of a downsampled signal are a
	// little lower than the true ones, which does not matter for a picture, and
	// 8 kHz is a tenth of the bytes to push through the pipe.
	waveRate  = 8000
	waveLaneH = 30.0 // one channel's lane
	waveGap   = 4.0  // between one recording's lanes and the next's
	wavePad   = 3.0  // above and below the lanes as a whole
)

// tlAudio is a recording placed on the session timeline, exactly as tlVideo is,
// minus everything to do with pictures. It is NOT part of the timeline's
// geometry: relayout lays out the videos and the audio is drawn against that.
//
// The footage's own sound track is one of these too, master set. It is not a
// separate recording by any definition -- it is the master, drawn from the same
// file as the pictures above it -- but on this page it is a waveform against
// the timeline like the others, and leaving it out was the reason a lane could
// not be read: a blue smear with nothing to compare it to says nothing about
// whether it is where it should be. Side by side with the footage's own sound,
// the same shout is visibly in both, at the same x.
type tlAudio struct {
	base   string
	path   string
	start  float64 // session time of this recording's t=0
	dur    float64
	chans  int  // 1 or 2 lanes; more than two is downmixed to a stereo picture
	master bool // the footage's own track: heard by the preview already
}

// masterLanes is the footage's own sound as lanes. There is no placing to do --
// a video's track starts where the video starts, by definition -- and a video
// with no sound in it (a silent screen capture, a camera used only for its
// pictures) gets no lane rather than a strip of ground and a decode that can
// only fail.
func masterLanes(vids []tlVideo) []tlAudio {
	var out []tlAudio
	for _, v := range vids {
		ch := ffprobeChannels(v.path)
		if ch < 1 {
			continue
		}
		out = append(out, tlAudio{base: v.base, path: v.path, start: v.start,
			dur: v.dur, chans: ch, master: true})
	}
	return out
}

// sortLanes is the order the lanes are read in: the footage's own first
// whatever the clock says, because it is the thing every other lane is being
// compared to, then the rest by when they started. A master that sorted itself
// into the middle of the recordings would be one more lane to find first.
func sortLanes(auds []tlAudio) {
	sort.SliceStable(auds, func(i, j int) bool {
		if auds[i].master != auds[j].master {
			return auds[i].master
		}
		return auds[i].start < auds[j].start
	})
}

// waveform is the peak envelope: one byte per bucket per channel, the loudest
// sample in that bucket. Peak rather than RMS because what a cut is aimed at is
// where someone starts talking, and RMS rounds an onset off.
type waveform struct {
	hz    float64
	chans [][]uint8
}

// peak is the loudest thing between two file-local times, 0..1. A drawn column
// covers many buckets when zoomed out and a fraction of one when zoomed in, and
// both have to answer: the first by taking the loudest of them (a peak envelope
// that averaged would fade out as you zoomed out), the second by taking the one
// it lands in.
func (wf *waveform) peak(ch int, from, to float64) float64 {
	if wf == nil || ch >= len(wf.chans) {
		return 0
	}
	buf := wf.chans[ch]
	i := int(from * wf.hz)
	j := int(math.Ceil(to * wf.hz))
	if j <= i {
		j = i + 1
	}
	if i < 0 {
		i = 0
	}
	if j > len(buf) {
		j = len(buf)
	}
	top := uint8(0)
	for ; i < j; i++ {
		if buf[i] > top {
			top = buf[i]
		}
	}
	return float64(top) / 255
}

// ---- getting one ------------------------------------------------------------

// waveCache is where the envelopes live between runs. Not under step3/, which
// is the cut's own folder and whose file count is reported to the user as what
// this step produced: an envelope is not an output, it is a picture of an input
// that would otherwise be decoded again on every start.
func (a *App) waveCache() string { return filepath.Join(a.outDir, "cache", "waves") }

// waveMagic changes when the format does, so an old cache is simply not read
// rather than read as something it is not. It changed for AWV3 without the
// layout changing: a stereo file whose two sides are the same signal is one
// channel now, and a cache written before that would go on drawing the second
// lane forever, since it is keyed by the recording and the recording has not
// changed.
const waveMagic = "AWV3"

// loadWave is the cache in front of buildWave, keyed by the source's size and
// modification time as well as its name: a session re-recorded to the same
// filename is a different recording, and drawing the old one under it would be
// a picture that quietly lies.
func loadWave(dir, path string, chans int) (*waveform, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cf := filepath.Join(dir, baseName(path)+".wave")
	if wf, ok := readWave(cf, fi.Size(), fi.ModTime().Unix()); ok {
		return wf, nil
	}
	wf, err := buildWave(path, chans)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		writeWave(cf, wf, fi.Size(), fi.ModTime().Unix())
	}
	return wf, nil
}

// header: magic, channels, hz, buckets, and the source's size and mtime
type waveHead struct {
	Chans uint8
	Hz    uint16
	Count uint32
	Size  int64
	Mtime int64
}

func readWave(file string, size, mtime int64) (*waveform, bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, len(waveMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != waveMagic {
		return nil, false
	}
	var h waveHead
	if binary.Read(r, binary.LittleEndian, &h) != nil {
		return nil, false
	}
	if h.Size != size || h.Mtime != mtime || h.Chans == 0 || h.Chans > 2 || h.Hz == 0 {
		return nil, false // a different recording, or a format we do not read
	}
	wf := &waveform{hz: float64(h.Hz)}
	for c := 0; c < int(h.Chans); c++ {
		buf := make([]uint8, h.Count)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, false
		}
		wf.chans = append(wf.chans, buf)
	}
	return wf, true
}

func writeWave(file string, wf *waveform, size, mtime int64) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	w.WriteString(waveMagic)
	n := 0
	if len(wf.chans) > 0 {
		n = len(wf.chans[0])
	}
	if err := binary.Write(w, binary.LittleEndian, waveHead{
		Chans: uint8(len(wf.chans)), Hz: uint16(wf.hz), Count: uint32(n),
		Size: size, Mtime: mtime,
	}); err != nil {
		return err
	}
	for _, c := range wf.chans {
		w.Write(c)
	}
	return w.Flush()
}

// buildWave decodes the recording and keeps the loudest sample of every bucket.
// Nothing is held but the envelope: the samples go past in a pipe, which is why
// this can be pointed at an hour of 48 kHz stereo without asking what it costs.
func buildWave(path string, chans int) (*waveform, error) {
	if chans < 1 {
		chans = 1
	}
	if chans > 2 {
		chans = 2
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vn",
		"-ac", strconv.Itoa(chans), "-ar", strconv.Itoa(waveRate),
		"-f", "s16le", "-c:a", "pcm_s16le", "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	wf := &waveform{hz: waveHz, chans: make([][]uint8, chans)}
	per := int(waveRate / waveHz) // samples per bucket per channel
	r := bufio.NewReaderSize(out, 1<<16)
	buf := make([]byte, 2*chans*per)
	loudest, apart := 0, 0 // the peak of the whole file, and how far the sides get from each other
	for {
		n, err := io.ReadFull(r, buf)
		if n >= 2*chans {
			frames := n / (2 * chans)
			for c := 0; c < chans; c++ {
				top := 0
				for f := 0; f < frames; f++ {
					if v := abs16(s16(buf, f*chans+c)); v > top {
						top = v
					}
				}
				if top > loudest {
					loudest = top
				}
				// 32767 would put a full-scale peak one short of the top byte
				wf.chans[c] = append(wf.chans[c], uint8(min(255, top*255/32767)))
			}
			for f := 0; chans == 2 && f < frames; f++ {
				if d := abs16(s16(buf, 2*f) - s16(buf, 2*f+1)); d > apart {
					apart = d
				}
			}
		}
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		tail := errBuf.String()
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return nil, fmt.Errorf("ffmpeg: %w\n%s", err, tail)
	}
	if len(wf.chans[0]) == 0 {
		return nil, fmt.Errorf("%s: no audio came out of it", filepath.Base(path))
	}
	// Two sides carrying the same signal are one signal, and drawing it twice
	// costs a lane to say nothing: a mic plugged into one input of an interface
	// and written out as stereo, a phone recording, anything mono that went
	// through a stereo container. Not sample-exact equality, because a file that
	// was mono until it was encoded comes back with the coder's own noise between
	// the sides; what is asked instead is whether the two ever get more than a
	// hundredth of the file's own peak apart -- 40 dB down, which is a difference
	// nobody could see in a 30 px lane, let alone hear as a stereo image.
	//
	// A silent stereo file collapses too (nothing is nothing twice over), which is
	// the right answer for the same reason.
	if chans == 2 && apart*dualMonoRatio <= loudest {
		wf.chans = wf.chans[:1]
	}
	return wf, nil
}

// dualMonoRatio is how much quieter the difference between two channels has to
// be than the recording itself before they count as one signal.
const dualMonoRatio = 100

// s16 is sample i of an interleaved little-endian s16 buffer.
func s16(b []byte, i int) int { return int(int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)) }

func abs16(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ffprobeChannels is how many lanes a recording gets. Anything above stereo is
// two, because ffmpeg's downmix is a stereo picture of it and five lanes of a
// surround capture is not what anyone is looking at this page for.
//
// Zero means ffprobe found no audio stream, and the caller decides what that is
// worth: a video with no sound gets no lane, because the lane would be a strip
// of ground with nothing in it and a decode that can only fail, while a
// recording gets its one lane anyway -- a file that is in the session to be
// listened to and probes as having nothing is more likely a probe that went
// wrong than a file with no sound in it.
func ffprobeChannels(path string) int {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=channels", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 1 {
		if strings.TrimSpace(string(out)) == "" {
			return 0 // ffprobe found no audio stream to report on
		}
		return 1
	}
	return min(2, n)
}

// ---- drawing ----------------------------------------------------------------

// lanes is how many lanes one recording draws.
//
// The envelope decides, not the probe: a stereo file whose two sides are the
// same signal is one lane, and that is not something ffprobe can be asked --
// only the decode knows it. Until the envelope lands the probe is what there
// is, so a stereo recording briefly takes two lanes and settles into one when
// its wave arrives, which is also when the area is re-fitted around it.
func (ed *cutEditor) lanes(au tlAudio) int {
	if wf := ed.waves[au.base]; wf != nil && len(wf.chans) > 0 {
		return len(wf.chans)
	}
	return max(1, au.chans)
}

// audioLanes is how many lanes the page has to make room for.
func (ed *cutEditor) audioLanes() int {
	n := 0
	for _, au := range ed.auds {
		n += ed.lanes(au)
	}
	return n
}

// audioHeight is the widget's height, and 0 when there is nothing to show --
// which hides the area entirely rather than leaving an empty black strip under
// the cut saying nothing.
func (ed *cutEditor) audioHeight() int {
	n := ed.audioLanes()
	if n == 0 {
		return 0
	}
	return int(float64(n)*waveLaneH + float64(len(ed.auds)-1)*waveGap + 2*wavePad)
}

// drawAudio paints every lane. Same discipline as drawTrack: timeline
// coordinates with the view translated under them, and every loop cut down to
// the columns actually on screen first, so an afternoon of recording costs the
// same per frame as a minute of it.
func (ed *cutEditor) drawAudio(cr *cairo.Context, w, h int) {
	cr.SetSourceRGB(0.13, 0.13, 0.13)
	cr.Rectangle(0, 0, float64(w), float64(h))
	cr.Fill()
	if len(ed.auds) == 0 || len(ed.vids) == 0 {
		return
	}
	vx0, vx1 := ed.viewX, ed.viewX+float64(w)

	cr.Save()
	cr.Translate(-ed.viewX, 0)
	y := wavePad
	for _, au := range ed.auds {
		wf := ed.waves[au.base]
		for ch := 0; ch < ed.lanes(au); ch++ {
			ed.drawLane(cr, au, wf, ch, y, vx0, vx1)
			y += waveLaneH
		}
		y += waveGap
	}
	// the playhead crosses every lane, because reading a waveform against the
	// picture is the whole reason the lanes are here
	if ed.hasPlay {
		x := ed.xOf(ed.playhead)
		cr.SetSourceRGB(0.9, 0.15, 0.15)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, float64(h))
		cr.Stroke()
	}
	if ed.edgeOn && ed.edgeSeg < len(ed.segs) {
		x := ed.xOf(ed.edgeTime())
		cr.SetSourceRGB(1, 1, 1)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, float64(h))
		cr.Stroke()
	}
	cr.Restore()

	// the names last and un-translated: a mixer strip's labels stay where they
	// are while the tape moves past, and a lane scrolled far from the start of
	// its recording would otherwise be an anonymous blue smear
	cr.SetFontSize(9)
	y = wavePad
	for _, au := range ed.auds {
		n := ed.lanes(au)
		for ch := 0; ch < n; ch++ {
			name := au.base + " " + laneName(ch, n, au.chans)
			if au.master {
				// which lane is the footage's own sound has to be readable from
				// the lane, not worked out from the order of the track above
				name += " · video"
			}
			plateText(cr, 4, y+12, name)
			y += waveLaneH
		}
		y += waveGap
	}
}

// laneName says which channel a lane is: lanes of them, out of chans recorded.
// A mono recording says so rather than calling its one channel "L", which would
// imply a missing R, and a stereo recording drawn on one lane says "L=R" rather
// than "mono" -- it is not a mono file, it is a stereo file with one signal in
// it, and a lane that called itself mono would look like the page had lost a
// channel somewhere.
func laneName(ch, lanes, chans int) string {
	if lanes >= 2 {
		if ch == 0 {
			return "L"
		}
		return "R"
	}
	if chans >= 2 {
		return "L=R"
	}
	return "mono"
}

// drawLane paints one channel of one recording across the footage it overlaps.
//
// Per video rather than straight across the timeline, because the timeline is
// not one continuous clock: recordings are laid out with a fixed-width hatched
// hole between them (gapPx) and tAt clamps inside those holes, so a column
// walked blindly across one would smear the same instant over the whole gap.
func (ed *cutEditor) drawLane(cr *cairo.Context, au tlAudio, wf *waveform, ch int, y, vx0, vx1 float64) {
	mid := y + waveLaneH/2
	half := waveLaneH/2 - 2

	for _, v := range ed.vids {
		// the overlap of this recording with this piece of footage, in session
		// time, then in px
		t0 := math.Max(au.start, v.start)
		t1 := math.Min(au.start+au.dur, v.start+v.dur)
		if t1 <= t0 {
			continue // this recording was not running while this one was
		}
		x0 := math.Max(v.pxOrigin+(t0-v.start)*ed.pps, vx0)
		x1 := math.Min(v.pxOrigin+(t1-v.start)*ed.pps, vx1)
		if x1 <= x0 {
			continue // off screen
		}
		// the ground says where the recording IS, which is the other half of
		// only drawing the relevant part: an empty lane and a lane of silence
		// are different things and have to look different
		cr.SetSourceRGB(0.16, 0.17, 0.2)
		cr.Rectangle(x0, y, x1-x0, waveLaneH)
		cr.Fill()
		cr.SetSourceRGBA(0.35, 0.6, 1, 0.35)
		cr.SetLineWidth(1)
		cr.MoveTo(x0, math.Round(mid)+0.5)
		cr.LineTo(x1, math.Round(mid)+0.5)
		cr.Stroke()
		if wf == nil {
			continue // still being decoded; the ground already says it is here
		}
		// one filled column per pixel, both ways from the middle
		cr.SetSourceRGB(0.29, 0.62, 1)
		spp := 1 / ed.pps // seconds per pixel
		for x := math.Floor(x0); x < x1; x++ {
			at := au.timeAt(v, ed.pps, x)
			p := wf.peak(ch, at, at+spp)
			if p <= 0 {
				continue
			}
			hgt := math.Max(1, p*half)
			cr.Rectangle(x, mid-hgt, 1, hgt*2)
		}
		cr.Fill()
	}
}

// timeAt is the recording's own time at a timeline x, through the video that x
// belongs to. The audio's clock and the footage's clock differ by exactly where
// each of them started, which is what start holds and what makes this a
// subtraction rather than an alignment problem.
func (au tlAudio) timeAt(v tlVideo, pps, x float64) float64 {
	return (v.start + (x-v.pxOrigin)/pps) - au.start
}
