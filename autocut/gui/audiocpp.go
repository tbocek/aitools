package main

// The audio.cpp server: one endpoint, one GPU, every audio job. It listens --
// speech recognition and diarization, step 1 -- and it speaks -- the narration
// in step 4.
//
// Listening used to be a container run per job: docker run audiocpp_cli, one
// model load per invocation, and a second copy of the weights in VRAM
// alongside the resident TTS model. The server was already running for
// narration, so now both go to it. What that costs autocut is nothing but
// HTTP; owning the container is the stack's business.
//
// Two things follow from talking to a server instead of running a program.
//
// Paths are the SERVER's. An "audio" field is a file the server opens itself,
// not an upload, so the project folder must be visible to it at that same
// absolute path -- which is exactly why the compose file mounts AUTOCUT_DIR at
// its own path, and the same reason a voice_ref travels as a path.
//
// A model id names everything else. Family, task, weights and session options
// live in audiocpp-server.json against that id, so the knobs that used to be
// flags here (--backend, parakeet_tdt.offline_mode=long_form,
// sortformer_diar.graph_capacity_mode=grow) are set where the model is
// declared, once, and not per request: they are chosen when the session is
// created, and the session outlives our call.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// audioURL is where an audio.cpp server is expected. AUDIOCPP_SERVER is
// audio.cpp's own variable -- the WebUI reads it to talk to an already-running
// server instead of managing one -- so setting it once points both frontends at
// the same server. AUTOCUT_TTS_URL overrides it when only autocut should move.
// The settings dialog wins over the environment: it is the one of the two a
// user can see and clear, and a stale export in the launching shell should not
// quietly outrank what the dialog says.
func (a *App) audioURL() string {
	if u := a.configuredAudio(); u != "" {
		return u
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ttsPort)
}

// configuredAudio is the server the user pointed us at, from the settings file
// or the environment; "" means the compose default on the loopback port.
func (a *App) configuredAudio() string {
	if u := a.readConf().TTS; u != "" {
		return u
	}
	for _, k := range []string{"AUTOCUT_TTS_URL", "AUDIOCPP_SERVER"} {
		if u := strings.TrimRight(strings.TrimSpace(os.Getenv(k)), "/"); u != "" {
			return u
		}
	}
	return ""
}

// ensureAudioServer checks that something is listening, and says so when
// nothing is. Autocut never starts a server: one GPU, started by whoever owns
// the stack. The compose "audio" service runs audiocpp_server and points the
// WebUI at it, so bringing that up is what makes narration -- and now step 1 --
// work.
func (a *App) ensureAudioServer() error {
	url := a.audioURL()
	r, err := http.Get(url + "/health")
	if err != nil {
		return fmt.Errorf("no audio.cpp server answering at %s -- start it "+
			"(in the stack's compose folder: docker compose up -d audio), or point "+
			"Settings at where one runs", url)
	}
	r.Body.Close()
	if a.audioNoted != url {
		a.audioNoted, a.ttsModel = url, "" // a different server, a different catalog
		a.logfIdle(">>> using the audio.cpp server on %s", url)
	}
	return nil
}

// audioModel is one entry of the server's catalog: the id we ask for, and what
// it turns out to be.
type audioModel struct {
	ID     string `json:"id"`
	Family string `json:"family"`
	Task   string `json:"task"`
}

