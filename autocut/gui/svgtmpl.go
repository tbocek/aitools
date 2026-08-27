package main

// Filling a card in.
//
// A card used to be a Go function that printed an SVG. That works and it is
// wrong in one specific way: the picture is then in the code, so everything
// about how a board LOOKS -- the corner radius, where the letter sits, what a
// place on a row is made of -- is a Go edit and a rebuild, and the file in
// assets is only ever an output. Nobody can restyle it and no model can be
// asked to.
//
// So the drawing lives in the file and the numbers come from here. The file is
// an ordinary SVG with every row and every place written out in it, and holes
// where the app has something to say:
//
//	{{name}}                     filled in, and nothing if there is nothing
//	{{name|what it says instead}} filled in, or that, when nobody says otherwise
//
// The holes are the contents of a place, the size the name has to be to fit, and
// every animation's own values and keyTimes -- so the moment something arrives
// is a number in the document rather than a number in a printf. Everything else,
// including where all of it is, is in the file and only in the file.

import (
	"bytes"
	"regexp"
	"strings"
)

// svgScope is what the card has to say about a document: hole name (lower case)
// to value. The value is escaped on the way in, so a title with an ampersand in
// it is a title and not a broken document.
type svgScope map[string]string

// svgFillScope answers the holes the card has a value for and leaves the rest as
// they are, defaults and all -- those are the parameters' turn, and after them
// their own defaults. A place the card said nothing about is a place the file
// itself calls empty.
func svgFillScope(src []byte, vals svgScope) []byte {
	if len(vals) == 0 {
		return src
	}
	return outsideComments(src, func(b []byte) []byte {
		return svgHole.ReplaceAllFunc(b, func(m []byte) []byte {
			g := svgHole.FindSubmatch(m)
			v, ok := vals[strings.ToLower(string(g[1]))]
			if !ok {
				return m
			}
			return []byte(escapeText(v))
		})
	})
}

// A comment is not part of the picture, and neither is a hole written in one:
// the file explains itself in comments, and the example it uses to explain what
// a {{hole}} is must not turn into a box in the dialog, be quietly answered by a
// path, or be filled in by the card and stop explaining anything. So everything
// that fills a document in steps over its comments.
var reComment = regexp.MustCompile(`(?s)<!--.*?-->`)

func outsideComments(src []byte, f func([]byte) []byte) []byte {
	var out bytes.Buffer
	pos := 0
	for _, c := range reComment.FindAllIndex(src, -1) {
		out.Write(f(src[pos:c[0]]))
		out.Write(src[c[0]:c[1]])
		pos = c[1]
	}
	out.Write(f(src[pos:]))
	return out.Bytes()
}

// stampArgs writes the parameters a card was drawn with back into its root
// element, so the finished document is the same complete statement of itself the
// drawn cards always were: what drew it, and what with.
func stampArgs(src []byte, q svgQuery) []byte {
	return reStampArgs.ReplaceAll(src, []byte(`data-autocut-args="`+attrEsc(q.args())+`"`))
}

var reStampArgs = regexp.MustCompile(`data-autocut-args="[^"]*"`)
