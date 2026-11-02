package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/vickingur/fleet-orchestrator/internal/audit"
)

type tab int

const (
	tabNodes tab = iota
	tabActivity
	tabGuide
)

var tabNames = []string{"Nodes", "Activity", "Guide"}

type mode int

const (
	modeBrowse mode = iota
	modePrompt
	modeConfirm
	modeOutput
)

type promptKind int

const (
	promptExec promptKind = iota
	promptSpawn
	promptDelegate
)

type confirmKind int

const (
	confirmPush confirmKind = iota
	confirmRemove
	confirmTakeShared
	confirmKeepNode
)

// item is one selectable row: a node, or an agent container on a node.
type item struct {
	node  string
	agent string
}

func (i item) target() string {
	if i.agent == "" {
		return i.node
	}
	return i.node + ":" + i.agent
}

// Model is the TUI state. All loading happens in commands; Update only folds
// results in, so the interface never blocks on the network.
type Model struct {
	b   Backend
	ctx context.Context

	w, h int
	st   styles
	tab  tab

	fleet        *Fleet
	fleetErr     string
	loadingFleet bool

	health        map[string]Health
	healthErr     map[string]string
	loadingHealth map[string]bool

	plans        map[string]Sync
	plansErr     string
	loadingPlans bool

	containers        []Container
	containersErr     string
	loadingContainers bool

	cursor int
	// sel is the selected row's target, so the selection follows the item
	// (not its index) when rows appear or disappear.
	sel  string
	mode mode

	prompt       promptKind
	input        textinput.Model
	spawnTailnet bool
	// agent is who fixes are delegated to; remembered across sessions.
	agent string

	confirm       confirmKind
	confirmTarget item

	out      viewport.Model
	outTitle string
	busy     string
	// planNode is set while the output shows that node's push preview, which
	// is where its conflicts can be resolved.
	planNode string

	activity    []audit.Record
	activityErr string
	feed        viewport.Model

	guide    viewport.Model
	guideErr string

	spin   spinner.Model
	flavor int
	flash  string
}

// New builds the model over a backend.
func New(ctx context.Context, b Backend) Model {
	in := textinput.New()
	in.Prompt = ""
	out := viewport.New()
	out.SoftWrap = true // command output must never lose its tail to the pane edge
	return Model{
		b: b, ctx: ctx, st: newStyles(true),
		health: map[string]Health{}, healthErr: map[string]string{}, loadingHealth: map[string]bool{},
		plans:        map[string]Sync{},
		loadingFleet: true, loadingContainers: true,
		input: in,
		out:   out, feed: viewport.New(), guide: viewport.New(),
		spin: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

// --- messages ----------------------------------------------------------------

type fleetMsg struct {
	f   Fleet
	err error
}

type healthMsg struct {
	node string
	h    Health
	err  error
}

type containersMsg struct {
	cs  []Container
	err error
}

type plansMsg struct {
	rs  []Sync
	err error
}

type actionMsg struct {
	title   string
	body    string
	err     error
	refresh bool
}

type shellDoneMsg struct{ err error }

type delegateDoneMsg struct {
	agent string
	err   error
}

type activityMsg struct {
	rs  []audit.Record
	err error
}

type guideMsg struct {
	text string
	err  error
}

type flavorMsg struct{}

// --- commands ----------------------------------------------------------------

func (m Model) loadFleet() tea.Cmd {
	return func() tea.Msg { f, err := m.b.Fleet(m.ctx); return fleetMsg{f, err} }
}

func (m Model) loadHealth(node string) tea.Cmd {
	return func() tea.Msg { h, err := m.b.Health(m.ctx, node); return healthMsg{node, h, err} }
}

func (m Model) loadContainers() tea.Cmd {
	return func() tea.Msg { cs, err := m.b.Containers(m.ctx); return containersMsg{cs, err} }
}

func (m Model) loadPlans(nodes []string) tea.Cmd {
	return func() tea.Msg { rs, err := m.b.Drift(m.ctx, nodes); return plansMsg{rs, err} }
}

func (m Model) loadActivity() tea.Cmd {
	return func() tea.Msg { rs, err := m.b.Activity(200); return activityMsg{rs, err} }
}

func (m Model) loadGuide() tea.Cmd {
	return func() tea.Msg { t, err := m.b.Guide(); return guideMsg{t, err} }
}

func flavorTick() tea.Cmd {
	return tea.Tick(1600*time.Millisecond, func(time.Time) tea.Msg { return flavorMsg{} })
}

// Init starts every load at once; results arrive independently.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadFleet(), m.loadContainers(), m.loadActivity(), m.loadGuide(),
		m.spin.Tick, flavorTick(), tea.RequestBackgroundColor)
}

