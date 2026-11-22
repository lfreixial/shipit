package main

import "testing"

func TestBump(t *testing.T) {
	v := Version{Prefix: "v", Major: 1, Minor: 4, Patch: 2}
	cases := map[string]string{Patch: "v1.4.3", Minor: "v1.5.0", Major: "v2.0.0"}
	for kind, want := range cases {
		if got := v.Bump(kind).String(); got != want {
			t.Errorf("%s: got %s, want %s", kind, got, want)
		}
	}
}

func TestLatestVersion(t *testing.T) {
	tags := []string{"v0.9.0", "v0.10.0", "v0.10.0-rc.1", "app-v9.0.0", "nightly", "v0.2.11"}
	v, name, ok := latestVersion(tags, "", false)
	if !ok || name != "v0.10.0" || v.Minor != 10 {
		t.Fatalf("got %v %q %v", v, name, ok)
	}
	v, name, ok = latestVersion(tags, "app-v", true)
	if !ok || name != "app-v9.0.0" || v.Prefix != "app-v" {
		t.Fatalf("prefixed: got %v %q %v", v, name, ok)
	}
	if _, _, ok := latestVersion([]string{"nightly"}, "", false); ok {
		t.Fatal("expected no version")
	}
}

func TestSummarise(t *testing.T) {
	cases := []struct {
		runs []Run
		want State
	}{
		{nil, StateNone},
		{[]Run{{Status: "completed", Conclusion: "success"}, {Status: "completed", Conclusion: "skipped"}}, StatePass},
		{[]Run{{Status: "completed", Conclusion: "success"}, {Status: "in_progress"}}, StateRunning},
		{[]Run{{Status: "in_progress"}, {Status: "completed", Conclusion: "failure"}}, StateFail},
		{[]Run{{Status: "completed", Conclusion: "cancelled"}}, StateCancelled},
	}
	for i, c := range cases {
		if got := summarise(c.runs); got != c.want {
			t.Errorf("case %d: got %v, want %v", i, got, c.want)
		}
	}
}
