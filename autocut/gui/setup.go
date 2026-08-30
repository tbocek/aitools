package main

// Settings: edits llm.conf in place. It stays bash-sourceable (quoted values,
// no GUI-only syntax) so a shell can read it too. Both of the pipeline's HTTP
// endpoints live here -- the LLM that writes (descriptions, cuts, narration)
// and the audio.cpp server, which speaks the narration and does the listening
// in Prepare -- plus the handful of names the second one needs: which of its
// models transcribes and which one tells speakers apart.
//
// What is deliberately NOT here any more is the language. This file is one
// machine's stack, the same for every session it ever runs; the language is a
// fact about the footage, and a machine that cut a German session yesterday
// still transcribed today's English one as German until somebody remembered to
// come in here. It lives on the Inputs page now, in the project, beside the
// sources it describes.
//
// Those were compiled in until they made the app run on exactly one machine:
// the paths were one person's, and the backend assumed an AMD card. The
// backend went away with the container -- where a model runs is now decided in
// audiocpp-server.json, by whoever runs the server.
//
// ffmpeg is the one local tool every step depends on, so it is shown, settable
// and testable here. Left blank it comes off PATH like any other tool, which is
// what nearly every machine wants; a path is for the machines where that is
// wrong -- two ffmpegs installed, or a GUI whose PATH is not the shell's.
//
// The dialog can query each server and say what it found, because the failures
// it is meant to catch are silent otherwise: a model id spelled differently
// than the server spells it (a hand-typed id with a stray suffix cost us a
// debug round once already), a TTS server that answers but serves the wrong
// family, and an ffmpeg built without the parts the render needs.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type appConf struct {
	Server, Model, Key string // the LLM that writes
	TTS                string // the audio.cpp server; blank = the compose default
	TTSKey             string // its API key; blank = none, which the local stack is

	// the sd.cpp server that draws the thumbnail; blank for the compose
	// default, like TTS, and its key, like TTSKey. Every server here speaks
	// HTTP and any of them can sit behind a proxy that wants a token -- so
	// every server has a key field, and blank simply sends none.
	//
	// There is no model box beside SD. sd-server loads one model when it starts
	// and its request bodies have no model field, so nothing autocut sends can
	// switch it -- and a box that cannot choose anything is a box that can only
	// be wrong. The Test button reports what the server actually has loaded,
	// which is the same information without a second place to keep it in sync.
	SD    string
	SDKey string

	// What to ask the audio server for. The voices folder is the ONE folder
	// autocut still reads -- the wavs the voice picker lists, and where "Add
	// sample…" converts new ones into -- and it is set on the Narrate step,
	// beside the list it fills, not in the settings dialog. Model weights are
	// not read from anywhere here; their paths are audiocpp-server.json's
	// business, on the server's side. The model ids are the server's own --
	// what that json calls them -- and they are here rather than compiled in
	// because the models get replaced faster than the code does.
	Voices              string
	ASRModel, DiarModel string
	TTSModel            string

	// Which ffmpeg to shell out to. Blank -- the ordinary answer -- means the
	// name alone, resolved off PATH like any other tool. A path is for the
	// machine with a hand-built ffmpeg beside the distro's, or one where the
	// GUI's PATH is not the shell's; see ffTool for where ffprobe comes from
	// then. No default: inventing one would break every machine but this one,
	// which is the fault the whole config file grew out of.
	FFmpeg string
}

// ffSet is the settings box, shared with the runners. The pipeline shells out
// from goroutines, so this is stored rather than passed: readConf refreshes it
// before each step, the same read that gives that step its endpoints.
var ffSet atomic.Pointer[string]

// ffTool is the command to run for one of the two ffmpeg binaries. With no
// path set it is the bare name, which exec resolves off PATH. With one set,
// ffprobe is taken from the SAME folder: a hand-built ffmpeg paired with the
// distro's ffprobe is precisely the mismatch the box exists to fix, and
// letting probe fall back to PATH would quietly recreate it.
func ffTool(name string) string {
	p := ffSet.Load()
	if p == nil || *p == "" {
		return name
	}
	if name == "ffmpeg" {
		return *p
	}
	return filepath.Join(filepath.Dir(*p), name)
}

// The dev box this grew up on. A config line that is missing or blank means
// "whatever the build shipped with" -- an empty model id would otherwise fail
// with a message about nothing.
const (
	defVoices    = "/mnt/models/audiocpp/voices"
	defASRModel  = "nemotron-asr"
	defDiarModel = "sortformer-diar"
	defTTSModel  = "index-tts2"
)

