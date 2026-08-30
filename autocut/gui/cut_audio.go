package main

// The audio lanes under the cut: one pair for the footage's own sound, and one
// per separate recording.
//
// The video track is the master and stays the master: the timeline is the
// footage's, x is still the footage's x, and a lane below is a slave drawing of
// something that happened at the same time. This matters because the audio was
// recorded by a different machine -- a headset recorder, OBS's second track, a
// phone on the table -- which started when it started and knows nothing about
// when the capture card did. What lines them up is the wall clock (srcClock: a
// timestamp in the name, else the session's own start), the same zero every
// other part of this app places sources by, so a lane is drawn where the
// recording actually was rather than from the left edge.
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
	"github.com/diamondburned/gotk4/pkg/glib/v2"
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
	start  float64 // session time of the lane's first second
	off    float64 // the second of the FILE that lands there; see at
	dur    float64
	chans  int  // 1 or 2 lanes; more than two is downmixed to a stereo picture
	master bool // the footage's own track: heard by the preview already
}

// at is the second of the file heard at session second t, which is tlVideo.at
// for sound and is a subtraction for the same reason: a lane's clock and the
// session's differ by exactly where the lane was put.
//
// off is nought for a recording -- a recording IS its file, whole, and its
// first second is the file's first second. A cut lane is a WINDOW opening
// partway into a file (cut_lane.go), and its sound is that same window: the
// row starts at second off of the file and runs dur from there.
func (au tlAudio) at(t float64) float64 { return t - au.start + au.off }

// soundAt says whether anything is audible at these seconds -- the capture's own
// track or a separate recording running under it. ed.auds is the right list to
// ask because it is both: masterLanes puts the footage's track in it, and only
// when the file actually has one.
func (ed *cutEditor) soundAt(t, dur float64) bool {
	for _, au := range ed.auds {
		if t < au.start+au.dur && t+dur > au.start {
			return true
		}
	}
	return false
}

// soundOpen is the whole condition for asking what an insert does to the sound,
// gathered in one place because it is a conjunction of four things and every one
// of them is a reason NOT to ask.
//
// The scope answers this question wherever it can: ▲▲ says keep, ▼ says replace
// that one, and ▲▼ says replace both -- which needs no asking either, so long as
// the file has a sound to replace it with. The gap is a file that brings none.
// Then "replace both" has nothing to put in the sound's place, and silence and
// carry-on are equally honest readings of what the hand meant.
//
// The rest are cases where no answer would change anything: a sound insert is
// not a picture and settles this by being what it is, a copied stretch took its
// answer when it was copied (cutEditor.copyPic), and seconds with nothing to
// hear sound the same either way.
func (ed *cutEditor) soundOpen(path string, at, dur float64, m insMode) bool {
	if ed == nil || m.mute || m.lane != "" || insKind(path) == "audio" {
		return false
	}
	if _, ok := copySrc(path); ok {
		return false
	}
	file, _ := insSplit(path)
	return !hasAudioStream(file) && ed.soundAt(at, dur)
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
			off: v.off, dur: v.dur, chans: ch, master: true})
	}
	return out
}

