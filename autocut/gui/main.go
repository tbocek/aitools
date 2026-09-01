package main

// Workflow console for the autocut pipeline. Five steps, one per tab in the
// header bar, each gated on the previous one's output, with a shared run bar
// and log at the bottom: 1 inputs (sources, STT, frames), 2 describe the frames
// and fix the transcripts into one session timeline, 3 cut, 4 narrate -- the
// voice on top and its lines below -- and 5 produce the upload.
//
// The voice had a step of its own, before the narration and two rows from it,
// so the one thing you cannot judge about a voice -- how it reads THIS
// narration -- came up before there was any. It is the top third of the narrate
// page now, with its own ▶ and ⏹ for the sample so the run bar keeps meaning
// "run the step on screen".
//
// One folder per step, stepN/, numbered by the tab it belonged to when the
// numbers were the names -- the pages and the files are called what they do
// now, and the folders are not, because renaming them would orphan every
// project already on disk. Describing runs two jobs and splits its folder
// between them by name, since a number would say nothing: step2/describe/ =
// the event logs, step2/transcript/ = the fixed transcripts, the subtitles and
// the session timeline.
//
//   cd autocut && ./gui/autocut-gui
//
// Everything is written under the open project's own output folder -- the
// project file's path with .data on the end (project.go) -- one folder per job,
// so a run can resume where it stopped.

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// steps is the pipeline in order, and the tab row is this table. The labels are
// short because five of them sit side by side in the header bar, where the
// width they ask for is a floor under the whole window -- what each step
// actually does is on hover, which is where the sidebar's longer titles went.
// icon is what the tab shows beside the label, and alone once the bar runs out
// of room for words (headfit.go): five icons fit a window no width of text
// would, and a tab that has shrunk to its icon is still a tab you can hit.
// wait is what the tab says instead while the step cannot be entered: it names
// the step to finish rather than its number, since the numbering has moved
// twice already and a hint pointing at the wrong tab is worse than none.
//
// help is the long form of tip, and it is the last of the paragraphs that used
// to sit at the top of each page. A page explains itself once and is then
// shorter by three lines forever: the paragraph is behind the ⓘ in the header
// bar, which shows the open page's and only that.
var steps = []struct{ name, label, icon, tip, wait, help string }{
	{"prep", "Prepare", "view-list-symbolic", "The sources, their transcripts, their frames, and what the models make of them", "",
		"Add this session's files — footage, voice recordings, or one screen capture " +
			"that is both. Each row says what its file is for: the camera means frames " +
			"come out of it and it can be cut, and the microphone tags who is speaking, " +
			"1 being the voice the narration is spoken in.\n\n" +
			"Everything in the list is placed on one clock — the timestamp in the file " +
			"name, or the file's own time and length — which is what puts a separate " +
			"recording's words, waveform and sound where they belong against the footage. " +
			"The footage is the master: everything else is drawn, mixed and cut " +
			"against where it falls on the footage, and a recording is heard for the " +
			"part of it that was running while the footage was.\n\n" +
			"Language is what this session is spoken in, told to the speech-to-text " +
			"model: it belongs to the footage, so it is here and in the project rather " +
			"than in Settings, and each project carries its own.\n\n" +
			"One ▶ does the whole step, in the order it has to happen: everything in " +
			"the list is transcribed and a frame is pulled out of the footage every few " +
			"seconds, and then two model jobs read that — the frames are described, and " +
			"the raw transcripts are cleaned up and merged into one timeline covering " +
			"the whole session. The run bar names the job it is on and, after the colon, " +
			"the piece of work it is doing, counted against everything queued so far. " +
			"Which file that piece came from is in the log; hover the bar for how many " +
			"tasks are still waiting.\n\n" +
			"The box on the right is everything the models are told, one row of its " +
			"menu at a time. The first row is not a prompt: it is what you want the " +
			"editor to know about this session — who is in it, how names are spelled, " +
			"what has to end up in the video — and every request this project makes " +
			"carries it. Behind it, in the order the pipeline sends them, is every " +
			"system prompt in the app: this step's two, the cut and the audit that " +
			"reads it back, the narration, the upload text. They are here rather than " +
			"on the pages that send them because a prompt is written before the first " +
			"run and then left alone, while those pages are where the session's work " +
			"happens — and reading down the menu is reading the whole run. A ✎ beside " +
			"a name means this project says something of its own there; the list " +
			"beside it is which wording that prompt uses, ＋ saves what is in the box " +
			"under a new name, and the button after it puts a built-in back or deletes " +
			"one you added.\n\n" +
			"This is the long one, so the two run buttons mean different things here. ⏸ " +
			"parks it between requests and ▶ carries on from the same frame; ⏹ ends it, " +
			"and if the describing had started the next ▶ describes the session from the " +
			"beginning. Edit a prompt and it is ⏹ you want — a resumed run would describe " +
			"the rest of the footage under the new wording and leave the first half " +
			"under the old."},
	{"cut", "Cut", "edit-cut-symbolic", "Choose the clips the video is made of",
		"Run Prepare first — the cut works on the footage and its frames",
		"The footage over the session timeline, with everything the cut keeps tinted " +
			"green over it, and a waveform lane per sound below. Each green stretch carries " +
			"an ✕ in its top-right corner, and pressing it drops that scene — ⌦ does the " +
			"same to whatever is in hand, which is how the cards and the sounds go. On the " +
			"seam between the pictures and the lanes a selection wears a ▲▼ handle saying " +
			"what it is about: both arrows lit is picture and sound together, ▲ narrows it " +
			"to the picture alone, and ▼ walks down through the recordings and back up. " +
			"That is what the verbs read, and the rule they read it by is that an " +
			"insert replaces what it brings and nothing else. A clip laid over a " +
			"picture-alone selection brings frames, so it takes the frames and leaves " +
			"every sound running underneath; laid over a ▼ recording it brings sound, so " +
			"it takes that one recording and the picture and the other lanes carry on; " +
			"laid over both it takes both. Spliced in it replaces nothing at all, because " +
			"it is time added to the session rather than a stretch of it, and it goes " +
			"silent. A file with no sound of its own laid over both is the one case the " +
			"arrows cannot settle, and the insert form asks. ▶ below asks " +
			"the model to fill the ▶ target length from what Describe found; from then " +
			"on the cut is yours — drag to select, add, remove, and Revert goes back to " +
			"the suggestion. Once you have edited by hand ▶ says so rather than throwing " +
			"the edits away — Revert first if you want a fresh suggestion. The scroll " +
			"wheel zooms around the cursor.\n\nA left click " +
			"drops the red playhead; the clock under the transport buttons is where it " +
			"landed, in mm:ss.d of session time, and hovering it says which recording " +
			"that is, which frame, and whether the cut keeps it; the ⟦ in and out ⟧ marks " +
			"print their times the same way, in small type under their own buttons. Over " +
			"the picture and the toolbar the wheel steps frames instead of zooming — a " +
			"notch is a frame, five with Shift — and stepping or playing past the view's " +
			"edge scrolls the timeline to put the red line back in the middle.\n\nA clip's own " +
			"edges are trimmed rather than re-selected, and that is two buttons: the " +
			"right one picks a green border up — it turns white — and does nothing else, " +
			"so the choice keeps while your hand moves. Then move it, either by dragging " +
			"the white bar with the left button or a frame at a time with ‹f and f› (the " +
			"arrow keys do the same, Shift for five). The picture follows the edge the " +
			"whole way, so you trim against the frame rather than against a ruler, and " +
			"▶ plays from the edge rather than from the playhead. A left click clear of " +
			"the bar, or a right-click clear of any border, puts it down; the whole trim " +
			"is one Undo.\n\nA whole clip moves the same way, one level up: the right " +
			"button pressed on a clip away from its borders picks up the CLIP — outlined " +
			"in white — and a left drag then slides it with its length intact, snapping " +
			"flush to the clip either side of it. ‹f and f› nudge it a frame, it never " +
			"leaves the recording it was cut from or overlaps its neighbours, and the " +
			"whole slide is one Undo. Picking anything up leaves the playhead where it " +
			"is — a press near a border takes the border, one away from it takes the clip, " +
			"and neither moves the picture. The preview follows what you MOVE: drag or " +
			"nudge either of them and it lands on the frame you left it on.\n\nA source on Inputs that is not marked as footage gets its own " +
			"lanes under the cut, in blue, one per channel — left and right apart, because " +
			"they are often separate things, a mic on one and the game on the other. A " +
			"stereo file with the same signal on both sides is one lane marked L=R: a mic " +
			"in one input of an interface is one recording however it was written out. The " +
			"video is the master: the timeline is the footage's, and a separate recording is " +
			"placed on it by its own clock (the timestamp in its name, or the file's time and " +
			"length), so it sits where it actually was rather than at the left edge. Only the " +
			"stretch that was running while the footage ran is drawn, over its own lighter " +
			"ground — the minutes before you started the capture card are visibly not there " +
			"rather than stretched to fit. The waveforms appear a moment after the page does; " +
			"each is decoded once and kept.\n\n" +
			"The two prompts this step sends — the cut, and the audit that reads it " +
			"back — are on Prepare, in the box that holds every prompt in the app. " +
			"What stays on this bar is the choice they are sent with: Style is what ▶ " +
			"asks for. General is the one to start on — it works out what the session " +
			"is before it cuts, and it is what a new project uses. The other three " +
			"already know what they are looking at, and cut better than General when " +
			"they are right: Highlights fills the length with the best moments of a " +
			"gaming session; Rating / tier list lines the subjects up, visits each one " +
			"and ends on the verdict; YouTube Shorts cuts a vertical video of half a " +
			"minute and decorates it with effects. Whichever you pick, what the cut is " +
			"actually about comes from the User Context on Prepare. It is the same " +
			"choice as the wording list beside the Cut prompt there — one store, two " +
			"views — so ＋ adds your own on Prepare and it appears here.\n\n" +
			"That column is where every form on this page opens — the insert's questions, " +
			"an effect's numbers — rather than in a window over the timeline, so the band " +
			"or the card a question is about stays on screen, and live, while it is " +
			"answered.\n\n" +
			"⧉ Insert drops a file into the cut at the playhead — a title card, a still, an " +
			"\"a few moments later\" clip, an SVG that animates itself. It shows in violet on " +
			"the track, the preview shows it while the red line is inside it — animated cards " +
			"animate — the footage under it gives way exactly as removing it would, and it is " +
			"never merged, trimmed or replaced by a later Suggest: a card is a file, not a " +
			"span. While a card is on the preview the session goes quiet, footage and " +
			"separate recordings alike, because that is the cut Produce makes; an inserted " +
			"video plays its own sound.\n\nThat is one of two modes, and the form asks " +
			"which — for every file, a sting as much as a card: over the footage, " +
			"as above, or between it. Between is what a card dropped at the playhead does, " +
			"since that is what Insert means; over the footage is what a card placed on a " +
			"marked selection does, since marking the seconds first is saying what they are " +
			"for. \"Insert between the footage\" cuts the video at that " +
			"point, plays the card, and carries on, so nothing filmed is lost and the video " +
			"gets longer by exactly the card. Where that shows is the \"cut\" figure under " +
			"the tracks, and in the finished video — not in the timeline, which is the " +
			"session's own clock and stays exactly as long as the recording however much " +
			"is cut out of it or spliced into it. Playing past one holds the footage where it " +
			"is, plays the card through, and lets it go again, which is what the finished " +
			"video does. An inserted card costs no session time, so it " +
			"is drawn as a violet marker hatched like the hole between two recordings " +
			"— the same picture, because it is the same thing: the footage stops here — as " +
			"wide as the card is long at the zoom you are at, so it grows with the clips " +
			"around it, and never narrower than a marker you can hit. Its name and length " +
			"are written beside it, and its seconds are typed in the form rather " +
			"than dragged. Right-click a card to hold it and ⧉ Insert becomes ✎ Edit: the " +
			"same form again, for what the card says, which mode it is in, and how long it " +
			"runs. Switching modes moves the footage back or out of the way to match.\n\n" +
			"A card that animates itself is rendered frame by frame, written either " +
			"way: SMIL (<animate>, <animateTransform>), or a <style> block with @keyframes " +
			"in it — opacity, transform and fill, with the delay and the easing the " +
			"stylesheet asks for. Whatever the file does not cover is drawn as it stands, " +
			"and anything Produce could not read says so in the log.\n\n" +
			"The project's assets folder starts with cards this app draws itself: tier.svg is " +
			"the S/A/B/C/D/F board, and s.svg, a.svg, b.svg, c.svg, d.svg, f.svg are single " +
			"letters that fly in. The board arrives with two of those letters flying onto it, " +
			"which is the one thing a still cannot show you and is what Just added below does. " +
			"They are filled in when you insert them — the board is the six tiers, S A B C D " +
			"F, and one line each: type \"Dust II, Mirage\" into Tier S and it is a red row " +
			"with two chips in it. A chip can be a " +
			"name, a logo, or both: \"Logo…\" beside a tier adds picture files to it, and " +
			"\"Dust II|logos/dust2.png\" is the name under the logo. A tier holds six; a " +
			"seventh is not drawn and the log says so. The last line, Just " +
			"added, is what the shot is about: the board you have already shown is put back up " +
			"quickly and these fly in on their own afterwards. A bare name (\"Mirage\") points " +
			"at an item already in a row above. \"D[1.1s]: logos/bla.svg, Test\" is the other " +
			"form — into row D, starting 1.1 seconds in (or 1100ms), the logo with Test under " +
			"it — and the list is comma separated, so several things can be sent in one after " +
			"another on a beat you choose. Leave the line empty and the whole board arrives at one " +
			"pace, as it always did. What you type is kept on the end of the path " +
			"(tier.svg?S=Dust II, Mirage&A=Nuke), so the same file is " +
			"a different board every time you place it and the cut says which. tier.svg is also " +
			"the file it is drawn from, and it is a board when you open it: every row and all " +
			"six places in it are written out with their own coordinates, and the {{holes}} in " +
			"them are what you typed and the timings the arrivals get — restyle that file and " +
			"every board drawn from it comes out restyled. " +
			"Your own SVG can " +
			"do the same: put {{name}} or {{name|default}} in it and Insert asks for each one, " +
			"and add <!-- Input: name | Label | what it is --> beside it to have the form ask " +
			"in your words rather than in the placeholder's. assets/CARDS.md is written into " +
			"the folder with the cards and says all of it — the canvas, the placeholders, and " +
			"how an animated one has to be written — for whoever, or whatever, writes the next one.\n\n" +
			"The aspect dropdown beside those buttons is for a video that is not the footage's " +
			"shape — 9:16 for a short, 1:1, 4:5 — and \"source\" is the absence of the choice: " +
			"nothing below applies until it is changed, and a cut that never touches it renders " +
			"exactly as it always did. With one chosen, the height set on Produce names the " +
			"output's height and the aspect names its width, and the video starts on the whole " +
			"frame, black either side, because throwing footage away is a choice you make, not " +
			"one made for you — until the first view: the moment one exists, the video is " +
			"framed on it from the very start, wherever on the timeline it was placed.\n\n" +
			"The four effects live in the ✚ Effect dropdown beside the aspect. " +
			"▭ View is that choice: jump anywhere, pick it, and draw on " +
			"the picture itself the region to show from there on. The rectangle keeps the cut's " +
			"aspect while you drag, snaps flush when you reach the full width or height of the " +
			"frame, and may be smaller — a crop, zoomed in — or larger, the frame with more " +
			"black around it. The form then asks the one number: 0 switches the camera at " +
			"that instant, seconds glide it over. Views chain — each one starts from wherever " +
			"the camera is at that moment, mid-glide included — and the camera stays until the " +
			"next one. ⊕ Zoom is a free drawing with a return ticket: any rectangle, not bound " +
			"to the aspect — the camera closes in on the smallest output-shaped window that " +
			"holds it — in over its own glide, held for its length, and back out over a separate " +
			"glide to wherever the views say the camera belongs. " +
			"⏩ Speed needs no drawing: it puts the seconds marked with in and out on a clock " +
			"of their own — type the rate, a fifth for slow motion or up to 100× to run " +
			"through the dead air of a long recording, the sound stretched or squeezed with " +
			"the picture and the pitch kept. ⏸ Stop holds the frame under " +
			"the playhead still for a time you type, which plays like a spliced card the " +
			"session never filmed.\n\nEvery effect is a mark on the lane under the picture band — orange " +
			"flags for views, teal brackets for zooms, rose bands for speed, rose also washed " +
			"over the footage that is off its own clock — and the marks answer to the timeline's own " +
			"verbs: the right button holds one (white) and on the held one opens its numbers, " +
			"a left drag slides it, ‹f and f› " +
			"nudge it a frame, ✎ Edit reopens its numbers, ⌦ deletes it, Esc puts it " +
			"down, and all of it shares the one Undo with the cut. A held view or zoom shows " +
			"its rectangle on the picture: draw beside it to re-frame it, or grab inside it " +
			"and slide it whole, its edges snapping onto the frame's top, left, right and " +
			"bottom as they come close; the right button on or inside it asks its times " +
			"again — a view's transition, a zoom's length and separate glides in and out — " +
			"except on the first view, which has none to ask. Paused, the preview draws " +
			"the camera as an outline over the full frame rather than cropping the picture — " +
			"you frame against everything the camera could see; playing, it zooms for real, " +
			"glides and all. It does not slow or " +
			"freeze; the \"cut\" figure under the tracks counts that added time, and " +
			"Produce renders it."},
	{"narrate", "Narrate", "audio-input-microphone-symbolic", "The narration, and the voice it is spoken in",
		"Finish Cut first — narration is written for the cut's clips",
		"▶ below is the initial fill: it writes the narration once (again only if the " +
			"cut moves) and speaks every line not cached yet. After that the lines are " +
			"yours: each box is \"[emotion] words\" with its start time beside it — edit " +
			"the words, the delivery, or when the line begins — a time inside another " +
			"clip moves the line to that clip, and one outside the cut is refused with " +
			"the line left where it was. ＋ beside the transport " +
			"adds a line at the paused second, 🗑 removes one, and an edited line is " +
			"simply re-spoken the first time it plays. The voice is cloned from a " +
			"sample, picked under the video; the slider and the picture play the cut, speaking each " +
			"line where it was placed, with the blue row following along. The bar is " +
			"the cut end to end — what the edit removed takes up no room on it, so " +
			"every point on it is video you keep, and the time beside it reads " +
			"\"session clock · how far into the finished video\". The cut is " +
			"the master: once it is rolling it keeps rolling, clip after clip, and " +
			"nothing moves the picture but you. Picking a line — clicking its row, or " +
			"its ▶ — starts three seconds before that line so you can watch it land, " +
			"and then the preview simply carries on down the cut. A clip the narration " +
			"left alone keeps its row and its ▶ too: that one plays the clip from the " +
			"top on its own audio, which is how you decide whether it wants a line. " +
			"A line whose time you retype into another clip leaves that row behind it, " +
			"empty. ⏪ and ⏩ " +
			"jump three seconds; stopped, the wheel over the slider steps one frame " +
			"at a time, which is the resolution a line's placement is judged at. The " +
			"playhead only lands on material the cut kept: dropped into a gap it " +
			"carries on the way it was going, to the head of the next clip or back " +
			"to the tail of the one behind.\n\nA take " +
			"is stable: the same words, delivery and voice always come back as the " +
			"same performance, so nothing you have approved changes behind you. " +
			"When a delivery is wrong rather than the words, the row's ↻ re-rolls " +
			"it — same line, a new draw — and plays the result.\n\nThe TTS blends eight base emotions — " +
			"happy, angry, sad, afraid, disgusted, melancholic, surprised, calm — " +
			"and maps whatever is in the [brackets] onto them. One or two of those " +
			"words work best; \"loud\" or \"fast\" is not an emotion and only dilutes " +
			"the match (anger already shouts, calm is already slow). Length lives in " +
			"the words themselves: more short exclamations, not stretched letters, " +
			"which get spelled out.\n\nPlain words are read by a small judge model, " +
			"which is forgiving but approximate. Write a weight and it is skipped, " +
			"the mix going to the engine exactly as asked: \"[angry=1]\" is pure " +
			"anger at full force, \"[happy=0.8, surprised=0.4]\" a blend you chose. " +
			"Weights run 0 to 1, and the difference is audible: \"[surprised]\" " +
			"is the judge's reading of the word, \"[surprised=1]\" the axis " +
			"itself at full force.\n\nBeside the eight there are named mixes of " +
			"them, which take a weight the same way: excited, ecstatic, playful, " +
			"proud, relieved, hopeful, tender, nostalgic, solemn, awed, alarmed, " +
			"horrified, desperate, confused, frustrated, bitter, contemptuous, " +
			"dismayed, heartbroken, ominous, tense. \"[excited=1]\" is happiness " +
			"with surprise mixed into it, which is what excitement sounds like " +
			"and what plain happiness does not. Any name none of these knows sends the " +
			"line back to the judge rather than guessing at an axis."},
	{"produce", "Produce", "applications-multimedia-symbolic", "Render the video and write the upload text",
		"Finish Cut first — there is no cut to produce a video from",
		"▶ below renders the final video: every clip is cut from its own recording, " +
			"the narration is laid over ducked game audio, and the whole thing is " +
			"loudness-normalized to -14 LUFS for YouTube. Lines that have not been " +
			"synthesized yet are spoken first.\n\nA session's separate recordings are " +
			"in the sound as well as in the picture: whatever was running while a clip " +
			"was running is mixed into it, from the second of that recording the clip " +
			"actually falls on — the same placement the blue lanes on Cut are drawn " +
			"from, with the footage as the master. It joins the game audio rather than " +
			"the narration, so \"Game audio under voice\" ducks both together and the " +
			"spoken lines still sit on top. Cards are left silent: a card is time added " +
			"to the cut, not a moment of the session, so there is nothing that was said " +
			"under it.\n\nWhen it finishes, the result is " +
			"waiting in the picture below: ▶ then plays it rather than producing " +
			"again, and ⏹ hands ▶ back to producing. The row at the top says what " +
			"is going in — the cut, the lines, and how many of them still have to " +
			"be spoken — and the row at the foot says what is on disk."},
	{"publish", "Publish", "send-to-symbolic", "Draw the thumbnail and write the upload text",
		"Finish Cut first — there is nothing to make a thumbnail of yet",
		"The two things a finished video still needs: a thumbnail, and the text " +
			"under it on the upload page. The page is split the same way — the " +
			"picture and everything that makes it on the left, the words on the " +
			"right.\n\nThe first ▶ writes the title and the description, and then " +
			"draws. Every ▶ after that only draws — the model is not asked again, " +
			"however much you edit the boxes, so rewording the instruction or " +
			"changing the images costs GPU time and no thinking. Deleting the step6 " +
			"folder is what starts the text over; Suggest again, beside the title, " +
			"rewrites both without redrawing.\n\nThe edit instruction is yours to " +
			"write — nothing suggests one. There was a second model job that picked " +
			"a frame and wrote an instruction for it, and it was removed because it " +
			"did neither well.\n\nThe picture is usually not made from nothing — " +
			"it is an edit of one of your own frames, which is what keeps it " +
			"recognizably this video rather than a stock illustration of the " +
			"genre. Two are taken from the cut on the first run, and the row is " +
			"yours after that: Add image… puts another one in, − takes one out, " +
			"Change… swaps one in place. The first in the row is the picture being " +
			"edited; the others are references the instruction can name by " +
			"position — \"put the ship from the second image behind them\" — and " +
			"Make base promotes one of them to the front. Empty the row and the " +
			"thumbnail is drawn from the instruction alone.\n\nWrite the instruction as instructions, not as a description " +
			"of a picture — say what to change, and everything you do not mention " +
			"stays as it is. That is the whole reason this is an edit model rather " +
			"than the image-to-image one it used to be: there, a single strength " +
			"dial renoised the entire frame, so asking for one change altered every " +
			"other thing too.\n\nThe title is lettered by the image model as part " +
			"of the instruction. Keep it to four or five words — that is what " +
			"survives being shrunk to a phone's sidebar, and it is also what a model " +
			"spells reliably. Leave it empty and the picture comes back without any."},
}