func or(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

// withDefaults fills the blanks. The TTS endpoint is pointedly not in here:
// empty there is a real answer, meaning "the compose service on loopback".
// The two SD fields are out for the same reason and for one more: an empty
// model name means "do not check", and inventing a default would turn every
// Test on a differently-stocked server into a failure about a name nobody
// typed.
func (c appConf) withDefaults() appConf {
	c.Voices = or(c.Voices, defVoices)
	c.ASRModel = or(c.ASRModel, defASRModel)
	c.DiarModel = or(c.DiarModel, defDiarModel)
	c.TTSModel = or(c.TTSModel, defTTSModel)
	return c
}

func (a *App) confPath() string { return filepath.Join(a.root, "llm.conf") }

// readConf is the whole config, with every blank filled by the built-in.
// Callable from a runner's goroutine: it re-reads a small file rather than
// sharing mutable state, which is also why a settings change takes effect on
// the next step without a restart.
func (a *App) readConf() appConf {
	var c appConf
	b, err := os.ReadFile(a.confPath())
	if err != nil {
		return c.withDefaults()
	}
	legacyRoot := "" // AUDIOCPP_MODELS named the parent; voices/ was implied
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "LLM_SERVER":
			c.Server = v
		case "LLM_MODEL":
			c.Model = v
		case "LLM_API_KEY":
			c.Key = v
		case "AUDIOCPP_SERVER":
			c.TTS = strings.TrimRight(strings.TrimSpace(v), "/")
		case "AUDIOCPP_API_KEY":
			c.TTSKey = v
		case "AUDIOCPP_VOICES":
			c.Voices = v
		case "AUDIOCPP_MODELS":
			legacyRoot = v
		case "AUDIOCPP_ASR_MODEL":
			c.ASRModel = v
		case "AUDIOCPP_DIAR_MODEL":
			c.DiarModel = v
		case "FFMPEG":
			c.FFmpeg = v
		case "AUDIOCPP_TTS_MODEL":
			c.TTSModel = v
			// AUDIOCPP_LANGUAGE was here, and is now the project's (Project.Language).
			// An old conf file still carrying the key falls through unread, the same
			// way SD_MODEL below does.
		case "SD_SERVER":
			c.SD = strings.TrimRight(strings.TrimSpace(v), "/")
			// SD_MODEL was here. It named the weights the Test button held the
			// server to; the server reports them itself, so an old conf file
			// still carrying the key just falls through unread.
		case "SD_API_KEY":
			c.SDKey = v
		}
	}
	if c.Voices == "" && legacyRoot != "" {
		c.Voices = filepath.Join(legacyRoot, "voices")
	}
	// the runners read no config of their own; this is where ffTool learns
	// what the settings box says, on the same read that feeds the step
	ff := strings.TrimSpace(c.FFmpeg)
	ffSet.Store(&ff)
	return c.withDefaults()
}

func (a *App) writeConf(c appConf) error {
	c = c.withDefaults() // a cleared box means the default, never an empty flag
	body := fmt.Sprintf(`# Endpoints and local tools used by the pipeline (written by the GUI's
# settings dialog). Bash-sourceable -- keep this file chmod 600, the key is a
# credential.
LLM_SERVER=%q
LLM_MODEL=%q
LLM_API_KEY=%q
# audio.cpp server -- it speaks the narration and listens for Prepare; empty
# means 127.0.0.1:%d. Autocut only talks to it over HTTP, never starts it --
# starting it is the job of whoever runs the stack.
AUDIOCPP_SERVER=%q
AUDIOCPP_API_KEY=%q

# The reference-voice wavs the voice picker lists; "Add sample…" converts new
# ones into here, and the folder is chosen on the Narrate step, beside the list
# it fills. The compose file mounts this same folder into the server as its
# voice library. Nothing else is read from disk: model weights and their paths
# are audiocpp-server.json's business, on the server's side.
AUDIOCPP_VOICES=%q
# The ids the server lists for its three jobs -- transcribe, tell speakers
# apart, speak the narration.
AUDIOCPP_ASR_MODEL=%q
AUDIOCPP_DIAR_MODEL=%q
AUDIOCPP_TTS_MODEL=%q

# Which ffmpeg every step shells out to; empty means whichever one is on PATH,
# which is what it should be unless this machine has more than one. ffprobe is
# taken from the same folder as whatever is named here.
FFMPEG=%q

# stable-diffusion.cpp's sd-server -- it draws the thumbnail on the Publish
# step; empty means 127.0.0.1:%d. There is no model key: the server serves the
# one model it was started with (SD_ARGS in cpp/run.sh), and nothing autocut
# sends can change it.
SD_SERVER=%q
SD_API_KEY=%q
`, c.Server, c.Model, c.Key, ttsPort, c.TTS, c.TTSKey,
		c.Voices, c.ASRModel, c.DiarModel, c.TTSModel, c.FFmpeg,
		sdPort, c.SD, c.SDKey)
	return os.WriteFile(a.confPath(), []byte(body), 0o600)
}

// fetchModels asks the server for its model list.
func fetchModels(server, key string) ([]string, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(server, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	bearer(req, key)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server answered %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("server lists no models")
	}
	return ids, nil
}

// llmRoundTrip is one completion against the configured server, shaped like
// the pipeline's own execute mode, thinking off and all: a reasoning model
// left to think spends the whole budget on it and answers with empty content,
// which is a pass that proves nothing. The content is either a plain string or
// a parts array (txtPart/imgPart), which is the whole difference between the
// two Test buttons that call it.
func llmRoundTrip(c appConf, content any, timeout time.Duration) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    []map[string]any{msg("user", content)},
		"temperature": 0.6,
		"max_tokens":  16, // one word and a stop; a rambling model is cut off here
		"chat_template_kwargs": map[string]any{
			"preserve_thinking": true, "enable_thinking": false,
		},
	})
	req, err := http.NewRequest("POST", strings.TrimRight(c.Server, "/")+"/v1/chat/completions",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	bearer(req, c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%s: unreadable answer", resp.Status)
	}
	if resp.StatusCode != 200 {
		if out.Error.Message != "" {
			return "", fmt.Errorf("%s: %s", resp.Status, out.Error.Message)
		}
		return "", fmt.Errorf("server answered %s", resp.Status)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no answer -- is %q the id the server lists?", c.Model)
	}
	reply := strings.TrimSpace(out.Choices[0].Message.Content)
	if reply == "" {
		return "", fmt.Errorf("%q answered with empty content -- it is probably spending "+
			"the token budget on reasoning", c.Model)
	}
	return reply, nil
}

