package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Read-only git data for the Changes and History sidebar views.

type gitChange struct {
	path string // relative to the repo root
	orig string // original path for renames
	x, y byte   // porcelain index / worktree status
}

type gitCommit struct {
	hash, short, author, subject string
	when                         time.Time
}

type gitSnapshot struct {
	status        map[string]byte
	changes       []gitChange
	branch        string
	head          string
	ahead, behind int
	upstream      bool
	commits       []gitCommit // only filled when head changed
	logLoaded     bool
}

const maxGitOutput = 8 << 20

func gitOut(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "core.quotepath=off", "--no-pager"}, args...)...)
	out, err := cmd.Output()
	if len(out) > maxGitOutput {
		out = append(out[:maxGitOutput], []byte("\n… output truncated …\n")...)
	}
	return out, err
}

// gitChanges lists staged, unstaged and untracked files.
func gitChanges(root string) []gitChange {
	out, err := gitOut(root, "status", "--porcelain=v1", "-z", "-uall", "--ignore-submodules")
	if err != nil {
		return nil
	}
	var cs []gitChange
	recs := bytes.Split(out, []byte{0})
	for i := 0; i < len(recs) && len(cs) < 5000; i++ {
		r := recs[i]
		if len(r) < 4 {
			continue
		}
		c := gitChange{x: r[0], y: r[1], path: string(r[3:])}
		if c.x == 'R' || c.x == 'C' {
			if i+1 < len(recs) {
				c.orig = string(recs[i+1])
			}
			i++
		}
		cs = append(cs, c)
	}
	return cs
}

func gitLog(root string, n int) []gitCommit {
	out, err := gitOut(root, "log", "-n", strconv.Itoa(n), "--format=%H%x1f%h%x1f%an%x1f%at%x1f%s%x1e")
	if err != nil {
		return nil
	}
	var cs []gitCommit
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) < 5 {
			continue
		}
		sec, _ := strconv.ParseInt(f[3], 10, 64)
		cs = append(cs, gitCommit{hash: f[0], short: f[1], author: f[2], when: time.Unix(sec, 0), subject: f[4]})
	}
	return cs
}

// gitSnapshotFor gathers everything the views need in one background pass.
func gitSnapshotFor(root, prevHead string, wantLog bool) gitSnapshot {
	s := gitSnapshot{status: gitStatus(root), changes: gitChanges(root)}
	if out, err := gitOut(root, "branch", "--show-current"); err == nil {
		s.branch = strings.TrimSpace(string(out))
	}
	if out, err := gitOut(root, "rev-parse", "HEAD"); err == nil {
		s.head = strings.TrimSpace(string(out))
	}
	if s.branch == "" && len(s.head) >= 7 {
		s.branch = s.head[:7] // detached HEAD
	}
	if out, err := gitOut(root, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
		f := strings.Fields(string(out))
		if len(f) == 2 {
			s.ahead, _ = strconv.Atoi(f[0])
			s.behind, _ = strconv.Atoi(f[1])
			s.upstream = true
		}
	}
	if wantLog || s.head != prevHead {
		s.commits = gitLog(root, 500)
		s.logLoaded = true
	}
	return s
}

// scmSection groups a change for the Changes view. A file can appear in
// both the staged and the unstaged section.
func (c gitChange) sections() []string {
	switch {
	case c.x == '?' && c.y == '?':
		return []string{"Untracked"}
	case c.x == 'U' || c.y == 'U' || (c.x == c.y && (c.x == 'A' || c.x == 'D')):
		return []string{"Merge Conflicts"}
	}
	var s []string
	if c.x != ' ' {
		s = append(s, "Staged Changes")
	}
	if c.y != ' ' {
		s = append(s, "Changes")
	}
	return s
}

// letter is the status shown for the change in a section.
func (c gitChange) letter(section string) byte {
	switch section {
	case "Untracked":
		return 'U'
	case "Merge Conflicts":
		return '!'
	case "Staged Changes":
		return c.x
	}
	return c.y
}

// diffFor returns the diff shown when a change is clicked.
func diffFor(root, section string, c gitChange) string {
	var out []byte
	switch section {
	case "Staged Changes":
		out, _ = gitOut(root, "diff", "--cached", "--no-color", "--", c.path)
	case "Untracked":
		// exits 1 when there are differences, which is the point here
		out, _ = gitOut(root, "diff", "--no-color", "--no-index", "--", "/dev/null", c.path)
	default:
		out, _ = gitOut(root, "diff", "--no-color", "--", c.path)
	}
	if len(out) == 0 {
		return "No changes in " + c.path + " (" + strings.ToLower(section) + ").\n"
	}
	return string(out)
}

func commitText(root, hash string) string {
	out, err := gitOut(root, "show", "--stat", "--patch", "--format=fuller", "--no-color", hash)
	if err != nil && len(out) == 0 {
		return fmt.Sprintf("Can't show commit %s: %v\n", hash, err)
	}
	return string(out)
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
	return t.Format("2006-01-02")
}

func splitDir(p string) (name, dir string) {
	return filepath.Base(p), filepath.Dir(p)
}
