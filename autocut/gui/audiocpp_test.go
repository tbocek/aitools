package main

// Step 1's half of the audio.cpp server, against a fake one. Nothing here
// reaches a real server or a GPU: what is being pinned is the contract between
// the two, which is where moving ASR and diarization off the CLI could go
// wrong quietly -- a request the server does not understand, or an answer read
// as if the numbers were seconds when they are sample counts.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAudio is an audio.cpp server that always answers /health and /v1/models,
// and hands /v1/tasks/run to the test. Every request body it saw is recorded:
// the shape of what we send is as much of the contract as what we read back.
func fakeAudio(t *testing.T, models string, run http.HandlerFunc) (*App, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[` + models + `]}`))
		case "/v1/tasks/run":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("unreadable request body: %v", err)
			}
			seen = append(seen, body)
			run(w, r)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	a := &App{root: dir, outDir: dir}
	if err := a.writeConf(appConf{TTS: srv.URL}); err != nil {
		t.Fatal(err)
	}
	return a, &seen
}

// the two models step 1 asks for, as a server that has them lists them
const step1Models = `{"id":"parakeet-tdt","family":"parakeet_tdt","task":"asr"},` +
	`{"id":"sortformer-diar","family":"sortformer_diar","task":"diar"}`

func answer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }
}

// one second in, one word, one speaker -- in the sample counts the server deals in
const asrAnswer = `{"text":"hello there","words":[
	{"word":"hello","start_sample":16000,"end_sample":24000,"confidence":0.9},
	{"word":"there","start_sample":24000,"end_sample":32000,"confidence":0.9}],
	"timing":{"wall_ms":12}}`

const diarAnswer = `{"speaker_turns":[
	{"start_sample":16000,"end_sample":32000,"speaker_id":"speaker_0","confidence":0.8}]}`

// TestASRPostsOneJobAndKeepsTheAnswerWhole pins the request the CLI's flags
// turned into, and that the answer is not filtered on the way to words.json.
func TestASRPostsOneJobAndKeepsTheAnswerWhole(t *testing.T) {
	a, seen := fakeAudio(t, step1Models, answer(asrAnswer))

	body, text, err := a.asrJSON(filepath.Join(a.outDir, "voice16k.wav"))
	if err != nil {
		t.Fatalf("asr: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("posted %d jobs, want 1", len(*seen))
	}
	got := (*seen)[0]
	if got["model"] != defASRModel {
		t.Errorf("asked model %v, want %q", got["model"], defASRModel)
	}
	req, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("no request object in %v", got)
	}
	// the server opens this itself, from a working directory that is not ours
	if p, _ := req["audio"].(string); !filepath.IsAbs(p) {
		t.Errorf("audio path %q is not absolute", p)
	}
	// the CLI could only carry the language on an empty --text; over HTTP it is
	// a field of its own, and it has to actually be sent
	if req["language"] != defLanguage {
		t.Errorf("language went as %v, want %q", req["language"], defLanguage)
	}
	if req["text"] != nil {
		t.Errorf("the empty-text workaround outlived the CLI: %v", req["text"])
	}
	if text != "hello there" {
		t.Errorf("transcript text = %q", text)
	}
	// verbatim: readers of words.json walk whatever shape they find, and the
	// parts we do not read today (timing, confidences) are why a run is
	// debuggable tomorrow
	if !strings.Contains(string(body), `"timing"`) || !strings.Contains(string(body), `"confidence"`) {
		t.Errorf("the answer was rewritten on the way to words.json: %s", body)
	}
}

// TestASRWithoutWordsIsAFailure: every later step is built on word times, so an
// answer without them has to stop step 1 here -- and, because words.json is
// also the resume marker, must not leave a file behind that says this input is
// done.
func TestASRWithoutWordsIsAFailure(t *testing.T) {
	a, _ := fakeAudio(t, step1Models, answer(`{"text":"","words":[]}`))
	if _, _, err := a.asrJSON(filepath.Join(a.outDir, "silence.wav")); err == nil {
		t.Fatal("an answer with no words passed as a transcript")
	} else if !strings.Contains(err.Error(), defASRModel) || !strings.Contains(err.Error(), "silence.wav") {
		t.Errorf("the error names neither the model nor the file: %v", err)
	}
}

// TestDiarSpansAreSecondsAndSilenceIsNotAnError covers the unit change (the
// server counts samples, the pipeline works in seconds) and the case the CLI
// expressed by writing no file at all.
func TestDiarSpansAreSecondsAndSilenceIsNotAnError(t *testing.T) {
	a, seen := fakeAudio(t, step1Models, answer(diarAnswer))
	spans, err := a.diarSpans(filepath.Join(a.outDir, "win.wav"))
	if err != nil {
		t.Fatalf("diar: %v", err)
	}
	if len(spans) != 1 || spans[0].s != 1 || spans[0].e != 2 || spans[0].slot != "speaker_0" {
		t.Fatalf("spans = %+v, want one second-long turn from 1 s to 2 s", spans)
	}
	if (*seen)[0]["model"] != defDiarModel {
		t.Errorf("asked model %v, want %q", (*seen)[0]["model"], defDiarModel)
	}

	quiet, _ := fakeAudio(t, step1Models, answer(`{"speaker_turns":[]}`))
	spans, err = quiet.diarSpans(filepath.Join(quiet.outDir, "win.wav"))
	if err != nil {
		t.Fatalf("a window with no speech failed: %v", err)
	}
	if len(spans) != 0 {
		t.Errorf("silence produced %+v", spans)
	}
}

// TestTheServersAnswersAreTheFilesStep1Keeps is the point of the port: the two
// answers, written to disk exactly as they arrive, are read by the code that
// used to read the CLI's output files -- same names, same numbers, same
// segments out the other end.
func TestTheServersAnswersAreTheFilesStep1Keeps(t *testing.T) {
	a, _ := fakeAudio(t, step1Models, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(asrAnswer)) // this one only ever gets the ASR job
	})
	out := filepath.Join(a.outDir, "step1", "clip")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	body, text, err := a.asrJSON(filepath.Join(out, "voice16k.wav"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("words.json", body)
	write("transcript.txt", []byte(text+"\n"))
	// turns.json is autocut's own resolved list rather than one answer, but it
	// is written in the server's units and read by the same walker
	write("turns.json", []byte(diarAnswer))

	if err := a.mergeSegments(out); err != nil {
		t.Fatalf("merging the server's answers: %v", err)
	}
	tsv, err := os.ReadFile(filepath.Join(out, "transcript.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	want := "1.00\t2.00\tspeaker_0\thello there\n"
	if string(tsv) != want {
		t.Errorf("transcript.tsv =\n%q\nwant\n%q", tsv, want)
	}
}

// TestAudioRunPassesTheServersRefusalBack: one server, one GPU, and a busy one
// answers 503. Whatever it says has to reach the log -- "the request failed" is
// not something a user can act on.
func TestAudioRunPassesTheServersRefusalBack(t *testing.T) {
	a, _ := fakeAudio(t, step1Models, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":{"message":"model busy: timed out after 30000 ms","type":"server_busy"}}`))
	})
	_, _, err := a.asrJSON(filepath.Join(a.outDir, "voice16k.wav"))
	if err == nil {
		t.Fatal("a 503 passed as a transcript")
	}
	for _, want := range []string{"503", "server_busy", defASRModel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error drops %q: %v", want, err)
		}
	}
}

