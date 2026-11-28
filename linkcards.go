package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Web content in Markdown previews: a link on a line of its own becomes a
// card with the page's Open Graph title, description and image, and remote
// images are downloaded. Both can be switched off in Settings, since they
// make requests to the sites a document links to.

type ogCard struct {
	done  bool // finished loading, successfully or not
	at    time.Time
	ok    bool
	title string
	desc  string
	site  string
	img   *mdImage
}

type webImage struct {
	done bool
	at   time.Time
	img  *mdImage
}

// A failed fetch is tried again after this long (the network may be back).
const webRetry = 2 * time.Minute

// Decoded web images are dropped, except those an open preview shows, once
// they take more than this much memory.
const webCacheBytes = 192 << 20

var webClient = &http.Client{Timeout: 8 * time.Second}

const webUA = "Mozilla/5.0 (compatible; ted link preview)"

func isWebURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// linkCard returns the card for u, starting a fetch the first time. It
// returns nil while loading, on failure, or when web content is off.
func (a *App) linkCard(u string) *ogCard {
	if a.noWeb || !isWebURL(u) {
		return nil
	}
	if a.og == nil {
		a.og = map[string]*ogCard{}
	}
	if c, ok := a.og[u]; ok && !(c.done && !c.ok && time.Since(c.at) > webRetry) {
		if c.ok {
			return c
		}
		return nil
	}
	c := &ogCard{}
	a.og[u] = c
	go func() {
		res := fetchCard(u)
		a.post(func() {
			*c = *res
			c.done, c.at = true, time.Now()
			a.webVer++
		})
	}()
	return nil
}

// webImageFor returns a downloaded image for u, starting the download the
// first time.
func (a *App) webImageFor(u string) *mdImage {
	if a.noWeb || !isWebURL(u) {
		return nil
	}
	if a.webImgs == nil {
		a.webImgs = map[string]*webImage{}
	}
	if w, ok := a.webImgs[u]; ok && !(w.done && w.img == nil && time.Since(w.at) > webRetry) {
		return w.img
	}
	w := &webImage{}
	a.webImgs[u] = w
	go func() {
		img := fetchImage(u, 2400)
		a.post(func() {
			w.done, w.img, w.at = true, img, time.Now()
			a.webVer++
		})
	}()
	return nil
}

// webSlots limits how many pages and images download at once.
var webSlots = make(chan struct{}, 4)

func webGet(u, accept string, limit int64) ([]byte, string, *url.URL, error) {
	webSlots <- struct{}{}
	defer func() { <-webSlots }()
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", nil, err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Accept", accept)
	resp, err := webClient.Do(req)
	if err != nil {
		return nil, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", nil, io.ErrUnexpectedEOF
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return data, resp.Header.Get("Content-Type"), resp.Request.URL, err
}

func fetchImage(u string, maxSide int) *mdImage {
	data, _, _, err := webGet(u, "image/*", 12<<20)
	if err != nil {
		return nil
	}
	img, _, err := decodeImage(data, maxSide)
	if err != nil {
		return nil
	}
	return img
}

func fetchCard(u string) *ogCard {
	data, ctype, final, err := webGet(u, "text/html,application/xhtml+xml", 1<<20)
	c := &ogCard{}
	if err != nil || (ctype != "" && !strings.Contains(strings.ToLower(ctype), "html")) {
		return c
	}
	data = toUTF8(data, ctype)
	meta := parseMeta(data)
	c.title = firstNonEmpty(meta["og:title"], meta["twitter:title"], meta["title"])
	c.desc = firstNonEmpty(meta["og:description"], meta["twitter:description"], meta["description"])
	c.site = firstNonEmpty(meta["og:site_name"], strings.TrimPrefix(final.Hostname(), "www."))
	if c.title == "" {
		return c
	}
	c.ok = true
	if im := firstNonEmpty(meta["og:image"], meta["og:image:url"], meta["twitter:image"]); im != "" {
		if ref, err := final.Parse(im); err == nil && isWebURL(ref.String()) {
			c.img = fetchImage(ref.String(), 800)
		}
	}
	return c
}

// parseMeta reads <meta> tags and the <title> from a page's head.
func parseMeta(data []byte) map[string]string {
	out := map[string]string{}
	z := html.NewTokenizer(bytes.NewReader(data))
	inTitle := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "body":
				return out
			case "title":
				inTitle = true
			case "meta":
				var key, content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch string(k) {
					case "property", "name":
						if key == "" {
							key = strings.ToLower(string(v))
						}
					case "content":
						content = string(v)
					}
				}
				if key != "" && out[key] == "" {
					out[key] = strings.TrimSpace(content)
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
			case "head":
				return out
			}
		case html.TextToken:
			if inTitle && out["title"] == "" {
				out["title"] = strings.Join(strings.Fields(string(z.Text())), " ")
			}
		}
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// toUTF8 converts a page in Latin-1 or Windows-1252, the common legacy
// encodings, to UTF-8. Other encodings are rare enough on today's web that
// their tables are not worth the binary size; such titles stay as they are.
func toUTF8(data []byte, ctype string) []byte {
	// LimitReader can cut a valid UTF-8 response mid-rune at the cap; trim up to
	// 3 trailing incomplete bytes before deciding it isn't UTF-8.
	trimmed := data
	for i := 0; i < 3 && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
		if utf8.RuneStart(trimmed[len(trimmed)-1]) || (trimmed[len(trimmed)-1]&0xC0) == 0x80 {
			trimmed = trimmed[:len(trimmed)-1]
		} else {
			break
		}
	}
	if utf8.Valid(trimmed) {
		return trimmed
	}
	cs := strings.ToLower(ctype)
	if i := bytes.Index(bytes.ToLower(data[:min(len(data), 2048)]), []byte("charset=")); i >= 0 {
		cs += " " + strings.ToLower(string(data[i:min(len(data), i+30)]))
	}
	latin := strings.Contains(cs, "1252") || strings.Contains(cs, "8859-1") || strings.Contains(cs, "latin1") || !strings.Contains(cs, "charset")
	if !latin {
		return data
	}
	var sb strings.Builder
	sb.Grow(len(data) + len(data)/8)
	for _, c := range data {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0 && cp1252[c-0x80] != 0:
			sb.WriteRune(cp1252[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return []byte(sb.String())
}

// cp1252 maps Windows-1252's 0x80-0x9F range (quotes, dashes, euro) to Unicode.
var cp1252 = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

// trimWebCache frees decoded web images and cards once they use more than
// webCacheBytes, keeping what open previews show. Called every second.
func (a *App) trimWebCache() {
	total := 0
	for _, w := range a.webImgs {
		total += imageBytes(w.img)
	}
	for _, c := range a.og {
		total += imageBytes(c.img)
	}
	if total <= webCacheBytes {
		return
	}
	inUse := map[string]bool{}
	for _, b := range a.bufs {
		if b.previewing() && b.md.doc != nil {
			for u := range b.md.doc.web {
				inUse[u] = true
			}
		}
	}
	for u, w := range a.webImgs {
		if w.done && !inUse[u] {
			delete(a.webImgs, u)
		}
	}
	for u, c := range a.og {
		if c.done && !inUse[u] {
			delete(a.og, u)
		}
	}
}

func imageBytes(im *mdImage) int {
	if im == nil {
		return 0
	}
	return im.size.X * im.size.Y * 4
}
