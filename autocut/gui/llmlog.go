package main

// Every LLM exchange, written down where it can be read. The log used to say
// that calls happened -- "two long LLM calls" -- and nothing about what was in
// them, which made a step a black box exactly when its output looked wrong.
// Now each call becomes one self-contained HTML page under the output
// folder's llm/: the system prompt, the user text, every image exactly as it
// was sent (the data URLs ride along inside the page, so it shows them with
// no other files around), the reply with its thinking folded away, and the
// error if the call failed. The log gets a short preview -- sizes, duration,
// the reply's first words -- and the page's path, clickable, so "what did the
// model actually see?" is one click, not an argument from memory.
//
// The record is made along the call, not after it. A suggest call thinks for
// minutes, and those minutes are exactly when "what did we just send?" gets
// asked -- so the page goes to disk with the request in it the moment the
// request goes out, link in the log and all. A streamed reply is appended to
// the page as it arrives -- refresh mid-call and read as far as the model has
// got -- and when the call ends the same file is rewritten whole, thinking
// folded and verdict in place.

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// llmDir is where the exchanges land. One folder for the whole project, not
// one per step: a session's calls read as one conversation with the model,
// and the step is right there in each file's name.
func (a *App) llmDir() string { return filepath.Join(a.outDir, "llm") }

// chatRec is one exchange being recorded: opened by recordChatStart when the
// request goes out, grown by stream while the reply arrives, completed by
// done. A nil recorder is valid and inert -- headless callers get one, and
// every method on it does nothing.
type chatRec struct {
	a        *App
	step     string
	thinking bool
	msgs     []map[string]any
	name     string
	f        *os.File // the page, held open so the reply can be appended live
	streamed int      // how much of the reply is already on the page
}

// recordChatStart files the request half of an exchange and puts what was
// sent -- sizes and the clickable page -- in the log, before the model has
// said a word. It never fails the call it records: observability must not
// break the work, so every problem here becomes a log line and nothing more.
// Called from llmChatOn on whatever goroutine the step runs on; the log lines
// go through the idle queue and the sequence counter is the only shared state.
func (a *App) recordChatStart(step string, thinking bool, msgs []map[string]any) *chatRec {
	if a.outDir == "" {
		return nil // headless (tests): nowhere agreed to hold the files
	}
	a.llmMu.Lock()
	a.llmSeq++
	// the clock orders files across runs, the counter inside a same-second burst
	name := fmt.Sprintf("%s-%02d-%s.html", time.Now().Format("0102-150405"), a.llmSeq, step)
	a.llmMu.Unlock()
	c := &chatRec{a: a, step: step, thinking: thinking, msgs: msgs, name: name}
	text, imgs := chatSent(msgs)
	a.logfIdle(">>> %s: %s of text and %d image(s) went to the LLM", step, sizeOf(text), imgs)
	// the page is created now and held open: everything sent, then an open
	// assistant section that stream appends the reply into as it arrives. Any
	// failure here just loses the live page -- done rewrites the whole file,
	// and the disk may be kinder then.
	if err := os.MkdirAll(a.llmDir(), 0o755); err != nil {
		a.logfIdle(">>>   could not keep the exchange: %v", err)
		return c
	}
	path := filepath.Join(a.llmDir(), name)
	f, err := os.Create(path)
	if err != nil {
		a.logfIdle(">>>   could not keep the exchange: %v", err)
		return c
	}
	if _, err := f.Write(chatHTML(step, a.readConf().Model, thinking, msgs, "", 0, nil, true)); err != nil {
		a.logfIdle(">>>   could not keep the exchange: %v", err)
		f.Close()
		return c
	}
	c.f = f
	glib.IdleAdd(func() {
		a.logPath(">>>   the whole exchange, images included: ", filepath.Join("llm", name), path)
	})
	return c
}

// stream appends what is new of the reply to the page. The streaming callback
// hands over everything said so far, not the latest piece, so the page holds
// a suffix count and writes only what it has not seen. One failed write ends
// the live page quietly -- done still rewrites the whole thing.
func (c *chatRec) stream(sofar string) {
	if c == nil || c.f == nil || len(sofar) <= c.streamed {
		return
	}
	if _, err := c.f.WriteString(html.EscapeString(sofar[c.streamed:])); err != nil {
		c.f.Close()
		c.f = nil
		return
	}
	c.streamed = len(sofar)
}

