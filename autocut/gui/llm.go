package main

// Chat client for the llama-server configured in llm.conf (the gear dialog).
// Two parameter sets: "thinking" for judgment-heavy calls
// (alignment, later the cut selection), "execute" with thinking disabled for
// the high-volume mechanical calls (frame description -- measured 32 s vs 2 s
// per request on the same prompt).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func txtPart(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func imgPart(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b),
		},
	}, nil
}

func msg(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": content}
}

// llmChat posts a chat completion; thinking selects the parameter set. step
// names the caller -- "suggest", "describe" -- and is what the recorded
// exchange is filed and logged under (recordChatStart in llmlog.go).
func (a *App) llmChat(step string, msgs []map[string]any, thinking bool) (string, error) {
	return a.llmChatOn(step, msgs, thinking, nil)
}

// llmChatOn is llmChat with the reply readable while it is still being written.
// Pass an onText and the request is streamed: onText is called with everything
// received so far, every time more arrives, on the calling goroutine. Pass nil
// and this is exactly the old single-response call.
//
// The point of it is the bar. A narration is one request that thinks for a
// minute and then writes nine clips, and from the outside that is a spinner and
// a promise; streamed, the clips can be counted as they close (see
// narrEntriesDone), and the same bar that counts the speaking counts the
// writing. Nothing else changes: a caller with no callback is not streamed, so
// the steps that ask for one JSON object and parse it whole are untouched.
//
// Every call is also written down: what went out, what came back, how long it
// took -- the recorder keeps the exchange as an HTML page under llm/, request
// first and the reply folded in when it lands, and puts a
// preview in the log, because "2 LLM calls ran" was all the log used to say
// about the step where all the judgment happens.
func (a *App) llmChatOn(step string, msgs []map[string]any, thinking bool, onText func(string)) (string, error) {
	rec := a.recordChatStart(step, thinking, msgs)
	// tee an existing stream through the live page; a caller with no callback
	// stays unstreamed (wrapping nil would flip the wire request to streaming)
	if onText != nil {
		user := onText
		onText = func(s string) { rec.stream(s); user(s) }
	}
	t0 := time.Now()
	reply, err := a.llmChatPost(step, msgs, thinking, onText)
	rec.done(reply, time.Since(t0), err)
	return reply, err
}