func (m *Model) refreshCmd() tea.Cmd {
	m.loadingFleet, m.loadingContainers = true, true
	return tea.Batch(m.loadFleet(), m.loadContainers(), m.loadActivity())
}

// --- derived state -------------------------------------------------------------

func (m Model) items() []item {
	if m.fleet == nil {
		return nil
	}
	var out []item
	for _, n := range m.fleet.Nodes {
		out = append(out, item{node: n.Name})
		for _, c := range m.containers {
			if c.Node == n.Name && c.Agent != "" {
				out = append(out, item{node: n.Name, agent: c.Agent})
			}
		}
	}
	return out
}

func (m Model) selected() (item, bool) {
	items := m.items()
	if m.cursor < 0 || m.cursor >= len(items) {
		return item{}, false
	}
	return items[m.cursor], true
}

// node returns the named node; rows only reference listed nodes.
func (m Model) node(name string) Node {
	if m.fleet != nil {
		for _, n := range m.fleet.Nodes {
			if n.Name == name {
				return n
			}
		}
	}
	return Node{Name: name}
}

func (m Model) container(it item) Container {
	for _, c := range m.containers {
		if c.Node == it.node && c.Agent == it.agent {
			return c
		}
	}
	return Container{Node: it.node, Agent: it.agent}
}

func reachable(n Node) bool { return n.State != "not on tailnet" }

// --- update --------------------------------------------------------------------

// Update folds one message into the model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.BackgroundColorMsg:
		m.st = newStyles(msg.IsDark())
		m.renderFeed()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case flavorMsg:
		m.flavor++
		return m, flavorTick()
	case fleetMsg:
		return m.onFleet(msg)
	case healthMsg:
		delete(m.loadingHealth, msg.node)
		if msg.err != nil {
			m.healthErr[msg.node] = msg.err.Error()
		} else {
			delete(m.healthErr, msg.node)
			m.health[msg.node] = msg.h
		}
		return m, nil
	case containersMsg:
		m.loadingContainers = false
		m.containers, m.containersErr = msg.cs, ""
		if msg.err != nil {
			m.containersErr = msg.err.Error()
		}
		m.reselect()
		return m, nil
	case plansMsg:
		m.loadingPlans = false
		m.plansErr = ""
		if msg.err != nil {
			m.plansErr = msg.err.Error()
		}
		for _, r := range msg.rs {
			m.plans[r.Node] = r
		}
		return m, nil
	case activityMsg:
		m.activity, m.activityErr = msg.rs, ""
		if msg.err != nil {
			m.activityErr = msg.err.Error()
		}
		m.renderFeed()
		return m, nil
	case guideMsg:
		if msg.err != nil {
			m.guideErr = msg.err.Error()
		} else {
			m.guide.SetContent(m.renderGuide(msg.text))
		}
		return m, nil
	case actionMsg:
		m.busy = ""
		body := msg.body
		if msg.err != nil && body == "" {
			body = msg.err.Error()
		}
		m.outTitle = msg.title
		m.out.SetContent(body)
		m.out.GotoTop()
		if m.mode != modeOutput {
			m.flash = "finished: " + msg.title + " · press o to see the output"
		}
		if msg.refresh {
			return m, m.refreshCmd()
		}
		return m, m.loadActivity()
	case shellDoneMsg:
		if msg.err != nil {
			m.flash = "session ended: " + msg.err.Error()
		}
		return m, m.loadActivity()
	case delegateDoneMsg:
		m.flash = "back from " + agentNames[msg.agent] + " · refreshing to show what changed"
		if msg.err != nil {
			m.flash = agentNames[msg.agent] + " exited: " + msg.err.Error()
		}
		return m, m.refreshCmd()
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) onFleet(msg fleetMsg) (tea.Model, tea.Cmd) {
	m.loadingFleet = false
	if msg.err != nil {
		m.fleetErr = msg.err.Error()
		return m, nil
	}
	m.fleetErr = ""
	f := msg.f
	m.fleet = &f
	m.reselect()
	var cmds []tea.Cmd
	var drift []string
	for _, n := range f.Nodes {
		if !reachable(n) {
			continue
		}
		m.loadingHealth[n.Name] = true
		cmds = append(cmds, m.loadHealth(n.Name))
		drift = append(drift, n.Name)
	}
	if m.b.IsController() && len(drift) > 0 {
		m.loadingPlans = true
		cmds = append(cmds, m.loadPlans(drift))
	}
	return m, tea.Batch(cmds...)
}

