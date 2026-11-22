package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const versionRb = `# frozen_string_literal: true

module Mygem
  VERSION = "1.2.3"
end
`

const gemspecRef = `Gem::Specification.new do |spec|
  spec.name = "mygem"
  spec.version = Mygem::VERSION
end
`

const gemfileLock = `PATH
  remote: .
  specs:
    mygem (1.2.3)
      rack (~> 3.0)

GEM
  remote: https://rubygems.org/
  specs:
    other (1.2.3)
    rack (3.1.0)

PLATFORMS
  ruby

DEPENDENCIES
  mygem!

BUNDLED WITH
   2.5.0
`

func bumpAll(t *testing.T, dir string, files []*VersionFile, next string) {
	t.Helper()
	for _, f := range files {
		if f.Skip != "" {
			continue
		}
		if _, err := bumpFile(dir, f, next); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func paths(files []*VersionFile) string {
	var out []string
	for _, f := range files {
		if f.Skip == "" {
			out = append(out, f.Path)
		}
	}
	return strings.Join(out, " ")
}

func TestRubyGem(t *testing.T) {
	dir := repo(t, map[string]string{
		"lib/mygem/version.rb": versionRb,
		"mygem.gemspec":        gemspecRef,
		"Gemfile.lock":         gemfileLock,
	})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Major: 1, Minor: 2, Patch: 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The gemspec reads Mygem::VERSION, so there's nothing to edit in it.
	if got := paths(files); got != "lib/mygem/version.rb Gemfile.lock" {
		t.Fatalf("files = %q", got)
	}
	bumpAll(t, dir, files, "1.3.0")
	if got, want := read(t, dir, "lib/mygem/version.rb"), strings.Replace(versionRb, "1.2.3", "1.3.0", 1); got != want {
		t.Errorf("version.rb:\n%s", got)
	}
	if got, want := read(t, dir, "Gemfile.lock"), strings.Replace(gemfileLock, "mygem (1.2.3)", "mygem (1.3.0)", 1); got != want {
		t.Errorf("Gemfile.lock (other gems must keep their versions):\n%s", got)
	}
}

func TestRubyLiteralGemspec(t *testing.T) {
	spec := "Gem::Specification.new do |s|\n  s.name    = 'tool'\n  s.version = '0.4.0'\nend\n"
	dir := repo(t, map[string]string{"tool.gemspec": spec})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Minor: 4}, true)
	if err != nil || paths(files) != "tool.gemspec" {
		t.Fatalf("%q %v", paths(files), err)
	}
	bumpAll(t, dir, files, "0.4.1")
	if got := read(t, dir, "tool.gemspec"); got != strings.Replace(spec, "'0.4.0'", "'0.4.1'", 1) {
		t.Errorf("gemspec:\n%s", got)
	}
}

func TestRubyApp(t *testing.T) {
	// A Rails app's Gemfile.lock has no PATH gem, so it's left alone.
	lock := "GEM\n  remote: https://rubygems.org/\n  specs:\n    rails (1.2.3)\n"
	dir := repo(t, map[string]string{"Gemfile.lock": lock, "VERSION": "1.2.3\n"})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Major: 1, Minor: 2, Patch: 3}, true)
	if err != nil || paths(files) != "VERSION" {
		t.Fatalf("%q %v", paths(files), err)
	}
}
