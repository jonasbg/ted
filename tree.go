package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type node struct {
	name     string
	path     string
	isDir    bool
	expanded bool
	loaded   bool
	children []*node
	parent   *node
	depth    int
}

type Tree struct {
	root       *node
	rows       []*node // flattened visible rows
	sel        int
	scroll     int
	showHidden bool
	git        map[string]byte // abs path -> status letter (M, A, ?, D, R)
	gitRoot    string
}

var alwaysHidden = map[string]bool{".git": true, ".DS_Store": true, ".hg": true, ".svn": true}

func newTree(root string) *Tree {
	t := &Tree{root: &node{name: filepath.Base(root), path: root, isDir: true, expanded: true}}
	if root != "" {
		t.load(t.root)
	}
	t.flatten()
	return t
}

func (t *Tree) load(n *node) {
	entries, err := os.ReadDir(n.path)
	n.loaded = true
	if err != nil {
		n.children = nil
		return
	}
	old := map[string]*node{}
	for _, c := range n.children {
		old[c.name] = c
	}
	kids := make([]*node, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if alwaysHidden[name] {
			continue
		}
		if !t.showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(n.path, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		if c, ok := old[name]; ok && c.isDir == isDir {
			kids = append(kids, c)
			continue
		}
		kids = append(kids, &node{name: name, path: filepath.Join(n.path, name), isDir: isDir, parent: n, depth: n.depth + 1})
	}
	sort.Slice(kids, func(i, j int) bool {
		if kids[i].isDir != kids[j].isDir {
			return kids[i].isDir
		}
		return naturalLess(strings.ToLower(kids[i].name), strings.ToLower(kids[j].name))
	})
	n.children = kids
}

func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := isDigit(a[0]), isDigit(b[0])
		if ad && bd {
			i, j := 0, 0
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (t *Tree) flatten() {
	var cur *node
	if t.sel >= 0 && t.sel < len(t.rows) {
		cur = t.rows[t.sel]
	}
	t.rows = t.rows[:0]
	var walk func(n *node)
	walk = func(n *node) {
		for _, c := range n.children {
			t.rows = append(t.rows, c)
			if c.isDir && c.expanded {
				walk(c)
			}
		}
	}
	walk(t.root)
	if cur != nil {
		for i, r := range t.rows {
			if r == cur {
				t.sel = i
				return
			}
		}
	}
	if t.sel >= len(t.rows) {
		t.sel = len(t.rows) - 1
	}
	if t.sel < 0 {
		t.sel = 0
	}
}

func (t *Tree) selected() *node {
	if t.sel >= 0 && t.sel < len(t.rows) {
		return t.rows[t.sel]
	}
	return nil
}

func (t *Tree) toggle(n *node) {
	if !n.isDir {
		return
	}
	n.expanded = !n.expanded
	if n.expanded && !n.loaded {
		t.load(n)
	}
	t.flatten()
}

// refresh re-reads every loaded directory, keeping expansion state.
func (t *Tree) refresh() {
	var walk func(n *node)
	walk = func(n *node) {
		if !n.loaded {
			return
		}
		t.load(n)
		for _, c := range n.children {
			if c.isDir && c.loaded {
				walk(c)
			}
		}
	}
	walk(t.root)
	t.flatten()
}

// reveal expands parents of path and selects it.
func (t *Tree) reveal(path string) bool {
	if t.root.path == "" {
		return false
	}
	rel, err := filepath.Rel(t.root.path, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	n := t.root
	for i, p := range parts {
		if !n.loaded {
			t.load(n)
		}
		var next *node
		for _, c := range n.children {
			if c.name == p {
				next = c
				break
			}
		}
		if next == nil {
			t.flatten()
			return false
		}
		if i < len(parts)-1 {
			next.expanded = true
			if !next.loaded {
				t.load(next)
			}
		}
		n = next
	}
	t.flatten()
	for i, r := range t.rows {
		if r == n {
			t.sel = i
			return true
		}
	}
	return false
}

// ---- git status ----

func gitToplevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// recordTreeStatus propagates a file status to its parent directories.
func recordTreeStatus(m map[string]byte, root string, c gitChange) {
	x, y, p := c.x, c.y, c.path
	var st byte
	switch {
	case x == '?' && y == '?':
		st = '?'
	case x == 'D' || y == 'D':
		st = 'D'
	case x == 'A':
		st = 'A'
	default:
		st = 'M'
	}
	abs := filepath.Join(root, strings.TrimSuffix(p, "/"))
	m[abs] = st
	dirSt := byte('M')
	if st == '?' || st == 'A' {
		dirSt = '?'
	}
	for d := filepath.Dir(abs); len(d) >= len(root); d = filepath.Dir(d) {
		if prev := m[d]; prev == 'M' || prev == dirSt {
			break
		}
		m[d] = dirSt
		if d == root || d == "/" {
			break
		}
	}
}

// gitStatusFor resolves a file's status, including when it lives inside an
// untracked directory.
func (t *Tree) gitStatusFor(path string) byte {
	if t.git == nil {
		return 0
	}
	if s, ok := t.git[path]; ok {
		return s
	}
	if t.gitRoot == "" {
		return 0
	}
	for d := filepath.Dir(path); len(d) > len(t.gitRoot); d = filepath.Dir(d) {
		if t.git[d] == '?' {
			return '?'
		}
		if d == filepath.Dir(d) {
			break // reached the filesystem root
		}
	}
	return 0
}
