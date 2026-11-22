package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Version files are edited in place: only the bytes of the version string
// change, so formatting, key order and comments stay exactly as they were.

type Config struct {
	Bump          *bool    `toml:"bump"`
	Files         []string `toml:"files"`
	CommitMessage string   `toml:"commit_message"`
	Command       string   `toml:"command"`
}

const configName = ".shipit.toml"
const defaultCommitMessage = "chore(release): {version}"

func loadConfig(root string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(root, configName))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return c, fmt.Errorf("%s: %w", configName, err)
	}
	return c, nil
}

func (c Config) commitMessage(version string) string {
	msg := c.CommitMessage
	if msg == "" {
		msg = defaultCommitMessage
	}
	return strings.ReplaceAll(msg, "{version}", version)
}

type VersionFile struct {
	Path    string // relative to the repo root, slash separated
	Kind    string
	Current string
	Crates  []string // Cargo.lock: the crates whose entries are bumped
	Skip    string   // why auto-detection left it out
}

// Auto-detection looks at tracked files with these names at the root and
// one directory down (e.g. src-tauri/).
var knownFiles = map[string]string{
	"package.json":      "json",
	"tauri.conf.json":   "json",
	"package-lock.json": "npm-lock",
	"Cargo.toml":        "cargo",
	"Cargo.lock":        "cargo-lock",
	"pyproject.toml":    "pyproject",
	"Gemfile.lock":      "gemfile-lock",
	"uv.lock":           "uv-lock",
	"setup.cfg":         "setup-cfg",
	"VERSION":           "plain",
}

// Lock files record the project's own version too. They are bumped along
// with the manifests next to (or below) them rather than detected on
// their own.
var lockKinds = map[string]bool{"cargo-lock": true, "gemfile-lock": true, "uv-lock": true}

// Python modules that conventionally hold __version__.
var pyVersionFiles = map[string]bool{
	"__init__.py": true, "__about__.py": true, "__version__.py": true, "_version.py": true, "version.py": true,
}

// autoKind says whether auto-detection should consider a tracked file.
func autoKind(p string) (string, bool) {
	base, depth := path.Base(p), strings.Count(p, "/")
	if k, ok := knownFiles[base]; ok && depth <= 1 {
		return k, true
	}
	switch {
	case base == "version.rb" && strings.HasPrefix(p, "lib/") && depth <= 4:
		return "ruby-version", true // lib/mygem/version.rb, lib/my/gem/version.rb
	case strings.HasSuffix(base, ".gemspec") && depth == 0:
		return "gemspec", true
	case base == "setup.py" && depth == 0:
		return "setup-py", true
	case pyVersionFiles[base] && (depth == 1 || (depth == 2 && strings.HasPrefix(p, "src/"))):
		return "py-version", true // mypkg/__init__.py, src/mypkg/__about__.py
	}
	return "", false
}

// kindFor is how a file listed in .shipit.toml is edited.
func kindFor(p string) (string, bool) {
	base := path.Base(p)
	if k, ok := knownFiles[base]; ok {
		return k, true
	}
	switch {
	case base == "version.rb":
		return "ruby-version", true
	case strings.HasSuffix(base, ".gemspec"):
		return "gemspec", true
	case base == "setup.py":
		return "setup-py", true
	case strings.HasSuffix(base, ".py"):
		return "py-version", true
	case strings.HasSuffix(base, ".json"):
		return "json", true
	case base == "version.txt" || strings.HasSuffix(base, "VERSION"):
		return "plain", true
	}
	return "", false
}

