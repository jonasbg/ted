# ted

A fast, native code editor you open from the terminal. `ted path` opens a
window with the file and its project tree, then returns your prompt
immediately. It's one Go binary with GPU rendering through Gio. There is no
Electron, no webview and no extension host.

```
ted                 # current folder
ted src/            # a folder
ted main.go         # a file; the tree shows its git project
ted main.go:42      # jump to a line (paste file:line straight from tool output)
ted a.go b.go       # several files as tabs
git config core.editor "ted -w"   # block until the window closes
```

If a window for the same project is already open, `ted file` opens the file
there instead of starting another window.

## What you get

- Preview tabs, as in VS Code: a single click shows a file in an italic
  preview tab that the next click replaces. Double-click the file or tab, or
  edit the file, to keep it open.
- Source control (read-only, Cmd/Ctrl+Shift+G): staged, unstaged and
  untracked files, each opening a coloured diff. History (Cmd/Ctrl+Shift+H)
  lists commits; click one to see its message, stats and full diff. Diff
  views refresh as files change.
- File tree with git status colours. You can create, rename and delete files,
  and toggle dotfiles.
- Syntax highlighting for about 250 languages. Large files are highlighted
  around the viewport, so a 10 MB file still edits instantly.
- Editing: undo/redo, mouse selection, double and triple click, auto-indent,
  bracket and quote pairing, bracket matching, indent guides, comment toggle,
  moving and duplicating lines, and selecting the next occurrence. Input
  methods and dead keys work through the platform text-input system.
- Search: find and replace, fuzzy *go to file* (Cmd/Ctrl+P), and project-wide
  search (Cmd/Ctrl+Shift+F) that uses ripgrep when available.
- Files changed on disk, for example by an AI agent, git or a formatter,
  reload automatically if you have no unsaved edits. Only the changed region
  is replaced, so the cursor stays put, and **undo restores your previous
  text**. If you do have unsaved edits, ted warns you and asks before
  overwriting.
- Hot exit: close the window any time. Open tabs and unsaved edits come back
  the next time you open the project.
- On macOS, the title bar is hidden in the Ghostty style: the traffic lights
  float over a top bar that uses the theme's background colour. Drag the
  window by empty space in that bar.

Press **F1** for all shortcuts.

## Install

Requires Go 1.27 or newer and a C compiler (cgo).

```
make install                  # installs ~/.local/bin/ted
make install PREFIX=/usr/local
```

Linux also needs the Wayland/X11 development packages. On Debian or Ubuntu:

```
sudo apt install gcc pkg-config libwayland-dev libx11-dev libx11-xcb-dev \
  libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev libffi-dev \
  libxcursor-dev libvulkan-dev
```

Optional: `ripgrep` for faster, .gitignore-aware search.

## Options

```
--theme NAME        colour theme (or TED_THEME); --themes lists them
--native-titlebar   keep the standard macOS title bar
-w, --wait          block until the window closes
-n, --new           always open a new window
```

The default theme is `catppuccin-mocha`. Light themes such as `github` and
`catppuccin-latte` work too.
