package main

import (
	"encoding/json"
	"os/exec"
	"sort"
	"time"
)

// GitHub access goes through the gh CLI so shipit reuses its login and
// works with private repositories and GitHub Enterprise alike.

type GHRepo struct {
	NameWithOwner    string `json:"nameWithOwner"`
	URL              string `json:"url"`
	DefaultBranchRef struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

type Release struct {
	TagName      string    `json:"tagName"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	IsDraft      bool      `json:"isDraft"`
	IsPrerelease bool      `json:"isPrerelease"`
	PublishedAt  time.Time `json:"publishedAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Run struct {
	ID           int64     `json:"databaseId"`
	WorkflowName string    `json:"workflowName"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	Event        string    `json:"event"`
	HeadBranch   string    `json:"headBranch"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
}

func haveGH() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func ghJSON(v any, args ...string) error {
	out, err := run("gh", args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

func loadGHRepo() (GHRepo, error) {
	var r GHRepo
	err := ghJSON(&r, "repo", "view", "--json", "nameWithOwner,url,defaultBranchRef")
	return r, err
}

func loadRelease(tag string) (*Release, error) {
	var r Release
	if err := ghJSON(&r, "release", "view", tag, "--json", "tagName,name,url,isDraft,isPrerelease,publishedAt,createdAt"); err != nil {
		return nil, err
	}
	return &r, nil
}

// runsForCommit lists the latest run of each workflow/event pair for a
// commit. With ref set, only runs triggered for that branch or tag count.
func runsForCommit(sha, ref string) ([]Run, error) {
	var all []Run
	if err := ghJSON(&all, "run", "list", "--commit", sha, "--limit", "100",
		"--json", "databaseId,workflowName,status,conclusion,event,headBranch,url,createdAt"); err != nil {
		return nil, err
	}
	latest := map[string]Run{}
	for _, r := range all {
		if ref != "" && r.HeadBranch != ref {
			continue
		}
		// Dependabot and similar bookkeeping runs say nothing about the code.
		if r.Event == "dynamic" {
			continue
		}
		key := r.WorkflowName + "\x00" + r.Event
		if cur, ok := latest[key]; !ok || r.CreatedAt.After(cur.CreatedAt) {
			latest[key] = r
		}
	}
	runs := make([]Run, 0, len(latest))
	for _, r := range latest {
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].WorkflowName < runs[j].WorkflowName })
	return runs, nil
}

type State int

const (
	StateUnknown State = iota
	StateNone
	StateRunning
	StatePass
	StateFail
	StateCancelled
)

func summarise(runs []Run) State {
	if len(runs) == 0 {
		return StateNone
	}
	running, cancelled := false, false
	for _, r := range runs {
		if r.Status != "completed" {
			running = true
			continue
		}
		switch r.Conclusion {
		case "failure", "timed_out", "startup_failure", "action_required":
			return StateFail
		case "cancelled":
			cancelled = true
		}
	}
	switch {
	case running:
		return StateRunning
	case cancelled:
		return StateCancelled
	}
	return StatePass
}

func runState(r Run) State { return summarise([]Run{r}) }