// loadWaves gets an envelope for every lane that has not got one, in the
// background and one goroutine each: decoding an hour of audio takes seconds,
// and a page that waited for them would be a tab that does not open. A lane
// whose envelope has not landed yet draws its ground and no wave, and the
// redraw when it does is the whole of the arrival -- there is nothing to
// recompute, because the audio is not part of the timeline's geometry.
//
// Asked again whenever the lanes change and not only on a reload: a cut lane
// added by hand is a lane with sound under it from the moment it appears
// (setLanes), and one that had to wait for the next visit to draw its wave
// would look like a lane that has none.
//
// Keyed by the lane's name rather than its path, because that is what draws it,
// and a copied shot is one file on two lanes with two windows on it. The second
// decode is a read of the disk cache the first one left (loadWave).
func (ed *cutEditor) loadWaves() {
	if ed.audArea == nil {
		return // no band to draw them in: an editor built for a test
	}
	if ed.waves == nil {
		ed.waves = map[string]*waveform{}
	}
	a := ed.a
	for _, au := range ed.auds {
		if _, ok := ed.waves[au.base]; ok {
			continue
		}
		au := au
		go func() {
			wf, err := loadWave(a.waveCache(), au.path, au.chans)
			glib.IdleAdd(func() {
				if err != nil {
					a.logf("no waveform for %s: %v", au.base, err)
					return
				}
				ed.waves[au.base] = wf
				ed.fitAudio()     // it may have come back on fewer lanes than the probe promised
				ed.fitSrc()       // ...and a master's collapse is a shallower ROW: the strip under
				ed.redrawTracks() // its pictures is row geometry now, not the band's
			})
		}()
	}
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

// ---- which lane the cut is heard on -----------------------------------------

// A session shot on two cameras is HEARD on one of them.
//
// Every camera records its own sound, so two of them is the same room recorded
// twice: a few frames apart, at two different distances from whoever is
// talking, with two different rooms' worth of tone. Cutting the sound with the
// picture would put a seam in the audio at every change of camera -- the tone
// jumps, a word half-said in one recording is half-said differently in the
// other -- and it sounds broken in a way nobody can point at.
//
// So which sound is heard is a choice, made once for the whole cut and not tied
// to the picture at all: ↑ and ↓ walk the lanes, every scene is heard with the
// one that is picked, and the pictures cut where they like underneath. The
// empty choice is every scene heard with the camera that shot it, which is
// exactly what a one-camera session has always done and is why it is the
// default and why a cut.json without the field still renders as it did.
//
// The PREVIEW does not follow it: it is one pipeline playing one file, and its
// sound is that file's. What says which lane is heard is the lane's own plate
// on the waveforms (drawAudio), and the render.

// soundOf is where a scene's sound comes from under that choice: the file and
// the second inside it, or "" meaning "the picture's own" -- which is both the
// default and the answer whenever the chosen lane was not running.
//
// A choice naming a camera names the ROW, not one file: a camera stopped and
// started again is several recordings in a line, and the sound carries across
// them exactly as the picture does.
func soundOf(vids []tlVideo, auds []tlAudio, snd string, t float64) (string, float64) {
	if strings.TrimSpace(snd) == "" {
		return "", 0
	}
	for i := range vids {
		if vids[i].base != snd {
			continue
		}
		if v := videoOn(vids, vids[i].lane, t); v != nil {
			return v.path, v.at(t)
		}
		return "", 0 // that camera was not rolling here
	}
	for _, au := range auds {
		if au.base == snd && t >= au.start && t < au.start+au.dur {
			return au.path, au.at(t)
		}
	}
	return "", 0
}

// sndChoices is what ↑ and ↓ walk, in order: "" first -- every scene heard with
// the camera that shot it -- then one entry per camera row, named by the first
// recording on it, then every separately recorded lane.
//
// One entry per ROW rather than per file, because a camera is picked whole. And
// no camera at all when there is only one row: "" already means that camera,
// and an arrow that cycles between two spellings of the same answer reads as a
// key that does not work.
func sndChoices(vids []tlVideo, auds []tlAudio) []string {
	out := []string{""}
	var rows []string
	seen := map[int]bool{}
	for _, v := range vids {
		if !seen[v.lane] {
			seen[v.lane] = true
			rows = append(rows, v.base)
		}
	}
	if len(rows) > 1 {
		out = append(out, rows...)
	}
	for _, au := range auds {
		if !au.master {
			out = append(out, au.base)
		}
	}
	return out
}

// sndLabel is a choice in a sentence.
func sndLabel(vids []tlVideo, snd string) string {
	if strings.TrimSpace(snd) == "" {
		return "each camera's own sound"
	}
	for _, v := range vids {
		if v.base == snd {
			return fmt.Sprintf("camera %d — %s", v.lane+1, v.base)
		}
	}
	return snd
}

// heardOn says this lane is the one the cut is heard on. Every recording on a
// chosen camera's row answers yes, not only the one the choice was written
// down as, because the choice was the row.
func (ed *cutEditor) heardOn(base string) bool {
	if strings.TrimSpace(ed.snd) == "" {
		return false
	}
	if base == ed.snd {
		return true
	}
	row := -1
	for _, v := range ed.vids {
		if v.base == ed.snd {
			row = v.lane
		}
	}
	if row < 0 {
		return false
	}
	for _, v := range ed.vids {
		if v.base == base {
			return v.lane == row
		}
	}
	return false
}

// cycleSound is ↑ and ↓ on the timeline: the next lane along, or the previous
// one, wrapping. Not an undo step -- it is one keystroke to put back, and a
// history full of "changed my mind about the microphone" is a history that has
// lost the edit before it.
func (ed *cutEditor) cycleSound(d int) {
	ch := sndChoices(ed.vids, ed.auds)
	if len(ch) < 2 {
		ed.a.setStatus("this session was shot on one camera with nothing recorded " +
			"separately — there is no other lane to hear the cut on")
		return
	}
	at := 0
	for i, c := range ch {
		if c == ed.snd {
			at = i
		}
	}
	ed.snd = ch[((at+d)%len(ch)+len(ch))%len(ch)]
	ed.persist()
	ed.redrawTracks()
	ed.a.setStatus(fmt.Sprintf("the whole cut is heard on %s — ↑ and ↓ walk the lanes. "+
		"The preview still plays the picture's own sound; the render uses this",
		sndLabel(ed.vids, ed.snd)))
}

// waveform is the peak envelope: one byte per bucket per channel, the loudest
// sample in that bucket. Peak rather than RMS because what a cut is aimed at is
// where someone starts talking, and RMS rounds an onset off.
type waveform struct {
	hz    float64
	chans [][]uint8
}

// ---- how loud is drawn ------------------------------------------------------
//
// A linear envelope is unreadable. Amplitude is linear and hearing is not, so
// a lane drawn straight from the sample values spends almost its whole height
// on the loudest few dB and puts everything else on the floor: speech peaking
// at -12 dBFS is a quarter of the lane, the room tone under it at -50 is three
// thousandths of it, and the picture is a row of spikes over a flat line. What
// you want to see -- where the talking is, where the quiet is, where a lull is
// merely quiet rather than empty -- is all in the part that got flattened.
//
// So the height is a meter reading, not an amplitude. The curve is IEC
// 60268-18, the broadcast meter scale: -70 dBFS at the bottom, 0 dBFS at the
// top, and progressively more of the lane per dB as it climbs. It is the same
// curve Shotcut's timeline waveform is drawn on -- MLT's audiolevel filter
// turns it on by default -- which is why the two now look like each other.
//
// One thing is still ours rather than Shotcut's: the envelope underneath is a
// peak and Shotcut's is a mean over the first four milliseconds of each video
// frame, so the same sound reads a few dB hotter here and an onset that
// Shotcut's strobe misses is still drawn.
//
// A lane is filled from the bottom up, not mirrored about a middle. A mirrored
// waveform is the picture of a signal -- a bipolar thing swinging both ways
// about zero -- and this is not one: a bucket holds the loudest ABSOLUTE
// sample in ten milliseconds, so the half below the line was the half above it
// drawn a second time. It carried nothing, and it cost the lane half its
// height to say it. What the envelope actually holds is a level, levels have a
// floor and a top and no negative side, and a lane filled from its floor gives
// every pixel of itself to the only number there is.

// iecRaw is the standard's own curve: linear amplitude in, meter deflection
// out, with the knots exactly where IEC 60268-18 puts them.
func iecRaw(amp float64) float64 {
	if amp <= 0 {
		return 0
	}
	db := 20 * math.Log10(amp)
	switch {
	case db < -70:
		return 0
	case db < -60:
		return (db + 70) * 0.0025
	case db < -50:
		return (db+60)*0.005 + 0.025
	case db < -40:
		return (db+50)*0.0075 + 0.075
	case db < -30:
		return (db+40)*0.015 + 0.15
	case db < -20:
		return (db+30)*0.02 + 0.3
	}
	return math.Min(1, (db+20)*0.025+0.5)
}

// iecFloor is where the curve stands when the envelope holds the quietest
// thing it can hold. A bucket is one byte, so the smallest sound that is not
// silence is 1/255 of full scale -- about -48 dBFS -- while the curve's own
// bottom is -70. Left alone, the 22 dB between them would be a band of the
// lane no signal could ever land in, and a noise floor hovering around the
// byte's own floor would draw as a picket fence: nothing, then a fourteenth of
// the lane, then nothing.
var iecFloor = iecRaw(1.0 / 255)

// iecScale is the curve as this page uses it: the quietest bucket the envelope
// can hold sits on the floor of the lane, full scale reaches the top, and
// everything between is the meter curve stretched across that range.
func iecScale(amp float64) float64 {
	v := (iecRaw(amp) - iecFloor) / (1 - iecFloor)
	return math.Max(0, math.Min(1, v))
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
// rather than read as something it is not. It has now changed twice without the
// layout changing, for the same reason both times: a stereo file whose two
// sides are the same signal is one channel (AWV3), and so is one whose two
// sides merely DRAW the same (AWV4, see sameLanes). A cache written before
// either would go on drawing the second lane forever, since it is keyed by the
// recording and the recording has not changed.
const waveMagic = "AWV4"

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
	cmd := exec.Command(ffTool("ffmpeg"), "-v", "error", "-i", path, "-vn",
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
	if chans == 2 && (apart*dualMonoRatio <= loudest || sameLanes(wf.chans[0], wf.chans[1])) {
		wf.chans = wf.chans[:1]
	}
	return wf, nil
}

// dualMonoRatio is how much quieter the difference between two channels has to
// be than the recording itself before they count as one signal.
const dualMonoRatio = 100

// sameLanes is the other half of that question, asked about the PICTURE rather
// than about the samples: do these two envelopes draw the same lane?
//
// The sample test above is the strict one -- it asks whether there is a stereo
// image at all, and it answers no only when the sides never get 40 dB from each
// other anywhere in the recording. That is the right question for "is this one
// signal" and too strict for "is this one lane": one transient where a lossy
// coder reconstructed the two sides slightly differently is enough to fail it,
// and the page then spends a row of itself drawing the same skyline twice.
//
// So this asks the drawn question instead, in the envelope's own unit. A bucket
// is a byte, so two envelopes that agree to within a byte are the same picture
// to the precision the picture is kept in -- there is nothing left to see in the
// second lane. Averaged, because coder noise is scattered over the recording
// and a mean is what a lane full of it looks like; with a ceiling on any single
// bucket, because "identical except for the one moment something panned hard"
// is a stereo image, and a mean over an hour would swallow it.
//
// The numbers are far apart on purpose. A mono file through a lossy coder comes
// back with the sides a small fraction of a byte apart; a pair of mics that are
// not quite matched -- a tenth of the level between them, which is under a dB
// and is the case this must NOT collapse -- is thirteen bytes apart at every
// bucket. There is an order of magnitude either side of where this sits.
const (
	laneSameAvg = 1.0 // bytes of envelope, meaned over the recording
	laneSameMax = 8   // ...and the most any one bucket may be out
)

func sameLanes(a, b []uint8) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	sum := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d > laneSameMax {
			return false
		}
		sum += d
	}
	return float64(sum)/float64(len(a)) < laneSameAvg
}

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
	out, err := exec.Command(ffTool("ffprobe"), "-v", "error", "-select_streams", "a:0",
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

// sepAuds is what the band under the timeline holds: the separate recordings,
// the sound nobody filmed. A master -- some row's own track -- is drawn under
// that row's pictures instead (drawPairStrip), so every question about the
// band's own layout is a question about this list and not about ed.auds.
func (ed *cutEditor) sepAuds() []tlAudio {
	var out []tlAudio
	for _, au := range ed.auds {
		if !au.master {
			out = append(out, au)
		}
	}
	return out
}

// audioHeight is the widget's height, and 0 when there is nothing to show --
// which hides the area entirely rather than leaving an empty black strip under
// the cut saying nothing. With the masters paired under their rows that is the
// everyday state, not the odd one: a cameras-only session has no band at all.
func (ed *cutEditor) audioHeight() int {
	n := ed.audioLanes()
	if n == 0 {
		return 0
	}
	return int(float64(n)*waveLaneH + float64(len(ed.sepAuds())-1)*waveGap + 2*wavePad)
}

// audioLanes is how many waveform lanes the band holds: the separate
// recordings' channels, and only theirs -- a master's wave lives under its
// own row of pictures now, not down here.
func (ed *cutEditor) audioLanes() int {
	n := 0
	for _, au := range ed.sepAuds() {
		n += ed.lanes(au)
	}
	return n
}

// audAtY is the recording whose lanes sit at y in the audio area, by base
// name, or "" when there are none at all. It walks the layout drawAudio
// draws, so what the hand lands on is the lane the eye is pointing at.
//
// Total, deliberately: every point in the area answers with a recording. The
// pad above the first lane and the hair of ground between two recordings are
// not places anyone is aiming at on purpose, and a press there that quietly
// meant "the pictures" would take the footage when the hand was on a
// waveform -- the one mistake this whole row is here to prevent.
func (ed *cutEditor) audAtY(y float64) string {
	auds := ed.sepAuds()
	if len(auds) == 0 {
		return ""
	}
	top := wavePad
	for _, au := range auds {
		if top += float64(ed.lanes(au)) * waveLaneH; y < top {
			return au.base
		}
		top += waveGap
	}
	return auds[len(auds)-1].base
}

// audLaneSpan is the top and bottom of one recording's lanes, and whether it
// has any -- the other half of audAtY, for drawing on the recording a
// selection is of rather than across all of them.
func (ed *cutEditor) audLaneSpan(base string) (float64, float64, bool) {
	top := wavePad
	for _, au := range ed.sepAuds() {
		h := float64(ed.lanes(au)) * waveLaneH
		if au.base == base {
			return top, top + h, true
		}
		top += h + waveGap
	}
	return 0, 0, false
}

// drawAudio paints every lane. Same discipline as drawTrack: timeline
// coordinates with the view translated under them, and every loop cut down to
// the columns actually on screen first, so an afternoon of recording costs the
// same per frame as a minute of it.
func (ed *cutEditor) drawAudio(cr *cairo.Context, w, h int) {
	fh := float64(h)
	cr.SetSourceRGB(0.13, 0.13, 0.13)
	cr.Rectangle(0, 0, float64(w), fh)
	cr.Fill()
	auds := ed.sepAuds()
	if len(auds) == 0 || len(ed.vids) == 0 {
		return
	}
	vx0, vx1 := ed.viewX, ed.viewX+float64(w)

	cr.Save()
	cr.Translate(-ed.viewX, 0)
	y := wavePad
	for _, au := range auds {
		wf := ed.waves[au.base]
		for ch := 0; ch < ed.lanes(au); ch++ {
			ed.drawLane(cr, au, wf, ch, y, vx0, vx1)
			y += waveLaneH
		}
		y += waveGap
	}
	// What the cut keeps, in green, exactly as it is said over the thumbnails
	// -- these seconds are in the video, and the rest is not. It has to be
	// said in both places: sound is chosen here now, and choosing where a
	// sound goes against a band that never showed the cut meant reading the
	// answer off a different row than the one being worked in.
	//
	// Fainter than the tint on the pictures, though. There it lies over a
	// thumbnail, which is a picture and survives being tinted; here it lies
	// over a waveform, which IS the reading, and a wash heavy enough to
	// colour the ground would take the wave with it.
	for _, s := range ed.segs {
		if s.isInsert() && !(s.audioIns() && !s.spliced()) {
			continue // violet below; the sound laid over running footage keeps its green
		}
		x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		cr.SetSourceRGBA(0.2, 0.8, 0.3, 0.16)
		cr.Rectangle(x0, 0, x1-x0, fh)
		cr.Fill()
		cr.SetSourceRGB(0.15, 0.85, 0.25)
		cr.SetLineWidth(2)
		for _, x := range []float64{x0, x1} {
			cr.MoveTo(x, 0)
			cr.LineTo(x, fh)
			cr.Stroke()
		}
	}
	// and under the ▶✂ preview the dropped stretches are dimmed rather than merely left
	// untinted, the picture band's rule: in that mode they are the seconds ▶
	// jumps over, and a lane that still showed them at full brightness would
	// be offering sound that is never heard.
	if ed.cutOnly {
		cr.SetSourceRGBA(0.04, 0.04, 0.05, 0.62)
		for _, g := range ed.droppedSpans() {
			x0, x1 := ed.xOf(g[0]), ed.xOf(g[1])
			if x1 < vx0 || x0 > vx1 {
				continue
			}
			cr.Rectangle(x0, 0, x1-x0, fh)
		}
		cr.Fill()
	}
	// A sound-only insert is marked here and nowhere else: the picture band is
	// about what is seen, and these seconds look no different there -- the
	// footage keeps its frames (or holds one, spliced). The sound is what was
	// placed, so the lanes carry the violet, the hatching for a splice, and the
	// held outline, exactly as the picture band does for a card.
	held := ed.heldSeg()
	for i := range ed.segs {
		s := ed.segs[i]
		if !s.audioIns() {
			continue
		}
		x0, x1 := ed.segSpan(s)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		ed.sndInsMark(cr, s, x0, x1, 0, fh, held == &ed.segs[i], true)
	}
	// the selection, over everything it covers. Drawn here and not only in its
	// own row because a selection made in a lane is a selection of that
	// SOUND: the span is session time and crosses every lane, so every lane
	// is tinted, and the one recording it is of is tinted again on top. That
	// second wash is the whole answer to "which sound is in hand" -- ⧉ Copy
	// takes that one, and nothing else on the page says which it is.
	if ed.sel.active {
		x0, x1 := ed.selSpanPx()
		if x1 >= vx0 && x0 <= vx1 {
			cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.16)
			cr.Rectangle(x0, 0, x1-x0, fh)
			cr.Fill()
			if y0, y1, ok := ed.audLaneSpan(ed.sel.aud); ok {
				cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.34)
				cr.Rectangle(x0, y0, x1-x0, y1-y0)
				cr.Fill()
			}
			cr.SetSourceRGB(0.62, 0.82, 1)
			for _, x := range []float64{x0, x1} {
				cr.Rectangle(x-1.5, 0, 3, fh)
			}
			cr.Fill()
		}
	}
	// the playhead crosses every lane, because reading a waveform against the
	// picture is the whole reason the lanes are here
	if ed.hasPlay {
		x := ed.xOf(ed.playhead)
		cr.SetSourceRGB(0.9, 0.15, 0.15)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, fh)
		cr.Stroke()
	}
	// the border under the pointer, then the one being held. The lanes answer to
	// the mouse as the picture band does, so they have to show the same offer:
	// a cut point you can see through the sound but cannot tell you are about to
	// grab is a cut point you trim by accident.
	if ed.edgeHovOn {
		x := ed.xOf(ed.edgeHovT)
		cr.SetSourceRGBA(1, 1, 1, 0.45)
		cr.SetLineWidth(3)
		cr.MoveTo(x, 0)
		cr.LineTo(x, fh)
		cr.Stroke()
	}
	if ed.edgeOn && ed.edgeSeg < len(ed.segs) {
		x := ed.xOf(ed.edgeTime())
		cr.SetSourceRGB(1, 1, 1)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, fh)
		cr.Stroke()
	}
	cr.Restore()

	// the names last and un-translated: a mixer strip's labels stay where they
	// are while the tape moves past, and a lane scrolled far from the start of
	// its recording would otherwise be an anonymous blue smear
	cr.SetFontSize(9)
	y = wavePad
	for _, au := range auds {
		n := ed.lanes(au)
		for ch := 0; ch < n; ch++ {
			name := au.base + " " + laneName(ch, n, au.chans)
			if ed.heardOn(au.base) {
				// and which one the finished video is heard on, said on the
				// lane itself: it is a choice with no other mark on the page,
				// and a cut that came out sounding like the wrong microphone
				// with nothing anywhere saying which was picked is a bug
				// nobody can find
				name += " · heard"
			}
			if d := ed.shiftOf(au.base); d != 0 {
				// a corrected clock is a fact about the project that is
				// otherwise invisible: the lane simply sits where it sits, and
				// nothing distinguishes "the recorder was two seconds out" from
				// "this is where the file says it starts"
				name += fmt.Sprintf(" · %+.2f s", d)
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
	for _, v := range ed.vids {
		ed.drawWaveSpan(cr, au, v, wf, ch, y, vx0, vx1, false)
	}
}

// sndInsMark paints one sound-only insert's marker over one strip of wave --
// the violet, the splice hatching, the edges, the held outline, and (asked
// once per insert) the name plate. One painter for the recorders' band and
// the rows' paired strips: a placed sound shows in both places, and two
// hand-copied blocks are two chances for them to stop saying the same thing.
func (ed *cutEditor) sndInsMark(cr *cairo.Context, s cutSeg, x0, x1, y, h float64, held, named bool) {
	cr.SetSourceRGBA(0.55, 0.35, 0.9, 0.45)
	cr.Rectangle(x0, y, x1-x0, h)
	cr.Fill()
	if s.spliced() {
		// the same sentence the picture band's hatching says for a card:
		// the footage stops for this
		hatchStrokes(cr, x0, x1-x0, y, h)
	}
	cr.SetSourceRGB(0.75, 0.6, 1)
	cr.SetLineWidth(2)
	for _, x := range []float64{x0, x1} {
		cr.MoveTo(x, y)
		cr.LineTo(x, y+h)
		cr.Stroke()
	}
	if held {
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.Rectangle(x0+1, y+1, x1-x0-2, h-2)
		cr.Stroke()
	}
	if !named {
		return
	}
	// named at the bottom of the strip: the plates own the top left
	cr.SetFontSize(10)
	switch {
	case s.spliced():
		tx := x1 + 4
		if x1-x0 > 90 {
			tx = x0 + 4
		}
		markPlate(cr, tx, y+h-6, "sound", fmt.Sprintf("%s  %.1fs", insName(s), s.Dur))
	case x1-x0 > 24:
		markPlate(cr, x0+4, y+h-6, "sound", insName(s))
	}
}

// drawPairStrip paints a row's own sound under its pictures: every channel of
// the footage's track, windowed exactly as the pictures are, in the dim voice.
// Over this one video only -- the strip belongs to the row's footage, not to
// the session, and two sources sharing a row each bring the stretch under
// their own pictures.
func (ed *cutEditor) drawPairStrip(cr *cairo.Context, v tlVideo, au tlAudio, y, vx0, vx1 float64) {
	wf := ed.waves[au.base]
	for ch := 0; ch < ed.lanes(au); ch++ {
		ed.drawWaveSpan(cr, au, v, wf, ch, y, vx0, vx1, true)
		y += waveLaneH
	}
}

// drawWaveSpan paints the stretch of one channel of one recording that
// overlaps one piece of footage.
//
// dim is the paired strip's voice: the same wave turned down, plateless, edge
// to edge with the thumbnails above it. The strip sits inside the picture
// band, and the rows of pictures are the things the eye compares when it
// chooses a camera -- so the wave has to read as the row's shadow, not as a
// row of its own between two of them.
func (ed *cutEditor) drawWaveSpan(cr *cairo.Context, au tlAudio, v tlVideo, wf *waveform, ch int, y, vx0, vx1 float64, dim bool) {
	bot := y + waveLaneH - 1 // the meter's zero, a hair inside the lane
	full := waveLaneH - 2    // and how far up full scale reaches
	alpha := 1.0
	if dim {
		alpha = 0.55
	}

	// the overlap of this recording with this piece of footage, in session
	// time, then in px
	t0 := math.Max(au.start, v.start)
	t1 := math.Min(au.start+au.dur, v.start+v.dur)
	if t1 <= t0 {
		return // this recording was not running while this one was
	}
	x0 := math.Max(v.pxOrigin+(t0-v.start)*ed.pps, vx0)
	x1 := math.Min(v.pxOrigin+(t1-v.start)*ed.pps, vx1)
	if x1 <= x0 {
		return // off screen
	}
	// the ground says where the recording IS, which is the other half of
	// only drawing the relevant part: an empty lane and a lane of silence
	// are different things and have to look different
	cr.SetSourceRGBA(0.16, 0.17, 0.2, alpha)
	cr.Rectangle(x0, y, x1-x0, waveLaneH)
	cr.Fill()
	// the baseline sits under the fill rather than through it: it is the
	// meter's zero, and on a silent stretch it is the only thing saying the
	// recorder was still running
	cr.SetSourceRGBA(0.35, 0.6, 1, 0.35*alpha)
	cr.SetLineWidth(1)
	cr.MoveTo(x0, math.Round(bot)+0.5)
	cr.LineTo(x1, math.Round(bot)+0.5)
	cr.Stroke()
	if wf == nil {
		return // still being decoded; the ground already says it is here
	}
	// one filled column per pixel, standing up from the baseline
	spp := 1 / ed.pps // seconds per pixel
	hgts := make([]float64, 0, int(x1-math.Floor(x0))+1)
	cr.SetSourceRGBA(0.29, 0.62, 1, alpha)
	for x := math.Floor(x0); x < x1; x++ {
		at := au.timeAt(v, ed.pps, x)
		// the envelope is a linear peak and the lane is a meter: iecScale
		// is the whole difference between a row of spikes over a flat line
		// and a picture of where the sound is
		p := iecScale(wf.peak(ch, at, at+spp))
		h := 0.0
		if p > 0 {
			h = math.Max(1, p*full)
			cr.Rectangle(x, bot-h, 1, h)
		}
		hgts = append(hgts, h)
	}
	cr.Fill()
	// and a darker cap along the top of the fill. A column is one pixel
	// wide, so at any zoom worth looking at the lane is a solid block of
	// blue whose only shape is its skyline; drawn in the same ink as the
	// block it sits on, that skyline is exactly where the eye stops being
	// able to see it.
	cr.SetSourceRGBA(0.09, 0.27, 0.52, alpha)
	for i, h := range hgts {
		if h >= 3 { // below that the cap would eat the column it caps
			cr.Rectangle(math.Floor(x0)+float64(i), bot-h, 1, 1)
		}
	}
	cr.Fill()
}

// timeAt is the recording's own time at a timeline x, through the video that x
// belongs to. The audio's clock and the footage's clock differ by exactly where
// each of them started, which is what start holds and what makes this a
// subtraction rather than an alignment problem.
func (au tlAudio) timeAt(v tlVideo, pps, x float64) float64 {
	return au.at(v.start + (x-v.pxOrigin)/pps)
}
