package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	tea "charm.land/bubbletea/v2"
)

var version = "dev"

const usage = `shipit: tag a new semver release from a TUI

Usage:
  shipit [flags]           pick patch/minor/major, bump version files, tag and push
  shipit init              add a "release" task to this repo's mise.toml
  shipit init --global     add a "release" task that works in every repo
  shipit version

Flags:
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := initCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "shipit:", err)
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" {
				version = bi.Main.Version
			}
			fmt.Println("shipit", version)
			return
		}
	}

	var opts Options
	fs := flag.NewFlagSet("shipit", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	fs.StringVar(&opts.Remote, "remote", "origin", "git remote to push the tag to")
	fs.StringVar(&opts.Prefix, "prefix", "", `tag prefix, e.g. "app-v" (default: match "v" or no prefix, new repos get "v")`)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would happen without creating a tag")
	fs.BoolVar(&opts.NoPush, "no-push", false, "create the tag locally but don't push it")
	fs.BoolVar(&opts.NoBump, "no-bump", false, "only tag; don't bump and commit version files")
	fs.BoolVar(&opts.NoWatch, "no-watch", false, "exit after pushing instead of watching the workflows")
	_ = fs.Parse(os.Args[1:])
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prefix" {
			opts.PrefixSet = true
		}
	})

	final, err := tea.NewProgram(newModel(opts)).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shipit:", err)
		os.Exit(1)
	}
	m := final.(model)
	if m.stage == stError || m.stage == stFailed || (m.stage == stDone && summarise(m.watchRuns) == StateFail) {
		os.Exit(1)
	}
}

const repoTask = `
[tasks.release]
description = "Tag a new release (patch/minor/major) with shipit"
run = "shipit"
`

const globalTask = `#!/usr/bin/env bash
#MISE description="Tag a new release (patch/minor/major) with shipit"
# Global tasks run from mise's config root, so go back to where mise was run.
cd "${MISE_ORIGINAL_CWD:-$PWD}" || exit 1
exec shipit "$@"
`

func initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	global := fs.Bool("global", false, "install the task in mise's global tasks directory")
	_ = fs.Parse(args)

	if *global {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir = filepath.Join(home, ".config")
		}
		path := filepath.Join(dir, "mise", "tasks", "release")
		if cur, err := os.ReadFile(path); err == nil {
			if string(cur) == globalTask {
				fmt.Println("already set up:", path)
				return nil
			}
			if !strings.Contains(string(cur), "exec shipit") {
				return fmt.Errorf("%s already exists and wasn't written by shipit; leaving it alone", path)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(globalTask), 0o755); err != nil {
			return err
		}
		fmt.Println("wrote", path)
		fmt.Println("run `mise run release` in any repository")
		return nil
	}

	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("run this inside a git repository (or use --global)")
	}
	path := filepath.Join(root, "mise.toml")
	for _, name := range []string{"mise.toml", ".mise.toml", "mise/config.toml", ".mise/config.toml", ".config/mise.toml"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			path = filepath.Join(root, name)
			break
		}
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), "[tasks.release]") {
		fmt.Println("already set up:", path)
		return nil
	}
	content := repoTask
	if len(existing) == 0 {
		content = strings.TrimLeft(content, "\n")
	} else if !strings.HasSuffix(string(existing), "\n") {
		content = "\n" + content
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	fmt.Println("added the release task to", path)
	fmt.Println("run `mise run release`")
	return nil
}
