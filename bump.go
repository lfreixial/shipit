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
	"VERSION":           "plain",
}

func kindFor(p string) (string, bool) {
	base := path.Base(p)
	if k, ok := knownFiles[base]; ok {
		return k, true
	}
	switch {
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
			if _, ok := knownFiles[path.Base(p)]; ok && strings.Count(p, "/") <= 1 {
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
		if kind == "cargo-lock" {
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

	// A Cargo.lock follows the crates of the Cargo.toml files being bumped
	// in its directory or below.
	for _, l := range locks {
		dir := path.Dir(l.Path)
		data, err := os.ReadFile(filepath.Join(root, l.Path))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Kind != "cargo" || f.Skip != "" || !under(path.Dir(f.Path), dir) {
				continue
			}
			toml, _ := os.ReadFile(filepath.Join(root, f.Path))
			if _, _, name, ok := tomlString(toml, "package", "name"); ok {
				if _, _, v, ok := lockEntry(data, name, f.Current); ok {
					l.Crates = append(l.Crates, name)
					l.Current = v
				}
			}
		}
		if len(l.Crates) > 0 {
			files = append(files, l)
		} else if explicit {
			return nil, fmt.Errorf("%s: no bumped crate found in %s", configName, l.Path)
		}
	}
	return files, nil
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
	case "cargo-lock":
		var v string
		for _, c := range crates {
			if cv, ok := add(lockEntry(data, c, current)); ok {
				v = cv
			} else {
				return nil, "", fmt.Errorf("no %s %s entry", c, current)
			}
		}
		return spans, v, nil
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
