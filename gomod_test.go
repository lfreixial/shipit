package main

import (
	"strings"
	"testing"
)

func TestGoMajorProblem(t *testing.T) {
	v := func(maj int) Version { return Version{Prefix: "v", Major: maj} }
	cases := []struct {
		module string
		next   Version
		bad    string // substring of the problem, "" for none
	}{
		{"github.com/a/b", v(0), ""},
		{"github.com/a/b", v(1), ""},
		{"github.com/a/b", v(2), "github.com/a/b/v2"},
		{"github.com/a/b/v2", v(2), ""},
		{"github.com/a/b/v2", v(3), "github.com/a/b/v3"},
		{"github.com/a/b/v2", v(1), "only accepts v2 tags"},
		{"gopkg.in/yaml.v3", v(4), ""},
		{"github.com/a/b", Version{Major: 1}, `starting with "v"`},
		{"", v(5), ""},
	}
	for _, c := range cases {
		got := goMajorProblem(c.module, c.next)
		if (c.bad == "") != (got == "") || !strings.Contains(got, c.bad) {
			t.Errorf("%s @ %s: got %q, want %q", c.module, c.next, got, c.bad)
		}
	}
}

func TestGoModule(t *testing.T) {
	dir := repo(t, map[string]string{"go.mod": "// comment\nmodule github.com/a/b/v3\n\ngo 1.22\n"})
	if got := goModule(dir); got != "github.com/a/b/v3" {
		t.Errorf("got %q", got)
	}
	if goModule(t.TempDir()) != "" {
		t.Error("no go.mod means no module")
	}
}

func TestGoMajorBlocks(t *testing.T) {
	m := model{
		repo:     RepoState{Latest: Version{Prefix: "v", Major: 1, Minor: 4}, LatestTag: "v1.4.0", HasLatest: true, Branch: "main"},
		goModule: "github.com/a/b",
	}
	if b := m.blockers(m.newPlan(Major)); len(b) == 0 || !strings.Contains(b[0], "github.com/a/b/v2 for a v2.0.0 tag") {
		t.Fatalf("v2.0.0 should be blocked: %v", b)
	}
	if b := m.blockers(m.newPlan(Minor)); len(b) != 0 {
		t.Fatalf("v1.5.0 is fine: %v", b)
	}
}