// llmChatPost is the wire call itself: build the body, post it, read the answer.
// step names the caller so the watch can say whose call is running (llmstall.go).
func (a *App) llmChatPost(step string, msgs []map[string]any, thinking bool, onText func(string)) (string, error) {
	c := a.readConf()
	if c.Server == "" || c.Model == "" {
		return "", fmt.Errorf("no LLM configured -- use the gear button")
	}
	body := map[string]any{
		"model":            c.Model,
		"messages":         msgs,
		"top_p":            0.95,
		"top_k":            20,
		"min_p":            0.0,
		"presence_penalty": 0.0,
	}
	if thinking {
		body["temperature"] = 1.0
		body["max_tokens"] = 32768
		body["chat_template_kwargs"] = map[string]any{"preserve_thinking": true}
	} else {
		body["temperature"] = 0.6
		body["max_tokens"] = 8192
		body["chat_template_kwargs"] = map[string]any{
			"preserve_thinking": true, "enable_thinking": false,
		}
	}
	if onText != nil {
		body["stream"] = true
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	ctx := a.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	// the watch holds the call to a silence rule and says every minute what is
	// arriving; a call nobody streams has no silence to measure, so it keeps a
	// whole-call ceiling on the client instead
	w := a.watchChat(step, onText != nil)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := w.guard(cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.Server, "/")+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	bearer(req, c.Key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	if onText == nil {
		client.Timeout = llmWhole
	}
	resp, err := client.Do(req)
	if err != nil {
		if a.stopFlag.Load() {
			return "", errStopped
		}
		return "", w.blame(err)
	}
	defer resp.Body.Close()
	// only if the server actually streamed: one that ignores the flag, or that
	// answers an error as plain JSON, falls through to the decode below
	if onText != nil && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		reply, err := a.readChatStream(w.wrap(resp.Body), onText, w)
		return reply, w.blame(err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("bad response (%s): %w", resp.Status, err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices (%s): %v", resp.Status, out.Error)
	}
	return out.Choices[0].Message.Content, nil
}

// readChatStream assembles a server-sent-event reply, handing the caller the
// text so far as each piece lands. Read with a bufio.Reader rather than a
// Scanner: one event carrying a long chunk is a line of any length, and a
// Scanner would stop dead at its 64 kB limit halfway through a narration.
func (a *App) readChatStream(r io.Reader, onText func(string), w *chatWatch) (string, error) {
	br := bufio.NewReader(r)
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
			if payload == "[DONE]" {
				return b.String(), nil
			}
			var ch struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
						// what the model tells itself on the way to the
						// answer. This server keeps it out of content
						// entirely, so a call that is thinking hard sends
						// nothing here that the old parser could see -- and
						// looked, from the log and from the bar, exactly like
						// one that had hung. It is read to be counted and
						// shown, and goes nowhere near the reply: onText feeds
						// the progress bar and the recorded page, and both are
						// about the answer.
						Reasoning string `json:"reasoning_content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			// a chunk that will not parse is a chunk, not the reply: keep reading
			if json.Unmarshal([]byte(payload), &ch) == nil && len(ch.Choices) > 0 {
				if d := ch.Choices[0].Delta.Reasoning; d != "" {
					w.wrote(d, true)
				}
				if d := ch.Choices[0].Delta.Content; d != "" {
					w.wrote(d, false)
					b.WriteString(d)
					onText(b.String())
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return b.String(), nil // a stream that ends without [DONE] still said what it said
			}
			if a.stopFlag.Load() {
				return "", errStopped
			}
			return "", err // a half-written JSON reply is worth less than the error
		}
	}
}

// jsonItemsDone reads a reply that is still arriving: how many objects have
// closed inside the last "<key>": [ , and how far into the session the last one
// to close reaches, for the shapes that carry an "end" in seconds. Only what is
// complete counts, so a bar never claims an item the model is still writing.
//
// It starts at the LAST "<key>": [ in the text, because everything before the
// answer is the model thinking, out loud, about the answer -- braces, quotes,
// the key's own name and all. Requiring the key's punctuation is what keeps a
// mention of it in that thinking from starting the count early; a worked
// example in there would still fool it, and the cost of that is one reading of
// a progress bar, which is the right price for not parsing prose.
//
// The second return is what makes a bar out of a reply whose length nobody
// knows in advance. How many moments a cut has is the model's decision, so
// there is no denominator to count against -- but the order is not its
// decision: it walks the session from the front, so the end time of the last
// finished item is how far through the session, and through the job, it is.
func jsonItemsDone(s, key string) (done int, through float64) {
	q := `"` + key + `"`
	i := -1
	for at := 0; ; {
		j := strings.Index(s[at:], q)
		if j < 0 {
			break
		}
		j += at
		at = j + 1
		rest := strings.TrimLeft(s[j+len(q):], " \t\r\n")
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(rest[1:], " \t\r\n"), "[") {
			i = j
		}
	}
	if i < 0 {
		return 0, 0
	}
	tail := s[i:]
	depth, from := 0, 0
	inStr, esc := false, false
	for k, r := range tail {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr: // braces inside a line of narration are just text
		case r == '{':
			if depth == 0 {
				from = k
			}
			depth++
		case r == '}':
			if depth--; depth == 0 {
				done++
				if e, ok := jsonEnd(tail[from : k+1]); ok && e > through {
					through = e
				}
			}
		}
	}
	return done, through
}

// jsonEnd reads the "end" out of one finished object. A shape without one -- a
// narration clip, say -- simply has no answer here, and its caller falls back
// to counting.
func jsonEnd(obj string) (float64, bool) {
	var o struct {
		End *float64 `json:"end"`
	}
	if json.Unmarshal([]byte(obj), &o) != nil || o.End == nil {
		return 0, false
	}
	return *o.End, true
}

// llmChatRetry absorbs one transport hiccup, which a multi-hour pass will hit
// -- but never retries a user stop.
func (a *App) llmChatRetry(step string, msgs []map[string]any, thinking bool) (string, error) {
	return a.llmChatRetryOn(step, msgs, thinking, nil)
}

func (a *App) llmChatRetryOn(step string, msgs []map[string]any, thinking bool, onText func(string)) (string, error) {
	reply, err := a.llmChatOn(step, msgs, thinking, onText)
	if err == nil || errors.Is(err, errStopped) {
		return reply, err
	}
	time.Sleep(2 * time.Second)
	return a.llmChatOn(step, msgs, thinking, onText)
}
