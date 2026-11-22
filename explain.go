package main

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
)

// Shown when auto-detection finds nothing, so it's clear what was checked.
// Lock files aren't listed: they're only bumped alongside these.
const lookedFor = "Node/Tauri (package.json, tauri.conf.json), Rust (Cargo.toml), " +
	"Python (pyproject.toml, setup.cfg, setup.py, __version__), " +
	"Ruby (version.rb, .gemspec) or a VERSION file"

// noFilesNote explains why there are no version files to bump. It returns
// "" when there's nothing to explain (files were found, or a bump command
// will run).
func (m model) noFilesNote() string {
	if m.cfg.Command != "" {
		return ""
	}
	if m.cfg.Bump != nil && !*m.cfg.Bump {
		return "bumping is turned off in " + configName + ", so only the tag is created"
	}
	if len(m.files) > 0 {
		if len(m.bumpFiles()) == 0 {
			return "none of these hold " + plain(m.repo.Latest) + ", so only the tag is created; list the right files in " + configName + " if that's wrong"
		}
		return ""
	}
	if m.goModule != "" {
		return "none needed: a Go module's version is its git tag, so only the tag is created"
	}
	if pyDynamicVersion(m.root) {
		return "none needed: pyproject.toml has a dynamic version (e.g. setuptools-scm or hatch-vcs read it from the tag), so only the tag is created"
	}
	if len(m.cfg.Files) > 0 {
		return "" // explicit files that failed to load are reported as an error
	}
	return "none found, so only the tag is created. Looked for " + lookedFor + ". To bump other files, list them in " + configName
}

// pyDynamicVersion reports whether pyproject.toml leaves the version to a
// build backend plugin rather than writing it down.
func pyDynamicVersion(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return false
	}
	var p struct {
		Project struct {
			Dynamic []string `toml:"dynamic"`
		} `toml:"project"`
	}
	if _, err := toml.Decode(string(data), &p); err != nil {
		return false
	}
	return slices.Contains(p.Project.Dynamic, "version")
}
