package main

import (
	"testing"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"
)

// The quick shaper must draw with exactly the fonts the full one would, or
// text would change when the system fonts finish loading.
func TestQuickShaperMatchesFull(t *testing.T) {
	coll := gofont.Collection()
	fonts := faceFonts
	full := fullShaper(coll)
	p, ok := resolveFontPicks(fonts, coll)
	if !ok {
		t.Skip("no system fonts to resolve")
	}
	quick, qf := quickShaper(p, fonts, coll)
	if quick == nil {
		t.Fatal("quick shaper failed to load the picked fonts")
	}
	shape := func(sh *text.Shaper, f font.Font, s string) (ids []uint32, adv fixed.Int26_6) {
		sh.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(14), MaxLines: 1, MaxWidth: 1 << 24}, s)
		for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
			ids = append(ids, uint32(g.ID))
			adv += g.Advance
		}
		return ids, adv
	}
	const sample = "Hello, {world} 0O1lI -> != fn(x) := 42;"
	for f := range nFaces {
		wantIDs, wantAdv := shape(full, fonts[f], sample)
		gotIDs, gotAdv := shape(quick, qf[f], sample)
		if gotAdv != wantAdv || len(gotIDs) != len(wantIDs) {
			t.Errorf("face %d (%s): quick advance %v over %d glyphs, full %v over %d (picked %+v)",
				f, fonts[f].Typeface, gotAdv, len(gotIDs), wantAdv, len(wantIDs), p.Faces[f])
			continue
		}
		for i := range wantIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Errorf("face %d: glyph %d is %x, full shaper has %x", f, i, gotIDs[i], wantIDs[i])
				break
			}
		}
	}
}

// A changed font file must not be used from the remembered picks.
func TestQuickShaperRejectsChangedFile(t *testing.T) {
	coll := gofont.Collection()
	fullShaper(coll)
	p, ok := resolveFontPicks(faceFonts, coll)
	if !ok || p.Faces[fMono].File == "" {
		t.Skip("mono face is a bundled font here")
	}
	p.Faces[fMono].Mod++
	if sh, _ := quickShaper(p, faceFonts, coll); sh != nil {
		t.Fatal("quick shaper used a font file whose timestamp changed")
	}
}
