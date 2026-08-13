package main

// Settings: edits llm.conf in place. It stays bash-sourceable (quoted values,
// no GUI-only syntax) so a shell can read it too. Both of the pipeline's HTTP
// endpoints live here -- the LLM that writes (descriptions, cuts, narration)
// and the audio.cpp server that speaks -- and so does the local ASR and
// diarization stack, which is a container run per job rather than a service, so
// what it needs is an image, paths, a backend and a language.
//
// Those last ones were compiled in until they made the app run on exactly one
// machine: the paths were one person's, and "hip" is an AMD card talking.
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
	TTS                string // the audio.cpp server that speaks; blank = auto

	// Where the local audio.cpp stack lives and how it runs. Compiled in, these
	// made the app run on exactly one machine: the paths are one person's, and
	// "hip" is an AMD card talking. A GGUF is here too because the models get
	// replaced faster than the code does.
	Image, CLI, Models string
	ASRGGUF, DiarGGUF  string
	Backend, Language  string
}

// The dev box this grew up on. A config line that is missing or blank means
// "whatever the build shipped with" -- an empty image name would otherwise fail
// with a message about nothing.
const (
	defImage    = "audio:latest"
	defCLI      = "/home/arch/audio.cpp/build/bin/audiocpp_cli"
	defModels   = "/mnt/models/audiocpp"
	defASRGGUF  = "models/Parakeet-TDT-0.6B-v3-GGUF/parakeet-tdt-0.6b-v3-q8_0.gguf"
	defDiarGGUF = "models/Sortformer-Diar-4spk-v1-GGUF/sortformer-diar-4spk-v1-q8_0.gguf"
	defBackend  = "hip"
	defLanguage = "en"
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
	c.Image = or(c.Image, defImage)
	c.CLI = or(c.CLI, defCLI)
	c.Models = or(c.Models, defModels)
	c.ASRGGUF = or(c.ASRGGUF, defASRGGUF)
	c.DiarGGUF = or(c.DiarGGUF, defDiarGGUF)
	c.Backend = or(c.Backend, defBackend)
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
		case "AUDIOCPP_IMAGE":
			c.Image = v
		case "AUDIOCPP_CLI":
			c.CLI = v
		case "AUDIOCPP_MODELS":
			c.Models = v
		case "AUDIOCPP_ASR_GGUF":
			c.ASRGGUF = v
		case "AUDIOCPP_DIAR_GGUF":
			c.DiarGGUF = v
		case "AUDIOCPP_BACKEND":
			c.Backend = v
		case "AUDIOCPP_LANGUAGE":
			c.Language = v
		}
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
# audio.cpp TTS server; empty means 127.0.0.1:%d. Autocut only talks to it,
# never starts it -- that is "cd cpp && docker compose up -d audio".
AUDIOCPP_SERVER=%q

