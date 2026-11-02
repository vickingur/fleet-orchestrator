package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const narrow = 72 // below this width the list and details stack

// View renders the whole screen in the alternate buffer.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) bodyHeight() int { return max(m.h-4, 1) }

func (m Model) listWidth() int {
	if m.w < narrow {
		return m.w
	}
	return min(34, m.w/3)
}

func (m Model) detailWidth() int {
	if m.w < narrow {
		return m.w
	}
	return m.w - m.listWidth() - 3
}

// layout sizes the scrollable panes after a resize.
func (m *Model) layout() {
	bh := m.bodyHeight()
	m.feed.SetWidth(m.w)
	m.feed.SetHeight(bh)
	m.guide.SetWidth(m.w)
	m.guide.SetHeight(bh)
	m.out.SetWidth(m.detailWidth())
	m.out.SetHeight(max(bh-2, 1))
	// Leave room on the footer line for the prompt label and key hints.
	m.input.SetWidth(max(min(m.w-70, 40), 10))
	m.renderFeed()
}

func (m Model) render() string {
	if m.w == 0 {
		return ""
	}
	rule := m.st.rule.Render(strings.Repeat("─", m.w))
	var body string
	switch m.tab {
	case tabActivity:
		body = m.feedView()
	case tabGuide:
		if m.guideErr != "" {
			body = m.st.bad.Render(m.guideErr)
		} else {
			body = m.guide.View()
		}
	default:
		body = m.nodesView()
	}
	return strings.Join([]string{m.header(), rule, fit(body, m.w, m.bodyHeight()), rule, m.footer()}, "\n")
}

// fit pads or cuts s to exactly h lines, each at most w cells wide.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

var (
	fleetFlavor  = []string{"asking the tailnet", "reading fleet.toml", "counting machines"}
	healthFlavor = []string{"knocking on %s", "asking %s for versions", "checking %s's tools"}
	planFlavor   = []string{"building fleet for each platform", "comparing shared layers", "diffing settings and skills", "checking plugins"}
	busyFlavor   = []string{"working", "still working", "waiting on the node", "almost there, probably"}
)

func (m Model) loading(text string) string {
	return m.st.accent.Render(m.spin.View()) + " " + m.st.faint.Render(text+"…")
}

func pick(list []string, i int) string { return list[i%len(list)] }

