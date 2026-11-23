package main

import "strings"

// fileTypeGroup is a set of file endings shown together on the settings page.
type fileTypeGroup struct {
	label string
	exts  []string
}

// fileTypes are the endings "Make default" binds to ted on macOS, and the
// list the settings page shows. HTML is left out on purpose: it stays with
// the browser unless the HTML toggle says otherwise.
var fileTypes = []fileTypeGroup{
	{"Text", []string{"txt", "md", "markdown", "rst", "log", "csv", "tsv", "tex", "bib", "diff", "patch"}},
	{"Data and config", []string{"json", "jsonc", "json5", "yaml", "yml", "toml", "xml", "ini", "cfg", "conf", "properties", "env", "lock", "mod", "sum"}},
	{"Shell", []string{"sh", "bash", "zsh", "fish", "ps1", "bat", "vim"}},
	{"Code", []string{"c", "h", "cc", "cpp", "hpp", "m", "mm", "swift", "go", "rs", "zig", "lua", "py", "rb", "php",
		"java", "kt", "kts", "scala", "dart", "hs", "ml", "elm", "erl", "ex", "exs", "clj", "cljs", "jl",
		"js", "mjs", "cjs", "jsx", "tsx", "vue", "svelte", "css", "scss", "sass", "less", "sql", "graphql", "gql", "proto"}},
	{".NET", []string{"cs", "csx", "fs", "fsx", "vb", "razor", "cshtml", "xaml", "csproj", "fsproj", "vbproj", "sln", "slnx", "props", "targets", "nuspec", "resx", "config"}},
	{"Build", []string{"mk", "cmake", "gradle", "just", "dockerfile", "containerfile", "tf", "tfvars", "hcl", "nix"}},
}

// noExtensionLabel names the files macOS types as public.data, which
// "Make default" also binds so Dockerfiles open in ted.
const noExtensionLabel = "Dockerfile, Containerfile, LICENSE, .gitignore, .env and other files with no ending"

// openWithOnly are endings ted is offered for in "Open With" but never made
// the default for: HTML belongs to the browser, and macOS registers .ts and
// .mts as video.
var openWithOnly = []string{"html", "htm", "ts", "mts", "cts"}

func defaultExts() []string {
	var all []string
	for _, g := range fileTypes {
		all = append(all, g.exts...)
	}
	return all
}

func dotted(exts []string) string {
	return "." + strings.Join(exts, "  .")
}
