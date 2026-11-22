package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Options struct {
	Remote    string
	Prefix    string
	PrefixSet bool
	DryRun    bool
	NoPush    bool
	NoWatch   bool
}

type stage int

const (
	stLoading stage = iota
	stSelect
	stConfirm
	stTagging
	stWatching
	stDone
	stError
)

const (
	pollEvery    = 5 * time.Second
	refreshEvery = 10 * time.Second
	noRunsAfter  = 60 * time.Second
)

// ---- messages ----

type loadedMsg struct {
	repo     RepoState
	gh       *GHRepo
	release  *Release
	tagRuns  []Run
	headRuns []Run
	ghNote   string
	err      error
}

type taggedMsg struct{ err error }

type watchMsg struct {
	runs    []Run
	release *Release
	err     error
}

type pollMsg struct{}
type refreshMsg struct{}

// ---- model ----

type model struct {
	opts  Options
	stage stage
	spin  spinner.Model
	err   error

	repo     RepoState
	gh       *GHRepo
	release  *Release
	tagRuns  []Run
	headRuns []Run
	ghNote   string

	cursor int
	newTag string

	watchStart time.Time
	watchRuns  []Run
	newRelease *Release
	watchErr   string
}

func newModel(opts Options) model {
	s := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(sAccent))
	return model{opts: opts, spin: s}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.load())
}

func (m model) load() tea.Cmd {
	opts := m.opts
	return func() tea.Msg {
		repo, err := loadRepo(opts.Remote, opts.Prefix, opts.PrefixSet)
		if err != nil {
			return loadedMsg{err: err}
		}
		msg := loadedMsg{repo: repo}
		if !haveGH() {
			msg.ghNote = "gh CLI not found, so workflow status is unavailable"
			return msg
		}
		ghr, err := loadGHRepo()
		if err != nil {
			msg.ghNote = "no GitHub remote (or gh is not logged in), so workflow status is unavailable"
			return msg
		}
		msg.gh = &ghr
		if repo.HasLatest {
			msg.release, _ = loadRelease(repo.LatestTag)
			msg.tagRuns, err = runsForCommit(repo.TagSHA, "")
			if err != nil {
				msg.ghNote = "could not list workflow runs: " + firstLine(err.Error())
			}
		}
		if repo.Head != repo.TagSHA {
			msg.headRuns, _ = runsForCommit(repo.Head, "")
		}
		return msg
	}
}

func (m model) next(kind string) Version {
	if !m.repo.HasLatest {
		return Version{Prefix: m.defaultPrefix()}.Bump(kind)
	}
	return m.repo.Latest.Bump(kind)
}

