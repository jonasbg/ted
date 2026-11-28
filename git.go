package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.quotepath=off", "--no-pager"}, args...)...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Cap read size while streaming so a giant diff/commit cannot OOM the editor.
	out, rerr := io.ReadAll(io.LimitReader(pipe, maxGitOutput+1))
	truncated := len(out) > maxGitOutput
	if truncated {
		out = append(out[:maxGitOutput], []byte("\n… output truncated …\n")...)
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}
	werr := cmd.Wait()
	if truncated {
		return out, nil
	}
	if rerr != nil {
		return out, rerr
	}
	return out, werr
}

// Read status once for both views. Only the Changes panel caps its row count.
func readGitStatus(root string) (map[string]byte, []gitChange) {
	s := readGitStatusV2(root)
	return s.status, s.changes
}

// readGitStatusV2 reads branch, HEAD, upstream ahead/behind and working-tree
// status in a single `git status --porcelain=v2 --branch -z` invocation.
func readGitStatusV2(root string) gitSnapshot {
	var s gitSnapshot
	// Optional locks stay on: GIT_OPTIONAL_LOCKS=0 would stop git saving its
	// refreshed index, and every poll would re-check the stale entries (2x
	// slower in BenchmarkGitStatus10k).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v2", "--branch", "-z", "-uall", "--ignore-submodules").Output()
	if err != nil {
		return s
	}
	status := make(map[string]byte)
	var changes []gitChange
	dotToSpace := func(b byte) byte {
		if b == '.' {
			return ' '
		}
		return b
	}
	for len(data) > 0 {
		var rec []byte
		rec, data, _ = bytes.Cut(data, []byte{0})
		if len(rec) < 2 {
			continue
		}
		switch rec[0] {
		case '#':
			line := string(rec)
			switch {
			case strings.HasPrefix(line, "# branch.oid "):
				oid := strings.TrimPrefix(line, "# branch.oid ")
				if oid != "(initial)" {
					s.head = oid
				}
			case strings.HasPrefix(line, "# branch.head "):
				head := strings.TrimPrefix(line, "# branch.head ")
				if head != "(detached)" {
					s.branch = head
				}
			case strings.HasPrefix(line, "# branch.upstream "):
				s.upstream = true
			case strings.HasPrefix(line, "# branch.ab "):
				f := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
				if len(f) == 2 {
					s.ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
					s.behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
					s.upstream = true
				}
			}
		case '1':
			// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
			fields := strings.SplitN(string(rec), " ", 9)
			if len(fields) < 9 || len(fields[1]) < 2 {
				continue
			}
			c := gitChange{x: dotToSpace(fields[1][0]), y: dotToSpace(fields[1][1]), path: fields[8]}
			recordTreeStatus(status, root, c)
			if len(changes) < 5000 {
				changes = append(changes, c)
			}
		case '2':
			// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>\0<origPath>
			var orig []byte
			orig, data, _ = bytes.Cut(data, []byte{0})
			fields := strings.SplitN(string(rec), " ", 10)
			if len(fields) < 10 || len(fields[1]) < 2 {
				continue
			}
			c := gitChange{x: dotToSpace(fields[1][0]), y: dotToSpace(fields[1][1]), path: fields[9], orig: string(orig)}
			recordTreeStatus(status, root, c)
			if len(changes) < 5000 {
				changes = append(changes, c)
			}
		case 'u':
			// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
			fields := strings.SplitN(string(rec), " ", 11)
			if len(fields) < 11 || len(fields[1]) < 2 {
				continue
			}
			c := gitChange{x: dotToSpace(fields[1][0]), y: dotToSpace(fields[1][1]), path: fields[10]}
			recordTreeStatus(status, root, c)
			if len(changes) < 5000 {
				changes = append(changes, c)
			}
		case '?':
			if len(rec) < 3 {
				continue
			}
			c := gitChange{x: '?', y: '?', path: string(rec[2:])}
			recordTreeStatus(status, root, c)
			if len(changes) < 5000 {
				changes = append(changes, c)
			}
		}
	}
	if s.branch == "" && len(s.head) >= 7 {
		s.branch = s.head[:7] // detached HEAD
	}
	s.status, s.changes = status, changes
	return s
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
	s := readGitStatusV2(root)
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