// TestAudioRunNeedsAServer: the message for "nothing is listening" is the one
// most likely to be read by someone who has not started the stack.
func TestAudioRunNeedsAServer(t *testing.T) {
	a, _ := fakeAudio(t, step1Models, answer(asrAnswer))
	if err := a.writeConf(appConf{TTS: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := a.asrJSON(filepath.Join(a.outDir, "voice16k.wav"))
	if err == nil {
		t.Fatal("a dead endpoint passed")
	}
	if !strings.Contains(err.Error(), "docker compose up -d audio") {
		t.Errorf("the error does not say how to start it: %v", err)
	}
}

// TestEnsureAudioModelsNamesWhatIsMissing guards step 1's preflight. It exists
// because the alternative is minutes of frame extraction followed by "unknown
// model", and because a server CAN answer perfectly while serving only the
// voice.
func TestEnsureAudioModelsNamesWhatIsMissing(t *testing.T) {
	fail := func(t *testing.T, models string, wants ...string) {
		t.Helper()
		a, _ := fakeAudio(t, models, answer(""))
		err := a.ensureAudioModels()
		if err == nil {
			t.Fatalf("%s passed the preflight", models)
		}
		for _, w := range wants {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("the error drops %q: %v", w, err)
			}
		}
	}

	a, _ := fakeAudio(t, step1Models, answer(""))
	if err := a.ensureAudioModels(); err != nil {
		t.Fatalf("a server with both models was rejected: %v", err)
	}

	// the narration server before anyone added the step-1 entries
	fail(t, `{"id":"index-tts2","family":"index_tts2","task":"clon"}`,
		defASRModel, "index-tts2", "audiocpp-server.json")
	// the id is there but points at the wrong thing -- a copied entry with the
	// task left as it was
	fail(t, `{"id":"parakeet-tdt","family":"parakeet_tdt","task":"asr"},`+
		`{"id":"sortformer-diar","family":"sortformer_diar","task":"asr"}`,
		defDiarModel, "diar")
}

// TestCatalogIDsIsStable: these names go into error messages, and a message
// that reorders itself between two runs of the same broken config reads like
// something changed.
func TestCatalogIDsIsStable(t *testing.T) {
	cat := map[string]audioModel{}
	for _, id := range []string{"sortformer-diar", "index-tts2", "parakeet-tdt"} {
		cat[id] = audioModel{ID: id}
	}
	for i := 0; i < 5; i++ {
		if got, want := catalogIDs(cat), "index-tts2, parakeet-tdt, sortformer-diar"; got != want {
			t.Fatalf("catalogIDs = %q, want %q", got, want)
		}
	}
	if got := catalogIDs(nil); got != "nothing" {
		t.Errorf("an empty catalog reads %q", got)
	}
}