# Speech-to-text and diarization: a container run per job, no HTTP. The GGUF
# paths are inside the container, relative to the models mount.
AUDIOCPP_IMAGE=%q
AUDIOCPP_CLI=%q
AUDIOCPP_MODELS=%q
AUDIOCPP_ASR_GGUF=%q
AUDIOCPP_DIAR_GGUF=%q
# hip = AMD/ROCm, cuda = NVIDIA, cpu = no GPU
AUDIOCPP_BACKEND=%q
# what the ASR model is told to expect; wrong here transcribes into gibberish
AUDIOCPP_LANGUAGE=%q
`, c.Server, c.Model, c.Key, ttsPort, c.TTS,
		c.Image, c.CLI, c.Models, c.ASRGGUF, c.DiarGGUF, c.Backend, c.Language)
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

// testTTS checks the thing that actually matters about a TTS endpoint: that
// something answers, and that what it serves can clone a voice. A server on the
// right port with only an ASR model loaded is the failure this catches.
func testTTS(url string) (string, error) {
	url = strings.TrimRight(url, "/")
	client := &http.Client{Timeout: 15 * time.Second}
	start := time.Now()
	if _, err := client.Get(url + "/health"); err != nil {
		return "", fmt.Errorf("nothing answering: %w", err)
	}
	resp, err := client.Get(url + "/v1/models")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct{ ID, Family, Task string } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%s does not look like an audio.cpp server", url)
	}
	var ids []string
	clone := ""
	for _, m := range out.Data {
		ids = append(ids, m.ID)
		if m.Family == "index_tts2" || m.Task == "clon" {
			clone = m.ID
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("healthy, but serving no models")
	}
	if clone == "" {
		return "", fmt.Errorf("serves %s -- none of them can clone a voice", strings.Join(ids, ", "))
	}
	return fmt.Sprintf("healthy in %.0f ms, will narrate with %q (of %d model(s))",
		float64(time.Since(start).Milliseconds()), clone, len(ids)), nil
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

	msg := gtk.NewLabel("")
	msg.SetXAlign(0)
	msg.SetWrap(true)

	fetch := gtk.NewButtonWithLabel("Fetch models")
	fetch.ConnectClicked(func() {
		msg.SetText("querying " + server.Text() + " …")
		srv, k := server.Text(), key.Text()
		go func() {
			got, err := fetchModels(srv, k)
			glib.IdleAdd(func() {
				if err != nil {
					msg.SetText("fetch failed: " + err.Error())
					return
				}
				ids = got
				pick.SetModel(gtk.NewStringList(ids))
				use.SetSensitive(true)
				msg.SetText(fmt.Sprintf("%d model(s) -- pick one and press Use", len(ids)))
			})
		}()
	})

	// the tests speak to what is typed in the boxes, not to what is saved: the
	// point is to find out whether a setting works before committing it
	testLLMBtn := gtk.NewButtonWithLabel("Test")
	testLLMBtn.SetTooltipText("Ask this server and model for one short completion")
	testLLMBtn.ConnectClicked(func() {
		msg.SetText("asking " + server.Text() + " …")
		testLLMBtn.SetSensitive(false)
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text()}
		go func() {
			got, err := testLLM(cc)
			glib.IdleAdd(func() {
				testLLMBtn.SetSensitive(true)
				if err != nil {
					msg.SetText("LLM test failed: " + err.Error())
					return
				}
				msg.SetText("LLM ok — " + got)
			})
		}()
	})

	testTTSBtn := gtk.NewButtonWithLabel("Test")
	testTTSBtn.SetTooltipText("Check that an audio.cpp server answers and serves a voice-cloning model")
	testTTSBtn.ConnectClicked(func() {
		url := strings.TrimSpace(tts.Text())
		if url == "" {
			url = fmt.Sprintf("http://127.0.0.1:%d", ttsPort)
			msg.SetText("no endpoint set — testing the default " + url + " …")
		} else {
			msg.SetText("asking " + url + " …")
		}
		testTTSBtn.SetSensitive(false)
		go func() {
			got, err := testTTS(url)
			glib.IdleAdd(func() {
				testTTSBtn.SetSensitive(true)
				if err != nil {
					msg.SetText("TTS test failed: " + err.Error())
					return
				}
				msg.SetText("TTS ok — " + got)
			})
		}()
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
	testFFBtn.ConnectClicked(func() {
		msg.SetText("checking ffmpeg …")
		testFFBtn.SetSensitive(false)
		go func() {
			got, err := testFFmpeg()
			glib.IdleAdd(func() {
				testFFBtn.SetSensitive(true)
				if err != nil {
					msg.SetText("ffmpeg test failed: " + err.Error())
					return
				}
				msg.SetText("ffmpeg ok — " + got)
			})
		}()
	})

	// the local ASR/diarization stack. Not an endpoint -- a container run per
	// job -- but every bit as configurable, and until now it was not: the paths
	// belonged to one machine and "hip" assumed an AMD card.
	entry := func(text, placeholder, tip string) *gtk.Entry {
		e := gtk.NewEntry()
		e.SetText(text)
		e.SetPlaceholderText(placeholder)
		e.SetTooltipText(tip)
		e.SetHExpand(true)
		return e
	}
	image := entry(c.Image, defImage, "Container image holding audiocpp_cli")
	models := entry(c.Models, defModels, "Host folder mounted as the models root; the built-in voices live in its voices/ subfolder")
	cli := entry(c.CLI, defCLI, "Path to audiocpp_cli INSIDE the container")
	asrGGUF := entry(c.ASRGGUF, defASRGGUF, "ASR model, relative to the models mount")
	diarGGUF := entry(c.DiarGGUF, defDiarGGUF, "Diarization model, relative to the models mount")
	acppBackend := entry(c.Backend, defBackend, "hip = AMD/ROCm · cuda = NVIDIA · cpu = no GPU")
	lang := entry(c.Language, defLanguage, "Language the ASR model is told to expect — the wrong one transcribes into gibberish")

	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	save.ConnectClicked(func() {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text(),
			TTS:      strings.TrimRight(strings.TrimSpace(tts.Text()), "/"),
			Image:    image.Text(),
			CLI:      cli.Text(),
			Models:   models.Text(),
			ASRGGUF:  asrGGUF.Text(),
			DiarGGUF: diarGGUF.Text(),
			Backend:  acppBackend.Text(),
			Language: lang.Text(),
		}
		if err := a.writeConf(cc); err != nil {
			msg.SetText("save failed: " + err.Error())
			return
		}
		// a different server has a different catalog; the cached model id and
		// the "already listening on" note both belong to the old one
		a.ttsModel, a.ttsNoted = "", ""
		note := ""
		// every step re-reads the file, so the rest takes effect on the next run
		// -- but the voice list was read once, when its page was built
		if cc.withDefaults().Models != c.Models {
			note = " — restart to list the voices in the new models folder"
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
	grid.Attach(head("Writing — the LLM that describes, cuts and narrates"), 0, 0, 3, 1)
	grid.Attach(lbl("Server:"), 0, 1, 1, 1)
	grid.Attach(server, 1, 1, 1, 1)
	grid.Attach(testLLMBtn, 2, 1, 1, 1)
	grid.Attach(lbl("API key:"), 0, 2, 1, 1)
	grid.Attach(key, 1, 2, 2, 1)
	grid.Attach(lbl("Model:"), 0, 3, 1, 1)
	grid.Attach(model, 1, 3, 2, 1)
	grid.Attach(fetch, 0, 4, 1, 1)
	grid.Attach(pick, 1, 4, 1, 1)
	grid.Attach(use, 2, 4, 1, 1)

	grid.Attach(head("Speaking — the audio.cpp server that synthesizes the narration"), 0, 5, 3, 1)
	grid.Attach(lbl("Endpoint:"), 0, 6, 1, 1)
	grid.Attach(tts, 1, 6, 1, 1)
	grid.Attach(testTTSBtn, 2, 6, 1, 1)

	// not configurable: it is taken from PATH like any other tool. Shown because
	// which ffmpeg answers, and what it was built with, decides whether the
	// render works
	grid.Attach(head("Cutting — ffmpeg, which every step shells out to"), 0, 7, 3, 1)
	grid.Attach(lbl("ffmpeg:"), 0, 8, 1, 1)
	grid.Attach(ffLbl, 1, 8, 1, 1)
	grid.Attach(testFFBtn, 2, 8, 1, 1)

	// ASR and diarization do not go over HTTP -- a container run per job -- so
	// what they need is paths and a backend, not an endpoint
	grid.Attach(head("Listening — speech-to-text and diarization, run in a container"), 0, 9, 3, 1)
	foot := gtk.NewLabel("Not an HTTP service: each job runs audiocpp_cli in this image. " +
		"Blank means the built-in default.")
	foot.SetXAlign(0)
	foot.SetWrap(true)
	foot.AddCSSClass("dim-label")
	grid.Attach(foot, 0, 10, 3, 1)
	for i, row := range []struct {
		name string
		w    *gtk.Entry
	}{
		{"Image:", image},
		{"Models folder:", models},
		{"audiocpp_cli:", cli},
		{"ASR model:", asrGGUF},
		{"Diarization model:", diarGGUF},
		{"Backend:", acppBackend},
		{"Language:", lang},
	} {
		grid.Attach(lbl(row.name), 0, 11+i, 1, 1)
		grid.Attach(row.w, 1, 11+i, 2, 1)
	}
	grid.Attach(msg, 0, 18, 3, 1)

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(cancel)
	btns.Append(save)
	grid.Attach(btns, 1, 19, 2, 1)

	win.SetChild(grid)
	win.SetVisible(true)
}
