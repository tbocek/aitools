package main

// The settings dialog's two Test buttons, without the dialog. Both talk to real
// servers, so both are opt-in -- but they are the same calls the buttons make,
// which makes this the way to answer "is my config broken or is the pipeline?"
// from a terminal.
//
//	AUTOCUT_LIVE=1 go test -run TestEndpoints -v

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestTTS covers what the button is for: not "does something answer" but
// "will narration work against this". A server on the right port serving the
// wrong models is the failure that otherwise only shows up in step 6.
func TestTestTTS(t *testing.T) {
	srv := func(models string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.Write([]byte(`{"status":"ok"}`))
				return
			}
			w.Write([]byte(`{"object":"list","data":[` + models + `]}`))
		}))
	}

	good := srv(`{"id":"index-tts2","family":"index_tts2","task":"clon"}`)
	defer good.Close()
	got, err := testTTS(good.URL)
	if err != nil {
		t.Fatalf("a server that can clone was rejected: %v", err)
	}
	if !strings.Contains(got, "index-tts2") {
		t.Errorf("the report does not name the model it would narrate with: %q", got)
	}
	// a trailing slash is what a pasted URL usually has
	if _, err := testTTS(good.URL + "/"); err != nil {
		t.Errorf("a trailing slash broke the check: %v", err)
	}

	asrOnly := srv(`{"id":"parakeet","family":"parakeet","task":"asr"}`)
	defer asrOnly.Close()
	if _, err := testTTS(asrOnly.URL); err == nil {
		t.Error("a server with no voice-cloning model passed the test")
	} else if !strings.Contains(err.Error(), "clone") {
		t.Errorf("unhelpful message for an ASR-only server: %v", err)
	}

	empty := srv("")
	defer empty.Close()
	if _, err := testTTS(empty.URL); err == nil {
		t.Error("a server with no models at all passed the test")
	}

	dead := srv("")
	dead.Close() // nothing listening on that port any more
	if _, err := testTTS(dead.URL); err == nil {
		t.Error("a dead endpoint passed the test")
	}
}

// TestTestLLM checks that the two answers a broken config actually produces --
// a rejection with a reason, and a reasoning model that never gets to the
// content -- come back as errors a user can act on.
func TestTestLLM(t *testing.T) {
	reply := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		}))
	}

	ok := reply(200, `{"choices":[{"message":{"content":" ok "}}]}`)
	defer ok.Close()
	got, err := testLLM(appConf{Server: ok.URL, Model: "m"})
	if err != nil {
		t.Fatalf("a working server was rejected: %v", err)
	}
	if !strings.Contains(got, `"ok"`) {
		t.Errorf("the report does not quote the answer: %q", got)
	}

	denied := reply(401, `{"error":{"message":"invalid api key"}}`)
	defer denied.Close()
	if _, err := testLLM(appConf{Server: denied.URL, Model: "m"}); err == nil {
		t.Error("a rejected key passed the test")
	} else if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("the server's own reason was dropped: %v", err)
	}

	thinking := reply(200, `{"choices":[{"message":{"content":""}}]}`)
	defer thinking.Close()
	if _, err := testLLM(appConf{Server: thinking.URL, Model: "m"}); err == nil {
		t.Error("an empty completion passed as a working model")
	} else if !strings.Contains(err.Error(), "reasoning") {
		t.Errorf("no hint about where the budget went: %v", err)
	}
}

func TestEndpointsLive(t *testing.T) {
	if os.Getenv("AUTOCUT_LIVE") == "" {
		t.Skip("set AUTOCUT_LIVE=1 to reach the configured servers")
	}
	a := &App{root: "/home/draft/git/aitools/autocut"}
	c := a.readConf()

	t.Run("llm", func(t *testing.T) {
		if c.Server == "" {
			t.Skip("no LLM server in " + a.confPath())
		}
		got, err := testLLM(c)
		if err != nil {
			t.Fatalf("%s: %v", c.Server, err)
		}
		t.Log(got)
	})

	t.Run("tts", func(t *testing.T) {
		got, err := testTTS(a.ttsURL())
		if err != nil {
			t.Fatalf("%s: %v", a.ttsURL(), err)
		}
		t.Logf("%s: %s", a.ttsURL(), got)
	})
}

// TestConfRoundTrip guards the config format: the GUI must not write something
// bash cannot read back, and adding the TTS endpoint must not disturb the three
// LLM keys.
func TestConfRoundTrip(t *testing.T) {
	a := &App{root: t.TempDir()}
	want := appConf{
		Server: "https://ai.example.com",
		Model:  "Qwen3.6 (27B; 128k ctx; Q4_K_XL; visual)", // spaces, parens, semicolons
		Key:    "sk-not-a-real-key",
		TTS:    "http://127.0.0.1:8765",
	}
	if err := a.writeConf(want); err != nil {
		t.Fatal(err)
	}
	if got := a.readConf(); got != want {
		t.Fatalf("round trip changed the config:\n got %+v\nwant %+v", got, want)
	}
	fi, err := os.Stat(a.confPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 { // it holds an API key
		t.Errorf("%s is mode %v, want 600", a.confPath(), fi.Mode().Perm())
	}
	if filepath.Base(a.confPath()) != "llm.conf" {
		t.Errorf("the config is llm.conf, the GUI writes %s", a.confPath())
	}

	// the file stays sourceable so a shell can read the endpoints too: the model
	// id is full of parens and semicolons, and anything that quotes it wrongly
	// leaves the GUI working while a shell chokes on it
	out, err := exec.Command("bash", "-c",
		"source "+a.confPath()+`; printf '%s|%s|%s' "$LLM_MODEL" "$LLM_SERVER" "$AUDIOCPP_SERVER"`).CombinedOutput()
	if err != nil {
		t.Fatalf("bash could not source the config: %v\n%s", err, out)
	}
	if got, want := string(out), want.Model+"|"+want.Server+"|"+want.TTS; got != want {
		t.Errorf("bash reads back %q, want %q", got, want)
	}

	// an empty endpoint is how the user asks for the compose default, and it has
	// to survive the round trip as empty rather than as a URL
	want.TTS = ""
	if err := a.writeConf(want); err != nil {
		t.Fatal(err)
	}
	if got := a.readConf(); got.TTS != "" {
		t.Errorf("cleared endpoint came back as %q", got.TTS)
	}
}