// reselect puts the cursor back on the selected item after the rows changed;
// if an agent row vanished (removed), its host is selected instead.
func (m *Model) reselect() {
	items := m.items()
	find := func(target string) int {
		for i, it := range items {
			if it.target() == target {
				return i
			}
		}
		return -1
	}
	i := find(m.sel)
	if i < 0 {
		host, _, _ := strings.Cut(m.sel, ":")
		i = find(host)
	}
	if i < 0 {
		i = min(m.cursor, max(len(items)-1, 0))
	}
	m.cursor = i
	if i < len(items) {
		m.sel = items[i].target()
	}
}

func (m *Model) move(delta int) {
	items := m.items()
	m.cursor = max(0, min(len(items)-1, m.cursor+delta))
	if m.cursor < len(items) {
		m.sel = items[m.cursor].target()
	}
}

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modePrompt:
		return m.onPromptKey(msg)
	case modeConfirm:
		return m.onConfirmKey(key)
	case modeOutput:
		switch key {
		case "esc", "q":
			m.mode = modeBrowse
			return m, nil
		case "f", "a":
			if m.busy == "" && len(m.conflicts(m.planNode)) > 0 {
				m.mode, m.confirmTarget = modeConfirm, item{node: m.planNode}
				m.confirm = confirmTakeShared
				if key == "a" {
					m.confirm = confirmKeepNode
				}
			}
			return m, nil
		case "tab", "shift+tab", "1", "2", "3":
			if m.busy == "" {
				m.mode = modeBrowse // switching views leaves the finished output
			}
		default:
			var cmd tea.Cmd
			m.out, cmd = m.out.Update(msg)
			return m, cmd
		}
	}
	if m.mode != modeBrowse {
		return m, nil
	}
	m.flash = ""
	switch key {
	case "q":
		return m, tea.Quit
	case "tab":
		m.tab = (m.tab + 1) % 3
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		return m, nil
	case "1", "2", "3":
		m.tab = tab(key[0] - '1')
		return m, nil
	case "r":
		m.flash = "refreshing"
		return m, m.refreshCmd()
	case "o":
		if m.outTitle != "" {
			m.mode, m.tab = modeOutput, tabNodes
		}
		return m, nil
	}
	if m.tab != tabNodes {
		var cmd tea.Cmd
		if m.tab == tabActivity {
			m.feed, cmd = m.feed.Update(msg)
		} else {
			m.guide, cmd = m.guide.Update(msg)
		}
		return m, cmd
	}
	return m.onNodesKey(key)
}