// stepLocked reports whether a tab's prerequisites are missing. Prepare
// never is -- it is where the sources are added, so a project with nothing in
// it still has to be able to open it -- and an unknown name (there is none, but
// the lookup can fail) counts as open rather than as locked: refusing to show a
// page is the worse mistake.
func (a *App) stepLocked(i int) bool {
	if i < 0 || i >= len(steps) {
		return false
	}
	switch steps[i].name {
	case "cut":
		return a.cutLocked
	case "narrate":
		// choosing a voice lives on this tab and therefore waits for a cut,
		// which it does not need. That is deliberate: it belongs beside the lines
		// it will speak, and hearing a sample before there is anything to narrate
		// decides nothing you cannot decide again afterwards.
		return a.narrateLocked
	case "produce":
		return a.produceLocked
	case "publish":
		// the thumbnail is painted over a frame the cut kept and its text is
		// written from the clips, so this waits on the same thing Produce does --
		// but not on the video itself, which is a long render you should be able
		// to start the upload text without waiting for
		return a.produceLocked
	}
	return false
}

func stepIndex(name string) int {
	for i, s := range steps {
		if s.name == name {
			return i
		}
	}
	return -1
}

// showStep moves to a page without going through the tab's click handler --
// for the bounce off a locked tab, and for sending a page whose prerequisites
// vanished back to the start.
func (a *App) showStep(name string) {
	// whatever was half-typed on the page being left is on disk before it is
	// left: the narration boxes write on a beat after the typing stops, and
	// clicking a tab is exactly the moment that beat has not come yet
	a.narr.flushSave()
	a.tabGuard = true
	for i, s := range steps {
		a.tabs[i].SetActive(s.name == name)
	}
	a.tabGuard = false
	a.stack.SetVisibleChildName(name)
	a.outStack.SetVisibleChildName(name) // the Outputs group on the shared bar is this page's
	a.updateRunControls()                // ▶ ⏹ belong to the new page's playback now
	a.syncHelp()                         // and so does the ⓘ
	// Cut's Inputs row lists what Suggest will be sent, and one of those things
	// is the context box on Describe -- the page you have usually just come
	// from. Refreshed on arrival rather than on every keystroke over there,
	// which would re-read the session timeline as you type.
	// ...and the tracks themselves, when the last run (or another project) moved
	// what they are drawn from. Only then: a rebuild probes every recording and
	// drops the undo history, which is not what a tab click should cost.
	if name == "cut" && a.ed != nil {
		if a.ed.stale || len(a.ed.vids) == 0 {
			a.updateCutInfo() // which does updateInputs itself
		} else {
			a.ed.updateInputs()
		}
	}
	// Narrate's row says the same thing about the narration, and one of the
	// things it counts is the cut -- which is the page you have just come from
	// and the one thing most likely to have changed under it.
	if name == "narrate" && a.narr != nil {
		a.narr.refit() // a clip dragged wider over there moves its line's slot here
		a.narr.updateInputs()
		a.narr.updateOut()
	}
	// ...and Produce reads both of those, plus the synthesis cache and the file
	// it wrote last time. It is the end of the chain, so everything upstream is
	// something that may have moved since it was last looked at.
	if name == "produce" {
		a.updateProduceInfo()
	}
	// Publish reads all of that AND the folder it wrote into last time, since the
	// thumbnail on the page is the file on disk rather than something remembered
	if name == "publish" && a.pub != nil {
		a.pub.refresh()
	}
}

