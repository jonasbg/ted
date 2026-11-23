package main

import (
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaxColsAcrossEdits(t *testing.T) {
	b := newBuffer("")
	b.setContent([]byte("short\n\t世abc\n1234567"))
	check := func(step string, want int) {
		t.Helper()
		if got := b.maxCols(); got != want {
			t.Fatalf("%s: maxCols() = %d, want %d", step, got, want)
		}
	}

	check("initial", 9)
	b.replaceRange(Pos{1, 0}, Pos{1, 5}, []rune("x"))
	check("shorten the widest line", 7)
	b.cur = Pos{0, 0}
	b.insert([]rune("\t\t\n"), "")
	check("insert a wider line", 8)
	b.deleteRange(Pos{0, 0}, Pos{1, 0}, "")
	check("remove the widest line", 7)
	b.doUndo()
	check("undo removal", 8)
	b.doUndo()
	check("undo insertion", 7)
	b.doUndo()
	check("undo replacement", 9)
	b.doRedo()
	check("redo replacement", 7)
	b.setContent(nil)
	check("clear content", 0)
	b.cur = Pos{}
	b.insert([]rune("世"), "")
	check("edit the empty buffer", 2)
}

// Random edits must keep the incremental widths equal to a full measure.
func TestMaxColsMatchesFullScanUnderRandomEdits(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	b := newBuffer("")
	b.setContent([]byte("a\nbb\n\tccc\n世界\n"))
	b.maxCols()
	pieces := []string{"x", "yyyyyyyy", "\n", "\t", "世", "\nzz\nwww\n", strings.Repeat("m", 400)}
	for step := 0; step < 3000; step++ {
		l := rng.IntN(len(b.lines))
		p := Pos{l, rng.IntN(len(b.lines[l]) + 1)}
		switch rng.IntN(4) {
		case 0, 1:
			b.cur = p
			b.insert([]rune(pieces[rng.IntN(len(pieces))]), "")
		case 2:
			l2 := min(len(b.lines)-1, l+rng.IntN(3))
			q := Pos{l2, rng.IntN(len(b.lines[l2]) + 1)}
			if q.Less(p) {
				p, q = q, p
			}
			b.deleteRange(p, q, "")
		case 3:
			if rng.IntN(2) == 0 {
				b.doUndo()
			} else {
				b.doRedo()
			}
		}
		want := 0
		for _, line := range b.lines {
			want = max(want, visCol(line, len(line)))
		}
		if got := b.maxCols(); got != want {
			t.Fatalf("step %d: maxCols() = %d, full scan = %d", step, got, want)
		}
		if len(b.lineWidths) != len(b.lines) {
			t.Fatalf("step %d: %d widths for %d lines", step, len(b.lineWidths), len(b.lines))
		}
	}
}

func TestTextRoundTripsUnicode(t *testing.T) {
	for _, s := range []string{"", "\n", "a", "café\n世界 🚀\n\n", "tail\n"} {
		b := newBuffer("")
		b.setContent([]byte(s))
		if got := b.text(); got != s {
			t.Errorf("text() = %q, want %q", got, s)
		}
	}
}

func TestCSVEmptySelectionStatsAreCached(t *testing.T) {
	b := newBuffer("x.csv")
	b.setContent([]byte("a,b\n1,\n2,\n"))
	b.setCSV(',')
	b.startBlock(Pos{0, 2}, false)
	if _, ok := b.selectionStats(); ok {
		t.Fatal("empty column reported values")
	}
	if !b.csv.statsOK {
		t.Fatal("empty result was not cached")
	}
	b.cur = Pos{1, 2}
	b.insert([]rune("5"), "") // a new version invalidates the cache
	st, ok := b.selectionStats()
	if !ok || st.numbers != 1 || st.sum != 5 {
		t.Fatalf("stats after edit = %+v, %v", st, ok)
	}
}

func TestGitSnapshotPreservesChangesAndTreeStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init")
	git("symbolic-ref", "HEAD", "refs/heads/main") // --initial-branch needs git 2.28
	git("config", "user.name", "Ted Test")
	git("config", "user.email", "ted-test@example.com")
	git("config", "commit.gpgsign", "false")
	git("config", "status.renames", "true")
	unborn := gitSnapshotFor(root, "", false)
	if unborn.branch != "main" || unborn.head != "" || unborn.upstream {
		t.Fatalf("unexpected unborn branch status: %+v", unborn)
	}
	write("project/old name.txt", "rename me\n")
	write("project/mixed.txt", "initial\n")
	git("add", ".")
	git("commit", "-m", "Initial files")
	git("branch", "tracking")
	git("branch", "--set-upstream-to=tracking")
	git("commit", "--allow-empty", "-m", "Ahead")
	git("mv", "project/old name.txt", "project/new name.txt")
	write("project/mixed.txt", "staged\n")
	git("add", "project/mixed.txt")
	write("project/mixed.txt", "unstaged\n")
	write("project/new.txt", "staged addition\n")
	git("add", "project/new.txt")
	write("loose/subdir/file.txt", "untracked\n")

	s := gitSnapshotFor(root, "", false)
	if s.branch != "main" || s.head == "" || !s.upstream || s.ahead != 1 || s.behind != 0 {
		t.Fatalf("unexpected branch status: %+v", s)
	}
	if !s.logLoaded || len(s.commits) != 2 || s.commits[0].hash != s.head {
		t.Fatalf("unexpected history: %+v", s.commits)
	}
	changes := make(map[string]gitChange)
	for _, c := range s.changes {
		changes[c.path] = c
	}
	wantChanges := map[string]gitChange{
		"project/new name.txt":  {path: "project/new name.txt", orig: "project/old name.txt", x: 'R', y: ' '},
		"project/mixed.txt":     {path: "project/mixed.txt", x: 'M', y: 'M'},
		"project/new.txt":       {path: "project/new.txt", x: 'A', y: ' '},
		"loose/subdir/file.txt": {path: "loose/subdir/file.txt", x: '?', y: '?'},
	}
	if len(s.changes) != len(wantChanges) {
		t.Errorf("changes = %+v, want %d entries", s.changes, len(wantChanges))
	}
	for path, want := range wantChanges {
		if got := changes[path]; got != want {
			t.Errorf("change for %q = %+v, want %+v", path, got, want)
		}
	}

	tree := Tree{git: s.status, gitRoot: root}
	for path, want := range map[string]byte{
		"project":               'M',
		"project/new name.txt":  'M',
		"project/old name.txt":  0,
		"project/mixed.txt":     'M',
		"project/new.txt":       'A',
		"loose":                 '?',
		"loose/subdir/file.txt": '?',
	} {
		if got := tree.gitStatusFor(filepath.Join(root, path)); got != want {
			t.Errorf("tree status for %q = %q, want %q", path, got, want)
		}
	}
	unchanged := gitSnapshotFor(root, s.head, false)
	if unchanged.logLoaded {
		t.Error("snapshot reloaded history with an unchanged HEAD")
	}
}

func TestGitPollIntervalFollowsSidebarFocus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		view    int
		visible bool
		focused bool
		want    time.Duration
	}{
		{"focused Changes", sideChanges, true, true, 2 * time.Second},
		{"focused History", sideHistory, true, true, 2 * time.Second},
		{"unfocused window", sideChanges, true, false, 15 * time.Second},
		{"hidden Changes", sideChanges, false, true, 15 * time.Second},
		{"focused Files", sideFiles, true, true, 15 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := App{sideView: tc.view, showSidebar: tc.visible, focused: tc.focused}
			if got := a.gitPollInterval(); got != tc.want {
				t.Errorf("gitPollInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}