// testLLM does one real round trip -- the model id and the key are only proven
// by a completion, not by a model list, and those are exactly what gets typed
// wrong.
//
// It is deliberately the smallest completion that still proves those two
// things. A warm server answers in well under a second; when this takes long it
// is the server loading the model, which no shorter request avoids -- hence the
// generous timeout and the elapsed time in the report.
func testLLM(c appConf) (string, error) {
	start := time.Now()
	reply, err := llmRoundTrip(c, "Reply with the single word: ok", 60*time.Second)
	if err != nil {
		return "", err
	}
	if r := []rune(reply); len(r) > 40 { // by rune: the answer can be anything
		reply = string(r[:40]) + "…"
	}
	return fmt.Sprintf("%s answered in %.1f s: %q", c.Model, time.Since(start).Seconds(), reply), nil
}

// visionProbe is the sample image the vision test shows the model: a plain red
// square, generated here rather than shipped, as the data URL the request
// carries. Small on purpose -- 48 px is enough pixels for any vision encoder,
// and few enough tokens to keep the test as quick as the text one.
func visionProbe() string {
	img := image.NewRGBA(image.Rect(0, 0, 48, 48))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 220, A: 255}), image.Point{}, draw.Src)
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// testVision asks the same model about a picture. A model that describes
// frames is what the describer is built on, and "can it see at all" is exactly what a
// completion of words cannot prove: a text-only model, or a vision model
// served without its mmproj file, passes the text test and then fails minutes
// into a run -- or worse, answers every frame from the file name alone. One
// red square settles it: the answer is either the colour or a confession.
func testVision(c appConf) (string, error) {
	content := []map[string]any{
		txtPart("In one word: what colour is this square?"),
		{"type": "image_url", "image_url": map[string]any{"url": visionProbe()}},
	}
	start := time.Now()
	// more generous than the text test: an image prompt makes a cold server
	// load the vision encoder too
	reply, err := llmRoundTrip(c, content, 120*time.Second)
	if err != nil {
		return "", err
	}
	if r := []rune(reply); len(r) > 40 {
		reply = string(r[:40]) + "…"
	}
	if !strings.Contains(strings.ToLower(reply), "red") {
		return "", fmt.Errorf("shown a plain red square, %q answered %q -- it is not seeing the "+
			"image. Prepare sends video frames to this model: it needs a vision model, served with "+
			"its mmproj/vision file", c.Model, reply)
	}
	return fmt.Sprintf("%s saw the red square in %.1f s: %q", c.Model, time.Since(start).Seconds(), reply), nil
}

// audioProbe is the half both audio.cpp tests share: something answers on that
// port, and it answers like audio.cpp.
func audioProbe(url, key string) (map[string]audioModel, time.Duration, error) {
	url = strings.TrimRight(url, "/")
	start := time.Now()
	req, err := http.NewRequest("GET", url+"/health", nil)
	if err != nil {
		return nil, 0, err
	}
	bearer(req, key)
	if _, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err != nil {
		return nil, 0, fmt.Errorf("nothing answering: %w", err)
	}
	cat, err := audioCatalog(url, key)
	if err != nil {
		return nil, 0, fmt.Errorf("%s does not look like an audio.cpp server: %w", url, err)
	}
	if len(cat) == 0 {
		return nil, 0, fmt.Errorf("healthy, but serving no models")
	}
	return cat, time.Since(start), nil
}

// testTTS checks the thing that actually matters about the endpoint for
// speaking: that what it serves can clone a voice. A server on the right port
// with only the step-1 models loaded is the failure this catches.
func testTTS(url, key string) (string, error) {
	cat, took, err := audioProbe(url, key)
	if err != nil {
		return "", err
	}
	clone := ""
	for _, id := range sortedKeys(cat) {
		if m := cat[id]; m.Family == "index_tts2" || m.Task == "clon" {
			clone = id
			break
		}
	}
	if clone == "" {
		return "", fmt.Errorf("serves %s -- none of them can clone a voice", catalogIDs(cat))
	}
	return fmt.Sprintf("healthy in %.0f ms, will narrate with %q (of %d model(s))",
		float64(took.Milliseconds()), clone, len(cat)), nil
}

// testAudioModel is the same question for one of the server's ids: is it
// really in the catalog, and is it declared for the task we will ask of it.
// One id per button, because a combined check answers "something is wrong"
// when the question was "which one". Getting this wrong is otherwise a run
// that starts, extracts frames for minutes, and then stops on "unknown model".
//
// Everything here is learned over HTTP -- the GUI does not read the server's
// config file, wherever the server may be running. So the error can only name
// the two ways a catalog grows, and neither is something autocut can do for
// you: registering a model needs its family and the path to its weights, which
// is knowledge of the server's machine, not of this session.
func testAudioModel(url, key, id, task, what string) (string, error) {
	cat, took, err := audioProbe(url, key)
	if err != nil {
		return "", err
	}
	m, ok := cat[id]
	if !ok {
		return "", fmt.Errorf("no model %q to %s -- the server serves %s. Add it either in "+
			"the server's own browser UI at %s (its model page installs and loads one live, "+
			"if the server was started with --ui-management), or in the audiocpp-server.json "+
			"it reads at startup, followed by docker compose up -d --force-recreate audio "+
			"-- recreate, because a restarted container can keep a stale copy of the edited file",
			id, what, catalogIDs(cat), url)
	}
	if m.Task != "" && m.Task != task {
		return "", fmt.Errorf("%q is declared task %q, and cannot %s", id, m.Task, what)
	}
	return fmt.Sprintf("%q is served, answered in %.0f ms", id, float64(took.Milliseconds())), nil
}

