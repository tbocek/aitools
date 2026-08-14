package main

// Settings: edits llm.conf in place. It stays bash-sourceable (quoted values,
// no GUI-only syntax) so a shell can read it too. Both of the pipeline's HTTP
// endpoints live here -- the LLM that writes (descriptions, cuts, narration)
// and the audio.cpp server, which speaks the narration and does the listening
// in step 1 -- plus the handful of names the second one needs: which of its
// models transcribes, which one tells speakers apart, and in what language.
//
// Those were compiled in until they made the app run on exactly one machine:
// the paths were one person's, and the backend assumed an AMD card. The
// backend went away with the container -- where a model runs is now decided in
// audiocpp-server.json, by whoever runs the server.
//
// ffmpeg is deliberately NOT configurable -- it comes off PATH like any other
// tool -- but it IS shown and testable here, because it is the one local tool
// every step depends on.
//
// The dialog can query each server and say what it found, because the failures
// it is meant to catch are silent otherwise: a model id spelled differently
// than the server spells it (a hand-typed id with a stray suffix cost us a
// debug round once already), a TTS server that answers but serves the wrong
// family, and an ffmpeg built without the parts the render needs.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type appConf struct {
	Server, Model, Key string // the LLM that writes
	TTS                string // the audio.cpp server; blank = the compose default

	// What to ask that server for, and where the reference voices live on this
	// machine -- the ONE folder autocut still reads: the wavs the voice picker
	// lists, and where "Add sample…" converts new ones into. Model weights are
	// not read from anywhere here; their paths are audiocpp-server.json's
	// business, on the server's side. The model ids are the server's own --
	// what that json calls them -- and they are here rather than compiled in
	// because the models get replaced faster than the code does.
	Voices              string
	ASRModel, DiarModel string
	TTSModel            string
	Language            string
}

// The dev box this grew up on. A config line that is missing or blank means
// "whatever the build shipped with" -- an empty model id would otherwise fail
// with a message about nothing.
const (
	defVoices    = "/mnt/models/audiocpp/voices"
	defASRModel  = "parakeet-tdt"
	defDiarModel = "sortformer-diar"
	defTTSModel  = "index-tts2"
	defLanguage  = "en"
)

func or(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

// withDefaults fills the blanks. The TTS endpoint is pointedly not in here:
// empty there is a real answer, meaning "the compose service on loopback".
func (c appConf) withDefaults() appConf {
	c.Voices = or(c.Voices, defVoices)
	c.ASRModel = or(c.ASRModel, defASRModel)
	c.DiarModel = or(c.DiarModel, defDiarModel)
	c.TTSModel = or(c.TTSModel, defTTSModel)
	c.Language = or(c.Language, defLanguage)
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
		case "AUDIOCPP_VOICES":
			c.Voices = v
		case "AUDIOCPP_MODELS":
			legacyRoot = v
		case "AUDIOCPP_ASR_MODEL":
			c.ASRModel = v
		case "AUDIOCPP_DIAR_MODEL":
			c.DiarModel = v
		case "AUDIOCPP_TTS_MODEL":
			c.TTSModel = v
		case "AUDIOCPP_LANGUAGE":
			c.Language = v
		}
	}
	if c.Voices == "" && legacyRoot != "" {
		c.Voices = filepath.Join(legacyRoot, "voices")
	}
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
# audio.cpp server -- it speaks the narration and listens for step 1; empty
# means 127.0.0.1:%d. Autocut only talks to it over HTTP, never starts it --
# starting it is the job of whoever runs the stack.
AUDIOCPP_SERVER=%q

