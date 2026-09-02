package main

// Chat client for the llama-server configured in llm.conf (the gear dialog).
// Two parameter sets: "thinking" for judgment-heavy calls
// (alignment, later the cut selection), "execute" with thinking disabled for
// the high-volume mechanical calls (frame description -- measured 32 s vs 2 s
// per request on the same prompt).
//
// A call may offer tools (llmChatTools): the OpenAI shape, a list of function
// schemas in the request and tool_calls in the reply, answered with one tool
// message per call and asked again, until the model answers with words. Every
// round is its own recorded exchange, so the log shows what the model asked
// the web and what the web said (websearch.go) as plainly as it shows the cut.

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

// toolCall is one call the model made, in the wire's own shape, so it can be
// echoed back in the assistant turn exactly as it arrived.
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatReply is what one round returns: words, or calls, or both.
type chatReply struct {
	Content string
	Calls   []toolCall
}

// toolRunner answers one call by name; what it returns is what the model
// reads as the tool's result.
type toolRunner func(name string, args json.RawMessage) string

// the most rounds of tool calls one question may take. A model that has not
// found its words after this many has found a loop instead.
const toolRounds = 8

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
	rep, err := a.chatRound(step, msgs, thinking, nil, onText)
	return rep.Content, err
}

// llmChatTools is llmChatOn with tools on the table. The loop is here and
// nowhere else: a round that comes back with calls is answered -- the
// assistant turn echoed with its calls, then one tool message per call, in
// order -- and the question is put again with all of it in the history, until
// a round comes back with words alone. Those words are the reply; the rounds
// before them are in the recorded exchanges.
func (a *App) llmChatTools(step string, msgs []map[string]any, thinking bool,
	tools []map[string]any, run toolRunner, onText func(string)) (string, error) {
	if len(tools) == 0 || run == nil {
		return a.llmChatOn(step, msgs, thinking, onText)
	}
	msgs = append([]map[string]any(nil), msgs...)
	for round := 0; round < toolRounds; round++ {
		rep, err := a.chatRound(step, msgs, thinking, tools, onText)
		if err != nil {
			return "", err
		}
		if len(rep.Calls) == 0 {
			return rep.Content, nil
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": rep.Content, "tool_calls": rep.Calls})
		for _, c := range rep.Calls {
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": c.ID,
				"content": run(c.Function.Name, json.RawMessage(c.Function.Arguments))})
		}
	}
	return "", fmt.Errorf("the model was still calling tools after %d rounds", toolRounds)
}

// chatRound is one request and its recorded exchange.
func (a *App) chatRound(step string, msgs []map[string]any, thinking bool,
	tools []map[string]any, onText func(string)) (chatReply, error) {
	rec := a.recordChatStart(step, thinking, msgs)
	// tee an existing stream through the live page; a caller with no callback
	// stays unstreamed (wrapping nil would flip the wire request to streaming)
	if onText != nil {
		user := onText
		onText = func(s string) { rec.stream(s); user(s) }
	}
	t0 := time.Now()
	rep, err := a.llmChatPost(step, msgs, thinking, tools, onText)
	rec.done(rep.recorded(), time.Since(t0), err)
	return rep, err
}

// recorded is the reply as the exchange log shows it: the words, and after
// them the calls -- a round that only called a tool is otherwise an empty
// page in the log.
func (r chatReply) recorded() string {
	if len(r.Calls) == 0 {
		return r.Content
	}
	var b strings.Builder
	b.WriteString(r.Content)
	for _, c := range r.Calls {
		fmt.Fprintf(&b, "\n[tool call %s(%s)]", c.Function.Name, c.Function.Arguments)
	}
	return b.String()
}

// llmChatPost is the wire call itself: build the body, post it, read the answer.
// step names the caller so the watch can say whose call is running (llmstall.go).
func (a *App) llmChatPost(step string, msgs []map[string]any, thinking bool,
	tools []map[string]any, onText func(string)) (chatReply, error) {
	c := a.readConf()
	if c.Server == "" || c.Model == "" {
		return chatReply{}, fmt.Errorf("no LLM configured -- use the gear button")
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
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if onText != nil {
		body["stream"] = true
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return chatReply{}, err
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
		return chatReply{}, err
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
			return chatReply{}, errStopped
		}
		return chatReply{}, w.blame(err)
	}
	defer resp.Body.Close()
	// only if the server actually streamed: one that ignores the flag, or that
	// answers an error as plain JSON, falls through to the decode below
	if onText != nil && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		rep, err := a.readChatStream(w.wrap(resp.Body), onText, w)
		return rep, w.blame(err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatReply{}, fmt.Errorf("bad response (%s): %w", resp.Status, err)
	}
	if len(out.Choices) == 0 {
		return chatReply{}, fmt.Errorf("no choices (%s): %v", resp.Status, out.Error)
	}
	return chatReply{Content: out.Choices[0].Message.Content, Calls: out.Choices[0].Message.ToolCalls}, nil
}

// readChatStream assembles a server-sent-event reply, handing the caller the
// text so far as each piece lands. Read with a bufio.Reader rather than a
// Scanner: one event carrying a long chunk is a line of any length, and a
// Scanner would stop dead at its 64 kB limit halfway through a narration.
//
// A tool call arrives in pieces too -- its name in one delta, its arguments
// spread over the ones after, each stamped with the call's index -- and is
// put back together by that index.
func (a *App) readChatStream(r io.Reader, onText func(string), w *chatWatch) (chatReply, error) {
	br := bufio.NewReader(r)
	var b strings.Builder
	var calls []toolCall
	for {
		line, err := br.ReadString('\n')
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
			if payload == "[DONE]" {
				return chatReply{Content: b.String(), Calls: calls}, nil
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
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			// a chunk that will not parse is a chunk, not the reply: keep reading
			if json.Unmarshal([]byte(payload), &ch) == nil && len(ch.Choices) > 0 {
				d := ch.Choices[0].Delta
				if d.Reasoning != "" {
					w.wrote(d.Reasoning, true)
				}
				if d.Content != "" {
					w.wrote(d.Content, false)
					b.WriteString(d.Content)
					onText(b.String())
				}
				for _, tc := range d.ToolCalls {
					for len(calls) <= tc.Index {
						calls = append(calls, toolCall{})
					}
					c := &calls[tc.Index]
					if tc.ID != "" {
						c.ID = tc.ID
					}
					if tc.Type != "" {
						c.Type = tc.Type
					}
					if tc.Function.Name != "" {
						c.Function.Name += tc.Function.Name
					}
					c.Function.Arguments += tc.Function.Arguments
					w.wrote(tc.Function.Arguments, true)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return chatReply{Content: b.String(), Calls: calls}, nil // a stream that ends without [DONE] still said what it said
			}
			if a.stopFlag.Load() {
				return chatReply{}, errStopped
			}
			return chatReply{}, err // a half-written JSON reply is worth less than the error
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
	return a.llmChatRetryTools(step, msgs, thinking, nil, nil, onText)
}

// llmChatRetryTools is the retrying call with tools on the table; with none
// it is llmChatRetryOn exactly.
func (a *App) llmChatRetryTools(step string, msgs []map[string]any, thinking bool,
	tools []map[string]any, run toolRunner, onText func(string)) (string, error) {
	reply, err := a.llmChatTools(step, msgs, thinking, tools, run, onText)
	if err == nil || errors.Is(err, errStopped) {
		return reply, err
	}
	time.Sleep(2 * time.Second)
	return a.llmChatTools(step, msgs, thinking, tools, run, onText)
}
