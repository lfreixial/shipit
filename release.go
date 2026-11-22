package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Plan is one release: the steps run in order and stop at the first error,
// undoing whatever can still be undone locally.
type Plan struct {
	Tag     string
	Version string // without prefix, as written to version files
	Root    string
	Remote  string
	Branch  string
	Push    bool
	Files   []*VersionFile
	Command string
	Message string

	steps []Step

	// progress, for rolling back
	touched   bool
	committed bool
	pushed    bool
	tagged    bool
	Head      string // commit the tag points at
	Changed   []string
}

type Step struct {
	Title string
	Run   func(*Plan) error
}

func (p *Plan) bumping() bool { return len(p.Files) > 0 || p.Command != "" }

func (p *Plan) build() {
	p.steps = nil
	if p.bumping() {
		n := len(p.Files)
		title := fmt.Sprintf("Bump %d version file(s) to %s", n, p.Version)
		if n == 0 {
			title = "Run the bump command"
		}
		p.steps = append(p.steps,
			Step{title, (*Plan).bump},
			Step{fmt.Sprintf("Commit %q", p.Message), (*Plan).commit},
		)
		if p.Push {
			p.steps = append(p.steps, Step{fmt.Sprintf("Push %s to %s", p.Branch, p.Remote), (*Plan).pushBranch})
		}
	}
	p.steps = append(p.steps, Step{"Create tag " + p.Tag, (*Plan).createTag})
	if p.Push {
		p.steps = append(p.steps, Step{fmt.Sprintf("Push %s to %s", p.Tag, p.Remote), (*Plan).pushTag})
	}
}

func (p *Plan) bump() error {
	if st, _ := git("-C", p.Root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		return fmt.Errorf("the working tree has uncommitted changes; commit or stash them first")
	}
	p.touched = true
	for _, f := range p.Files {
		changed, err := bumpFile(p.Root, f, p.Version)
		if err != nil {
			return err
		}
		if changed {
			p.Changed = append(p.Changed, f.Path)
		}
	}
	if p.Command != "" {
		cmd := exec.Command("sh", "-c", p.Command)
		cmd.Dir = p.Root
		cmd.Env = append(os.Environ(), "SHIPIT_VERSION="+p.Version, "SHIPIT_TAG="+p.Tag)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("bump command failed: %s", lastLine(string(out), err))
		}
	}
	return nil
}

func (p *Plan) commit() error {
	st, _ := git("-C", p.Root, "status", "--porcelain", "--untracked-files=no")
	if st == "" {
		// Already at this version (e.g. re-running after a failed tag
		// push): tag the current commit.
		p.Head, _ = git("-C", p.Root, "rev-parse", "HEAD")
		return nil
	}
	if _, err := git("-C", p.Root, "commit", "-q", "-a", "-m", p.Message); err != nil {
		return err
	}
	p.committed = true
	p.Head, _ = git("-C", p.Root, "rev-parse", "HEAD")
	return nil
}

func (p *Plan) pushBranch() error {
	if !p.committed {
		return nil
	}
	_, err := git("-C", p.Root, "push", "-q", p.Remote, "HEAD:refs/heads/"+p.Branch)
	if err != nil {
		return pushError(err)
	}
	p.pushed = true
	return nil
}

func (p *Plan) createTag() error {
	if p.Head == "" {
		p.Head, _ = git("-C", p.Root, "rev-parse", "HEAD")
	}
	if _, err := git("-C", p.Root, "tag", "-a", p.Tag, "-m", "Release "+p.Tag, p.Head); err != nil {
		return err
	}
	p.tagged = true
	return nil
}

func (p *Plan) pushTag() error {
	if _, err := git("-C", p.Root, "push", "-q", p.Remote, "refs/tags/"+p.Tag); err != nil {
		return pushError(err)
	}
	return nil
}

// pushError keeps the lines of git's output that say why a push failed.
func pushError(err error) error {
	var keep []string
	for _, l := range lines(err.Error()) {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "remote:") || strings.HasPrefix(l, "! [") || strings.HasPrefix(l, "fatal:") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		return err
	}
	return fmt.Errorf("push failed: %s", strings.Join(keep, "\n  "))
}

// rollback undoes local changes after a failed step and says what's left.
func (p *Plan) rollback() string {
	var notes []string
	if p.tagged {
		_, _ = git("-C", p.Root, "tag", "-d", p.Tag)
		notes = append(notes, "deleted the local tag")
	}
	switch {
	case p.pushed:
		notes = append(notes, fmt.Sprintf("the bump commit is on %s/%s; run shipit again and pick %s to tag it", p.Remote, p.Branch, p.Version))
	case p.committed:
		if _, err := git("-C", p.Root, "reset", "-q", "--keep", "HEAD~1"); err == nil {
			notes = append(notes, "undid the version bump commit")
		} else {
			notes = append(notes, "couldn't undo the version bump commit: "+firstLine(err.Error()))
		}
	case p.touched:
		// The tree was clean before bumping, so every change is ours.
		_, _ = git("-C", p.Root, "checkout", "-q", "HEAD", "--", ".")
		notes = append(notes, "restored the version files")
	}
	return strings.Join(notes, "; ")
}

func lastLine(out string, err error) string {
	ls := lines(strings.TrimSpace(out))
	if len(ls) == 0 {
		return err.Error()
	}
	return ls[len(ls)-1]
}