// helpPopover is what the ⓘ in the header bar drops down: what the open page
// does, and only that. It listed all five steps at once to begin with, which
// made the paragraph you actually wanted something to go looking for in a
// scrolling column of four you did not.
//
// The two labels are kept because the popover now follows the tabs; the table
// they are filled from is a compile-time constant, so syncHelp is all there is.
func (a *App) helpPopover() *gtk.Popover {
	a.helpTitle = gtk.NewLabel("")
	a.helpTitle.SetXAlign(0)
	a.helpTitle.AddCSSClass("heading")
	a.helpBody = gtk.NewLabel("")
	a.helpBody.SetXAlign(0)
	a.helpBody.SetWrap(true)
	a.helpBody.SetMaxWidthChars(64) // wrapping needs a width to wrap at, or it never does
	a.helpBody.AddCSSClass("dim-label")

	box := gtk.NewBox(gtk.OrientationVertical, 4)
	box.SetMarginTop(4)
	box.SetMarginBottom(4)
	box.SetMarginStart(4)
	box.SetMarginEnd(4)
	box.Append(a.helpTitle)
	box.Append(a.helpBody)

	p := gtk.NewPopover()
	p.SetChild(box)
	return p
}

// syncHelp points the ⓘ at the page that is open. Called from showStep, which
// every page change goes through -- including the bounce off a locked tab,
// where the help must stay on the page you did not leave.
func (a *App) syncHelp() {
	if a.helpTitle == nil {
		return
	}
	i := stepIndex(a.stack.VisibleChildName())
	if i < 0 {
		return
	}
	a.helpTitle.SetText(steps[i].label)
	a.helpBody.SetText(steps[i].help)
}

