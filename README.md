# shipit

A small TUI for cutting semver releases from any git repository: it bumps your version files, commits, tags, pushes and tells you whether the release went green.

```
 shipit  lfreixial/OctoTray · main @ 8c1e6c7

 Latest release
   v0.2.1   PASSED   2d ago
    ✓ CI                     push · success
    ✓ Release                push · success

 Unreleased
   38 commit(s) since v0.2.1  HEAD  PASSED
   8c1e6c7 build(deps): bump the frontend group with 11 updates

 Version files
   package-lock.json          0.2.1
   package.json               0.2.1
   src-tauri/Cargo.toml       0.2.1
   src-tauri/tauri.conf.json  0.2.1
   src-tauri/Cargo.lock       0.2.1

 What kind of release?
 ❯ patch   v0.2.1 → v0.2.2
   minor   v0.2.1 → v0.3.0
   major   v0.2.1 → v1.0.0
```

It:

1. Fetches tags and finds the latest `vX.Y.Z` (or `X.Y.Z`) tag.
2. Shows whether the GitHub Actions runs for that release passed or failed, plus the checks on `HEAD` (what you're about to ship).
3. Warns before you tag the wrong thing: not on the default branch, behind or ahead of upstream, uncommitted changes, HEAD already released, failing checks.
4. Lets you pick **patch / minor / major**, works out the next version, and shows the plan:

   ```
    Release v0.2.2?
      1. Bump 5 version file(s) to 0.2.2
      2. Commit "chore(release): 0.2.2"
      3. Push main to origin
      4. Create tag v0.2.2
      5. Push v0.2.2 to origin
   ```

5. Runs it, then watches the workflows the tag triggered until they finish. It exits non-zero if they fail.

Your CI does the rest, for example a workflow `on: push: tags: ['v*']`.

If a step fails, shipit undoes what it can: it restores the files, removes an unpushed bump commit and deletes the local tag. If the bump commit was already pushed but the tag push failed, run shipit again and pick the same version. It sees the files already match and only tags.

## Version files

Before tagging, shipit writes the new version into your version files and commits them, so the tag points at a commit that already carries its own version. It edits only the version string, so formatting, comments and key order stay exactly as they were.

It looks for these tracked files at the repo root and one directory down (e.g. `src-tauri/`):

| File | What changes |
| --- | --- |
| `package.json`, `tauri.conf.json` | top-level `"version"` |
| `package-lock.json` | `"version"` and `packages[""].version` |
| `Cargo.toml` | `[package]` (or `[workspace.package]`) `version` |
| `Cargo.lock` | the entry for each bumped crate |
| `pyproject.toml` | `[project]` (or `[tool.poetry]`) `version` |
| `VERSION` | the whole file |

A detected file is bumped only if it currently holds the latest tag's version, so unrelated packages in a monorepo are left alone. The TUI lists each file it found, including any it skipped and why. With no version files, shipit only creates the tag.

Bumping needs a clean working tree and an up-to-date branch. The commit is pushed straight to your current branch, so on a protected branch you need permission to push (admins can usually bypass the PR rule). Use `--no-bump` to only tag.

### `.shipit.toml`

Optional, at the repo root:

```toml
# Bump exactly these files instead of auto-detecting (any .json gets its top-level "version").
files = ["package.json", "package-lock.json", "src-tauri/Cargo.toml", "src-tauri/Cargo.lock", "src-tauri/tauri.conf.json"]

# Extra command run after the files are edited; its changes are committed too.
# SHIPIT_VERSION (0.2.2) and SHIPIT_TAG (v0.2.2) are set.
command = "./scripts/update-changelog.sh"

commit_message = "chore(release): {version}"

# Never bump files in this repo; only tag.
# bump = false
```

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
| `--dry-run` | Show everything, change nothing |
| `--no-bump` | Only tag; don't edit or commit version files |
| `--no-push` | Commit and tag locally only |
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