func (m Model) header() string {
	left := m.st.title.Render("fleet")
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if tab(i) == m.tab {
			left += "   " + m.st.bold.Render(label)
		} else {
			left += "   " + m.st.faint.Render(label)
		}
	}
	var right string
	switch {
	case m.fleetErr != "":
		right = m.st.bad.Render("tailnet unavailable")
	case m.fleet == nil:
		right = m.loading(pick(fleetFlavor, m.flavor))
	default:
		online := 0
		for _, n := range m.fleet.Nodes {
			if n.State == "online" || n.State == "this machine" {
				online++
			}
		}
		right = m.st.faint.Render(fmt.Sprintf("%d of %d online", online, len(m.fleet.Nodes)))
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return ansi.Truncate(left, m.w, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// --- nodes -------------------------------------------------------------------

func (m Model) nodesView() string {
	if m.fleetErr != "" {
		return m.st.bad.Render("Could not read the fleet: "+m.fleetErr) + "\n\n" + m.st.faint.Render("Press r to retry.")
	}
	if m.fleet == nil {
		return m.loading(pick(fleetFlavor, m.flavor))
	}
	right := m.detailView()
	if m.mode == modeOutput {
		right = m.outputView()
	}
	if m.w < narrow {
		it, _ := m.selected()
		pos := m.st.faint.Render(fmt.Sprintf("  %d/%d  ↑↓", m.cursor+1, len(m.items())))
		return m.st.bold.Render(it.target()) + pos + "\n\n" + right
	}
	list := strings.Split(m.listView(), "\n")
	detail := strings.Split(right, "\n")
	lw := m.listWidth()
	sep := m.st.rule.Render(" │ ")
	var b strings.Builder
	for i := 0; i < m.bodyHeight(); i++ {
		var l, r string
		if i < len(list) {
			l = list[i]
		}
		if i < len(detail) {
			r = detail[i]
		}
		b.WriteString(pad(l, lw) + sep + ansi.Truncate(r, m.detailWidth(), "…") + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func shortState(state string) string {
	if strings.HasPrefix(state, "offline") {
		return "offline"
	}
	return state
}

func (m Model) glyph(n Node) string {
	switch {
	case n.State == "online" || n.State == "this machine":
		return m.st.ok.Render("●")
	case strings.HasPrefix(n.State, "offline"):
		return m.st.warn.Render("●")
	default:
		return m.st.faint.Render("○")
	}
}

func (m Model) listView() string {
	var b strings.Builder
	lw := m.listWidth()
	for i, it := range m.items() {
		marker := "  "
		if i == m.cursor {
			marker = m.st.accent.Render("› ")
		}
		var line string
		if it.agent == "" {
			n := m.node(it.node)
			name := it.node
			if i == m.cursor {
				name = m.st.bold.Render(name)
			}
			line = marker + m.glyph(n) + " " + pad(name, 12) + " " + m.st.faint.Render(shortState(n.State))
		} else {
			c := m.container(it)
			g := m.st.faint.Render("○")
			if c.State == "running" {
				g = m.st.ok.Render("●")
			}
			name := it.agent
			if i == m.cursor {
				name = m.st.bold.Render(name)
			}
			line = marker + m.st.faint.Render("└ ") + g + " " + pad(name, 10) + " " + m.st.faint.Render(c.State)
		}
		b.WriteString(ansi.Truncate(line, lw, "…") + "\n")
	}
	if m.loadingContainers {
		b.WriteString("\n" + m.loading("listing agent containers") + "\n")
	} else if m.containersErr != "" {
		b.WriteString("\n" + m.st.warn.Render("containers: "+m.containersErr) + "\n")
	}
	if m.fleet.TailnetError != "" {
		b.WriteString("\n" + m.st.warn.Render("tailnet: "+m.fleet.TailnetError) + "\n")
	}
	return b.String()
}

func row(st styles, label, value string) string {
	return st.faint.Render(pad(label, 14)) + value
}

func (m Model) detailView() string {
	it, ok := m.selected()
	if !ok {
		return m.st.faint.Render("No nodes in fleet.toml.")
	}
	if it.agent != "" {
		return m.agentDetail(it)
	}
	n := m.node(it.node)
	var lines []string
	head := m.st.bold.Render(n.Name) + "  " + m.glyph(n) + " " + shortState(n.State)
	if n.Address != "" {
		head += m.st.faint.Render("  " + n.Address)
	}
	lines = append(lines, head)
	meta := []string{n.OS, n.Role, "host " + n.Host}
	if n.Runtime != "" {
		meta = append(meta, "containers via "+n.Runtime)
	}
	lines = append(lines, m.st.faint.Render(strings.Join(meta, " · ")), "")

	switch {
	case !reachable(n):
		lines = append(lines, m.st.faint.Render("Not on the tailnet. Run tailscale up on that machine, then press r."))
		return strings.Join(lines, "\n")
	case m.loadingHealth[n.Name]:
		lines = append(lines, m.loading(fmt.Sprintf(pick(healthFlavor, m.flavor), n.Name)))
	case m.healthErr[n.Name] != "":
		lines = append(lines, m.st.bad.Render(m.healthErr[n.Name]))
	default:
		h := m.health[n.Name]
		if h.Reach != "ok" {
			lines = append(lines, row(m.st, "reach", m.st.bad.Render("unreachable")), m.st.faint.Render(h.Error))
			break
		}
		fleet := m.st.ok.Render(h.Fleet + " ✓ current")
		switch {
		case h.Fleet == "missing":
			fleet = m.st.warn.Render("not installed · press p to push")
		case !h.Current:
			fleet = m.st.warn.Render(h.Fleet + " · stale, press p to push")
		}
		lines = append(lines, row(m.st, "fleet", fleet))
		names := make([]string, 0, len(h.Tools))
		for name := range h.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := h.Tools[name]
			if v == "missing" {
				v = m.st.warn.Render("missing")
			}
			lines = append(lines, row(m.st, name, v))
		}
		lines = append(lines, row(m.st, "platform", h.Platform))
	}

	lines = append(lines, "", row(m.st, "shared layer", m.planSummary(n.Name)))
	if n.Runtime != "" {
		running, total := 0, 0
		for _, c := range m.containers {
			if c.Node == n.Name && c.Agent != "" {
				total++
				if c.State == "running" {
					running++
				}
			}
		}
		summary := fmt.Sprintf("%d running of %d", running, total)
		if total == 0 {
			summary = "none yet · press n to start one"
		}
		lines = append(lines, row(m.st, "agents", summary))
	}
	if problems := m.problems(n.Name); len(problems) > 0 && !m.loadingHealth[n.Name] && !m.loadingPlans {
		lines = append(lines, "", m.st.warn.Render(fmt.Sprintf("%d problem(s)", len(problems))))
		for i, p := range problems {
			if i == 4 {
				lines = append(lines, m.st.faint.Render(fmt.Sprintf("  … and %d more", len(problems)-4)))
				break
			}
			lines = append(lines, m.st.faint.Render("  · "+p))
		}
	}
	lines = append(lines, "", m.actions(n, it))
	return strings.Join(lines, "\n")
}

func (m Model) planSummary(node string) string {
	if !m.b.IsController() {
		return m.st.faint.Render("checked from the controller")
	}
	if m.loadingPlans {
		return m.loading(pick(planFlavor, m.flavor))
	}
	if m.plansErr != "" {
		return m.st.bad.Render(m.plansErr)
	}
	r, ok := m.plans[node]
	switch {
	case !ok:
		return m.st.faint.Render("not checked")
	case r.Error != "":
		return m.st.bad.Render(r.Error)
	case r.Plan == nil:
		return m.st.bad.Render("unreadable result · press d")
	}
	p := r.Plan
	if c := p.Counts["conflict"]; c > 0 {
		return m.st.bad.Render(fmt.Sprintf("%d conflict(s) · press d", c))
	}
	pending := 0
	for k, v := range p.Counts {
		if k != "ok" {
			pending += v
		}
	}
	ensures, met := len(p.Ensures), 0
	for _, e := range p.Ensures {
		if e.Satisfied {
			met++
		}
	}
	if pending > 0 || met < ensures {
		return m.st.warn.Render(fmt.Sprintf("%d change(s) pending · press d", pending+ensures-met))
	}
	return m.st.ok.Render(fmt.Sprintf("in sync · %d files · %d/%d ensures", p.Counts["ok"], met, ensures))
}

func (m Model) key(k, label string) string {
	return m.st.accent.Render(k) + " " + m.st.faint.Render(label)
}

func (m Model) actions(n Node, it item) string {
	parts := []string{m.key("enter", "shell"), m.key("x", "run")}
	if m.b.IsController() && it.agent == "" {
		parts = append(parts, m.key("d", "diff"), m.key("p", "push"))
	}
	if n.Runtime != "" && it.agent == "" {
		parts = append(parts, m.key("n", "new agent"))
	}
	if it.agent != "" {
		parts = append(parts, m.key("D", "remove"))
	}
	parts = append(parts, m.key("F", "fix with an agent"))
	return strings.Join(parts, "   ")
}

func (m Model) agentDetail(it item) string {
	c := m.container(it)
	n := m.node(it.node)
	g := m.st.faint.Render("○")
	if c.State == "running" {
		g = m.st.ok.Render("●")
	}
	lines := []string{
		m.st.bold.Render(it.target()) + "  " + g + " " + c.State,
		m.st.faint.Render("agent container on " + it.node),
		"",
		row(m.st, "status", c.Status),
		row(m.st, "image", c.Image),
		"",
		m.actions(n, it),
	}
	return strings.Join(lines, "\n")
}

func (m Model) outputView() string {
	title := m.st.bold.Render(m.outTitle)
	if m.busy != "" {
		return title + "\n\n" + m.loading(pick(busyFlavor, m.flavor))
	}
	return title + "\n\n" + m.out.View()
}

// --- activity and guide ----------------------------------------------------------

func (m Model) feedView() string {
	if m.activityErr != "" {
		return m.st.bad.Render(m.activityErr)
	}
	if len(m.activity) == 0 {
		return m.st.faint.Render("Nothing yet. Every remote action from this machine shows up here.")
	}
	return m.feed.View()
}

func (m *Model) renderFeed() {
	var b strings.Builder
	for _, r := range m.activity {
		status := m.st.ok.Render(fmt.Sprintf("exit %d", r.ExitCode))
		switch {
		case r.Error != "":
			status = m.st.bad.Render("error ")
		case r.ExitCode != 0:
			status = m.st.warn.Render(fmt.Sprintf("exit %d", r.ExitCode))
		}
		line := m.st.faint.Render(r.Time.Local().Format("Jan 02 15:04:05")) + "  " +
			pad(r.Actor, 14) + pad(r.Command, 13) + pad(r.Target, 18) + pad(status, 10) +
			m.st.faint.Render(pad(fmt.Sprintf("%dms", r.Millis), 9)) +
			m.st.faint.Render(strings.ReplaceAll(r.Detail, "\n", " ⏎ "))
		b.WriteString(ansi.Truncate(line, max(m.w, 20), "…") + "\n")
	}
	m.feed.SetContent(b.String())
}

func (m Model) renderGuide(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") {
			line = m.st.title.Render(strings.TrimLeft(line, "# "))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// --- footer ----------------------------------------------------------------------

func (m Model) footer() string {
	switch m.mode {
	case modePrompt:
		it, _ := m.selected()
		if m.prompt == promptDelegate {
			n := len(m.problems(m.confirmTarget.node))
			found := m.st.faint.Render(fmt.Sprintf("%d problem(s) detected", n))
			if m.flash != "" {
				found = m.st.warn.Render(m.flash)
			}
			return m.st.bold.Render("fix "+m.confirmTarget.target()+" with ") + m.st.accent.Render(agentNames[m.agent]) +
				m.st.bold.Render(" › ") + m.input.View() + "  " + found + "   " + m.key("tab", "switch agent") + "  " + m.key("esc", "cancel")
		}
		if m.prompt == promptSpawn {
			tailnet := m.st.faint.Render("off")
			if m.spawnTailnet {
				tailnet = m.st.ok.Render("on")
			}
			return m.st.bold.Render("new agent on "+m.confirmTarget.node+" › ") + m.input.View() +
				"  " + m.st.faint.Render("tailnet ") + tailnet + "   " + m.key("tab", "toggle") + "  " + m.key("esc", "cancel")
		}
		return m.st.bold.Render("run on "+it.target()+" › ") + m.input.View() + "  " + m.key("esc", "cancel")
	case modeConfirm:
		node := m.confirmTarget.node
		n := len(m.conflicts(node))
		q := "Push the shared layer to " + node + "?"
		switch m.confirm {
		case confirmRemove:
			q = "Remove agent " + m.confirmTarget.target() + "? Its volumes are kept."
		case confirmTakeShared:
			q = fmt.Sprintf("Overwrite %d edited file(s) on %s with the shared version? They are backed up there.", n, node)
		case confirmKeepNode:
			q = fmt.Sprintf("Copy %s's version of %d file(s) into the fleet repo?", node, n)
		}
		return m.st.warn.Render(q) + "   " + m.key("y", "yes") + "  " + m.key("n", "no")
	case modeOutput:
		if m.busy == "" && len(m.conflicts(m.planNode)) > 0 {
			return m.key("f", "take shared version") + "   " + m.key("a", "keep "+m.planNode+"'s version") + "   " +
				m.key("↑↓", "scroll") + "   " + m.key("esc", "back")
		}
		return m.key("↑↓", "scroll") + "   " + m.key("esc", "back")
	}
	if m.flash != "" {
		return m.st.warn.Render(m.flash)
	}
	if m.tab != tabNodes {
		return ansi.Truncate(strings.Join([]string{m.key("↑↓", "scroll"), m.key("tab", "views"), m.key("r", "refresh"), m.key("q", "quit")}, "   "), m.w, "…")
	}
	return ansi.Truncate(strings.Join([]string{m.key("↑↓", "select"), m.key("enter", "shell"), m.key("x", "run"),
		m.key("tab", "views"), m.key("r", "refresh"), m.key("q", "quit")}, "   "), m.w, "…")
}
