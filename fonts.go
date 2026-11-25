package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/text"
	gtfont "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"
	"golang.org/x/image/math/fixed"
)

// Indexing the system fonts takes around 100 ms on macOS, and longer with
// many fonts installed, yet ted draws with only a handful of them. So ted
// remembers which font file each face resolved to, opens just those files
// for the first frame, and indexes the rest after the window is up, for
// fallback glyphs (emoji, CJK and so on).

// fontPick is the system font file a face resolved to.
type fontPick struct {
	File  string `json:"file,omitempty"` // empty: a bundled Go font
	Index int    `json:"index,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Mod   int64  `json:"mod,omitempty"`
}

type fontPicks struct {
	Key   string           `json:"key"`
	Faces [nFaces]fontPick `json:"faces"`
}

func fontPicksPath() string { return filepath.Join(cacheDir(), "fonts.json") }

// fontPicksKey changes whenever the requested faces do, such as when
// Omarchy's font changes.
func fontPicksKey(fonts [nFaces]font.Font) string {
	var b strings.Builder
	b.WriteString("v1")
	for _, f := range fonts {
		fmt.Fprintf(&b, "|%s/%d/%d", f.Typeface, f.Weight, f.Style)
	}
	return b.String()
}

// sysFace is a parsed system font, usable by any number of shapers.
type sysFace struct{ f *gtfont.Font }

func (s sysFace) Face() *gtfont.Face { return gtfont.NewFace(s.f) }

// loadFontPicks reads the font files remembered for fonts.
func loadFontPicks(fonts [nFaces]font.Font) (fontPicks, bool) {
	var p fontPicks
	b, err := os.ReadFile(fontPicksPath())
	if err != nil || json.Unmarshal(b, &p) != nil || p.Key != fontPicksKey(fonts) {
		return fontPicks{}, false
	}
	return p, true
}

// quickShaper builds a shaper from the picked font files alone. It returns
// the faces to query it with: each face asks for its file first, under a
// private family name, then for the usual families. It returns nil when a
// file has changed.
func quickShaper(p fontPicks, fonts [nFaces]font.Font, coll []font.FontFace) (*text.Shaper, [nFaces]font.Font) {
	loaders := map[string][]*ot.Loader{}
	parsed := map[fontPick]*gtfont.Font{}
	faces := coll[:len(coll):len(coll)]
	qf := fonts
	for f, pk := range p.Faces {
		if pk.File == "" {
			continue
		}
		ft := parsed[pk]
		if ft == nil {
			var err error
			lds, ok := loaders[pk.File]
			if !ok {
				st, err := os.Stat(pk.File)
				if err != nil || st.Size() != pk.Size || st.ModTime().UnixNano() != pk.Mod {
					return nil, fonts
				}
				data, err := os.ReadFile(pk.File)
				if err != nil {
					return nil, fonts
				}
				if lds, err = ot.NewLoaders(bytes.NewReader(data)); err != nil {
					return nil, fonts
				}
				loaders[pk.File] = lds
			}
			if pk.Index < 0 || pk.Index >= len(lds) {
				return nil, fonts
			}
			if ft, err = gtfont.NewFont(lds[pk.Index]); err != nil {
				return nil, fonts
			}
			parsed[pk] = ft
		}
		name := fmt.Sprintf("tedpick%c", 'a'+f)
		faces = append(faces, font.FontFace{
			Font: font.Font{Typeface: font.Typeface(name), Style: fonts[f].Style, Weight: fonts[f].Weight},
			Face: sysFace{ft},
		})
		qf[f].Typeface = font.Typeface(name+", ") + fonts[f].Typeface
	}
	return text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces)), qf
}

// fullShaper indexes the system fonts and builds a shaper over all of them.
func fullShaper(coll []font.FontFace) *text.Shaper {
	// Index the system fonts first; the index is process-wide, so
	// NewShaper below reuses it.
	if dir, err := os.UserCacheDir(); err == nil {
		fontscan.NewFontMap(quietLog{}).UseSystemFonts(dir)
	}
	return text.NewShaper(text.WithCollection(coll))
}

// warmShaper loads the faces in use, so the first frame doesn't.
func warmShaper(sh *text.Shaper, fonts [nFaces]font.Font) {
	for _, f := range fonts {
		sh.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(16), MaxLines: 1, MaxWidth: 1 << 24}, "Mg")
		for _, ok := sh.NextGlyph(); ok; _, ok = sh.NextGlyph() {
		}
	}
}

// resolveFontPicks finds the file each face resolves to, the way Gio's
// shaper does: system fonts first, then the bundled collection, queried
// with the face's families and aspect. Call it after fullShaper.
func resolveFontPicks(fonts [nFaces]font.Font, coll []font.FontFace) (fontPicks, bool) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return fontPicks{}, false
	}
	fm := fontscan.NewFontMap(quietLog{})
	if fm.UseSystemFonts(dir) != nil {
		return fontPicks{}, false
	}
	for _, f := range coll {
		desc := opentype.FontToDescription(f.Font)
		fm.AddFace(f.Face.Face(), fontscan.Location{File: fmt.Sprint(desc)}, desc)
	}
	p := fontPicks{Key: fontPicksKey(fonts)}
	for i, f := range fonts {
		var families []string
		for _, s := range strings.Split(string(f.Typeface), ",") {
			if s = strings.TrimSpace(s); s != "" {
				families = append(families, s)
			}
		}
		fm.SetQuery(fontscan.Query{Families: families, Aspect: opentype.FontToDescription(f).Aspect})
		face := fm.ResolveFace('M')
		if face == nil {
			return fontPicks{}, false
		}
		loc := fm.FontLocation(face.Font)
		if !filepath.IsAbs(loc.File) {
			continue // a bundled Go font, which the quick shaper has too
		}
		st, err := os.Stat(loc.File)
		if err != nil {
			return fontPicks{}, false
		}
		p.Faces[i] = fontPick{File: loc.File, Index: int(loc.Index), Size: st.Size(), Mod: st.ModTime().UnixNano()}
	}
	return p, true
}

// saveFontPicks remembers the font files for the next start, writing only
// when they changed.
func saveFontPicks(fonts [nFaces]font.Font, coll []font.FontFace) {
	p, ok := resolveFontPicks(fonts, coll)
	if !ok {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	path := fontPicksPath()
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}
