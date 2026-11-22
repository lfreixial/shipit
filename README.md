# shipit

A small TUI for cutting semver releases from any git repository.

```
 shipit  lfreixial/OctoTray · main @ 8c1e6c7

 Latest release
   v0.2.1   PASSED   2d ago
    ✓ CI                     push · success
    ✓ Release                push · success

 Unreleased
   38 commit(s) since v0.2.1  HEAD  PASSED
   8c1e6c7 build(deps): bump the frontend group with 11 updates

 What kind of release?
 ❯ patch   v0.2.1 → v0.2.2
   minor   v0.2.1 → v0.3.0
   major   v0.2.1 → v1.0.0
```

It:

1. Fetches tags and finds the latest `vX.Y.Z` (or `X.Y.Z`) tag.
2. Shows whether the GitHub Actions runs for that release passed or failed, plus the checks on `HEAD` (what you're about to ship).
3. Warns before you tag the wrong thing: not on the default branch, behind or ahead of upstream, uncommitted changes, HEAD already released, failing checks.
4. Lets you pick **patch / minor / major**, works out the next version, and asks you to confirm.
5. Creates an annotated tag, pushes it, then watches the workflows the tag triggered until they finish. It exits non-zero if they fail.

It only creates tags. Your CI does the rest, for example a workflow `on: push: tags: ['v*']`.

## Requirements

- `git`
- [`gh`](https://cli.github.com), logged in, for the workflow and release status. Without it shipit still tags and pushes; it just can't show pass/fail.

## Install

With mise (builds from source with Go):

```sh
mise use -g go:github.com/lfreixial/shipit@latest
```

Or with Go directly: `go install github.com/lfreixial/shipit@latest`.

## Use it as a mise task

**Every repository at once:**

```sh
shipit init --global     # writes ~/.config/mise/tasks/release
mise run release         # now works in any repo
```

**One repository** (so collaborators get the task too):

```sh
shipit init              # appends [tasks.release] to the repo's mise.toml
```

which adds:

```toml
[tasks.release]
description = "Tag a new release (patch/minor/major) with shipit"
run = "shipit"
```

To make the repo fully self-contained, also pin the tool there:

```toml
[tools]
"go:github.com/lfreixial/shipit" = "latest"
```

## Flags

| Flag | |
| --- | --- |
| `--dry-run` | Show everything, create nothing |
| `--no-push` | Create the tag locally only |
| `--no-watch` | Exit straight after pushing |
| `--remote origin` | Remote to push to |
| `--prefix app-v` | Use tags with a different prefix (monorepos). New repos start at `v0.0.1` / `v0.1.0` / `v1.0.0` |

Arguments after `mise run release` are passed through, e.g. `mise run release --dry-run`.

## Keys

`↑/↓` or `j/k` choose · `enter` select · `y` confirm · `n`/`esc` back · `r` refresh · `q` quit

## Development

```sh
mise run test
mise run build
mise run try      # dry run against the current repo
```
