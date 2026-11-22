package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pkgJSON = `{
  "name": "demo",
  "version": "0.2.1",
  "scripts": { "build": "vite build" },
  "dependencies": { "x": { "version": "0.2.1" } }
}
`

const lockJSON = `{
  "name": "demo",
  "version": "0.2.1",
  "lockfileVersion": 3,
  "packages": {
    "": {
      "name": "demo",
      "version": "0.2.1"
    },
    "node_modules/dep": {
      "version": "0.2.1"
    }
  }
}
`

const cargoToml = `[package]
name = "demo"
version = "0.2.1" # keep in sync
edition = "2021"

[dependencies]
serde = { version = "1", features = ["derive"] }
`

const cargoLock = `version = 4

[[package]]
name = "aaa"
version = "0.2.1"

[[package]]
name = "demo"
version = "0.2.1"
dependencies = [
 "aaa",
]
`

const pyproject = `[build-system]
requires = ["hatchling"]

[project]
name = "demo"
version = "0.2.1"
`

func TestJSONString(t *testing.T) {
	s, e, v, ok := jsonString([]byte(lockJSON), "packages", "", "version")
	if !ok || v != "0.2.1" || lockJSON[s:e] != "0.2.1" {
		t.Fatalf("got %d %d %q %v", s, e, v, ok)
	}
	if _, _, _, ok := jsonString([]byte(`{"a":{"version":"1.0.0"}}`), "version"); ok {
		t.Fatal("nested version must not match top-level")
	}
}

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

func TestBumpFiles(t *testing.T) {
	dir := repo(t, map[string]string{
		"package.json":              pkgJSON,
		"package-lock.json":         lockJSON,
		"src-tauri/Cargo.toml":      cargoToml,
		"src-tauri/Cargo.lock":      cargoLock,
		"src-tauri/tauri.conf.json": `{"productName":"Demo","version":"0.2.1"}`,
		"pyproject.toml":            pyproject,
		"other/package.json":        `{"name":"other","version":"5.0.0"}`,
		"deep/a/b/package.json":     `{"version":"0.2.1"}`,
	})
	latest := Version{Prefix: "v", Major: 0, Minor: 2, Patch: 1}
	files, err := findVersionFiles(dir, Config{}, latest, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*VersionFile{}
	for _, f := range files {
		got[f.Path] = f
	}
	for _, p := range []string{"package.json", "package-lock.json", "src-tauri/Cargo.toml", "src-tauri/Cargo.lock", "src-tauri/tauri.conf.json", "pyproject.toml"} {
		if f := got[p]; f == nil || f.Skip != "" {
			t.Errorf("%s should be bumped, got %+v", p, f)
		}
	}
	if f := got["other/package.json"]; f == nil || f.Skip == "" {
		t.Errorf("other/package.json should be skipped, got %+v", f)
	}
	if got["deep/a/b/package.json"] != nil {
		t.Error("deep files aren't auto-detected")
	}

	for _, f := range files {
		if f.Skip != "" {
			continue
		}
		if _, err := bumpFile(dir, f, "0.3.0"); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"package.json":         strings.Replace(pkgJSON, `"version": "0.2.1"`, `"version": "0.3.0"`, 1),
		"package-lock.json":    strings.Replace(lockJSON, `"version": "0.2.1"`, `"version": "0.3.0"`, 2),
		"src-tauri/Cargo.toml": strings.Replace(cargoToml, `"0.2.1"`, `"0.3.0"`, 1),
		"src-tauri/Cargo.lock": strings.Replace(cargoLock, "name = \"demo\"\nversion = \"0.2.1\"", "name = \"demo\"\nversion = \"0.3.0\"", 1),
		"pyproject.toml":       strings.Replace(pyproject, `"0.2.1"`, `"0.3.0"`, 1),
		"other/package.json":   `{"name":"other","version":"5.0.0"}`,
	}
	for p, w := range want {
		b, _ := os.ReadFile(filepath.Join(dir, p))
		if string(b) != w {
			t.Errorf("%s:\n%s\nwant:\n%s", p, b, w)
		}
	}
}

func TestBumpAlreadyAtVersion(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": strings.ReplaceAll(pkgJSON, "0.2.1", "0.2.2")})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Patch: 1, Minor: 2}, true)
	if err != nil || len(files) != 1 || files[0].Skip != "" {
		t.Fatalf("a file already at a candidate version should be kept: %+v %v", files, err)
	}
	if changed, err := bumpFile(dir, files[0], "0.2.2"); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestExplicitFiles(t *testing.T) {
	dir := repo(t, map[string]string{"app/manifest.json": `{"version":"9.9.9"}`, "VERSION": "9.9.9\n"})
	files, err := findVersionFiles(dir, Config{Files: []string{"app/manifest.json", "VERSION"}}, Version{Prefix: "v", Patch: 1}, true)
	if err != nil || len(files) != 2 {
		t.Fatalf("%+v %v", files, err)
	}
	for _, f := range files {
		if f.Skip != "" {
			t.Errorf("explicit files are never skipped: %+v", f)
		}
		if _, err := bumpFile(dir, f, "10.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "VERSION")); string(b) != "10.0.0\n" {
		t.Errorf("VERSION = %q", b)
	}
	if _, err := findVersionFiles(dir, Config{Files: []string{"README.md"}}, Version{}, false); err == nil {
		t.Error("unknown file kinds must be rejected")
	}
}

func TestTagWorkflows(t *testing.T) {
	cases := map[string]bool{
		"on: push\n":                                             true,
		"on: [pull_request, push]\n":                             true,
		"on:\n  push:\n":                                         true,
		"on:\n  push:\n    branches: [main]\n":                   false,
		"on:\n  push:\n    tags: ['v*']\n":                       true,
		"on:\n  push:\n    branches: [main]\n    tags: ['v*']\n": true,
		"on:\n  push:\n    paths: ['src/**']\n":                  true,
		"on:\n  pull_request:\n  workflow_dispatch:\n":           false,
	}
	for body, want := range cases {
		dir := repo(t, map[string]string{".github/workflows/x.yml": body})
		got, known := tagWorkflows(dir)
		if !known || got != want {
			t.Errorf("%q: got %v (known %v), want %v", body, got, known, want)
		}
	}
	if got, known := tagWorkflows(t.TempDir()); got || !known {
		t.Error("no workflows means nothing runs on tags")
	}
}