type App struct {
	root   string // autocut directory (the scripts and project files live here)
	vidDir string // where the last video came from; where a chooser opens
	audDir string // same, for a recording -- neither is what the list is made of
	outDir string // where step outputs go; default root, project-settable

	win   *gtk.ApplicationWindow
	stack *gtk.Stack
	// one tab per entry in steps, in that order; they gray out with a hint
	tabs          []*gtk.ToggleButton
	tabGuard      bool          // suppresses re-entrant toggles while reverting
	cutLocked     bool          // no session timeline yet -- nothing to cut
	narrateLocked bool          // no cut yet -- nothing to narrate
	produceLocked bool          // no cut yet -- nothing to produce
	logExp        *gtk.Expander // collapsed until something actually runs
	helpTitle     *gtk.Label    // the ⓘ popover, refilled per page by syncHelp
	helpBody      *gtk.Label
	log           *gtk.TextView
	linkTag       *gtk.TextTag      // paths in the log that open on a click (logPath)
	linkPaths     map[string]string // what a tagged path displays -> where it really is
	llmMu         sync.Mutex        // guards llmSeq; describe calls from worker goroutines
	llmSeq        int               // per-run counter naming the llm/ exchange files
	status        *gtk.Label
	running       bool
	audioNoted    string // the audio.cpp server already reported in the log
	ttsModel      string // the model id that server serves, asked for once

	// The Prepare page's own controls -- the settings a run reads off it,
	// which is everything on that page a runner needs and nothing it draws.
	srcList   *sourceList // the session's files, and what each one is for
	interval  *freqPick
	scalePick *gtk.DropDown
	langEntry *gtk.Entry // what the ASR is told this session is spoken in

	// One controller per page, in tab order (steps). Each is nil until its page
	// has been built, which is what every headless test is and also what the
	// first seconds of a session are -- so every reader checks.
	prep *preproc
	ed   *cutEditor
	narr *narrator
	prod *producer
	pub  *publisher

	// The Narrate page's voice picker, and the choice it holds. The id is
	// cached from step4/voice.txt and guarded: since the narrator's name became
	// part of every step's input (tlLabel), it is read by the describe and
	// transcript workers as well as by the GUI thread.
	voicePick *voicePicker
	voiceMu   sync.Mutex
	voiceSel  string
	pitchSel  float64 // semitones the reference is shifted by, from step4/pitch.txt
	pitchRead bool    // ...and whether that file has been read yet (0 is a real value)
	// the hand-picked voice-clone takes, by recording (narrate_take.go), under
	// the same lock and for the same reason: they are part of the cache key, so
	// they are read by whatever thread is about to speak a line.
	takesMap  map[string][]voiceTake
	takesRead bool

	progress *gtk.ProgressBar
	playBtn  *gtk.Button
	stopBtn  *gtk.Button
	outStack *gtk.Stack // the visible step's Outputs group; each page adds its own by step name

	// pipeline control: pause parks the runners at the next checkpoint, stop
	// kills the in-flight subprocesses; finished stages stay on disk either
	// way. STT and frame extraction run as parallel tracks (GPU vs CPU), so
	// several subprocesses can be live at once.
	ctlMu     sync.Mutex
	curCmds   map[*exec.Cmd]bool
	stopFlag  atomic.Bool
	pauseFlag atomic.Bool
	runCtx    context.Context // canceled by stop -- aborts in-flight LLM calls
	runCancel context.CancelFunc
	// ⏸ parks a run, ⏹ abandons one, and the difference has to be visible on
	// the next ▶: resume where it stopped, or start over. Only the describer
	// can tell them apart at all -- it is the one job that resumes per chunk --
	// so a stopped Describe + Transcript sets this, and the next run of that
	// step throws its half-written event logs away first. See resetDescribe.
	undRestart bool

	srcMu sync.Mutex
	// snapshot of the session's sources, taken on the GUI thread when a run
	// starts. selItems is the whole of it and the other three are what the
	// pipeline actually asks for, derived from it: the footage, then the rest,
	// and who was tagged as which narrator. They are kept together because a
	// run can rewrite the session -- splitting a voice off a recording turns
	// one row into two -- and every one of them has to say so at once.
	selItems []sourceItem
	selVid   []string
	selAud   []string
	selNarr  [narratorSlots]string
	// which audio tracks of each source the session uses, keyed on path. A path
	// this says nothing about uses its first track alone, so an ordinary
	// session's map is empty and every reader of it is a no-op (cut_tracks.go).
	selTracks map[string][]int

	// the editable system prompts. The views are the GUI thread's; promptTxt is
	// the copy a runner reads, kept current by the buffers' changed handler --
	// same rule as selVid/selAud, for the same reason.
	promptMu    sync.Mutex
	promptTxt   map[string]string
	promptViews map[string]*gtk.TextView
	// the several wordings a job can have and which one this project picked
	// (prompts.go). promptSty holds only what the project has of its own to say
	// -- an edited built-in or one it invented -- so both are keyed by job, then
	// by style name. Under promptMu with promptTxt: setPrompt writes all three.
	promptSty  map[string][]promptStyle
	promptPick map[string]string
	// what the prompt folder is believed to hold, so the flush that mirrors
	// promptSty onto disk writes only what changed (promptstore.go). GUI
	// thread only, like the views: it is written from the autosave tick.
	promptDisk map[string]string
	// the picker widgets, and the flag that stops filling them from reading as
	// the user editing them. GUI thread only, so no lock (showPromptStyle).
	promptRows  map[string]promptRow
	styleDrops  map[string]styleDrop // wording dropdowns surfaced on a page (styleBar), by key
	promptQuiet bool
	// redraws the ✎ marks on the bench's menu when a project load or an edit
	// changes what the project is holding (prepedit.go)
	prepSync func()
	// every text box's heading row, so a row with a Reset button and a row with
	// a bare label are the same height and the boxes under them line up
	// (editorBody). GUI thread only, like the views.
	headGroup *gtk.SizeGroup
	// what langEntry says, for the runner to read; guarded like ctxTxt below and
	// for the same reason
	langTxt string
	// what the editor says about THIS session, typed on Prepare and read by
	// every step (context.go). Under promptMu for the same reason as the
	// prompts: the box belongs to the GUI thread, the string is what a runner
	// reads. Not in promptTxt -- it is not a prompt and it is stored in full,
	// whereas a prompt is stored only when it differs from the built-in.
	ctxTxt string
	// the bench's box while it is showing the context row, and nil while it is
	// showing a prompt (prepedit.go). Nil is normal, not a "page not built
	// yet": a project loaded while a prompt is on screen writes the cache
	// only, and the box rereads it on the next switch.
	ctxView *gtk.TextView

	// The project file and what was last written to it. projPath is never empty:
	// a session with no name of its own is root/session.autocut, and outDir is
	// derived from whichever of the two it is (project.go). Both are the GUI
	// thread's. projLabel is projPath's name in the header bar; nil under the
	// tests, which build an App without a window. openPath is a file the desktop
	// handed us to open -- a double-click -- and is read once, by build().
	projPath  string
	projSaved []byte
	projLabel *gtk.Label
	openPath  string

	// the header bar and the parts of it that give up words when the window is
	// narrow (headfit.go). headBtns is the six icon buttons at the two ends --
	// they never change size, so what they take is simply subtracted; tabWords
	// is the word inside each tab, hidden as a group when only the icons fit.
	// tabWordsOn says which of the two states the row is in, because the row's
	// own measurement cannot tell us and both states have to be priced from
	// either one. All GUI-thread, like every widget here.
	head       *gtk.HeaderBar
	headBtns   []gtk.Widgetter
	tabRow     *gtk.Box
	tabWords   []*gtk.Label
	tabWordsOn bool
	headFitQ   bool
	headWatch  bool

	// one progress bar fed by both tracks: summed fractions, and the work each
	// of them still has queued (runqueue.go)
	progMu    sync.Mutex
	progParts [2]float64
	// where in the bar the running phase is drawn, for the one press that is
	// two steps (qPhase). Share 0 is the whole bar, which is every other press.
	progBase, progShare float64
	progQ               [2]qTrack
}

