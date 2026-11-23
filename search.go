package main

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".hg": true, ".svn": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true,
	".next": true, ".cache": true, "vendor": true, ".idea": true, ".vscode": true,
}

// listFiles returns project files relative to root. It prefers ripgrep (which
// honours .gitignore), then git, then a plain directory walk.
func listFiles(ctx context.Context, root string) []string {
	if _, err := exec.LookPath("rg"); err == nil {
		cmd := exec.CommandContext(ctx, "rg", "--files", "--hidden", "-g", "!.git/")
		cmd.Dir = root
		if out, err := cmd.Output(); err == nil || len(out) > 0 {
			return splitNonEmpty(string(out))
		}
	}
	if gitToplevel(root) != "" {
		cmd := exec.CommandContext(ctx, "git", "ls-files", "-co", "--exclude-standard")
		cmd.Dir = root
		if out, err := cmd.Output(); err == nil {
			return splitNonEmpty(string(out))
		}
	}
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			files = append(files, rel)
		}
		if len(files) > 200000 {
			return fs.SkipAll
		}
		return nil
	})
	return files
}

func splitNonEmpty(s string) []string {
	parts := strings.Split(s, "\n")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type fuzzyHit struct {
	path    string
	score   int
	matches []int // byte indices into path that matched
}

// fuzzyScore does a subsequence match with bonuses for word starts,
// consecutive runs and matches in the base name.
func fuzzyScore(query, cand string) (int, []int) {
	if query == "" {
		return 0, nil
	}
	lc := strings.ToLower(cand)
	base := strings.LastIndexByte(cand, '/') + 1
	// Try to match as late as possible for the base name: simple greedy from start
	// but prefer the base name when the whole query fits there.
	try := func(from int) (int, []int, bool) {
		score := 0
		var idx []int
		qi := 0
		prev := -2
		q := []rune(strings.ToLower(query))
		for i, r := range lc {
			if i < from {
				continue
			}
			if qi >= len(q) {
				break
			}
			if r == q[qi] {
				s := 1
				if i == prev+utf8.RuneLen(r) || i == prev+1 {
					s += 5
				}
				if i == 0 || i == base || strings.ContainsRune("/_-. ", rune(cand[i-1])) {
					s += 8
				} else if unicode.IsUpper(rune(cand[i])) && !unicode.IsUpper(rune(cand[i-1])) {
					s += 6
				}
				if i >= base {
					s += 2
				}
				score += s
				idx = append(idx, i)
				prev = i
				qi++
			}
		}
		return score, idx, qi == len(q)
	}
	s1, i1, ok1 := try(base)
	s2, i2, ok2 := try(0)
	if !ok1 && !ok2 {
		return -1, nil
	}
	score, idx := s2, i2
	if ok1 && (!ok2 || s1+10 >= s2) {
		score, idx = s1+10, i1
	}
	score -= len(cand) / 8
	if strings.EqualFold(cand[base:], query) {
		score += 50
	}
	return score, idx
}

func fuzzyFilter(query string, files []string, limit int) []fuzzyHit {
	var hits []fuzzyHit
	if query == "" {
		for i, f := range files {
			if i >= limit {
				break
			}
			hits = append(hits, fuzzyHit{path: f})
		}
		return hits
	}
	for _, f := range files {
		if s, idx := fuzzyScore(query, f); s > -1 && idx != nil {
			hits = append(hits, fuzzyHit{f, s, idx})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return len(hits[i].path) < len(hits[j].path)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// ---- project-wide text search ----

type grepHit struct {
	path string // relative to root
	line int    // 1-based
	col  int    // 1-based byte column
	text string
	buf  *Buffer // set when the hit is in an open tab (open-files scope)
}

const maxGrepHits = 2000

func smartCase(q string) bool {
	for _, r := range q {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func projectGrep(ctx context.Context, root, query string) ([]grepHit, bool) {
	if query == "" {
		return nil, false
	}
	if _, err := exec.LookPath("rg"); err == nil {
		return rgGrep(ctx, root, query)
	}
	return walkGrep(ctx, root, query)
}

func rgGrep(ctx context.Context, root, query string) ([]grepHit, bool) {
	cmd := exec.CommandContext(ctx, "rg", "--vimgrep", "--null", "--no-heading", "--color=never",
		"--smart-case", "--fixed-strings", "--hidden", "-g", "!.git/", "--max-columns=400",
		"--max-count=200", "--", query)
	cmd.Dir = root
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	var hits []grepHit
	truncated := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		nul := strings.IndexByte(line, 0)
		if nul < 0 {
			continue
		}
		path, rest := line[:nul], line[nul+1:]
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) < 3 {
			continue
		}
		ln, _ := strconv.Atoi(parts[0])
		col, _ := strconv.Atoi(parts[1])
		hits = append(hits, grepHit{path: strings.TrimPrefix(path, "./"), line: ln, col: col, text: parts[2]})
		if len(hits) >= maxGrepHits {
			truncated = true
			break
		}
	}
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	cmd.Wait()
	// ripgrep searches in parallel; give results a stable order
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	return hits, truncated
}

func walkGrep(ctx context.Context, root, query string) ([]grepHit, bool) {
	files := listFiles(ctx, root)
	cs := smartCase(query)
	q := []byte(query)
	if !cs {
		q = bytes.ToLower(q)
	}
	var hits []grepHit
	for _, f := range files {
		if ctx.Err() != nil {
			return hits, false
		}
		p := filepath.Join(root, f)
		st, err := os.Stat(p)
		if err != nil || st.Size() > 4<<20 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue
		}
		for i, line := range bytes.Split(data, []byte{'\n'}) {
			hay := line
			if !cs {
				hay = bytes.ToLower(line)
			}
			if c := bytes.Index(hay, q); c >= 0 {
				t := string(line)
				if len(t) > 400 {
					t = t[:400]
				}
				hits = append(hits, grepHit{path: f, line: i + 1, col: c + 1, text: t})
				if len(hits) >= maxGrepHits {
					return hits, true
				}
			}
		}
	}
	return hits, false
}

// bufferGrep searches the text of open buffers, as rgGrep does files:
// fixed string, smart case, at most 200 hits per buffer.
func bufferGrep(bufs []*Buffer, labels []string, query string) ([]grepHit, bool) {
	if query == "" {
		return nil, false
	}
	cs := smartCase(query)
	q := query
	if !cs {
		q = strings.ToLower(q)
	}
	var hits []grepHit
	for bi, b := range bufs {
		n := 0
		for i, l := range b.lines {
			if len(l) < len(q)/utf8.UTFMax {
				continue
			}
			line := string(l)
			hay := line
			if !cs {
				hay = strings.ToLower(line)
			}
			c := strings.Index(hay, q)
			if c < 0 {
				continue
			}
			if len(hay) != len(line) {
				// lower-casing changed byte lengths; locate the match by runes
				c = len(string(l[:byteToRuneCol([]rune(hay), c)]))
			}
			t := line
			if len(t) > 400 {
				t = strings.ToValidUTF8(t[:400], "")
			}
			hits = append(hits, grepHit{path: labels[bi], line: i + 1, col: c + 1, text: t, buf: b})
			if len(hits) >= maxGrepHits {
				return hits, true
			}
			if n++; n >= 200 {
				break
			}
		}
	}
	return hits, false
}
