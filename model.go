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
	NoBump    bool
}

type stage int

const (
	stLoading stage = iota
	stSelect
	stConfirm
	stRunning // running the plan's steps
	stWatching
	stDone
	stFailed // a step failed
	stError  // couldn't start at all
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

	root     string
	cfg      Config
	files    []*VersionFile
	fileErr  string
	goModule string

	err error
}

type stepMsg struct{ err error }

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

	root     string
	cfg      Config
	files    []*VersionFile
	fileErr  string
	goModule string // root go.mod's module path

	width  int // terminal width, for wrapping long messages
	cursor int
	note   string // transient message on the select screen
	plan   *Plan
	step   int
	undo   string // what rollback did after a failed step

	watchStart time.Time
	watchRuns  []Run
	newRelease *Release
	watchErr   string
	// no workflow in .github/workflows runs on tag pushes
	noTagWorkflows bool
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
		msg.root, _ = git("rev-parse", "--show-toplevel")
		if msg.cfg, err = loadConfig(msg.root); err != nil {
			return loadedMsg{err: err}
		}
		msg.goModule = goModule(msg.root)
		if !opts.NoBump {
			msg.files, err = findVersionFiles(msg.root, msg.cfg, repo.Latest, repo.HasLatest)
			if err != nil {
				msg.fileErr = err.Error()
			}
		}

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

// bumpFiles are the version files that will be rewritten.
func (m model) bumpFiles() []*VersionFile {
	var out []*VersionFile
	for _, f := range m.files {
		if f.Skip == "" {
			out = append(out, f)
		}
	}
	return out
}