func (m model) defaultPrefix() string {
	if m.opts.PrefixSet {
		return m.opts.Prefix
	}
	return "v"
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case loadedMsg:
		if msg.err != nil {
			m.stage, m.err = stError, msg.err
			return m, tea.Quit
		}
		m.repo, m.gh, m.release = msg.repo, msg.gh, msg.release
		m.tagRuns, m.headRuns, m.ghNote = msg.tagRuns, msg.headRuns, msg.ghNote
		if m.stage == stLoading {
			m.stage = stSelect
		}
		if m.anyRunning() {
			return m, tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshMsg{} })
		}
		return m, nil

	case refreshMsg:
		if m.stage == stSelect || m.stage == stConfirm {
			return m, m.load()
		}
		return m, nil

	case taggedMsg:
		if msg.err != nil {
			m.stage, m.err = stError, msg.err
			return m, tea.Quit
		}
		if m.opts.DryRun || m.opts.NoPush || m.opts.NoWatch || m.gh == nil {
			m.stage = stDone
			return m, tea.Quit
		}
		m.stage = stWatching
		m.watchStart = time.Now()
		return m, m.watch()

	case watchMsg:
		if msg.err != nil {
			m.watchErr = firstLine(msg.err.Error())
		} else {
			m.watchErr = ""
			m.watchRuns = msg.runs
		}
		if msg.release != nil {
			m.newRelease = msg.release
		}
		if m.watchFinished() {
			m.stage = stDone
			return m, tea.Quit
		}
		return m, tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} })

	case pollMsg:
		return m, m.watch()

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.stage {
	case stSelect:
		switch k {
		case "q", "esc":
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor + len(bumpKinds) - 1) % len(bumpKinds)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(bumpKinds)
		case "1", "p":
			m.cursor = 0
		case "2", "m":
			m.cursor = 1
		case "3", "M":
			m.cursor = 2
		case "r":
			return m, m.load()
		case "enter", "space", " ":
			m.newTag = m.next(bumpKinds[m.cursor]).String()
			if tagExists(m.newTag) {
				m.ghNote = "tag " + m.newTag + " already exists locally"
				return m, nil
			}
			m.stage = stConfirm
		}
	case stConfirm:
		switch k {
		case "y", "Y", "enter":
			m.stage = stTagging
			return m, m.tag()
		case "n", "N", "esc", "backspace":
			m.stage = stSelect
		case "q":
			return m, tea.Quit
		}
	case stWatching, stDone, stError:
		if k == "q" || k == "esc" || k == "enter" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) tag() tea.Cmd {
	tag, opts := m.newTag, m.opts
	return func() tea.Msg {
		if opts.DryRun {
			return taggedMsg{}
		}
		if err := createTag(tag); err != nil {
			return taggedMsg{fmt.Errorf("creating tag %s: %w", tag, err)}
		}
		if opts.NoPush {
			return taggedMsg{}
		}
		if err := pushTag(opts.Remote, tag); err != nil {
			// Leave nothing half-done: a local-only tag would make the
			// next run think this version was already released.
			deleteLocalTag(tag)
			return taggedMsg{fmt.Errorf("pushing tag %s to %s: %w", tag, opts.Remote, err)}
		}
		return taggedMsg{}
	}
}

func (m model) watch() tea.Cmd {
	sha, tag := m.repo.Head, m.newTag
	return func() tea.Msg {
		runs, err := runsForCommit(sha, tag)
		rel, _ := loadRelease(tag)
		return watchMsg{runs: runs, release: rel, err: err}
	}
}

func (m model) watchFinished() bool {
	if len(m.watchRuns) == 0 {
		return time.Since(m.watchStart) > noRunsAfter
	}
	return summarise(m.watchRuns) != StateRunning
}

func (m model) anyRunning() bool {
	return summarise(m.tagRuns) == StateRunning || summarise(m.headRuns) == StateRunning
}

func (m model) warnings() []string {
	var w []string
	r := m.repo
	if m.gh != nil && m.gh.DefaultBranchRef.Name != "" && r.Branch != m.gh.DefaultBranchRef.Name {
		w = append(w, fmt.Sprintf("you are on %q, not the default branch %q", r.Branch, m.gh.DefaultBranchRef.Name))
	}
	if r.Branch == "HEAD" {
		w = append(w, "HEAD is detached")
	}
	if r.Behind > 0 {
		w = append(w, fmt.Sprintf("%d commit(s) behind upstream; pull first or you will tag old code", r.Behind))
	}
	if r.Ahead > 0 {
		w = append(w, fmt.Sprintf("%d commit(s) not pushed; the tag will point at code the branch on %s doesn't have", r.Ahead, m.opts.Remote))
	}
	if !r.HasUpstream && r.Branch != "HEAD" {
		w = append(w, "branch has no upstream")
	}
	if r.Dirty {
		w = append(w, "uncommitted changes will not be part of the release")
	}
	if r.HasLatest && r.Unreleased == 0 {
		w = append(w, "HEAD is already released as "+r.LatestTag)
	}
	if summarise(m.headRuns) == StateFail {
		w = append(w, "checks on HEAD are failing")
	}
	return w
}