// The frame slider snaps to these stops; a linear 0.1..5 s scale would cram
// all the useful low end into the first pixel. Index 0 keeps every frame.
var frameStops = []float64{0, 0.1, 0.2, 0.5, 1, 2, 3, 4, 5}
var frameStopLabels = []string{"each", "0.1", "0.2", "0.5", "1s", "2s", "3s", "4s", "5s"}

// where a session with nobody's opinion on it starts: one frame a second. Named
// rather than typed twice, since a new project has to land on the same stop the
// stepper builds itself at -- and index 0 is not it (that is every frame, which
// is gigabytes).
const defFrameStop = 4

// Frame size presets, no-resize first because that is the default and picking a
// size is the exception. Name is the identity and Label is only what the drop
// down shows: the name goes into the project file AND into the stamp that
// decides whether frames must be extracted again, so it has to stay put even
// when the wording moves. 896w is the width the vision model gets fed.
var scalePresets = []struct{ Name, Label, VF string }{
	// the label said "Resize", which on the closed button read as the
	// control's caption and in the open list as a nonsense first choice
	{"original", "Original", ""},
	{"896w (LLM)", "896w (LLM)", "scale=896:-2"},
	{"480p", "480p", "scale=-2:480"},
	{"720p", "720p", "scale=-2:720"},
	{"1080p", "1080p", "scale=-2:1080"},
}

func (a *App) frameScale() (name, vf string) {
	i := int(a.scalePick.Selected())
	if i < 0 || i >= len(scalePresets) {
		i = 0
	}
	return scalePresets[i].Name, scalePresets[i].VF
}

func (a *App) setFrameScale(name string) {
	for i, p := range scalePresets {
		if p.Name == name || p.Label == name {
			a.scalePick.SetSelected(uint(i))
			return
		}
	}
}

// defLanguage is what a session is assumed to be in when nobody says. It was a
// setting on this machine (llm.conf) and is now a field on the project, for the
// reason setup.go's header gives: the stack is the same every night, the
// footage is not.
const defLanguage = "en"

// projectLanguage is the box's text as the project file stores it: what was
// typed, trimmed, and "" when nothing was -- an empty box means the default,
// and writing "en" into every project would freeze today's default into files
// that never asked for it (same rule as an unedited prompt).
func (a *App) projectLanguage() string {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	return strings.TrimSpace(a.langTxt)
}

// asrLanguage is the same value with the default filled in: what Prepare puts in
// the request. Callable from a runner's goroutine, which is the whole reason
// the string is cached beside the widget rather than read off it -- the box is
// the GUI thread's (same rule as sessionCtx).
func (a *App) asrLanguage() string {
	if s := a.projectLanguage(); s != "" {
		return s
	}
	return defLanguage
}

func (a *App) setLanguage(s string) {
	a.promptMu.Lock()
	a.langTxt = s
	a.promptMu.Unlock()
}

// applyLanguage loads a project's language into the box as well as the cache.
// GUI thread only, like applySessionCtx and for the same reason.
func (a *App) applyLanguage(s string) {
	a.setLanguage(s)
	if a.langEntry != nil {
		a.langEntry.SetText(s)
	}
}

func (a *App) frameInterval() float64 { return frameStops[a.interval.i] }

func (a *App) setFrameInterval(v float64) { a.interval.set(nearestStop(v)) }

// nearestStop maps seconds onto the stop table -- a project may store a value
// from a build whose table was different, and typing is free-form.
func nearestStop(v float64) int {
	best, bd := 4, math.MaxFloat64 // default 1 s
	for i, s := range frameStops {
		if d := math.Abs(s - v); d < bd {
			best, bd = i, d
		}
	}
	return best
}

// freqPick is the frame-frequency stepper: one stop visible at a time, - / +
// walking the table, and the text editable by hand. Hand-rolled rather than a
// GtkSpinButton because the spin button re-reads its own display as a plain
// number before every step -- and "each" and "1s" are not numbers, so every
// click first corrupted the value it was about to step.
type freqPick struct {
	box   *gtk.Box
	entry *gtk.Entry
	i     int
}

func newFreqPick() *freqPick {
	f := &freqPick{}
	f.entry = gtk.NewEntry()
	f.entry.SetWidthChars(4)
	f.entry.SetMaxWidthChars(4)
	f.entry.SetAlignment(0.5)
	f.entry.SetTooltipText("Seconds between frames — type 0.1 to 5, or 'each' for every frame")
	f.entry.ConnectActivate(f.parse)
	// clicking elsewhere commits like Enter does: a half-typed value silently
	// kept on screen would not be the value a run then uses
	fc := gtk.NewEventControllerFocus()
	fc.ConnectLeave(f.parse)
	f.entry.AddController(fc)

	minus := gtk.NewButtonFromIconName("list-remove-symbolic")
	minus.ConnectClicked(func() { f.set(f.i - 1) })
	plus := gtk.NewButtonFromIconName("list-add-symbolic")
	plus.ConnectClicked(func() { f.set(f.i + 1) })

	f.box = gtk.NewBox(gtk.OrientationHorizontal, 0)
	f.box.AddCSSClass("linked") // one control, three parts
	f.box.Append(f.entry)
	f.box.Append(minus)
	f.box.Append(plus)
	f.set(defFrameStop)
	return f
}

// set clamps to the table -- past either end the buttons simply stop moving.
func (f *freqPick) set(i int) {
	if i < 0 {
		i = 0
	}
	if i >= len(frameStops) {
		i = len(frameStops) - 1
	}
	f.i = i
	f.entry.SetText(frameStopLabels[i])
}

// parse commits a typed value: seconds with or without the s, or "each"/"0"
// for every frame, landing on the nearest stop. Anything unreadable snaps the
// display back to the value still in force.
func (f *freqPick) parse() {
	t := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(f.entry.Text())), "s")
	if t == "each" {
		f.set(0)
		return
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		f.set(f.i)
		return
	}
	f.set(nearestStop(v))
}

// Where each step writes. The folders keep their stepN names although the tabs
// no longer do: renaming them would orphan every project already on disk, and a
// folder name is not a label anyone reads twice. Prepare owns the first
// two -- step1/ for the transcripts and frames, step2/ for what the models made
// of them -- and inside step2/ the two jobs get a folder each, because the
// describer resumes per chunk and the fixer does not.
func (a *App) inputsDir() string     { return filepath.Join(a.outDir, "step1") }
func (a *App) narrateDir() string    { return filepath.Join(a.outDir, "step4") }
func (a *App) produceDir() string    { return filepath.Join(a.outDir, "step5") }
func (a *App) understandDir() string { return filepath.Join(a.outDir, "step2") }
func (a *App) describeDir() string   { return filepath.Join(a.understandDir(), "describe") }
func (a *App) transcriptDir() string { return filepath.Join(a.understandDir(), "transcript") }

// framesDir is where the first half of Prepare leaves one video's frames:
// the describer reads them and the Cut page waits for them, so the path is
// named once.
func (a *App) framesDir(base string) string {
	return filepath.Join(a.inputsDir(), "frames", base)
}