func (m model) newPlan(kind string) *Plan {
	v := m.next(kind)
	p := &Plan{
		Tag:     v.String(),
		Version: plain(v),
		Root:    m.root,
		Remote:  m.opts.Remote,
		Branch:  m.repo.Branch,
		Push:    !m.opts.NoPush,
	}
	if !m.opts.NoBump {
		// Files already at this version (a re-run after a failed tag
		// push) need no commit.
		for _, f := range m.bumpFiles() {
			if f.Current != p.Version {
				p.Files = append(p.Files, f)
			}
		}
		p.Command = m.cfg.Command
		p.Message = m.cfg.commitMessage(p.Version)
	}
	p.build()
	return p
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
		m.root, m.cfg, m.files, m.fileErr = msg.root, msg.cfg, msg.files, msg.fileErr
		m.goModule = msg.goModule
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

	case stepMsg:
		if msg.err != nil {
			m.err = msg.err
			m.undo = m.plan.rollback()
			m.stage = stFailed
			return m, tea.Quit
		}
		m.step++
		if m.step < len(m.plan.steps) {
			return m, m.runStep()
		}
		if m.opts.NoPush || m.opts.NoWatch || m.gh == nil {
			m.stage = stDone
			return m, tea.Quit
		}
		if any, known := tagWorkflows(m.root); known && !any {
			m.noTagWorkflows = true
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

	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// wrap renders a message line, wrapping it to the terminal so nothing is
// cut off. Continuation lines are indented to line up with the text.
func (m model) wrap(style lipgloss.Style, icon, text string, indent int) string {
	pad := strings.Repeat(" ", indent)
	if m.width <= indent+len(icon)+10 {
		return pad + style.Render(icon+text) + "\n"
	}
	body := lipgloss.NewStyle().Width(m.width - indent - len([]rune(icon)) - 1).Render(text)
	ls := strings.Split(body, "\n")
	var b strings.Builder
	for i, l := range ls {
		lead := icon
		if i > 0 {
			lead = strings.Repeat(" ", len([]rune(icon)))
		}
		b.WriteString(pad + style.Render(lead+strings.TrimRight(l, " ")) + "\n")
	}
	return b.String()
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.stage {
	case stSelect:
		m.note = ""
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
			p := m.newPlan(bumpKinds[m.cursor])
			if tagExists(p.Tag) {
				m.note = "tag " + p.Tag + " already exists"
				return m, nil
			}
			if b := m.blockers(p); len(b) > 0 {
				m.note = "can't release yet: " + b[0]
				return m, nil
			}
			m.plan = p
			m.stage = stConfirm
		}
	case stConfirm:
		switch k {
		case "y", "Y", "enter":
			if m.opts.DryRun {
				m.stage = stDone
				return m, tea.Quit
			}
			m.stage, m.step = stRunning, 0
			return m, m.runStep()
		case "n", "N", "esc", "backspace":
			m.stage = stSelect
		case "q":
			return m, tea.Quit
		}
	case stWatching, stDone, stFailed, stError:
		if k == "q" || k == "esc" || k == "enter" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) runStep() tea.Cmd {
	p, s := m.plan, m.plan.steps[m.step]
	return func() tea.Msg { return stepMsg{s.Run(p)} }
}

func (m model) watch() tea.Cmd {
	sha, tag := m.plan.Head, m.plan.Tag
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

// blockers stop a release that would go wrong. Committing a version bump
// needs a clean tree and a branch that can be pushed.
func (m model) blockers(p *Plan) []string {
	var b []string
	if m.fileErr != "" {
		b = append(b, m.fileErr)
	}
	if next, ok := parseTag(p.Tag); ok {
		if problem := goMajorProblem(m.goModule, next); problem != "" {
			b = append(b, problem)
		}
	}
	if !p.bumping() {
		return b
	}
	if next, ok := parseTag(p.Version); ok {
		for _, f := range p.Files {
			if cur, ok := parseTag(f.Current); ok && next.less(cur) {
				b = append(b, fmt.Sprintf("%s is already %s, newer than %s; pick a bigger bump", f.Path, f.Current, p.Version))
			}
		}
	}
	r := m.repo
	if r.Dirty {
		b = append(b, "uncommitted changes; commit or stash them, or run with --no-bump")
	}
	if r.Branch == "HEAD" {
		b = append(b, "HEAD is detached, so there's no branch to commit the version bump to")
	}
	if r.Behind > 0 && p.Push {
		b = append(b, fmt.Sprintf("%d commit(s) behind upstream; pull first", r.Behind))
	}
	return b
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
		w = append(w, fmt.Sprintf("%d commit(s) not pushed yet", r.Ahead))
	}
	if !r.HasUpstream && r.Branch != "HEAD" {
		w = append(w, "branch has no upstream")
	}
	if r.Dirty {
		w = append(w, "uncommitted changes")
	}
	if r.HasLatest && r.Unreleased == 0 {
		w = append(w, "HEAD is already released as "+r.LatestTag)
	}
	if summarise(m.headRuns) == StateFail {
		w = append(w, "checks on HEAD are failing")
	}
	if m.fileErr != "" {
		w = append(w, m.fileErr)
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
	case stRunning, stWatching, stDone, stFailed:
		if m.plan != nil && !(m.stage == stDone && m.opts.DryRun) {
			m.renderSteps(&b)
			if m.stage != stRunning {
				m.renderResult(&b)
			}
			return b.String()
		}
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

	// Version files
	if !m.opts.NoBump {
		b.WriteString("\n " + sHead.Render("Version files") + "\n")
		width := 0
		for _, f := range m.files {
			width = max(width, len(f.Path))
		}
		for _, f := range m.files {
			if f.Skip != "" {
				fmt.Fprintf(&b, "   %s\n", sDim.Render(fmt.Sprintf("%-*s  %s  skipped: %s", width, f.Path, f.Current, f.Skip)))
			} else {
				fmt.Fprintf(&b, "   %-*s  %s\n", width, f.Path, f.Current)
			}
		}
		if m.cfg.Command != "" {
			b.WriteString("   " + sDim.Render("then runs: "+m.cfg.Command) + "\n")
		}
		if note := m.noFilesNote(); note != "" {
			b.WriteString(m.wrap(sDim, "", note, 3))
		}
	}

	if m.ghNote != "" {
		b.WriteString("\n" + m.wrap(sDim, "ℹ ", m.ghNote, 1))
	}
	if w := m.warnings(); len(w) > 0 {
		b.WriteString("\n")
		for _, s := range w {
			b.WriteString(m.wrap(sYellow, "⚠ ", s, 1))
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
			if goMajorProblem(m.goModule, m.next(k)) != "" {
				arrow += sYellow.Render("  ⚠ not valid for this Go module")
			}
			if i == m.cursor {
				fmt.Fprintf(&b, " %s %s  %s\n", sSel.Render("❯"), sSel.Render(fmt.Sprintf("%-6s", k)), arrow)
			} else {
				fmt.Fprintf(&b, "   %-6s  %s\n", k, arrow)
			}
		}
		if m.note != "" {
			b.WriteString("\n" + m.wrap(sRed, "✗ ", m.note, 1))
		}
		b.WriteString("\n " + sDim.Render("↑/↓ choose · enter select · r refresh · q quit") + "\n")

	case stConfirm:
		b.WriteString(" " + sHead.Render("Release "+m.plan.Tag+"?") + "\n")
		for i, s := range m.plan.steps {
			fmt.Fprintf(&b, "   %d. %s\n", i+1, s.Title)
		}
		b.WriteString("\n " + sDim.Render("y/enter go · n/esc back") + "\n")

	case stDone: // dry run
		b.WriteString(" " + sYellow.Render("Dry run: nothing was changed. These steps would have run:") + "\n")
		for i, s := range m.plan.steps {
			fmt.Fprintf(&b, "   %d. %s\n", i+1, s.Title)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m model) renderSteps(b *strings.Builder) {
	b.WriteString(" " + sHead.Render("Releasing "+m.plan.Tag) + "\n")
	for i, s := range m.plan.steps {
		var mark string
		switch {
		case i < m.step:
			mark = sGreen.Render("✓")
		case i == m.step && m.stage == stRunning:
			mark = m.spin.View()
		case i == m.step && m.stage == stFailed:
			mark = sRed.Render("✗")
		default:
			mark = sDim.Render("·")
		}
		fmt.Fprintf(b, "   %s %s\n", mark, s.Title)
	}
	b.WriteString("\n")
}

func (m model) renderResult(b *strings.Builder) {
	if m.stage == stFailed {
		b.WriteString(m.wrap(sRed, "✗ ", m.err.Error(), 1))
		if m.undo != "" {
			b.WriteString(m.wrap(sDim, "", m.undo, 3))
		}
		b.WriteString("\n")
		return
	}
	if m.opts.NoPush {
		b.WriteString(" " + sGreen.Render("✓ Created "+m.plan.Tag+" locally") + "\n")
		push := fmt.Sprintf("git push %s %s", m.opts.Remote, m.plan.Tag)
		if m.plan.committed {
			push = fmt.Sprintf("git push %s %s %s", m.opts.Remote, m.plan.Branch, m.plan.Tag)
		}
		b.WriteString("   " + sDim.Render("push with: "+push) + "\n\n")
		return
	}
	if m.gh == nil || m.opts.NoWatch || m.noTagWorkflows {
		b.WriteString(" " + sGreen.Render("✓ Released "+m.plan.Tag) + "\n")
		if m.noTagWorkflows {
			b.WriteString("   " + sDim.Render("no workflow in .github/workflows runs on tag pushes, so there's nothing to watch") + "\n")
		}
		b.WriteString("\n")
		return
	}

	b.WriteString(" " + sHead.Render("Release workflows") + "  ")
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
	} else {
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
