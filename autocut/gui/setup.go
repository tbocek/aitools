package main

// Settings: edits llm.conf in place. It stays bash-sourceable (quoted values,
// no GUI-only syntax) so a shell can read the endpoints too. Both of the
// pipeline's HTTP endpoints live here: the LLM that writes (descriptions, cuts,
// narration) and the audio.cpp server that speaks. Everything else the pipeline
// reaches for is local -- ffmpeg, gstreamer, and audiocpp_cli inside the
// audio:latest container for ASR and diarization -- so there is no third URL to
// configure.
//
// The dialog can query each server and say what it found, because both failures
// it is meant to catch are silent otherwise: a model id spelled differently
// than the server spells it (a hand-typed id with a stray suffix cost us a
// debug round once already), and a TTS server that answers but serves the wrong
// family.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type appConf struct {
	Server, Model, Key string // the LLM that writes
	TTS                string // the audio.cpp server that speaks; blank = auto
}

func (a *App) confPath() string { return filepath.Join(a.root, "llm.conf") }

func (a *App) readConf() appConf {
	var c appConf
	b, err := os.ReadFile(a.confPath())
	if err != nil {
		return c
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
		}
	}
	return c
}

func (a *App) writeConf(c appConf) error {
	body := fmt.Sprintf(`# Endpoints used by the pipeline (written by the GUI's settings dialog).
# Bash-sourceable -- keep this file chmod 600, the key is a credential.
LLM_SERVER=%q
LLM_MODEL=%q
LLM_API_KEY=%q
# audio.cpp TTS server; empty means 127.0.0.1:%d. Autocut only talks to it,
# never starts it -- that is "cd cpp && docker compose up -d audio".
AUDIOCPP_SERVER=%q
`, c.Server, c.Model, c.Key, ttsPort, c.TTS)
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

	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	save.ConnectClicked(func() {
		cc := appConf{Server: server.Text(), Model: model.Text(), Key: key.Text(),
			TTS: strings.TrimRight(strings.TrimSpace(tts.Text()), "/")}
		if err := a.writeConf(cc); err != nil {
			msg.SetText("save failed: " + err.Error())
			return
		}
		// a different server has a different catalog; the cached model id and
		// the "already listening on" note both belong to the old one
		a.ttsModel, a.ttsNoted = "", ""
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

	// ASR and diarization do not go over HTTP -- they run audiocpp_cli in the
	// audio:latest container -- so this is the whole list of endpoints
	foot := gtk.NewLabel("Speech-to-text and diarization run locally in the audio:latest " +
		"container, not over HTTP, so they need no endpoint here.")
	foot.SetXAlign(0)
	foot.SetWrap(true)
	foot.AddCSSClass("dim-label")
	grid.Attach(foot, 0, 7, 3, 1)
	grid.Attach(msg, 0, 8, 3, 1)

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(cancel)
	btns.Append(save)
	grid.Attach(btns, 1, 9, 2, 1)

	win.SetChild(grid)
	win.SetVisible(true)
}