// canCut is what the Cut page needs before it can show anything: frames, out
// of a source marked as footage.
//
// It used to be session.tsv -- Describe's output -- which is not the same
// thing. A silent screen capture has no words to fix and a session nobody
// wants described is cut by hand; in both, Describe would be a step run for no
// reason except to unlock the next one. The timeline is built from the sources
// and their frames. What Describe adds is the text ON that timeline, and
// having none of it is an empty track, not a locked page -- the page says so
// itself, under the tracks, and the suggestion button says it again.
func (a *App) canCut() bool {
	vids, _ := a.snapSources()
	for _, v := range vids {
		ents, err := os.ReadDir(a.framesDir(baseName(v)))
		if err != nil {
			continue
		}
		for _, e := range ents {
			// the same rule planVideo reads them by: jpgs, never the marker
			if strings.HasSuffix(e.Name(), ".jpg") && !strings.HasPrefix(e.Name(), ".") {
				return true
			}
		}
	}
	return false
}

func main() {
	a := &App{curCmds: map[*exec.Cmd]bool{}}
	wd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(wd, "input_video")); err != nil {
		// started elsewhere: the binary lives in <root>/gui
		exe, _ := os.Executable()
		wd = filepath.Dir(filepath.Dir(exe))
	}
	a.root = wd
	a.vidDir = filepath.Join(wd, "input_video")
	a.audDir = filepath.Join(wd, "input_audio")
	// A session always has a file, before anyone saves one (see projExt). Set
	// here rather than through setProject, which refreshes pages that do not
	// exist yet; from build() on, setProject is the only thing that moves it.
	a.projPath = filepath.Join(wd, workName)
	a.outDir = dataDir(a.projPath)

	// HandlesOpen is the double-click. Without the flag gio treats a file
	// argument as an error and refuses to start; with it, the desktop's "open
	// with Autocut" arrives at ConnectOpen and a bare launch still goes to
	// ConnectActivate. GApplication is single-instance, so a double-click while
	// a window is up is forwarded to this same process rather than starting a
	// second one -- which is why open loads into the window that exists instead
	// of building another.
	app := gtk.NewApplication(appID, gio.ApplicationHandlesOpen)
	app.ConnectActivate(func() { a.build(app) })
	app.ConnectOpen(func(files []gio.Filer, _ string) {
		var path string
		if len(files) > 0 {
			path = files[0].Path() // one window, one project: the rest are ignored
		}
		if a.win == nil {
			a.openPath = path
			a.build(app)
			return
		}
		if path != "" {
			a.loadProjectFrom(path)
		}
		a.win.Present()
	})
	os.Exit(app.Run(os.Args))
}

// ---- state -----------------------------------------------------------------

// loadMeta reads step1/meta.env: the primary video and recording of the last
// run, and the frame settings it ran with. Its existence is also the marker
// that Prepare has run at all -- either of the two files may be missing from a
// legitimate session, so the keys are not that marker.
func (a *App) loadMeta() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(a.outDir, "step1", "meta.env"))
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

// snapSources caches the session's sources. Background runners must never touch
// the list widget -- GTK objects belong to the GUI thread -- so every run takes
// this snapshot first and works from it.
//
// The pair it hands back is the list split by role: the footage, then
// everything else. Every source is in exactly one of the two, which is what
// makes appending them the whole session -- the reason a video that is also a
// voice is one row here and not one row in each of two lists.
func (a *App) snapSources() (vids, auds []string) {
	if a.srcList == nil {
		return a.snappedSources()
	}
	return a.snapItems(append([]sourceItem(nil), a.srcList.items...))
}

// snapItems takes one list of sources as the run's snapshot and hands back the
// two path lists the pipeline works from. It goes through a sourceList of its
// own rather than reading the fields: what counts as footage and who holds a
// narrator slot are that type's rules, and the run must not answer either
// question differently from the page.
func (a *App) snapItems(items []sourceItem) (vids, auds []string) {
	l := sourceList{items: items} // the rules, without the widget
	vids, auds = l.split()
	var narr [narratorSlots]string
	for n := 1; n <= narratorSlots; n++ {
		narr[n-1] = l.narratorPath(n)
	}
	// only the rows that answered, so the map is empty for every session that
	// has no multi-track file in it and nothing downstream has to tell "the
	// first track" from "no answer" a second time
	tracks := map[string][]int{}
	for _, it := range items {
		if len(it.tracks) > 0 {
			tracks[it.path] = append([]int(nil), it.tracks...)
		}
	}
	a.srcMu.Lock()
	a.selItems, a.selVid, a.selAud, a.selNarr, a.selTracks = items, vids, auds, narr, tracks
	a.srcMu.Unlock()
	return
}

// snappedTracks is the per-file track choice from the run's snapshot, for the
// background work that must not touch the list widget. Nil before any snapshot
// has been taken, which reads as "nobody chose anything" and is the truth.
func (a *App) snappedTracks() map[string][]int {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selTracks
}

func (a *App) snappedSources() (vids, auds []string) {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selVid, a.selAud
}

// snappedItems is the whole snapshot, for the one thing that rewrites it.
func (a *App) snappedItems() []sourceItem {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return append([]sourceItem(nil), a.selItems...)
}

// sepWanted is every source still waiting for its voice to be lifted off, from
// the snapshot -- so the runner can ask without touching a widget.
func (a *App) sepWanted() []string {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	l := sourceList{items: a.selItems}
	return l.sepVoiceWanted()
}

// narratorPath is the recording tagged with slot n, from the snapshot -- so a
// runner can ask who narrator 2 is without touching a widget.
func (a *App) narratorPath(n int) string {
	if n < 1 || n > narratorSlots {
		return ""
	}
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selNarr[n-1]
}

// voiceSource is the recording the narration is cloned from: narrator 1, or --
// for a session nobody has tagged -- the same guess the pipeline made before
// the tags existed, the first recording, and failing that the first source at
// all, which is the single-video session.
func (a *App) voiceSource() string {
	if p := a.narratorPath(1); p != "" {
		return p
	}
	vids, auds := a.snappedSources()
	if len(auds) > 0 {
		return auds[0]
	}
	if len(vids) > 0 {
		return vids[0]
	}
	return ""
}

// ---- UI --------------------------------------------------------------------

