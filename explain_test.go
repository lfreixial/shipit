package main

import (
	"strings"
	"testing"
)

func TestNoFilesNote(t *testing.T) {
	off := false
	latest := RepoState{Latest: Version{Prefix: "v", Minor: 3}, HasLatest: true}
	pyDyn := repo(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\ndynamic = [\n  \"readme\",\n  \"version\",\n]\n"})
	pyStatic := repo(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\nversion = \"0.3.0\"\n"})
	cases := []struct {
		name string
		m    model
		want string // substring, "" for no note
	}{
		{"files found", model{repo: latest, files: []*VersionFile{{Path: "package.json", Current: "0.3.0"}}}, ""},
		{"all skipped", model{repo: latest, files: []*VersionFile{{Path: "package.json", Current: "9.0.0", Skip: "is 9.0.0"}}}, "none of these hold 0.3.0"},
		{"bump off", model{cfg: Config{Bump: &off}}, "turned off"},
		{"command", model{cfg: Config{Command: "make version"}}, ""},
		{"go", model{goModule: "github.com/a/b"}, "Go module's version is its git tag"},
		{"python dynamic", model{root: pyDyn}, "dynamic version"},
		{"python static but unmatched", model{root: pyStatic}, "none found"},
		{"nothing", model{root: t.TempDir()}, "Ruby (version.rb"},
	}
	for _, c := range cases {
		got := c.m.noFilesNote()
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
