package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Web content in Markdown previews: a link on a line of its own becomes a
// card with the page's Open Graph title, description and image, and remote
// images are downloaded. Both can be switched off in Settings, since they
// make requests to the sites a document links to.

type ogCard struct {
	done  bool // finished loading, successfully or not
	ok    bool
	title string
	desc  string
	site  string
	img   *mdImage
}

type webImage struct {
	done bool
	img  *mdImage
}

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
	if c, ok := a.og[u]; ok {
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
			c.done = true
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
	if w, ok := a.webImgs[u]; ok {
		return w.img
	}
	w := &webImage{}
	a.webImgs[u] = w
	go func() {
		img := fetchImage(u, 2400)
		a.post(func() {
			w.done, w.img = true, img
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
	if err != nil || (ctype != "" && !strings.Contains(ctype, "html")) {
		return c
	}
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
