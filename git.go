package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func git(args ...string) (string, error) { return run("git", args...) }

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// RepoState is everything shipit knows about the local repository.
type RepoState struct {
	Branch      string
	Head        string // full SHA
	HeadSubject string
	Dirty       bool
	Ahead       int // commits on HEAD not on its upstream
	Behind      int
	HasUpstream bool

	Latest     Version
	LatestTag  string
	HasLatest  bool
	TagSHA     string // commit the latest tag points at
	Unreleased int    // commits since the latest tag
}

func loadRepo(remote, prefix string, prefixSet bool) (RepoState, error) {
	var s RepoState
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return s, errors.New("not inside a git repository")
	}
	// Best effort: make tags and upstream counts current.
	_, _ = git("fetch", "--quiet", "--tags", remote)

	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return s, errors.New("this repository has no commits yet")
	}
	s.Head = head
	s.HeadSubject, _ = git("log", "-1", "--format=%s")
	s.Branch, _ = git("rev-parse", "--abbrev-ref", "HEAD")
	if st, _ := git("status", "--porcelain", "--untracked-files=no"); st != "" {
		s.Dirty = true
	}
	if counts, err := git("rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		s.HasUpstream = true
		if f := strings.Fields(counts); len(f) == 2 {
			s.Ahead, _ = strconv.Atoi(f[0])
			s.Behind, _ = strconv.Atoi(f[1])
		}
	}

	tags, _ := git("tag", "--list")
	s.Latest, s.LatestTag, s.HasLatest = latestVersion(lines(tags), prefix, prefixSet)
	if s.HasLatest {
		s.TagSHA, _ = git("rev-list", "-n", "1", s.LatestTag)
		if n, err := git("rev-list", "--count", s.LatestTag+"..HEAD"); err == nil {
			s.Unreleased, _ = strconv.Atoi(n)
		}
	} else {
		if n, err := git("rev-list", "--count", "HEAD"); err == nil {
			s.Unreleased, _ = strconv.Atoi(n)
		}
	}
	return s, nil
}

func tagExists(tag string) bool {
	_, err := git("rev-parse", "-q", "--verify", "refs/tags/"+tag)
	return err == nil
}

func createTag(tag string) error {
	_, err := git("tag", "-a", tag, "-m", "Release "+tag)
	return err
}

func pushTag(remote, tag string) error {
	_, err := git("push", remote, "refs/tags/"+tag)
	return err
}

func deleteLocalTag(tag string) { _, _ = git("tag", "-d", tag) }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