// ---- view ----

var (
	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("5")).Padding(0, 1)
	sAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	sBold   = lipgloss.NewStyle().Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sHead   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	sGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	sRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	sYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	sSel    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	sBadgeG = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("2")).Padding(0, 1)
	sBadgeR = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("1")).Padding(0, 1)
	sBadgeY = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3")).Padding(0, 1)
	sBadgeN = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("8")).Padding(0, 1)
)

func badge(s State) string {
	switch s {
	case StatePass:
		return sBadgeG.Render("PASSED")
	case StateFail:
		return sBadgeR.Render("FAILED")
	case StateRunning:
		return sBadgeY.Render("RUNNING")
	case StateCancelled:
		return sBadgeN.Render("CANCELLED")
	case StateNone:
		return sBadgeN.Render("NO RUNS")
	}
	return sBadgeN.Render("UNKNOWN")
}

func (m model) icon(s State) string {
	switch s {
	case StatePass:
		return sGreen.Render("✓")
	case StateFail:
		return sRed.Render("✗")
	case StateRunning:
		return m.spin.View()
	case StateCancelled:
		return sDim.Render("⊘")
	}
	return sDim.Render("·")
}

func (m model) runLines(runs []Run, b *strings.Builder) {
	for _, r := range runs {
		label := r.Conclusion
		if r.Status != "completed" {
			label = strings.ReplaceAll(r.Status, "_", " ")
		}
		fmt.Fprintf(b, "    %s %-22s %s\n", m.icon(runState(r)), truncate(r.WorkflowName, 22), sDim.Render(r.Event+" · "+label))
	}
}

func (m model) View() tea.View {
	return tea.NewView(m.render())
}

func (m model) render() string {
	var b strings.Builder
	header := sTitle.Render("shipit")
	if m.gh != nil {
		header += " " + sBold.Render(m.gh.NameWithOwner)
	}
	if m.repo.Branch != "" {
		header += sDim.Render(" · " + m.repo.Branch + " @ " + short(m.repo.Head))
	}
	if m.opts.DryRun {
		header += " " + sBadgeY.Render("DRY RUN")
	}
	b.WriteString("\n " + header + "\n\n")

	switch m.stage {
	case stLoading:
		b.WriteString(" " + m.spin.View() + " Fetching tags and workflow status…\n")
		return b.String()
	case stError:
		b.WriteString(" " + sRed.Render("✗ "+m.err.Error()) + "\n\n")
		return b.String()
	}

	// Latest release
	b.WriteString(" " + sHead.Render("Latest release") + "\n")
	if !m.repo.HasLatest {
		b.WriteString("   " + sDim.Render("no release tags yet") + "\n")
	} else {
		line := "   " + sBold.Render(m.repo.LatestTag)
		if m.gh != nil {
			line += "  " + badge(summarise(m.tagRuns))
		}
		if m.release != nil {
			when := m.release.PublishedAt
			kind := ""
			if m.release.IsDraft {
				when, kind = m.release.CreatedAt, " (draft)"
			} else if m.release.IsPrerelease {
				kind = " (pre-release)"
			}
			line += sDim.Render("  " + ago(when) + kind)
		} else if m.gh != nil {
			line += sDim.Render("  tag only, no GitHub release")
		}
		b.WriteString(line + "\n")
		m.runLines(m.tagRuns, &b)
		if m.release != nil {
			b.WriteString("    " + sDim.Render(m.release.URL) + "\n")
		}
	}

	// What's going out
	b.WriteString("\n " + sHead.Render("Unreleased") + "\n")
	since := "in total"
	if m.repo.HasLatest {
		since = "since " + m.repo.LatestTag
	}
	line := fmt.Sprintf("   %d commit(s) %s", m.repo.Unreleased, since)
	if m.repo.Head != m.repo.TagSHA && m.gh != nil {
		line += "  " + sDim.Render("HEAD") + " " + badge(summarise(m.headRuns))
	}
	b.WriteString(line + "\n")
	b.WriteString("   " + sDim.Render(short(m.repo.Head)+" "+truncate(m.repo.HeadSubject, 60)) + "\n")

	if m.ghNote != "" {
		b.WriteString("\n " + sDim.Render("ℹ "+m.ghNote) + "\n")
	}
	if w := m.warnings(); len(w) > 0 && m.stage <= stConfirm {
		b.WriteString("\n")
		for _, s := range w {
			b.WriteString(" " + sYellow.Render("⚠ "+s) + "\n")
		}
	}
	b.WriteString("\n")

	from := "(none)"
	if m.repo.HasLatest {
		from = m.repo.LatestTag
	}

	switch m.stage {
	case stSelect:
		b.WriteString(" " + sHead.Render("What kind of release?") + "\n")
		for i, k := range bumpKinds {
			arrow := sDim.Render(from+" → ") + sBold.Render(m.next(k).String())
			if i == m.cursor {
				fmt.Fprintf(&b, " %s %s  %s\n", sSel.Render("❯"), sSel.Render(fmt.Sprintf("%-6s", k)), arrow)
			} else {
				fmt.Fprintf(&b, "   %-6s  %s\n", k, arrow)
			}
		}
		b.WriteString("\n " + sDim.Render("↑/↓ choose · enter select · r refresh · q quit") + "\n")

	case stConfirm:
		action := "Create tag %s on %s and push it to %s?"
		if m.opts.NoPush {
			action = "Create tag %s on %s (local only, not pushed to %s)?"
		}
		b.WriteString(" " + fmt.Sprintf(action, sSel.Render(m.newTag), sBold.Render(short(m.repo.Head)), m.opts.Remote) + "\n")
		b.WriteString("\n " + sDim.Render("y/enter confirm · n/esc back") + "\n")

	case stTagging:
		b.WriteString(" " + m.spin.View() + " Creating and pushing " + sBold.Render(m.newTag) + "…\n")

	case stWatching, stDone:
		m.renderResult(&b)
	}
	return b.String()
}

