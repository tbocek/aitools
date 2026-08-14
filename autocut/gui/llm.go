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

// llmChat posts a chat completion; thinking selects the parameter set.
func (a *App) llmChat(msgs []map[string]any, thinking bool) (string, error) {
	return a.llmChatOn(msgs, thinking, nil)
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
func (a *App) llmChatOn(msgs []map[string]any, thinking bool, onText func(string)) (string, error) {
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
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.Server, "/")+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		if a.stopFlag.Load() {
			return "", errStopped
		}
		return "", err
	}
	defer resp.Body.Close()
	// only if the server actually streamed: one that ignores the flag, or that
	// answers an error as plain JSON, falls through to the decode below
	if onText != nil && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return a.readChatStream(resp.Body, onText)
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
func (a *App) readChatStream(r io.Reader, onText func(string)) (string, error) {
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
					} `json:"delta"`
				} `json:"choices"`
			}
			// a chunk that will not parse is a chunk, not the reply: keep reading
			if json.Unmarshal([]byte(payload), &ch) == nil && len(ch.Choices) > 0 {
				if d := ch.Choices[0].Delta.Content; d != "" {
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

// llmChatRetry absorbs one transport hiccup, which a multi-hour pass will hit
// -- but never retries a user stop.
func (a *App) llmChatRetry(msgs []map[string]any, thinking bool) (string, error) {
	return a.llmChatRetryOn(msgs, thinking, nil)
}

func (a *App) llmChatRetryOn(msgs []map[string]any, thinking bool, onText func(string)) (string, error) {
	reply, err := a.llmChatOn(msgs, thinking, onText)
	if err == nil || errors.Is(err, errStopped) {
		return reply, err
	}
	time.Sleep(2 * time.Second)
	return a.llmChatOn(msgs, thinking, onText)
}