// done completes the record: the same page, rewritten whole with the reply --
// or the error, which is when the record matters most -- and the verdict and
// the answer's first words in the log.
func (c *chatRec) done(reply string, took time.Duration, callErr error) {
	if c == nil {
		return
	}
	if c.f != nil {
		c.f.Close() // the live page is done growing; the rewrite replaces it
		c.f = nil
	}
	verdict := fmt.Sprintf("%s came back in %s", sizeOf(len(reply)), durOf(took))
	if callErr != nil {
		verdict = fmt.Sprintf("the call failed after %s: %v", durOf(took), callErr)
	}
	c.a.logfIdle(">>> %s: %s", c.step, verdict)
	if p := chatPreview(reply); p != "" {
		c.a.logfIdle(">>>   the reply begins: %s", p)
	}
	if err := c.write(reply, took, callErr, false); err != nil {
		c.a.logfIdle(">>>   could not keep the exchange: %v", err)
	}
}

// write puts the page down as it stands. Both moments come through here, so
// the pending page and the finished one are the same file by construction.
func (c *chatRec) write(reply string, took time.Duration, callErr error, pending bool) error {
	if err := os.MkdirAll(c.a.llmDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.a.llmDir(), c.name),
		chatHTML(c.step, c.a.readConf().Model, c.thinking, c.msgs, reply, took, callErr, pending), 0o644)
}

// chatSent measures a request the way the preview reports it: how much text,
// how many images. Content is either a plain string or the parts list that
// txtPart/imgPart build; an image's bytes are counted as a picture, not text,
// because "1.8 MB of text" would be a lie about a request with two frames.
func chatSent(msgs []map[string]any) (text, imgs int) {
	for _, m := range msgs {
		switch c := m["content"].(type) {
		case string:
			text += len(c)
		case []any:
			for _, p := range c {
				part, _ := p.(map[string]any)
				if s, _ := part["text"].(string); s != "" {
					text += len(s)
				}
				if part["type"] == "image_url" {
					imgs++
				}
			}
		}
	}
	return
}

// chatPreview is the first words of the answer itself. Everything up to the
// last </think> is the model talking to itself, and quoting that would make
// every preview open the same way; the whole of it is in the page.
func chatPreview(reply string) string {
	s := reply
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 110 {
		s = string(r[:110]) + "…"
	}
	return s
}

// sizeOf writes a byte count the way a person reads one.
func sizeOf(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f kB", float64(n)/1024)
}

// durOf keeps a duration readable: seconds once it has any, otherwise as is.
func durOf(d time.Duration) string {
	if d >= time.Second {
		d = d.Round(time.Second)
	} else {
		d = d.Round(time.Millisecond)
	}
	return d.String()
}

const chatCSS = `body{font-family:sans-serif;max-width:60em;margin:2em auto;padding:0 1em;color:#222}
pre{white-space:pre-wrap;background:#f6f6f6;border:1px solid #ddd;border-radius:6px;padding:.8em}
img{max-width:24em;margin:.3em .3em 0 0;border:1px solid #ccc;border-radius:4px;vertical-align:top}
h1{font-size:1.4em}
h2{margin-top:1.5em;font-size:.9em;color:#666;text-transform:uppercase;letter-spacing:.05em}
.meta{color:#666}
.err{color:#b00;font-weight:bold}
details summary{color:#666;cursor:pointer}
`