// What the pipeline asks ffmpeg for by name. Each of these is a real build
// option, not a given: rubberband and libx264 need --enable-gpl, and the
// subtitles filter needs libass. A build missing one works perfectly until the
// step that uses it, which is minutes into a render.
var (
	// drawtext used to be here for the Publish step's thumbnail title. The
	// title is now lettered by the image model itself (publish.go), so requiring
	// drawtext would fail a build over a filter nothing asks for any more.
	ffFilters  = []string{"rubberband", "subtitles", "loudnorm", "atempo", "amix", "adelay"}
	ffEncoders = []string{"libx264", "libx265", "aac", "libopus"}
)

// ffMissing lists which of want this build does not have. listFlag is -filters
// or -encoders; both print one component per line with the name in the second
// field, after a flags column.
func ffMissing(bin, listFlag string, want []string) []string {
	out, err := exec.Command(bin, "-hide_banner", listFlag).Output()
	if err != nil {
		return want
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			have[f[1]] = true
		}
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

// testFFmpeg checks the build named by the settings box -- or, with the box
// empty, whichever ffmpeg is on PATH. Failures report the path too: "which
// ffmpeg is it finding" is half of every ffmpeg problem, and PATH here is the
// GUI's, not the shell's.
func testFFmpeg(bin string) (string, error) {
	bin = strings.TrimSpace(bin)
	ff, err := exec.LookPath(or(bin, "ffmpeg"))
	if err != nil {
		if bin != "" {
			return "", fmt.Errorf("%s will not run: %w -- leave the box empty to use PATH", bin, err)
		}
		return "", fmt.Errorf("not on PATH -- every step shells out to it (Arch: pacman -S ffmpeg)")
	}
	// beside it, never off PATH: the pair has to match, and ffTool pairs them
	probe := filepath.Join(filepath.Dir(ff), "ffprobe")
	if _, err := exec.LookPath(probe); err != nil {
		return "", fmt.Errorf("found ffmpeg at %s, but no ffprobe beside it -- both are used", ff)
	}
	out, err := exec.Command(ff, "-hide_banner", "-version").Output()
	if err != nil {
		return "", fmt.Errorf("%s will not run: %w", ff, err)
	}
	ver := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	where := ff
	if bin == "" {
		where += " (off PATH)" // which one PATH gave is the thing worth seeing
	}
	missing := append(ffMissing(ff, "-filters", ffFilters),
		ffMissing(ff, "-encoders", ffEncoders)...)
	if len(missing) > 0 {
		return "", fmt.Errorf("%s at %s is built without %s -- the steps that need "+
			"those will fail mid-run", ver, where, strings.Join(missing, ", "))
	}
	return fmt.Sprintf("%s\nat %s, with every filter and encoder the pipeline uses", ver, where), nil
}

// testBadge is a Test button's verdict, at the row that asked: a spinner while
// the test runs, then a green check or a red cross, with the reason on hover.
// The log below says the same in words; the badge is what can be read at a
// glance, and it stays put while other rows are tested.
type testBadge struct {
	stack *gtk.Stack
	spin  *gtk.Spinner
}

func newTestBadge() *testBadge {
	b := &testBadge{stack: gtk.NewStack(), spin: gtk.NewSpinner()}
	ok := gtk.NewImageFromIconName("object-select-symbolic")
	ok.AddCSSClass("test-ok")
	bad := gtk.NewImageFromIconName("window-close-symbolic")
	bad.AddCSSClass("test-bad")
	b.stack.AddNamed(gtk.NewLabel(" "), "idle")
	b.stack.AddNamed(b.spin, "busy")
	b.stack.AddNamed(ok, "ok")
	b.stack.AddNamed(bad, "bad")
	return b
}

func (b *testBadge) busy() {
	b.stack.SetTooltipText("")
	b.spin.Start()
	b.stack.SetVisibleChildName("busy")
}

func (b *testBadge) done(ok bool, why string) {
	b.spin.Stop()
	b.stack.SetTooltipText(why) // the words, where the symbol is
	name := "bad"
	if ok {
		name = "ok"
	}
	b.stack.SetVisibleChildName(name)
}

func (a *App) setupDialog() {
	c := a.readConf()

	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle("Settings")
	win.SetDefaultSize(680, -1)

	server := gtk.NewEntry()
	server.SetText(c.Server)
	server.SetPlaceholderText("https://ai.example.com")
	server.SetHExpand(true)

	key := gtk.NewPasswordEntry()
	key.SetShowPeekIcon(true)
	key.SetText(c.Key)
	key.SetHExpand(true)

	model := gtk.NewEntry()
	model.SetText(c.Model)
	model.SetPlaceholderText("model id exactly as the server lists it")
	model.SetHExpand(true)

	var ids []string
	pick := gtk.NewDropDownFromStrings([]string{"(fetch models first)"})
	pick.SetHExpand(true)
	use := gtk.NewButtonWithLabel("Use")
	use.SetSensitive(false)
	use.ConnectClicked(func() {
		if i := int(pick.Selected()); i >= 0 && i < len(ids) {
			model.SetText(ids[i])
		}
	})

	// the audio.cpp server that speaks; empty means the compose service on the
	// loopback port. Autocut only ever talks to it -- starting it is the stack's
	// job, not a side effect of opening this dialog.
	tts := gtk.NewEntry()
	tts.SetText(c.TTS)
	tts.SetPlaceholderText(fmt.Sprintf("empty = http://127.0.0.1:%d", ttsPort))
	tts.SetHExpand(true)

	// every server gets a key box, shaped like the LLM's: the local stack wants
	// none -- blank sends none -- but any of these can sit behind a proxy that
	// does, and the one that can not be authenticated is the one that ends up
	// exposed on the LAN instead
	ttsKey := gtk.NewPasswordEntry()
	ttsKey.SetShowPeekIcon(true)
	ttsKey.SetText(c.TTSKey)
	ttsKey.SetHExpand(true)

	// the sd.cpp server the Publish step draws its thumbnail on -- the compose
	// "sd" service by default, like the one above
	sd := gtk.NewEntry()
	sd.SetText(c.SD)
	sd.SetPlaceholderText(fmt.Sprintf("empty = http://127.0.0.1:%d", sdPort))
	sd.SetHExpand(true)

	sdKey := gtk.NewPasswordEntry()
	sdKey.SetShowPeekIcon(true)
	sdKey.SetText(c.SDKey)
	sdKey.SetHExpand(true)

	// the log: what each test asked and what came back, in order -- the badge
	// on the row says pass or fail, this says why, and it keeps saying it after
	// the next click. Mirrored into the main log so it outlives the dialog.
	logView, logScroll := newLogPane(110)
	// collapsed like the main window's log: the badges answer the usual
	// question, and the words are one click away -- except on a failure, which
	// opens it, because then the words ARE the answer
	logExp := gtk.NewExpander("Log")
	logExp.SetChild(logScroll)
	// open, the log is the dialog's one stretchy row -- enlarging the window
	// enlarges it. Closed, it hands the height back rather than holding an
	// empty stretch of dialog open. Following the property rather than setting
	// it in a click handler keeps the failure auto-open in step too.
	//
	// Both widgets, and that is the whole trick: vexpand on the expander alone
	// only wins the row extra height, which the expander then spends on empty
	// space below a child still sitting at its natural 110 px. The scroller has
	// to be told to fill what the expander won.
	logGrow := func() {
		on := logExp.Expanded()
		logScroll.SetVExpand(on)
		logExp.SetVExpand(on)
	}
	logExp.NotifyProperty("expanded", logGrow)
	logGrow()
	slog := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		buf := logView.Buffer()
		end := buf.EndIter()
		buf.Insert(end, s+"\n")
		mark := buf.CreateMark("", buf.EndIter(), false)
		logView.ScrollToMark(mark, 0, false, 0, 1)
		buf.DeleteMark(mark)
		a.logf("settings: %s", s)
	}

	fetch := gtk.NewButtonWithLabel("Fetch models")
	fetch.ConnectClicked(func() {
		slog("LLM: querying %s for its model list …", server.Text())
		srv, k := server.Text(), key.Text()
		go func() {
			got, err := fetchModels(srv, k)
			glib.IdleAdd(func() {
				if err != nil {
					logExp.SetExpanded(true)
					slog("LLM: fetch FAILED: %v", err)
					return
				}
				ids = got
				pick.SetModel(gtk.NewStringList(ids))
				use.SetSensitive(true)
				slog("LLM: %d model(s) -- pick one and press Use", len(ids))
			})
		}()
	})

	// every Test behaves the same way: read the boxes on the GUI thread, work
	// in the background, and land the verdict twice -- as a badge on the row
	// that asked, and as the words in the log. The tests speak to what is typed,
	// not to what is saved: the point is to find out whether a setting works
	// before committing it.
	//
	// Each one also lands in runAll, which is the whole of what Test All is:
	// the same presses, made at once. The buttons stay the source of truth --
	// a run in flight has its button greyed, and the guard reads that rather
	// than keeping a second list of what is busy.
	var runAll []func()
	hook := func(btn *gtk.Button, badge *testBadge, name string, prep func() (string, func() (string, error))) {
		run := func() {
			if !btn.Sensitive() {
				return // already running; its verdict is on the way
			}
			doing, job := prep()
			slog("%s: %s", name, doing)
			btn.SetSensitive(false)
			badge.busy()
			go func() {
				got, err := job()
				glib.IdleAdd(func() {
					btn.SetSensitive(true)
					if err != nil {
						badge.done(false, err.Error())
						logExp.SetExpanded(true)
						slog("%s FAILED: %v", name, err)
						return
					}
					badge.done(true, got)
					slog("%s ok — %s", name, got)
				})
			}()
		}
		btn.ConnectClicked(run)
		runAll = append(runAll, run)
	}
	// the audio.cpp tests all aim at the same server; empty means the compose
	// service on the loopback port
	audioTarget := func() string {
		if u := strings.TrimSpace(tts.Text()); u != "" {
			return u
		}
		return fmt.Sprintf("http://127.0.0.1:%d", ttsPort)
	}

	testLLMBtn := gtk.NewButtonWithLabel("Test")
	testLLMBtn.SetTooltipText("Ask this server and model for one short completion")
	llmBadge := newTestBadge()
	hook(testLLMBtn, llmBadge, "LLM", func() (string, func() (string, error)) {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text()}
		return "asking " + cc.Server + " for one completion …",
			func() (string, error) { return testLLM(cc) }
	})

	// the same model, shown a picture: the describer works frames through it, and
	// whether it can see at all is the one thing the text round trip cannot say
	testVisBtn := gtk.NewButtonWithLabel("Test")
	testVisBtn.SetTooltipText("Show this model a small sample image and check it names what it sees")
	visBadge := newTestBadge()
	hook(testVisBtn, visBadge, "LLM vision", func() (string, func() (string, error)) {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text()}
		return "showing " + or(cc.Model, "the model") + " a red square …",
			func() (string, error) { return testVision(cc) }
	})

	testTTSBtn := gtk.NewButtonWithLabel("Test")
	testTTSBtn.SetTooltipText("Check that an audio.cpp server answers and serves a voice-cloning model")
	ttsBadge := newTestBadge()
	hook(testTTSBtn, ttsBadge, "TTS", func() (string, func() (string, error)) {
		url, k := audioTarget(), ttsKey.Text()
		return "asking " + url + " for a voice-cloning model …",
			func() (string, error) { return testTTS(url, k) }
	})

	// a box, because on a machine with two ffmpegs the right one is not always
	// the one PATH finds first. The placeholder is what PATH gives right now --
	// PATH here is the GUI's, not the shell's, and seeing which one that is
	// answers half the ffmpeg questions before anything is typed.
	ff := gtk.NewEntry()
	ff.SetText(c.FFmpeg)
	ff.SetHExpand(true)
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ff.SetPlaceholderText("empty = " + p)
	} else {
		ff.SetPlaceholderText("empty = off PATH, where none was found")
	}

	testFFBtn := gtk.NewButtonWithLabel("Test")
	testFFBtn.SetTooltipText("Check ffmpeg and ffprobe, and that this build has the filters and encoders the pipeline uses")
	ffBadge := newTestBadge()
	hook(testFFBtn, ffBadge, "ffmpeg", func() (string, func() (string, error)) {
		bin := strings.TrimSpace(ff.Text())
		where := "the build on PATH"
		if bin != "" {
			where = bin
		}
		return "checking " + where + " …",
			func() (string, error) { return testFFmpeg(bin) }
	})

	// Prepare's side of the same server: which of its models does which job.
	// Model ids, not paths -- what the weights are and how they run is settled
	// in audiocpp-server.json, where the server can act on it.
	entry := func(text, placeholder, tip string) *gtk.Entry {
		e := gtk.NewEntry()
		e.SetText(text)
		e.SetPlaceholderText(placeholder)
		e.SetTooltipText(tip)
		e.SetHExpand(true)
		return e
	}
	// the voices folder is deliberately NOT here any more: it is chosen on the
	// Narrate step, beside the voice list it fills, where changing it can reload
	// that list live instead of asking for a restart
	ttsm := entry(c.TTSModel, defTTSModel,
		"Id of the voice-cloning model the narration is spoken through, exactly as the server lists it")
	asrModel := entry(c.ASRModel, defASRModel, "Id of the speech-to-text model, exactly as the server lists it")
	diarModel := entry(c.DiarModel, defDiarModel, "Id of the diarization model — the one that tells speakers apart")
	testTTSMBtn := gtk.NewButtonWithLabel("Test")
	testTTSMBtn.SetTooltipText("Check that the audio.cpp server serves this voice-cloning model")
	ttsmBadge := newTestBadge()
	hook(testTTSMBtn, ttsmBadge, "TTS model", func() (string, func() (string, error)) {
		url, k, id := audioTarget(), ttsKey.Text(), or(ttsm.Text(), defTTSModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) { return testAudioModel(url, k, id, "clon", "clone a voice") }
	})

	testASRBtn := gtk.NewButtonWithLabel("Test")
	testASRBtn.SetTooltipText("Check that the audio.cpp server really serves this model, declared for speech-to-text")
	asrBadge := newTestBadge()
	hook(testASRBtn, asrBadge, "ASR", func() (string, func() (string, error)) {
		url, k, id := audioTarget(), ttsKey.Text(), or(asrModel.Text(), defASRModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) { return testAudioModel(url, k, id, "asr", "transcribe") }
	})

	testDiarBtn := gtk.NewButtonWithLabel("Test")
	testDiarBtn.SetTooltipText("Check that the audio.cpp server really serves this model, declared for diarization")
	diarBadge := newTestBadge()
	hook(testDiarBtn, diarBadge, "diarization", func() (string, func() (string, error)) {
		url, k, id := audioTarget(), ttsKey.Text(), or(diarModel.Text(), defDiarModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) {
				return testAudioModel(url, k, id, "diar", "tell speakers apart")
			}
	})

	// one Test for both boxes: the endpoint and the model are one question here
	// -- the server has exactly one model, so "does it answer" and "is it the
	// right one" are answered by the same capabilities call
	sdTarget := func() string {
		if u := strings.TrimRight(strings.TrimSpace(sd.Text()), "/"); u != "" {
			return u
		}
		return fmt.Sprintf("http://127.0.0.1:%d", sdPort)
	}
	testSDBtn := gtk.NewButtonWithLabel("Test")
	testSDBtn.SetTooltipText("Check that an sd.cpp server answers, can draw an image, and say which weights it has loaded")
	sdBadge := newTestBadge()
	hook(testSDBtn, sdBadge, "sd.cpp", func() (string, func() (string, error)) {
		url, k := sdTarget(), sdKey.Text()
		return fmt.Sprintf("asking %s what it has loaded …", url),
			func() (string, error) { return testSD(url, k) }
	})

	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	save.ConnectClicked(func() {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text(),
			TTS:    strings.TrimRight(strings.TrimSpace(tts.Text()), "/"),
			TTSKey: ttsKey.Text(),
			// the voices folder has no box here: it is edited on the Narrate
			// step, and Save must not wipe what was chosen there
			Voices:    c.Voices,
			ASRModel:  asrModel.Text(),
			DiarModel: diarModel.Text(),
			TTSModel:  strings.TrimSpace(ttsm.Text()),
			SD:        strings.TrimRight(strings.TrimSpace(sd.Text()), "/"),
			SDKey:     sdKey.Text(),
			FFmpeg:    strings.TrimSpace(ff.Text()),
		}
		if err := a.writeConf(cc); err != nil {
			logExp.SetExpanded(true)
			slog("save FAILED: %v", err)
			return
		}
		// a different server has a different catalog; the cached model id and
		// the "already listening on" note both belong to the old one
		a.ttsModel, a.audioNoted = "", ""
		a.setStatus("settings saved to " + a.confPath())
		win.Close()
	})
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { win.Close() })

	grid := gtk.NewGrid()
	grid.SetRowSpacing(10)
	grid.SetColumnSpacing(10)
	grid.SetMarginTop(16)
	grid.SetMarginBottom(16)
	grid.SetMarginStart(16)
	grid.SetMarginEnd(16)
	lbl := func(s string) *gtk.Label { l := gtk.NewLabel(s); l.SetXAlign(1); return l }
	// A section is its title and an ⓘ. The explanations are worth having --
	// which API a box is expected to speak is not guessable from the box --
	// but they are read once, when something is wrong, and a page of prose
	// above every row is in the way on all the other openings. So the title
	// stays on the page and the words go behind the button.
	head := func(title, why string) *gtk.Box {
		l := gtk.NewLabel(title)
		l.SetXAlign(0)
		l.AddCSSClass("heading")
		body := gtk.NewLabel(why)
		body.SetXAlign(0)
		body.SetWrap(true)
		body.SetMaxWidthChars(64) // wrapping needs a width to wrap at
		body.SetSelectable(true)  // endpoints are for copying into a terminal
		body.SetMarginTop(4)
		body.SetMarginBottom(4)
		body.SetMarginStart(4)
		body.SetMarginEnd(4)
		pop := gtk.NewPopover()
		pop.SetChild(body)
		info := gtk.NewMenuButton()
		info.SetIconName("help-about-symbolic")
		info.SetTooltipText("What this is, and which API it expects")
		info.AddCSSClass("flat")
		info.SetPopover(pop)
		box := gtk.NewBox(gtk.OrientationHorizontal, 6)
		box.SetMarginTop(6)
		box.Append(l)
		box.Append(info)
		return box
	}
	// four columns: what it is, the value, the verdict, its Test -- the check
	// lands beside the box it judges, before the button that made it. Every
	// server section reads the same way: Server, API key, then whatever that
	// server is asked for -- the same words for the same things.
	grid.Attach(head("Writing", "The model that describes the footage, proposes the cut and "+
		"writes the narration.\n\nExpects an OpenAI-compatible chat API: POST /v1/chat/completions, "+
		"and GET /v1/models for the Fetch models button. The key is sent as "+
		"Authorization: Bearer …; leave it empty for a server that wants none. "+
		"Test asks for one short completion; the Test beside Model shows it a small "+
		"picture, because describing footage needs a model that can see."), 0, 0, 4, 1)
	grid.Attach(lbl("Server:"), 0, 1, 1, 1)
	grid.Attach(server, 1, 1, 1, 1)
	grid.Attach(llmBadge.stack, 2, 1, 1, 1)
	grid.Attach(testLLMBtn, 3, 1, 1, 1)
	grid.Attach(lbl("API key:"), 0, 2, 1, 1)
	grid.Attach(key, 1, 2, 3, 1)
	// the model's own Test is the vision one: the server round trip is the row
	// above, and what is left to prove about the MODEL is that it can see
	grid.Attach(lbl("Model:"), 0, 3, 1, 1)
	grid.Attach(model, 1, 3, 1, 1)
	grid.Attach(visBadge.stack, 2, 3, 1, 1)
	grid.Attach(testVisBtn, 3, 3, 1, 1)
	grid.Attach(fetch, 0, 4, 1, 1)
	grid.Attach(pick, 1, 4, 2, 1)
	grid.Attach(use, 3, 4, 1, 1)

	// one server, two sections: the same server does the listening below
	grid.Attach(head("Speaking", "The audio.cpp server that speaks the narration.\n\n"+
		"Expects an OpenAI-compatible speech API: POST /v1/audio/speech, and GET /v1/models "+
		"to check the model id. Empty means the compose service on loopback. Autocut only "+
		"ever talks to this server over HTTP -- starting it is the job of whoever runs the "+
		"stack.\n\nThe TTS model is the id the server lists, not a file: which weights they "+
		"are, and on which backend, is set in audiocpp-server.json."), 0, 5, 4, 1)
	grid.Attach(lbl("Server:"), 0, 6, 1, 1)
	grid.Attach(tts, 1, 6, 1, 1)
	grid.Attach(ttsBadge.stack, 2, 6, 1, 1)
	grid.Attach(testTTSBtn, 3, 6, 1, 1)
	grid.Attach(lbl("API key:"), 0, 7, 1, 1)
	grid.Attach(ttsKey, 1, 7, 3, 1)
	grid.Attach(lbl("TTS model:"), 0, 8, 1, 1)
	grid.Attach(ttsm, 1, 8, 1, 1)
	grid.Attach(ttsmBadge.stack, 2, 8, 1, 1)
	grid.Attach(testTTSMBtn, 3, 8, 1, 1)

	// the one local tool here: no API, a binary. Which ffmpeg answers, and what
	// it was built with, decides whether the render works at all
	grid.Attach(head("Cutting", "ffmpeg, which every step shells out to. Not a server: a "+
		"local binary.\n\nLeave the box empty and it comes off PATH like any other tool, "+
		"which is what almost every machine wants. Give a path -- /usr/bin/ffmpeg, or a "+
		"build of your own -- and that one is used instead, with ffprobe taken from the "+
		"same folder; both are needed, and a mismatched pair is its own kind of bug.\n\n"+
		"Test runs it and checks this build has the filters and encoders the pipeline "+
		"uses: rubberband, subtitles, loudnorm, atempo, amix, adelay, libx264, libx265, "+
		"aac, libopus. A build missing one works perfectly until the step that needs it, "+
		"which is minutes into a render."), 0, 9, 4, 1)
	grid.Attach(lbl("ffmpeg:"), 0, 10, 1, 1)
	grid.Attach(ff, 1, 10, 1, 1)
	grid.Attach(ffBadge.stack, 2, 10, 1, 1)
	grid.Attach(testFFBtn, 3, 10, 1, 1)

	// no server of its own: Prepare talks to the one named above. What is left
	// is which of its models to ask -- what language to ask them in is the
	// project's, on the Inputs page, where the footage it describes is
	grid.Attach(head("Listening", "Speech-to-text and diarization -- who said what, and who "+
		"is who -- on the same audio.cpp server as Speaking above, so there is no second "+
		"address to keep.\n\nExpects POST /v1/tasks/run, and GET /v1/models to check the "+
		"ids.\n\nThese are model ids as the server lists them, not files: which weights they "+
		"are, and on which backend, is set in audiocpp-server.json. Blank means the built-in "+
		"default. The server opens the project folder itself, so it has to see it at this "+
		"same path."), 0, 11, 4, 1)
	for i, row := range []struct {
		name  string
		w     *gtk.Entry
		btn   *gtk.Button
		badge *testBadge
	}{
		{"ASR model:", asrModel, testASRBtn, asrBadge},
		{"Diarization model:", diarModel, testDiarBtn, diarBadge},
	} {
		grid.Attach(lbl(row.name), 0, 12+i, 1, 1)
		grid.Attach(row.w, 1, 12+i, 1, 1)
		grid.Attach(row.badge.stack, 2, 12+i, 1, 1)
		grid.Attach(row.btn, 3, 12+i, 1, 1)
	}

	// the last step's server. No model row: unlike audio.cpp above, there is
	// no model id to send per request, so there is nothing here to choose. Test
	// reports which weights it found rather than checking them against a box.
	grid.Attach(head("Drawing", "The stable-diffusion.cpp server that paints the thumbnail on "+
		"the Publish step. Empty means the compose service on loopback.\n\nExpects sd.cpp's own "+
		"asynchronous API, not an OpenAI-shaped one: GET /sdcpp/v1/capabilities, POST "+
		"/sdcpp/v1/img_gen for a job id, then GET /sdcpp/v1/jobs/{id} until the picture "+
		"arrives.\n\nThere is no model box: sd-server loads one model when it starts and "+
		"nothing autocut sends can switch it, so Test reports which weights it found "+
		"instead of holding it to a name."), 0, 14, 4, 1)
	grid.Attach(lbl("Server:"), 0, 15, 1, 1)
	grid.Attach(sd, 1, 15, 1, 1)
	grid.Attach(sdBadge.stack, 2, 15, 1, 1)
	grid.Attach(testSDBtn, 3, 15, 1, 1)
	grid.Attach(lbl("API key:"), 0, 16, 1, 1)
	grid.Attach(sdKey, 1, 16, 3, 1)

	// the dialog's one row of verbs: Test All on the left, because it acts on
	// the rows above it and not on the dialog; Cancel and Save on the right,
	// where a dialog keeps its answers. Every verdict still lands on the row
	// that owns it -- this button only saves eight trips up the page.
	testAll := gtk.NewButtonWithLabel("Test All")
	testAll.SetTooltipText("Run every Test on this page at once — each verdict lands beside its own row")
	testAll.ConnectClicked(func() {
		for _, run := range runAll {
			run()
		}
	})
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.Append(testAll)
	spring := gtk.NewBox(gtk.OrientationHorizontal, 0)
	spring.SetHExpand(true)
	btns.Append(spring)
	btns.Append(cancel)
	btns.Append(save)
	grid.Attach(btns, 0, 17, 4, 1)

	// the log is the LAST row, below even the verbs: expanded it grows downward
	// into space the dialog adds, instead of shoving the buttons off the bottom
	// of the screen while a failure is being read
	grid.Attach(logExp, 0, 18, 4, 1)

	win.SetChild(grid)
	win.SetVisible(true)
}