// findVersionFiles returns the files to bump. Explicitly listed files are
// always used. Auto-detected ones are used only when their version is the
// latest tag's (or a version it could become), so unrelated packages in a
// monorepo are left alone.
func findVersionFiles(root string, cfg Config, latest Version, hasLatest bool) ([]*VersionFile, error) {
	if cfg.Bump != nil && !*cfg.Bump {
		return nil, nil
	}
	explicit := len(cfg.Files) > 0
	var paths []string
	if explicit {
		paths = cfg.Files
	} else {
		out, err := run("git", "-C", root, "ls-files")
		if err != nil {
			return nil, err
		}
		for _, p := range lines(out) {
			if _, ok := autoKind(p); ok {
				paths = append(paths, p)
			}
		}
	}

	accept := map[string]bool{}
	if hasLatest {
		for _, v := range []Version{latest, latest.Bump(Patch), latest.Bump(Minor), latest.Bump(Major)} {
			accept[plain(v)] = true
		}
	}

	var files, locks []*VersionFile
	for _, p := range paths {
		kind, ok := kindFor(p)
		if !ok {
			return nil, fmt.Errorf("%s: don't know how to bump %s", configName, p)
		}
		f := &VersionFile{Path: filepath.ToSlash(p), Kind: kind}
		if lockKinds[kind] {
			locks = append(locks, f)
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			if explicit {
				return nil, err
			}
			continue
		}
		cur, err := readVersion(kind, data)
		if err != nil {
			if explicit {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			continue // e.g. a package.json with no version
		}
		f.Current = cur
		if !explicit && hasLatest && !accept[cur] {
			f.Skip = fmt.Sprintf("is %s, not %s", cur, plain(latest))
		}
		files = append(files, f)
	}

	for _, l := range locks {
		data, err := os.ReadFile(filepath.Join(root, l.Path))
		if err != nil {
			continue
		}
		if resolveLock(root, l, data, files) {
			files = append(files, l)
		} else if explicit {
			return nil, fmt.Errorf("%s: %s has no entry for a bumped package", configName, l.Path)
		}
	}
	return files, nil
}

// resolveLock works out which entries of a lock file belong to the
// manifests being bumped, and reports whether there are any.
func resolveLock(root string, l *VersionFile, data []byte, files []*VersionFile) bool {
	dir := path.Dir(l.Path)
	for _, f := range files {
		if f.Skip != "" || !under(path.Dir(f.Path), dir) {
			continue
		}
		switch {
		case l.Kind == "cargo-lock" && f.Kind == "cargo":
			// A Cargo.lock follows the crates of the Cargo.toml files below it.
			manifest, _ := os.ReadFile(filepath.Join(root, f.Path))
			if _, _, name, ok := tomlString(manifest, "package", "name"); ok {
				if _, _, v, ok := lockEntry(data, name, f.Current); ok {
					l.Crates = append(l.Crates, name)
					l.Current = v
				}
			}
		case l.Kind == "uv-lock" && f.Kind == "pyproject":
			// uv.lock lists the project itself under its normalised name.
			manifest, _ := os.ReadFile(filepath.Join(root, f.Path))
			if _, _, name, ok := tomlString(manifest, "project", "name"); ok {
				name = normalisePyName(name)
				if _, _, v, ok := lockEntry(data, name, f.Current); ok {
					l.Crates = append(l.Crates, name)
					l.Current = v
				}
			}
		case l.Kind == "gemfile-lock" && (f.Kind == "ruby-version" || f.Kind == "gemspec"):
			// A gem's own Gemfile.lock lists it under PATH (remote: .).
			if spans := gemfileSpans(data, f.Current); len(spans) > 0 {
				l.Current = f.Current
			}
		}
	}
	return l.Current != ""
}

func under(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

// plain is a version without its tag prefix, as version files store it.
func plain(v Version) string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

type span struct{ start, end int }

// versionSpans locates every version string a file kind keeps.
func versionSpans(kind string, data []byte, crates []string, current string) ([]span, string, error) {
	var spans []span
	add := func(s, e int, v string, ok bool) (string, bool) {
		if ok {
			spans = append(spans, span{s, e})
		}
		return v, ok
	}
	switch kind {
	case "json":
		v, ok := add(jsonString(data, "version"))
		if !ok {
			return nil, "", errors.New(`no top-level "version"`)
		}
		return spans, v, nil
	case "npm-lock":
		v, ok := add(jsonString(data, "version"))
		if !ok {
			return nil, "", errors.New(`no top-level "version"`)
		}
		add(jsonString(data, "packages", "", "version"))
		return spans, v, nil
	case "cargo":
		v, ok := add(tomlString(data, "package", "version"))
		if !ok {
			v, ok = add(tomlString(data, "workspace.package", "version"))
		}
		if !ok {
			return nil, "", errors.New(`no version in [package] or [workspace.package]`)
		}
		return spans, v, nil
	case "pyproject":
		v, ok := add(tomlString(data, "project", "version"))
		if !ok {
			v, ok = add(tomlString(data, "tool.poetry", "version"))
		}
		if !ok {
			return nil, "", errors.New(`no version in [project] or [tool.poetry]`)
		}
		return spans, v, nil
	case "py-version":
		v, ok := add(regexString(data, pyVersionRe))
		if !ok {
			return nil, "", errors.New("no __version__ = \"…\"")
		}
		return spans, v, nil
	case "setup-py":
		v, ok := add(regexString(data, setupPyVersionRe))
		if !ok {
			return nil, "", errors.New("no literal version=\"…\"")
		}
		return spans, v, nil
	case "setup-cfg":
		v, ok := add(iniValue(data, "metadata", "version"))
		if !ok {
			return nil, "", errors.New("no version in [metadata]")
		}
		return spans, v, nil
	case "cargo-lock", "uv-lock":
		var v string
		for _, c := range crates {
			if cv, ok := add(lockEntry(data, c, current)); ok {
				v = cv
			} else {
				return nil, "", fmt.Errorf("no %s %s entry", c, current)
			}
		}
		return spans, v, nil
	case "ruby-version":
		v, ok := add(regexString(data, rubyVersionRe))
		if !ok {
			return nil, "", errors.New("no VERSION = \"…\" constant")
		}
		return spans, v, nil
	case "gemspec":
		v, ok := add(regexString(data, gemspecVersionRe))
		if !ok {
			return nil, "", errors.New("no literal .version = \"…\"")
		}
		return spans, v, nil
	case "gemfile-lock":
		spans = gemfileSpans(data, current)
		if len(spans) == 0 {
			return nil, "", fmt.Errorf("no PATH gem at %s", current)
		}
		return spans, current, nil
	case "plain":
		s := bytes.TrimSpace(data)
		if _, ok := parseTag(string(s)); !ok || bytes.ContainsAny(s, " \n") {
			return nil, "", errors.New("doesn't hold a single X.Y.Z version")
		}
		start := bytes.Index(data, s)
		spans = append(spans, span{start, start + len(s)})
		return spans, string(s), nil
	}
	return nil, "", fmt.Errorf("unknown kind %s", kind)
}

func readVersion(kind string, data []byte) (string, error) {
	_, v, err := versionSpans(kind, data, nil, "")
	return v, err
}

// bumpFile rewrites a version file to next and checks the result reads back.
func bumpFile(root string, f *VersionFile, next string) (changed bool, err error) {
	full := filepath.Join(root, filepath.FromSlash(f.Path))
	data, err := os.ReadFile(full)
	if err != nil {
		return false, err
	}
	spans, _, err := versionSpans(f.Kind, data, f.Crates, f.Current)
	if err != nil {
		return false, fmt.Errorf("%s: %w", f.Path, err)
	}
	var out []byte
	last := 0
	for _, s := range spans {
		out = append(out, data[last:s.start]...)
		out = append(out, next...)
		last = s.end
	}
	out = append(out, data[last:]...)
	if bytes.Equal(out, data) {
		return false, nil
	}
	after, _, err := versionSpans(f.Kind, out, f.Crates, next)
	if err != nil || len(after) != len(spans) {
		return false, fmt.Errorf("%s: the edit didn't read back as %s", f.Path, next)
	}
	info, err := os.Stat(full)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(full, out, info.Mode().Perm())
}

// ---- JSON ----

// jsonString finds the string at a key path (e.g. "packages", "", "version")
// and returns the byte span of its contents.
func jsonString(data []byte, keys ...string) (int, int, string, bool) {
	type frame struct {
		obj, wantKey bool
		key          string
	}
	var stack []frame
	done := func() {
		if n := len(stack); n > 0 && stack[n-1].obj {
			stack[n-1].wantKey = true
		}
	}
	at := func() bool {
		if len(stack) != len(keys) {
			return false
		}
		for i, f := range stack {
			if !f.obj || f.key != keys[i] {
				return false
			}
		}
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			return 0, 0, "", false
		}
		if n := len(stack); n > 0 && stack[n-1].obj && stack[n-1].wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				done()
				continue
			}
			stack[n-1].key, _ = tok.(string)
			stack[n-1].wantKey = false
			continue
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{obj: true, wantKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				done()
			}
			continue
		case string:
			if at() {
				end := int(dec.InputOffset()) - 1
				start := bytes.LastIndexByte(data[:end], '"') + 1
				if string(data[start:end]) == v {
					return start, end, v, true
				}
				return 0, 0, "", false
			}
		}
		done()
	}
}