func (m model) renderResult(b *strings.Builder) {
	switch {
	case m.opts.DryRun:
		b.WriteString(" " + sYellow.Render("Dry run: would have created and pushed "+m.newTag) + "\n\n")
		return
	case m.opts.NoPush:
		b.WriteString(" " + sGreen.Render("✓ Created "+m.newTag+" locally") + sDim.Render(fmt.Sprintf(" (push with: git push %s %s)", m.opts.Remote, m.newTag)) + "\n\n")
		return
	}
	b.WriteString(" " + sGreen.Render("✓ Pushed "+m.newTag) + "\n")
	if m.gh == nil || m.opts.NoWatch {
		b.WriteString("\n")
		return
	}

	b.WriteString("\n " + sHead.Render("Release workflows") + "  ")
	if len(m.watchRuns) == 0 {
		if m.stage == stDone {
			b.WriteString(sDim.Render("none were triggered by the tag") + "\n")
		} else {
			b.WriteString(m.spin.View() + sDim.Render(" waiting for runs to start…") + "\n")
		}
	} else {
		b.WriteString(badge(summarise(m.watchRuns)) + "\n")
		m.runLines(m.watchRuns, b)
	}
	if m.watchErr != "" {
		b.WriteString("    " + sDim.Render("(gh: "+m.watchErr+")") + "\n")
	}
	if m.newRelease != nil {
		b.WriteString("\n    " + sDim.Render(m.newRelease.URL) + "\n")
	} else if m.gh != nil {
		b.WriteString("\n    " + sDim.Render(m.gh.URL+"/actions") + "\n")
	}
	if m.stage == stWatching {
		b.WriteString("\n " + sDim.Render("q to stop watching (the workflows keep running)") + "\n")
	}
	b.WriteString("\n")
}

// ---- helpers ----

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