// chatHTML renders the exchange as one page that needs nothing else: the
// images ride along as the data URLs they were sent as, and the thinking sits
// behind a fold so the answer is what the eye lands on.
func chatHTML(step, model string, thinking bool, msgs []map[string]any, reply string, took time.Duration, callErr error, pending bool) []byte {
	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>%s -- autocut LLM exchange</title>\n<style>%s</style></head><body>\n",
		esc(step), chatCSS)
	mode := "execute"
	if thinking {
		mode = "thinking"
	}
	status := "took " + durOf(took)
	if pending {
		status = "reply pending"
	}
	fmt.Fprintf(&b, "<h1>%s</h1>\n<p class=\"meta\">%s · model %s · %s mode · %s</p>\n",
		esc(step), time.Now().Format("2006-01-02 15:04:05"), esc(model), mode, status)
	if callErr != nil {
		fmt.Fprintf(&b, "<p class=\"err\">the call failed: %s</p>\n", esc(callErr.Error()))
	}
	for _, m := range msgs {
		role, _ := m["role"].(string)
		fmt.Fprintf(&b, "<h2>%s</h2>\n", esc(role))
		switch c := m["content"].(type) {
		case string:
			b.WriteString("<pre>" + esc(c) + "</pre>\n")
		case []any:
			for _, p := range c {
				part, _ := p.(map[string]any)
				if s, _ := part["text"].(string); s != "" {
					b.WriteString("<pre>" + esc(s) + "</pre>\n")
				}
				if iu, _ := part["image_url"].(map[string]any); iu != nil {
					// the data URL is base64 and punctuation: nothing to escape
					if url, _ := iu["url"].(string); strings.HasPrefix(url, "data:image/") {
						b.WriteString("<img src=\"" + url + "\">\n")
					}
				}
			}
		}
	}
	b.WriteString("<h2>assistant</h2>\n")
	if pending {
		// left open on purpose: this is the live page, and stream appends the
		// reply right here as it arrives. Browsers render the unclosed tags
		// fine, and the finished page replaces the whole file anyway.
		b.WriteString("<pre>")
		return []byte(b.String())
	}
	answer := reply
	if i := strings.LastIndex(reply, "</think>"); i >= 0 {
		b.WriteString("<details><summary>thinking</summary><pre>" +
			esc(reply[:i+len("</think>")]) + "</pre></details>\n")
		answer = reply[i+len("</think>"):]
	}
	if strings.TrimSpace(answer) != "" {
		b.WriteString("<pre>" + esc(answer) + "</pre>\n")
	} else if callErr == nil {
		b.WriteString("<p class=\"meta\">(empty reply)</p>\n")
	}
	b.WriteString("</body></html>\n")
	return []byte(b.String())
}

// logPath is logf with a tail that opens. The path is drawn like a link and a
// click on it launches whatever opens the file -- the browser, for the llm/
// pages. display is the short form shown in the log; full is where the file
// really is, kept aside because a path pretty enough to read is relative and
// a path that opens is not.
func (a *App) logPath(prefix, display, full string) {
	if a.log == nil {
		a.logf("%s%s", prefix, full) // stderr only: the real path, nothing to click
		return
	}
	if a.linkTag == nil {
		a.linkTag = gtk.NewTextTag("link")
		a.linkTag.SetObjectProperty("foreground", "#1a5fb4")
		a.linkTag.SetObjectProperty("underline", int(pango.UnderlineSingle))
		a.log.Buffer().TagTable().Add(a.linkTag)
		a.linkPaths = map[string]string{}
		click := gtk.NewGestureClick()
		click.ConnectReleased(func(n int, x, y float64) { a.openLogLink(x, y) })
		a.log.AddController(click)
	}
	a.linkPaths[display] = full
	fmt.Fprintf(os.Stderr, "%s%s\n", prefix, full)
	buf := a.log.Buffer()
	buf.Insert(buf.EndIter(), prefix)
	from := buf.EndIter().Offset()
	buf.Insert(buf.EndIter(), display)
	buf.ApplyTag(a.linkTag, buf.IterAtOffset(from), buf.EndIter())
	buf.Insert(buf.EndIter(), "\n")
	mark := buf.CreateMark("", buf.EndIter(), false)
	a.log.ScrollToMark(mark, 0, false, 0, 1)
	buf.DeleteMark(mark)
}

// openLogLink resolves a click on the log to the tagged path under it, if any,
// and opens it the way openFolder opens folders: the portal first, xdg-open
// when the portal will not.
func (a *App) openLogLink(x, y float64) {
	if a.linkTag == nil {
		return
	}
	bx, by := a.log.WindowToBufferCoords(gtk.TextWindowText, int(x), int(y))
	it, ok := a.log.IterAtLocation(bx, by)
	if !ok || !it.HasTag(a.linkTag) {
		return
	}
	start := it.Copy()
	if !start.TogglesTag(a.linkTag) {
		start.BackwardToTagToggle(a.linkTag)
	}
	end := it.Copy()
	end.ForwardToTagToggle(a.linkTag)
	full := a.linkPaths[a.log.Buffer().Text(start, end, false)]
	if full == "" {
		return
	}
	l := gtk.NewFileLauncher(gio.NewFileForPath(full))
	l.Launch(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		if err := l.LaunchFinish(res); err != nil {
			a.logf("portal launch failed (%v), trying xdg-open", err)
			if err := exec.Command("xdg-open", full).Start(); err != nil {
				a.logf("xdg-open: %v", err)
			}
		}
	})
}