// ---- TOML (line based, enough for version keys) ----

var (
	tableRe = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	arrayRe = regexp.MustCompile(`^\s*\[\[\s*([^\[\]]+?)\s*\]\]\s*(#.*)?$`)
)

func keyRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]*)"`)
}

type tomlLine struct {
	table string // "[[x]]" for array tables
	off   int
	text  string
}

func tomlLines(data []byte) []tomlLine {
	var out []tomlLine
	table, off := "", 0
	for _, l := range strings.SplitAfter(string(data), "\n") {
		text := strings.TrimRight(l, "\r\n")
		if m := arrayRe.FindStringSubmatch(text); m != nil {
			table = "[[" + m[1] + "]]"
		} else if m := tableRe.FindStringSubmatch(text); m != nil {
			table = m[1]
		}
		out = append(out, tomlLine{table, off, text})
		off += len(l)
	}
	return out
}

func tomlString(data []byte, table, key string) (int, int, string, bool) {
	re := keyRe(key)
	for _, l := range tomlLines(data) {
		if l.table != table {
			continue
		}
		if m := re.FindStringSubmatchIndex(l.text); m != nil {
			return l.off + m[2], l.off + m[3], l.text[m[2]:m[3]], true
		}
	}
	return 0, 0, "", false
}

// lockEntry finds the version of crate's [[package]] entry in a Cargo.lock.
// With want set, only an entry at that version counts.
func lockEntry(data []byte, crate, want string) (int, int, string, bool) {
	nameRe, verRe := keyRe("name"), keyRe("version")
	name := ""
	for _, l := range tomlLines(data) {
		if l.table != "[[package]]" {
			continue
		}
		if arrayRe.MatchString(l.text) {
			name = ""
		} else if m := nameRe.FindStringSubmatch(l.text); m != nil {
			name = m[1]
		} else if m := verRe.FindStringSubmatchIndex(l.text); m != nil && name == crate {
			v := l.text[m[2]:m[3]]
			if want == "" || v == want {
				return l.off + m[2], l.off + m[3], v, true
			}
		}
	}
	return 0, 0, "", false
}