func (m Model) onNodesKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.move(-1)
		return m, nil
	case "down", "j":
		m.move(1)
		return m, nil
	}
	it, ok := m.selected()
	if !ok {
		return m, nil
	}
	n := m.node(it.node)
	switch key {
	case "enter", "s":
		if !reachable(n) {
			m.flash = it.node + " is not on the tailnet"
			return m, nil
		}
		cmd, err := m.b.Shell(it.target())
		if err != nil {
			m.flash = err.Error()
			return m, nil
		}
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return shellDoneMsg{err} })
	case "x":
		if !reachable(n) {
			m.flash = it.node + " is not on the tailnet"
			return m, nil
		}
		m.mode, m.prompt = modePrompt, promptExec
		m.input.SetValue("")
		m.input.Placeholder = "a shell command, e.g. uptime"
		return m, m.input.Focus()
	case "d":
		if it.agent != "" {
			return m, nil
		}
		return m.showPlan(it.node)
	case "p":
		if it.agent != "" {
			return m, nil
		}
		if !m.b.IsController() {
			m.flash = "push runs on the controller, with the fleet config and source"
			return m, nil
		}
		m.mode, m.confirm, m.confirmTarget = modeConfirm, confirmPush, it
		return m, nil
	case "n":
		if n.Runtime == "" {
			m.flash = it.node + " has no container runtime"
			return m, nil
		}
		m.mode, m.prompt, m.spawnTailnet = modePrompt, promptSpawn, false
		m.input.SetValue("")
		m.input.Placeholder = "agent name, e.g. builder"
		m.confirmTarget = item{node: it.node}
		return m, m.input.Focus()
	case "F":
		m.mode, m.prompt, m.flash = modePrompt, promptDelegate, ""
		if m.agent == "" {
			m.agent = m.b.Preference("delegate")
		}
		if _, ok := agentNames[m.agent]; !ok {
			m.agent = agents[0]
		}
		m.input.SetValue("")
		m.input.Placeholder = "optional: what else should it fix or check"
		m.confirmTarget = it
		return m, m.input.Focus()
	case "D", "delete":
		if it.agent == "" {
			return m, nil
		}
		m.mode, m.confirm, m.confirmTarget = modeConfirm, confirmRemove, it
		return m, nil
	}
	return m, nil
}

func (m Model) onPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode, m.flash = modeBrowse, ""
		m.input.Blur()
		return m, nil
	case "tab":
		switch m.prompt {
		case promptSpawn:
			m.spawnTailnet = !m.spawnTailnet
		case promptDelegate:
			for i, a := range agents {
				if a == m.agent {
					m.agent = agents[(i+1)%len(agents)]
					break
				}
			}
		}
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		if m.prompt == promptDelegate {
			return m.delegate(value)
		}
		if value == "" {
			return m, nil
		}
		m.input.Blur()
		if m.prompt == promptExec {
			it, _ := m.selected()
			return m.run("run on "+it.target()+": "+value, false, func() (string, error) {
				r, err := m.b.Exec(m.ctx, it.target(), value)
				return formatRun(r), err
			})
		}
		node, tailnet := m.confirmTarget.node, m.spawnTailnet
		return m.run("new agent "+node+":"+value, true, func() (string, error) {
			return m.b.Spawn(m.ctx, node, value, tailnet)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) onConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "enter":
		it := m.confirmTarget
		switch m.confirm {
		case confirmPush, confirmTakeShared:
			force := m.confirm == confirmTakeShared
			title := "push to " + it.node
			if force {
				title = "take the shared version on " + it.node
			}
			return m.run(title, true, func() (string, error) {
				r, err := m.b.Push(m.ctx, it.node, force)
				return formatSync(r), err
			})
		case confirmKeepNode:
			paths := m.conflicts(it.node)
			return m.run("keep "+it.node+"'s version", true, func() (string, error) {
				out, err := m.b.Adopt(m.ctx, it.node, paths)
				return out + "\n\nThe config now has " + it.node + "'s version. Review it with git diff in the fleet config, commit, then push to the other machines.", err
			})
		}
		return m.run("remove "+it.target(), true, func() (string, error) {
			out, err := m.b.Remove(m.ctx, it.node, it.agent)
			return out + "\n\nIts volumes are kept: spawn " + it.agent + " on " + it.node + " again to get its homes and logins back.", err
		})
	case "n", "esc", "q":
		m.mode = modeBrowse
		if m.planNode != "" {
			m.mode = modeOutput // back to the preview the question came from
		}
	}
	return m, nil
}