# The reference-voice wavs the voice picker lists; "Add sample…" converts new
# ones into here. The compose file mounts this same folder into the server as
# its voice library. Nothing else is read from disk: model weights and their
# paths are audiocpp-server.json's business, on the server's side.
AUDIOCPP_VOICES=%q
# The ids the server lists for its three jobs -- transcribe, tell speakers
# apart, speak the narration.
AUDIOCPP_ASR_MODEL=%q
AUDIOCPP_DIAR_MODEL=%q
AUDIOCPP_TTS_MODEL=%q
# what the ASR model is told to expect; wrong here transcribes into gibberish
AUDIOCPP_LANGUAGE=%q
`, c.Server, c.Model, c.Key, ttsPort, c.TTS,
		c.Voices, c.ASRModel, c.DiarModel, c.TTSModel, c.Language)
	return os.WriteFile(a.confPath(), []byte(body), 0o600)
}

// fetchModels asks the server for its model list.
func fetchModels(server, key string) ([]string, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(server, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
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

// testLLM does one real round trip -- the model id and the key are only proven
// by a completion, not by a model list, and those are exactly what gets typed
// wrong. The request is shaped like the pipeline's own execute mode, thinking
// off and all: a reasoning model left to think spends the whole budget on it
// and answers with empty content, which is a pass that proves nothing.
//
// It is deliberately the smallest completion that still proves those two
// things. A warm server answers in well under a second; when this takes long it
// is the server loading the model, which no shorter request avoids -- hence the
// generous timeout and the elapsed time in the report.
func testLLM(c appConf) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    []map[string]string{{"role": "user", "content": "Reply with the single word: ok"}},
		"temperature": 0.6,
		"max_tokens":  16, // "ok" and a stop; a rambling model is cut off here
		"chat_template_kwargs": map[string]any{
			"preserve_thinking": true, "enable_thinking": false,
		},
	})
	req, err := http.NewRequest("POST", strings.TrimRight(c.Server, "/")+"/v1/chat/completions",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
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
	if r := []rune(reply); len(r) > 40 { // by rune: the answer can be anything
		reply = string(r[:40]) + "…"
	}
	return fmt.Sprintf("%s answered in %.1f s: %q", c.Model, time.Since(start).Seconds(), reply), nil
}

// audioProbe is the half both audio.cpp tests share: something answers on that
// port, and it answers like audio.cpp.
func audioProbe(url string) (map[string]audioModel, time.Duration, error) {
	url = strings.TrimRight(url, "/")
	start := time.Now()
	if _, err := (&http.Client{Timeout: 15 * time.Second}).Get(url + "/health"); err != nil {
		return nil, 0, fmt.Errorf("nothing answering: %w", err)
	}
	cat, err := audioCatalog(url)
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
func testTTS(url string) (string, error) {
	cat, took, err := audioProbe(url)
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
func testAudioModel(url, id, task, what string) (string, error) {
	cat, took, err := audioProbe(url)
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

// testFFmpeg locates ffmpeg and ffprobe and checks this build has what the
// pipeline uses. Failures report the path too: "which ffmpeg is it finding" is
// half of every ffmpeg problem, and PATH here is the GUI's, not the shell's.
func testFFmpeg() (string, error) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("not on PATH -- every step shells out to it (Arch: pacman -S ffmpeg)")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", fmt.Errorf("found ffmpeg at %s, but no ffprobe -- both are used", ff)
	}
	out, err := exec.Command(ff, "-hide_banner", "-version").Output()
	if err != nil {
		return "", fmt.Errorf("%s will not run: %w", ff, err)
	}
	ver := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	where := ff
	if filepath.Dir(probe) != filepath.Dir(ff) {
		where += ", ffprobe at " + probe // a mismatched pair is worth seeing
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

	// the log: what each test asked and what came back, in order -- the badge
	// on the row says pass or fail, this says why, and it keeps saying it after
	// the next click. Mirrored into the main log so it outlives the dialog.
	logView := gtk.NewTextView()
	logView.SetEditable(false)
	logView.SetCursorVisible(false)
	logView.SetMonospace(true)
	logView.SetWrapMode(gtk.WrapWordChar)
	logScroll := gtk.NewScrolledWindow()
	logScroll.SetChild(logView)
	logScroll.SetMinContentHeight(110)
	logScroll.AddCSSClass("frame")
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
	hook := func(btn *gtk.Button, badge *testBadge, name string, prep func() (string, func() (string, error))) {
		btn.ConnectClicked(func() {
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
		})
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

	testTTSBtn := gtk.NewButtonWithLabel("Test")
	testTTSBtn.SetTooltipText("Check that an audio.cpp server answers and serves a voice-cloning model")
	ttsBadge := newTestBadge()
	hook(testTTSBtn, ttsBadge, "TTS", func() (string, func() (string, error)) {
		url := audioTarget()
		return "asking " + url + " for a voice-cloning model …",
			func() (string, error) { return testTTS(url) }
	})

	// the location without pressing anything: PATH here is the GUI's, which is
	// not always the shell's, and that difference is worth seeing on open
	ffLbl := gtk.NewLabel("")
	ffLbl.SetXAlign(0)
	ffLbl.SetSelectable(true) // a path is for copying into a terminal
	ffLbl.SetWrap(true)
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffLbl.SetText(p)
	} else {
		ffLbl.SetText("not found on PATH")
	}

	testFFBtn := gtk.NewButtonWithLabel("Test")
	testFFBtn.SetTooltipText("Check ffmpeg and ffprobe, and that this build has the filters and encoders the pipeline uses")
	ffBadge := newTestBadge()
	hook(testFFBtn, ffBadge, "ffmpeg", func() (string, func() (string, error)) {
		return "checking the build on PATH …", testFFmpeg
	})

	// step 1's side of the same server: which of its models does which job.
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
	voices := entry(c.Voices, defVoices, "The reference-voice wavs the voice picker lists; Add sample… converts new "+
		"ones into here. This is the only folder autocut reads — model weights are audiocpp-server.json's business")
	ttsm := entry(c.TTSModel, defTTSModel,
		"Id of the voice-cloning model the narration is spoken through, exactly as the server lists it")
	asrModel := entry(c.ASRModel, defASRModel, "Id of the speech-to-text model, exactly as the server lists it")
	diarModel := entry(c.DiarModel, defDiarModel, "Id of the diarization model — the one that tells speakers apart")
	lang := entry(c.Language, defLanguage, "Language the ASR model is told to expect — the wrong one transcribes into gibberish")

	testTTSMBtn := gtk.NewButtonWithLabel("Test")
	testTTSMBtn.SetTooltipText("Check that the audio.cpp server serves this voice-cloning model")
	ttsmBadge := newTestBadge()
	hook(testTTSMBtn, ttsmBadge, "TTS model", func() (string, func() (string, error)) {
		url, id := audioTarget(), or(ttsm.Text(), defTTSModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) { return testAudioModel(url, id, "clon", "clone a voice") }
	})

	testASRBtn := gtk.NewButtonWithLabel("Test")
	testASRBtn.SetTooltipText("Check that the audio.cpp server really serves this model, declared for speech-to-text")
	asrBadge := newTestBadge()
	hook(testASRBtn, asrBadge, "ASR", func() (string, func() (string, error)) {
		url, id := audioTarget(), or(asrModel.Text(), defASRModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) { return testAudioModel(url, id, "asr", "transcribe") }
	})

	testDiarBtn := gtk.NewButtonWithLabel("Test")
	testDiarBtn.SetTooltipText("Check that the audio.cpp server really serves this model, declared for diarization")
	diarBadge := newTestBadge()
	hook(testDiarBtn, diarBadge, "diarization", func() (string, func() (string, error)) {
		url, id := audioTarget(), or(diarModel.Text(), defDiarModel)
		return fmt.Sprintf("asking %s for %q …", url, id),
			func() (string, error) {
				return testAudioModel(url, id, "diar", "tell speakers apart")
			}
	})

	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	save.ConnectClicked(func() {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text(),
			TTS:       strings.TrimRight(strings.TrimSpace(tts.Text()), "/"),
			Voices:    voices.Text(),
			ASRModel:  asrModel.Text(),
			DiarModel: diarModel.Text(),
			TTSModel:  strings.TrimSpace(ttsm.Text()),
			Language:  lang.Text(),
		}
		if err := a.writeConf(cc); err != nil {
			logExp.SetExpanded(true)
			slog("save FAILED: %v", err)
			return
		}
		// a different server has a different catalog; the cached model id and
		// the "already listening on" note both belong to the old one
		a.ttsModel, a.audioNoted = "", ""
		note := ""
		// every step re-reads the file, so the rest takes effect on the next run
		// -- but the voice list was read once, when its page was built
		if cc.withDefaults().Voices != c.Voices {
			note = " — restart to list the voices in the new folder"
		}
		a.setStatus("settings saved to " + a.confPath() + note)
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
	head := func(s string) *gtk.Label {
		l := gtk.NewLabel(s)
		l.SetXAlign(0)
		l.SetMarginTop(6)
		l.AddCSSClass("heading")
		return l
	}
	// four columns: what it is, the value, the verdict, its Test -- the check
	// lands beside the box it judges, before the button that made it
	grid.Attach(head("Writing — the LLM that describes, cuts and narrates"), 0, 0, 4, 1)
	grid.Attach(lbl("Server:"), 0, 1, 1, 1)
	grid.Attach(server, 1, 1, 1, 1)
	grid.Attach(llmBadge.stack, 2, 1, 1, 1)
	grid.Attach(testLLMBtn, 3, 1, 1, 1)
	grid.Attach(lbl("API key:"), 0, 2, 1, 1)
	grid.Attach(key, 1, 2, 3, 1)
	grid.Attach(lbl("Model:"), 0, 3, 1, 1)
	grid.Attach(model, 1, 3, 3, 1)
	grid.Attach(fetch, 0, 4, 1, 1)
	grid.Attach(pick, 1, 4, 2, 1)
	grid.Attach(use, 3, 4, 1, 1)

	// one endpoint, two sections: the same server does the listening below.
	// The voices folder sits here because it is what speaking reads: the
	// reference wavs the picker lists and Add sample… writes.
	grid.Attach(head("Speaking — the audio.cpp server that synthesizes the narration"), 0, 5, 4, 1)
	grid.Attach(lbl("Endpoint:"), 0, 6, 1, 1)
	grid.Attach(tts, 1, 6, 1, 1)
	grid.Attach(ttsBadge.stack, 2, 6, 1, 1)
	grid.Attach(testTTSBtn, 3, 6, 1, 1)
	grid.Attach(lbl("TTS model:"), 0, 7, 1, 1)
	grid.Attach(ttsm, 1, 7, 1, 1)
	grid.Attach(ttsmBadge.stack, 2, 7, 1, 1)
	grid.Attach(testTTSMBtn, 3, 7, 1, 1)
	grid.Attach(lbl("Voices folder:"), 0, 8, 1, 1)
	grid.Attach(voices, 1, 8, 3, 1)

	// not configurable: it is taken from PATH like any other tool. Shown because
	// which ffmpeg answers, and what it was built with, decides whether the
	// render works
	grid.Attach(head("Cutting — ffmpeg, which every step shells out to"), 0, 9, 4, 1)
	grid.Attach(lbl("ffmpeg:"), 0, 10, 1, 1)
	grid.Attach(ffLbl, 1, 10, 1, 1)
	grid.Attach(ffBadge.stack, 2, 10, 1, 1)
	grid.Attach(testFFBtn, 3, 10, 1, 1)

	// no endpoint of its own: step 1 talks to the server named above. What is
	// left is which of its models to ask, and in what language
	grid.Attach(head("Listening — speech-to-text and diarization, on that same server"), 0, 11, 4, 1)
	foot := gtk.NewLabel("Model ids as the server lists them, not files: which weights they are, and on " +
		"which backend, is set in audiocpp-server.json. The server opens the project folder itself, " +
		"so it has to see it at this same path. Blank means the built-in default.")
	foot.SetXAlign(0)
	foot.SetWrap(true)
	foot.AddCSSClass("dim-label")
	grid.Attach(foot, 0, 12, 4, 1)
	for i, row := range []struct {
		name  string
		w     *gtk.Entry
		btn   *gtk.Button // a row with one keeps the entry two columns narrower
		badge *testBadge
	}{
		{"ASR model:", asrModel, testASRBtn, asrBadge},
		{"Diarization model:", diarModel, testDiarBtn, diarBadge},
		{"Language:", lang, nil, nil},
	} {
		grid.Attach(lbl(row.name), 0, 13+i, 1, 1)
		if row.btn == nil {
			grid.Attach(row.w, 1, 13+i, 3, 1)
			continue
		}
		grid.Attach(row.w, 1, 13+i, 1, 1)
		grid.Attach(row.badge.stack, 2, 13+i, 1, 1)
		grid.Attach(row.btn, 3, 13+i, 1, 1)
	}
	grid.Attach(logExp, 0, 16, 4, 1)

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(cancel)
	btns.Append(save)
	grid.Attach(btns, 0, 17, 4, 1)

	win.SetChild(grid)
	win.SetVisible(true)
}