// ---- Ruby and other source files ----

var (
	rubyVersionRe    = regexp.MustCompile(`(?m)^\s*VERSION\s*=\s*["']([^"'\n]+)["']`)
	gemspecVersionRe = regexp.MustCompile(`(?m)^\s*\w+\.version\s*=\s*["']([^"'\n]+)["']`)
	gemSpecLineRe    = regexp.MustCompile(`^    [A-Za-z0-9_.-]+ \(([^)\s]+)\)$`)
)

// regexString returns the span of the first capture group of re.
func regexString(data []byte, re *regexp.Regexp) (int, int, string, bool) {
	m := re.FindSubmatchIndex(data)
	if m == nil {
		return 0, 0, "", false
	}
	return m[2], m[3], string(data[m[2]:m[3]]), true
}

// gemfileSpans finds the versions of the gems a Gemfile.lock loads from
// the repo itself:
//
//	PATH
//	  remote: .
//	  specs:
//	    mygem (1.2.3)
func gemfileSpans(data []byte, current string) []span {
	var spans []span
	inPath, local, off := false, false, 0
	for _, l := range strings.SplitAfter(string(data), "\n") {
		text := strings.TrimRight(l, "\r\n")
		switch {
		case text == "PATH":
			inPath, local = true, false
		case text == "" || !strings.HasPrefix(text, " "):
			inPath = false
		case inPath && strings.TrimSpace(text) == "remote: .":
			local = true
		case inPath && local:
			if m := gemSpecLineRe.FindStringSubmatchIndex(text); m != nil && text[m[2]:m[3]] == current {
				spans = append(spans, span{off + m[2], off + m[3]})
			}
		}
		off += len(l)
	}
	return spans
}

// ---- Python ----

var (
	pyVersionRe      = regexp.MustCompile(`(?m)^__version__\s*(?::\s*str\s*)?=\s*["']([^"'\n]+)["']`)
	setupPyVersionRe = regexp.MustCompile(`\bversion\s*=\s*["']([0-9][^"'\n]*)["']`)
	pyNameRe         = regexp.MustCompile(`[-_.]+`)
)

// normalisePyName is the PEP 503 form uv.lock uses for package names.
func normalisePyName(name string) string {
	return strings.ToLower(pyNameRe.ReplaceAllString(name, "-"))
}

// iniValue finds an unquoted `key = value` in an INI section (setup.cfg).
// Values that aren't plain versions, like `attr: pkg.__version__`, don't
// count.
func iniValue(data []byte, section, key string) (int, int, string, bool) {
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*([0-9][^\s#;]*)\s*$`)
	for _, l := range tomlLines(data) {
		if l.table != section {
			continue
		}
		if m := re.FindStringSubmatchIndex(l.text); m != nil {
			return l.off + m[2], l.off + m[3], l.text[m[2]:m[3]], true
		}
	}
	return 0, 0, "", false
}
