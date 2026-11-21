package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// statusClick handles a click on one of the status bar's right-hand items.
func (a *App) statusClick(id string) {
	b := a.buf()
	if b == nil {
		return
	}
	a.closeOverlay()
	switch id {
	case "pos":
		a.gotoLinePrompt()
	case "enc":
		a.setMsg("ted reads and writes UTF-8")
	case "eol":
		cur := "LF"
		if b.crlf {
			cur = "CRLF"
		}
		a.pick("Line endings", []string{"LF", "CRLF"}, cur, func(s string) {
			if crlf := s == "CRLF"; crlf != b.crlf {
				b.crlf = crlf
				b.metaDirty = true
				a.setMsg("Line endings set to %s (save to apply)", s)
			}
		})
	case "indent":
		cur := "Indent with Tabs"
		if b.indent != "\t" {
			cur = fmt.Sprintf("Indent with %d Spaces", len(b.indent))
		}
		a.pick("Indentation", []string{
			"Indent with Tabs", "Indent with 2 Spaces", "Indent with 4 Spaces", "Indent with 8 Spaces",
			"Convert Indentation to Tabs", "Convert Indentation to Spaces",
		}, cur, func(s string) {
			switch s {
			case "Indent with Tabs":
				b.indent = "\t"
			case "Indent with 2 Spaces":
				b.indent = "  "
			case "Indent with 4 Spaces":
				b.indent = "    "
			case "Indent with 8 Spaces":
				b.indent = "        "
			case "Convert Indentation to Tabs":
				n := b.convertIndent(true)
				a.setMsg("Converted %d line(s) to tabs", n)
			case "Convert Indentation to Spaces":
				n := b.convertIndent(false)
				a.setMsg("Converted %d line(s) to spaces", n)
			}
		})
	case "lang":
		var names []string
		for _, n := range lexers.Names(false) {
			if n != "plaintext" {
				names = append(names, n)
			}
		}
		names = append([]string{"Plain Text"}, names...)
		sort.SliceStable(names[1:], func(i, j int) bool {
			return strings.ToLower(names[1+i]) < strings.ToLower(names[1+j])
		})
		cur := b.hl.lang
		if cur == "" {
			cur = "Plain Text"
		}
		a.pick("Language", names, cur, func(s string) {
			h := b.hl
			h.manual, h.resolved = true, true
			h.lexer, h.lang, h.spans = nil, "", nil
			if s != "Plain Text" {
				if l := lexers.Get(s); l != nil {
					h.lexer = chroma.Coalesce(l)
					h.lang = l.Config().Name
					if h.lang == "plaintext" {
						h.lang = "Plain Text"
					}
				}
			}
			h.version = -1 // re-highlight
			h.windowed = false
		})
	}
}

// convertIndent rewrites leading whitespace to tabs or spaces using the
// buffer's indent width, as one undo step. It returns lines changed.
func (b *Buffer) convertIndent(toTabs bool) int {
	w := len(b.indent)
	if b.indent == "\t" {
		w = tabW
	}
	n := len(b.undo)
	changed := 0
	for l, line := range b.lines {
		ws := leadingWS(line)
		if len(ws) == 0 {
			continue
		}
		cols := 0
		for _, r := range ws {
			if r == '\t' {
				cols += w - cols%w
			} else {
				cols++
			}
		}
		var want string
		if toTabs {
			want = strings.Repeat("\t", cols/w) + strings.Repeat(" ", cols%w)
		} else {
			want = strings.Repeat(" ", cols)
		}
		if want != string(ws) {
			b.replaceRange(Pos{l, 0}, Pos{l, len(ws)}, []rune(want))
			changed++
		}
	}
	b.groupSince(n)
	if toTabs {
		b.indent = "\t"
	} else {
		b.indent = strings.Repeat(" ", w)
	}
	b.cur = b.clamp(b.cur)
	return changed
}