// conflicts lists the targets a push to node would leave untouched.
func (m Model) conflicts(node string) []string {
	r, ok := m.plans[node]
	if !ok || r.Plan == nil {
		return nil
	}
	var out []string
	for _, c := range r.Plan.Changes {
		if c.Action == "conflict" {
			out = append(out, c.Target)
		}
	}
	return out
}

// delegate hands the terminal to an interactive agent session seeded with the
// selected row's problems. With nothing detected and no note there is nothing
// to ask for, so nothing launches.
func (m Model) delegate(note string) (tea.Model, tea.Cmd) {
	it := m.confirmTarget
	problems := m.problems(it.node)
	if len(problems) == 0 && note == "" {
		m.flash = "nothing detected on " + it.node + " · type what you want done" // the prompt stays open
		return m, nil
	}
	m.flash = ""
	m.mode = modeBrowse
	m.input.Blur()
	if err := m.b.SetPreference("delegate", m.agent); err != nil {
		m.flash = "could not remember the agent choice: " + err.Error()
	}
	cmd, err := m.b.Delegate(m.agent, m.delegatePrompt(it.target(), note, problems, m.b.IsController()))
	if err != nil {
		m.flash = err.Error()
		return m, nil
	}
	agent := m.agent
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return delegateDoneMsg{agent, err} })
}

// run starts an action in the background and shows its output when done.
func (m Model) run(title string, refresh bool, fn func() (string, error)) (tea.Model, tea.Cmd) {
	m.mode, m.busy, m.outTitle, m.planNode = modeOutput, title, title, ""
	m.out.SetContent("")
	return m, func() tea.Msg {
		body, err := fn()
		if err != nil && body == "" {
			body = err.Error()
		}
		return actionMsg{title: title, body: body, err: err, refresh: refresh}
	}
}

func (m Model) showPlan(node string) (tea.Model, tea.Cmd) {
	if !m.b.IsController() {
		m.flash = "drift runs on the controller, with the fleet config and source"
		return m, nil
	}
	r, ok := m.plans[node]
	if !ok {
		m.flash = "no drift result for " + node + " yet"
		return m, nil
	}
	m.mode, m.outTitle, m.planNode = modeOutput, "what a push would change on "+node, node
	m.out.SetContent(formatSync(r))
	m.out.GotoTop()
	return m, nil
}

func formatRun(r RunResult) string {
	var b strings.Builder
	b.WriteString(r.Stdout)
	if r.Stderr != "" {
		b.WriteString(r.Stderr)
	}
	status := fmt.Sprintf("exit %d · %dms", r.ExitCode, r.Millis)
	if r.Error != "" {
		status = r.Error
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n" + status)
	return b.String()
}

func formatSync(r Sync) string {
	var b strings.Builder
	if r.Error != "" {
		b.WriteString(r.Error + "\n")
	}
	if r.Plan == nil {
		b.WriteString(r.Output)
		b.WriteString(r.Stderr)
		return b.String()
	}
	pending := 0
	for _, c := range r.Plan.Changes {
		if c.Action == "ok" {
			continue
		}
		pending++
		line := fmt.Sprintf("%-9s %s", c.Action, c.Target)
		if c.Detail != "" {
			line += "  (" + c.Detail + ")"
		}
		b.WriteString(line + "\n")
	}
	for _, e := range r.Plan.Ensures {
		switch {
		case e.Error != "":
			b.WriteString("failed    " + e.Name + "  (" + e.Error + ")\n")
		case e.Fixed:
			b.WriteString("ensured   " + e.Name + "\n")
		case !e.Satisfied:
			b.WriteString("ensure    " + e.Name + "\n")
		}
	}
	if pending == 0 && !r.Plan.Pending {
		b.WriteString("in sync: nothing to change\n")
	}
	if r.Stderr != "" {
		b.WriteString("\n" + r.Stderr)
	}
	return b.String()
}