// audioCatalog is what the server serves right now, by id.
func audioCatalog(url string) (map[string]audioModel, error) {
	r, err := http.Get(strings.TrimRight(url, "/") + "/v1/models")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var out struct {
		Data []audioModel `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("model list from %s: %w", url, err)
	}
	got := map[string]audioModel{}
	for _, m := range out.Data {
		got[m.ID] = m
	}
	return got, nil
}

func sortedKeys(cat map[string]audioModel) []string {
	ids := make([]string, 0, len(cat))
	for id := range cat {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// catalogIDs names the catalog for an error message: what a server offers is
// half of why the model you asked for is not there.
func catalogIDs(cat map[string]audioModel) string {
	if len(cat) == 0 {
		return "nothing"
	}
	return strings.Join(sortedKeys(cat), ", ")
}

// audioRun posts one job and hands back the answer verbatim. Verbatim matters:
// what comes back is what gets written to words.json and turns.json, and every
// reader of those walks whatever shape it finds rather than a fixed one.
//
// No client timeout on purpose. An hour of audio is an hour of work, and the
// only honest deadline is the user's: the request rides a.runCtx, so stop
// aborts it mid-flight the way it used to kill the container.
func (a *App) audioRun(model string, req map[string]any) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("no model id configured -- set one in Settings")
	}
	if err := a.ensureAudioServer(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"model": model, "request": req})
	if err != nil {
		return nil, err
	}
	ctx := a.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	hreq, err := http.NewRequestWithContext(ctx, "POST",
		a.audioURL()+"/v1/tasks/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(hreq)
	if err != nil {
		if a.stopFlag.Load() {
			return nil, errStopped
		}
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		if a.stopFlag.Load() {
			return nil, errStopped
		}
		return nil, err
	}
	if resp.StatusCode != 200 {
		// the server answers errors as JSON, but a proxy in between might not:
		// print what came back, trimmed, rather than a status alone
		return nil, fmt.Errorf("%s on %s answered %s: %.400s",
			model, a.audioURL(), resp.Status, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// asrJSON transcribes one wav. It returns the whole answer -- that is what
// words.json is -- and the plain text out of it, for transcript.txt.
//
// The language goes as its own field: over HTTP it lands in the request
// options directly, so the empty --text the CLI needed to carry it is gone.
// Models that ignore it (parakeet is multilingual) simply ignore it.
func (a *App) asrJSON(wav string) ([]byte, string, error) {
	// absolute, always: the path is opened by the server, whose working
	// directory is its own and is not ours
	path, err := filepath.Abs(wav)
	if err != nil {
		return nil, "", err
	}
	c := a.readConf()
	req := map[string]any{"audio": path}
	if c.Language != "" {
		req["language"] = c.Language
	}
	body, err := a.audioRun(c.ASRModel, req)
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Text  string `json:"text"`
		Words []struct {
			Word string `json:"word"`
		} `json:"words"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", fmt.Errorf("unreadable answer from %s: %w", c.ASRModel, err)
	}
	if len(out.Words) == 0 {
		// every later step is built on word times; an answer without them is a
		// failure here rather than an empty transcript three stages on
		return nil, "", fmt.Errorf("%s returned no words for %s -- silence, or the wrong model for the job",
			c.ASRModel, filepath.Base(wav))
	}
	return body, out.Text, nil
}

// diarSpans runs one clip through diarization. A clip with no speech is not a
// failure: it comes back with no turns, and no turns is silence.
func (a *App) diarSpans(wav string) ([]span, error) {
	path, err := filepath.Abs(wav)
	if err != nil {
		return nil, err
	}
	c := a.readConf()
	body, err := a.audioRun(c.DiarModel, map[string]any{"audio": path})
	if err != nil {
		return nil, err
	}
	return spansFrom(body)
}

// ensureAudioModels is step 1's preflight. The step is minutes of ffmpeg before
// the first model call, and "no such model" is worth hearing at the start of
// that rather than at the end of it.
func (a *App) ensureAudioModels() error {
	if err := a.ensureAudioServer(); err != nil {
		return err
	}
	cat, err := audioCatalog(a.audioURL())
	if err != nil {
		return err
	}
	c := a.readConf()
	for _, want := range []struct{ id, def, task, pkg string }{
		{c.ASRModel, defASRModel, "asr", "parakeet_tdt_q8_0"},
		{c.DiarModel, defDiarModel, "diar", "sortformer_diar_4spk_v1_q8_0"},
	} {
		m, ok := cat[want.id]
		if !ok {
			// the package name only fits the model this ships expecting; for a
			// hand-picked id, saying which weights to fetch would be a guess
			how := ""
			if want.id == want.def {
				how = fmt.Sprintf(" (the weights install with: docker compose exec audio "+
					"python3 tools/model_manager_v2.py install %s --models-root models)", want.pkg)
			}
			return fmt.Errorf("the audio.cpp server at %s serves %s, but not %q -- add it on "+
				"that server's own model page in the browser, or in the audiocpp-server.json "+
				"it reads at startup followed by docker compose up -d --force-recreate audio%s",
				a.audioURL(), catalogIDs(cat), want.id, how)
		}
		if m.Task != "" && m.Task != want.task {
			return fmt.Errorf("%q on %s is declared task %q, but step 1 needs %q there",
				want.id, a.audioURL(), m.Task, want.task)
		}
	}
	return nil
}