func (a *App) build(app *gtk.Application) {
	// activate fires again when a second launch forwards to this instance;
	// building twice would spawn extra windows that start playing on their own
	if a.win != nil {
		a.win.Present()
		return
	}
	a.win = gtk.NewApplicationWindow(app)
	a.win.SetTitle("Autocut")
	// the few styles of our own: the no-timestamp flag on a source row, and the
	// settings dialog's test verdicts. Plain GTK only promises the semantic
	// warning/success classes on a handful of widgets, so the colors are stated
	// here rather than hoped for from the theme.
	css := gtk.NewCSSProvider()
	css.LoadFromData(".stamp-warn { color: #e5a50a; } " +
		".test-ok { color: #26a269; } .test-bad { color: #c01c28; } " +
		// the bars a preview's aspect leaves over, in the color every other
		// player puts there instead of the page background (see videoFrame)
		".videoframe { background-color: #101010; } " +
		// a slider whose scale marks are words rather than numbers: three lines
		// of body text stacked under a trough make the row taller than
		// everything beside it, and the words are a legend, not a reading
		".tinyscale value, .tinyscale marks label { font-size: 0.78em; }")
	gtk.StyleContextAddProviderForDisplay(gtk.BaseWidget(a.win).Display(), css,
		gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	// fits a 1366x768 laptop with room for the panel: every page is either
	// scrolled or split by a divider, so a bigger screen is worth more space
	// but no page depends on having it
	a.win.SetDefaultSize(1240, 740)

	head := gtk.NewHeaderBar()
	a.head = head
	// Symbols, like everything else in this bar. Two spelled-out labels took
	// more of the title bar than the four icon buttons at the other end put
	// together, for the two things pressed least often in the app -- and they
	// were the only words up there, so they read as the important ones. The
	// tooltip says what the icon means, which is the deal every other button
	// here already makes.
	// New, then Open, then Save: the order every application puts them in, and
	// the order they happen in
	newP := gtk.NewButtonFromIconName("document-new-symbolic")
	newP.SetTooltipText("New project — empty the session and start over")
	newP.ConnectClicked(a.newProjectDialog)
	loadP := gtk.NewButtonFromIconName("document-open-symbolic")
	loadP.SetTooltipText("Load a project — sources, prompts and settings")
	loadP.ConnectClicked(a.loadProjectDialog)
	saveP := gtk.NewButtonFromIconName("document-save-symbolic")
	saveP.SetTooltipText("Save this project to a file")
	saveP.ConnectClicked(a.saveProjectDialog)
	head.PackStart(newP)
	head.PackStart(loadP)
	head.PackStart(saveP)
	// Which file this session is being written to. Projects are files in a
	// folder and several sit side by side (a recut, a variant, last week's), and
	// until now the window said nothing about which one Save was following --
	// the title bar was the five tabs and nothing else, so the only way to find
	// out was to open the Save dialog and read the name it proposed.
	//
	// The whole path when the bar has room for it, the file name alone when it
	// does not (fitHeader). The name alone used to be all it ever showed, on
	// the argument that variants of one session share a folder and the leading
	// directories are the identical half -- true, but not the only question
	// asked of this label. "Which of the three tom.json on this machine is
	// open" is the other one, and it is the one you ask after loading from a
	// dialog that started somewhere else. So the path when it is free, and the
	// name when it costs the tab row its words.
	//
	// Ellipsized either way: the tabs are the title widget and sit centered, so
	// a project with a long name must not push them off center or shove the
	// buttons at the other end off the bar.
	a.projLabel = gtk.NewLabel("")
	a.projLabel.SetEllipsize(pango.EllipsizeEnd)
	a.projLabel.AddCSSClass("dim-label")
	a.projLabel.SetMarginStart(6)
	head.PackStart(a.projLabel)
	a.showProject()
	// every step needs a rescan after files change on disk, so it lives here
	rescan := gtk.NewButtonFromIconName("view-refresh-symbolic")
	rescan.SetTooltipText("Rescan inputs and outputs")
	rescan.ConnectClicked(a.rescanAll)
	head.PackEnd(rescan)
	setup := gtk.NewButtonFromIconName("preferences-system-symbolic")
	setup.SetTooltipText("Settings — the LLM and audio.cpp endpoints")
	setup.ConnectClicked(a.setupDialog)
	head.PackEnd(setup)
	info := gtk.NewMenuButton()
	info.SetIconName("help-about-symbolic")
	info.SetTooltipText("What this step does")
	info.SetPopover(a.helpPopover())
	head.PackEnd(info)
	// what the bar spends on things other than the tabs and the project name
	a.headBtns = []gtk.Widgetter{newP, loadP, saveP, rescan, setup, info}
	a.win.SetTitlebar(head)

	// run controls exist BEFORE the pages: page builders refresh their info
	// texts during construction, and those touch the shared progress bar
	// ▶ is play and pause both -- it draws itself from what is under way, and
	// what is under way may be a run or this page's playback. See transport.
	a.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	a.playBtn.AddCSSClass("suggested-action")
	a.playBtn.SetTooltipText("Run this step — or resume what is paused")
	a.playBtn.ConnectClicked(a.playClicked)
	a.stopBtn = gtk.NewButtonFromIconName("media-playback-stop-symbolic")
	a.stopBtn.SetTooltipText("Stop the run or the playback — ⏸ is what parks one to carry on later")
	a.stopBtn.SetSensitive(false)
	a.stopBtn.ConnectClicked(a.stopClicked)
	a.progress = gtk.NewProgressBar()
	a.progress.SetShowText(true)
	a.progress.SetText("nothing running")
	// the bar reads "describe 1/2: chunk 4/12": the job, which of the run's jobs
	// it is, and where the work queue has got to. Which file is in the log --
	// the tooltip a run replaces this one with counts the tasks instead
	a.progress.SetTooltipText("The run: the job, which of the run's jobs it is, and the task it is on")
	a.progress.SetHExpand(true)
	a.progress.SetVAlign(gtk.AlignCenter)

	// What the visible step has written -- how many files, newest when, and a
	// way into the folder. Every step answers that, and each used to answer it
	// on a row of its own at the page's bottom edge: five copies of the same
	// line, each costing its page a line of height. The answer lives once now,
	// at the right end of the shared bottom bar, and follows the visible tab
	// the way ▶ and ⏹ do. Built here with the other run controls because the
	// pages fill it as they are built.
	a.outStack = gtk.NewStack()
	a.outStack.SetHhomogeneous(false) // prep's three folders must not set the width for every tab

	a.stack = gtk.NewStack()
	a.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	// each page asks for its own size, not for the largest page's. Homogeneous
	// (the GTK default) means the timeline on the cut step sets the floor for
	// every other page too, and the whole window inherits it -- so a page that
	// would fit a small screen on its own is clipped because a page you are not
	// looking at would not.
	a.stack.SetHhomogeneous(false)
	a.stack.SetVhomogeneous(false)
	a.stack.SetVExpand(true)
	a.stack.AddNamed(a.buildPrep(), "prep")
	a.stack.AddNamed(a.buildCut(), "cut")
	a.stack.AddNamed(a.buildNarrate(), "narrate")
	a.stack.AddNamed(a.buildProduce(), "produce")
	a.stack.AddNamed(a.buildPublish(), "publish")

	// Tabs, not a sidebar down the left. The steps are a fixed five, so a list
	// spent 170px of width on five rows and a column of empty space under them;
	// as a group of toggle buttons they fit the header bar, which is already
	// there -- the page gets the width back and gives up no height for it.
	// Hand-rolled rather than a GtkStackSwitcher, because a tab has to be able
	// to gray out and say what is missing, which the stock widget cannot do.
	tabRow := gtk.NewBox(gtk.OrientationHorizontal, 0)
	tabRow.AddCSSClass("linked") // one segmented control, not five loose buttons
	a.tabRow, a.tabWordsOn = tabRow, true
	for i, st := range steps {
		// icon and word, the word being the part that goes when the bar runs
		// short (fitHeader). The icon is always drawn, so the row never
		// changes how many things are in it -- only how wide they are, which
		// is what keeps a tab in the same place across a resize.
		i, b := i, gtk.NewToggleButton()
		row := gtk.NewBox(gtk.OrientationHorizontal, tabGap)
		row.Append(gtk.NewImageFromIconName(st.icon))
		word := gtk.NewLabel(st.label)
		row.Append(word)
		b.SetChild(row)
		a.tabWords = append(a.tabWords, word)
		b.SetTooltipText(st.tip)
		if i > 0 {
			b.SetGroup(a.tabs[0]) // radio behaviour: some page is always current
		}
		b.ConnectToggled(func() {
			// the other half of every switch is a tab going inactive, and the
			// bounce below re-toggles two more -- only the one being entered acts
			if a.tabGuard || !b.Active() {
				return
			}
			if a.stepLocked(i) {
				a.showStep(a.stack.VisibleChildName()) // bounce: the page did not move
				// the same sentence the tooltip carries: a click is how you find
				// out the tab is closed, so it must not answer with less than
				// hovering it would have
				a.setStatus(steps[i].wait)
				return
			}
			a.showStep(steps[i].name)
		})
		a.tabs = append(a.tabs, b)
		tabRow.Append(b)
	}
	head.SetTitleWidget(tabRow)
	a.watchHeadWidth()

	// shared log + status across all pages: one bottom row, the status text
	// living in the expander header so nothing reserves empty space
	var logScroll *gtk.ScrolledWindow
	a.log, logScroll = newLogPane(220)
	a.status = gtk.NewLabel("")
	a.status.SetXAlign(1) // status lives right-aligned in the free header space
	a.status.SetHExpand(true)
	a.status.SetEllipsize(pango.EllipsizeEnd)
	a.status.AddCSSClass("dim-label")
	a.status.SetMarginEnd(8)
	logLbl := gtk.NewLabel("Log")
	head2 := gtk.NewBox(gtk.OrientationHorizontal, 12)
	head2.SetHExpand(true)
	head2.Append(logLbl)
	head2.Append(a.status)
	a.logExp = gtk.NewExpander("")
	a.logExp.SetLabelWidget(head2)
	a.logExp.SetChild(logScroll)
	a.logExp.SetMarginStart(8)
	a.logExp.SetMarginEnd(8)
	a.logExp.SetMarginTop(2)
	a.logExp.SetMarginBottom(2)

	// the shared bottom bar: run controls act on the visible step
	ctlRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	ctlRow.SetMarginStart(8)
	ctlRow.SetMarginEnd(8)
	ctlRow.SetMarginTop(4)
	ctlRow.SetMarginBottom(2)
	ctlRow.Append(a.playBtn)
	ctlRow.Append(a.stopBtn)
	// No volume slider on this bar. It was here for one page: Produce, which
	// used to watch its own result and had no transport of its own to hang a
	// slider off. Produce no longer plays anything -- the finished file is
	// opened in whatever plays videos on this machine -- so the bar's slider
	// was a control over silence on every page in the app. The two pages that
	// do play have their own beside their own ▶ (cut.go, narrate.go), and
	// those are still one number between them (volumeCtl, SetPreviewVolume).
	ctlRow.Append(a.progress)
	// Ask about what just happened, from wherever you noticed it. On this bar
	// and not on a page, because the complaint is never about the page you are
	// standing on: what a cut kept is noticed while watching the produced file,
	// and what the narration says is noticed after it has been spoken
	// (improve.go).
	improve := gtk.NewButtonWithLabel("Improve")
	improve.SetTooltipText("Say what you did not like — the model reads this session's " +
		"log and its own exchanges, says why it did that, and names the sentence in the " +
		"prompt that would change it")
	improve.ConnectClicked(a.improveClicked)
	ctlRow.Append(improve)
	// the one Outputs heading in the app; the group behind it is the visible
	// page's own, swapped by showStep
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	ctlRow.Append(outLbl)
	ctlRow.Append(a.outStack)

	bottom := gtk.NewBox(gtk.OrientationVertical, 0)
	// a hard edge above the control/log rows, so the page ending there reads as
	// a status bar rather than as widgets stopping mid-air
	bottom.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	bottom.Append(ctlRow)
	bottom.Append(a.logExp)

	// the first page is chosen here rather than beside the tabs it moves,
	// because showStep dresses this bar for the page it opens -- the Outputs
	// group and the volume slider are both on the row built just above
	a.showStep("prep")

	// The log against the page above it, on a divider. How much log you want is
	// a per-moment question -- all of it while a run talks, none of it while
	// cutting -- and it was a fixed 220 px that the page had to live around.
	// Only the page takes the window's extra height; the log keeps whatever it
	// was dragged to, and cannot be dragged over the run controls.
	outer := gtk.NewPaned(gtk.OrientationVertical)
	outer.SetStartChild(a.stack)
	outer.SetEndChild(bottom)
	outer.SetResizeStartChild(true)
	outer.SetResizeEndChild(false)
	outer.SetShrinkEndChild(false)
	// Two halves, and both are needed. The expander and its scroller have to be
	// told to fill what the divider gives them, or the log sits at its natural
	// height under an empty stretch of window (same trick as the settings
	// dialog). And collapsing it hands the height back rather than leaving the
	// divider parked where a log nobody can see used to be -- position(-1) is
	// how a GtkPaned is told to forget a dragged position.
	logGrow := func() {
		on := a.logExp.Expanded()
		logScroll.SetVExpand(on)
		a.logExp.SetVExpand(on)
		if !on {
			outer.SetPosition(-1)
		}
	}
	a.logExp.NotifyProperty("expanded", logGrow)
	logGrow()
	a.win.SetChild(outer)
	// after the log exists, so that a theme that cannot find the icon says so
	// somewhere the user will look rather than only on stderr
	a.setupIcons()

	// The config left the session folder; a machine that has been cutting for
	// months still has it there. Also after the log, because the one thing it
	// does that is worth seeing is the line saying where the file went.
	a.migrateConf()
	a.loadGlobalPrompts()

	// Pick up where the last session left off. A file handed over by the desktop
	// comes first -- a double-click is somebody asking for THAT project, not for
	// whatever was open last -- then the named project that was open when the
	// last session ended, and only failing that the working copy. Opening the
	// working copy unconditionally was the old behaviour, and it silently undid
	// the Save that named a variant: you saved jan-video, quit, and came back to
	// the session you had saved it to get away from.
	switch {
	case a.openPath != "":
		a.loadProjectFrom(a.openPath)
	case a.lastProject() != "":
		a.loadProjectFrom(a.lastProject())
	case exists(a.projPath):
		a.loadProjectFrom(a.projPath)
	case exists(filepath.Join(a.root, "project.json")):
		// The working copy from before a project was a file you double-click.
		// Its contents are this session and its name is the one a session has
		// now (projectName); project.json is left on disk exactly as it was.
		// What it wrote is NOT moved: it went into the root, which also holds
		// the checkout, and no rename could tell one from the other.
		a.loadProjectFrom(filepath.Join(a.root, "project.json"))
	}

	a.updateGates()
	a.updateCutInfo()
	a.updateProduceInfo()
	a.startAutosave() // from here on the project file follows the window
	a.win.SetVisible(true)
}

// updateGates grays the tabs whose prerequisites are missing and puts the
// reason where the description usually is; clicking a locked tab bounces back.
// Graying rather than desensitizing is the point: an insensitive button gets no
// hover, so the one place that says what is missing would be the one place you
// cannot reach.
//
// Describe used to gate Transcript from the sidebar, and Inputs used to gate
// Describe. All three share a page and a ▶ now, so the ordering between them is
// the page's business, not a locked tab's.
func (a *App) updateGates() {
	if len(a.tabs) == 0 {
		return
	}
	// the cut works on the footage and its frames, which is the first half of
	// Prepare's output. Describe's session timeline is what it prints on
	// the tracks and what the suggestion reads, and a session can be cut by
	// hand without it
	a.cutLocked = !a.canCut()
	a.narrateLocked = !exists(a.cutPath())
	a.produceLocked = !exists(a.cutPath())
	for i, s := range steps {
		w := gtk.BaseWidget(a.tabs[i].Child()) // the label, so the button keeps its frame
		if a.stepLocked(i) {
			w.AddCSSClass("dim-label")
			a.tabs[i].SetTooltipText(s.wait)
		} else {
			w.RemoveCSSClass("dim-label")
			a.tabs[i].SetTooltipText(s.tip)
		}
	}
	if a.stepLocked(stepIndex(a.stack.VisibleChildName())) {
		a.showStep("prep")
	}
}

func (a *App) rescanAll() {
	// the list is the session, so a rescan cannot re-read it from a folder --
	// what it can do is notice that a source is no longer on disk. Say which:
	// a row that quietly disappeared is how a render comes out missing an angle.
	for _, gone := range a.srcList.prune() {
		a.logf("!!! dropped %s -- it is no longer there", gone)
	}
	a.updateGates() // which re-reads what is on disk
	a.prep.refresh()
	a.updateCutInfo()
	a.updateNarrateInfo()
	a.updateProduceInfo()
	a.setStatus("rescanned")
}

// updateNarrateInfo re-reads the narration from disk. It was the one step a
// rescan skipped: delete step4/ and the page went on showing the narration it
// had in memory and an Outputs line counting files that were no longer there.
// Nothing is lost by re-reading -- every edit on that page is written as it is
// typed -- and after a rescan the folder is the answer, including when the
// answer is that there is nothing in it.
func (a *App) updateNarrateInfo() {
	n := a.narr
	if n == nil || n.list == nil {
		return // page not built yet
	}
	n.load()
	n.rebuildRows()
	n.updateInputs()
	n.updateOut()
}

// ---- helpers ---------------------------------------------------------------

// setStatus is the answer to a click: what an edit did, or why it did nothing.
// It sits in the log expander's header, which is the only line on screen with
// room for a sentence, and it holds that sentence until the next press.
//
// It is not where a run reports. A run has the progress bar, two widgets to the
// left of it, and everything a run had to say was being said twice -- the same
// words on the bar and on this line at the same instant, which reads as two
// events rather than as one. Nor does it repeat the header bar: the project it
// names is the project the header names.
func (a *App) setStatus(s string) {
	if a.status == nil {
		return // headless (tests): the status line is the window's
	}
	a.status.SetText(s)
}

// newLogPane is what a log looks like, everywhere one appears: a read-only
// monospace view that wraps rather than scrolls sideways, in a scroller with a
// border around it.
//
// There are two -- the run log at the bottom of the window and the settings
// dialog's test log -- and they were built separately, a dozen lines apart in
// two files, which is how they drifted: same font and same expander, but only
// one of them had the frame, so the run log's text sat loose on the window
// background with nothing to say where it began. One builder, one design.
//
// minHeight is the only thing the two disagree on, and legitimately: the run
// log is a page of a session, the dialog's is a handful of verdicts.
func newLogPane(minHeight int) (*gtk.TextView, *gtk.ScrolledWindow) {
	tv := gtk.NewTextView()
	tv.SetEditable(false)
	tv.SetCursorVisible(false) // read-only: a blinking caret in it is a lie
	tv.SetMonospace(true)
	tv.SetWrapMode(gtk.WrapWordChar) // paths and ffmpeg lines are long
	sw := gtk.NewScrolledWindow()
	sw.SetChild(tv)
	sw.SetMinContentHeight(minHeight)
	sw.AddCSSClass("frame")
	return tv, sw
}

// logf writes one line to the run log and to the terminal behind it.
//
// A log line reports: a name, a count, a size, a failure. What a thing is, what
// is going to happen next, and what to do about it are not log lines -- they are
// on the page, in a tooltip, or in the source. A log that explains itself is a
// log whose real news scrolls past unread, and the run's own news already has a
// widget: the progress bar carries the task and the counter (see prog), so a
// line that says only "window 7 of 60" belongs there and not here.
func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...) // mirror to the launching terminal
	if a.log == nil {
		return // called before the window exists: stderr is the whole log
	}
	buf := a.log.Buffer()
	end := buf.EndIter()
	buf.Insert(end, fmt.Sprintf(format, args...)+"\n")
	mark := buf.CreateMark("", buf.EndIter(), false)
	a.log.ScrollToMark(mark, 0, false, 0, 1)
	buf.DeleteMark(mark)
}

func (a *App) logfIdle(format string, args ...any) {
	glib.IdleAdd(func() { a.logf(format, args...) })
}
