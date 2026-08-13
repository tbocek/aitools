package main

// Chat client for the llama-server configured in llm.conf (the gear dialog).
// Two parameter sets: "thinking" for judgment-heavy calls
// (alignment, later the cut selection), "execute" with thinking disabled for
// the high-volume mechanical calls (frame description -- measured 32 s vs 2 s
// per request on the same prompt).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// llmChatRetry absorbs one transport hiccup, which a multi-hour pass will hit
// -- but never retries a user stop.
func (a *App) llmChatRetry(msgs []map[string]any, thinking bool) (string, error) {
	reply, err := a.llmChat(msgs, thinking)
	if err == nil || errors.Is(err, errStopped) {
		return reply, err
	}
	time.Sleep(2 * time.Second)
	return a.llmChat(msgs, thinking)
}
