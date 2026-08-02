package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultTUIWidth  = 120
	defaultTUIHeight = 30
	wideTUIWidth     = 100
	horizontalStep   = 8
	refreshInterval  = 100 * time.Millisecond
)

type panel uint8

const (
	panelDAG panel = iota
	panelDetail
	panelTimeline
	panelCount
)

type scrollPosition struct {
	x int
	y int
}

type (
	refreshMsg       time.Time
	applyFinishedMsg struct{}
)

type TUIModel struct {
	projector  *Projector
	cancel     context.CancelFunc
	width      int
	height     int
	focus      panel
	selected   int
	scroll     [panelCount]scrollPosition
	collapsed  map[string]bool
	follow     bool
	autoSelect bool
	confirm    bool
}

func NewTUIModel(projector *Projector, cancel context.CancelFunc) TUIModel {
	model := TUIModel{
		projector: projector, cancel: cancel, width: defaultTUIWidth, height: defaultTUIHeight,
		collapsed: make(map[string]bool), follow: true, autoSelect: true,
	}
	model.selectCurrent()
	return model
}

func (m TUIModel) Init() tea.Cmd {
	return refreshCommand()
}

func refreshCommand() tea.Cmd {
	return tea.Tick(refreshInterval, func(now time.Time) tea.Msg { return refreshMsg(now) })
}

func (m TUIModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.ensureSelectedVisible()
		m.clampScroll()
		return m, tea.ClearScreen
	case refreshMsg:
		if m.autoSelect {
			m.selectCurrent()
		}
		m.ensureSelectedVisible()
		if m.follow {
			m.followTimeline()
		}
		return m, refreshCommand()
	case applyFinishedMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		return m.updateKey(message)
	default:
		return m, nil
	}
}

func (m TUIModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	name := key.String()
	if m.confirm && name != "q" && name != "Q" && name != "ctrl+c" {
		m.confirm = false
	}
	switch name {
	case "q", "Q", "ctrl+c":
		if !m.confirm {
			m.confirm = true
			return m, nil
		}
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "tab", "right":
		m.focus = (m.focus + 1) % panelCount
	case "shift+tab", "left":
		m.focus = (m.focus + panelCount - 1) % panelCount
	case "shift+right", "alt+right":
		m.scroll[m.focus].x += horizontalStep
	case "shift+left", "alt+left":
		m.scroll[m.focus].x -= horizontalStep
		if m.scroll[m.focus].x < 0 {
			m.scroll[m.focus].x = 0
		}
	case "down", "j":
		m.moveDown(1)
	case "up", "k":
		m.moveUp(1)
	case "pgdown", " ":
		m.moveDown(m.pageSize())
	case "pgup":
		m.moveUp(m.pageSize())
	case "home":
		m.moveHome()
	case "end":
		m.moveEnd()
	case "enter":
		m.toggleModule()
	case "f", "F":
		m.follow = !m.follow
	}
	m.ensureSelectedVisible()
	m.clampScroll()
	return m, nil
}

func (m *TUIModel) moveDown(amount int) {
	if m.focus == panelDAG {
		nodes := m.visibleNodes()
		previous := m.selected
		m.selected += amount
		if m.selected >= len(nodes) {
			m.selected = len(nodes) - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
		m.autoSelect = false
		if m.selected != previous {
			m.resetNodeViewports()
		}
		return
	}
	m.scroll[m.focus].y += amount
	m.follow = false
}

func (m *TUIModel) moveUp(amount int) {
	if m.focus == panelDAG {
		previous := m.selected
		m.selected -= amount
		if m.selected < 0 {
			m.selected = 0
		}
		m.autoSelect = false
		if m.selected != previous {
			m.resetNodeViewports()
		}
		return
	}
	m.scroll[m.focus].y -= amount
	if m.scroll[m.focus].y < 0 {
		m.scroll[m.focus].y = 0
	}
	m.follow = false
}

func (m *TUIModel) resetNodeViewports() {
	m.scroll[panelDetail] = scrollPosition{}
	m.scroll[panelTimeline] = scrollPosition{}
}

func (m *TUIModel) moveHome() {
	if m.focus == panelDAG {
		m.selected = 0
		m.autoSelect = false
		return
	}
	m.scroll[m.focus].y = 0
	m.follow = false
}

func (m *TUIModel) moveEnd() {
	if m.focus == panelDAG {
		nodes := m.visibleNodes()
		if len(nodes) > 0 {
			m.selected = len(nodes) - 1
		}
		m.autoSelect = false
		return
	}
	m.scroll[m.focus].y = 1 << 30
	m.follow = false
}

func (m *TUIModel) pageSize() int {
	size := m.bodyHeight() - 4
	if size < 1 {
		return 1
	}
	return size
}

func (m *TUIModel) toggleModule() {
	if m.focus != panelDAG {
		return
	}
	nodes := m.visibleNodes()
	if m.selected < 0 || m.selected >= len(nodes) || nodes[m.selected].Kind != "module" {
		return
	}
	address := nodes[m.selected].Address
	m.collapsed[address] = !m.collapsed[address]
}

func (m *TUIModel) selectCurrent() {
	nodes := m.visibleNodes()
	best := uint64(0)
	selected := m.selected
	for index, node := range nodes {
		if node.Status == StatusRunning && node.LastSequence >= best {
			best = node.LastSequence
			selected = index
		}
	}
	if len(nodes) == 0 {
		selected = 0
	} else if selected >= len(nodes) {
		selected = len(nodes) - 1
	}
	m.selected = selected
}

func (m *TUIModel) ensureSelectedVisible() {
	nodes := m.visibleNodes()
	if len(nodes) == 0 {
		m.selected = 0
		m.scroll[panelDAG].y = 0
		return
	}
	if m.selected >= len(nodes) {
		m.selected = len(nodes) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	visible := max(1, m.bodyHeight()-3)
	if m.selected < m.scroll[panelDAG].y {
		m.scroll[panelDAG].y = m.selected
	}
	if m.selected >= m.scroll[panelDAG].y+visible {
		m.scroll[panelDAG].y = m.selected - visible + 1
	}
}

func (m *TUIModel) followTimeline() {
	lines := strings.Split(m.timelineContent(), "\n")
	visible := max(1, m.bodyHeight()-4)
	offset := max(0, len(lines)-visible)
	m.scroll[panelTimeline].y = offset
}

func (m *TUIModel) clampScroll() {
	contents := [panelCount]string{m.dagContent(), m.detailContent(), m.timelineContent()}
	visible := max(1, m.bodyHeight()-3)
	for index := range m.scroll {
		if m.scroll[index].x < 0 {
			m.scroll[index].x = 0
		}
		if m.scroll[index].y < 0 {
			m.scroll[index].y = 0
		}
		maximum := max(0, len(strings.Split(contents[index], "\n"))-visible)
		if m.scroll[index].y > maximum {
			m.scroll[index].y = maximum
		}
		contentWidth := 0
		for line := range strings.SplitSeq(contents[index], "\n") {
			contentWidth = max(contentWidth, ansi.StringWidth(line))
		}
		maximum = max(0, contentWidth-max(1, m.panelWidth(panel(index))-2))
		if m.scroll[index].x > maximum {
			m.scroll[index].x = maximum
		}
	}
}

func (m TUIModel) panelWidth(target panel) int {
	if m.width < wideTUIWidth {
		return m.width
	}
	dagWidth := m.width * 30 / 100
	detailWidth := m.width * 34 / 100
	switch target {
	case panelDAG:
		return dagWidth
	case panelDetail:
		return detailWidth
	default:
		return m.width - dagWidth - detailWidth - 2
	}
}

func (m TUIModel) View() string {
	width, height := m.width, m.height
	if width < minimumWidth || height < minimumHeight {
		return clipLine(fmt.Sprintf("Terminal too small (need at least %dx%d)", minimumWidth, minimumHeight), width, 0)
	}
	snapshot := m.projector.Snapshot()
	status := "RUNNING"
	running := 0
	failed := 0
	allCompleted := len(snapshot.Nodes) > 0
	for _, node := range snapshot.Nodes {
		switch node.Status {
		case StatusRunning:
			running++
			allCompleted = false
		case StatusFailed:
			failed++
			allCompleted = false
		case StatusCompleted, StatusSkipped:
		default:
			allCompleted = false
		}
	}
	if failed > 0 {
		status = "FAILED"
	} else if allCompleted {
		status = "COMPLETED"
	}
	header := fmt.Sprintf("r42 apply | %s | Tasks: %d/%d done | Running: %d | Failed: %d | Tokens: %d",
		status, snapshot.Research.Completed, snapshot.Research.Total, running,
		failed, snapshot.Usage.TotalTokens())
	run := "Run: " + terminalText(snapshot.RunDirectory)
	body := m.renderBody()
	footer := "Tab/Left/Right panel  Up/Down scroll  PgUp/PgDn page  Alt+Left/Right horizontal  Enter fold  f follow  q quit"
	if m.confirm {
		footer = "Press q again to cancel this run; any other key returns to the run"
	}
	return fitBlock(strings.Join([]string{
		clipLine(header, width, 0), clipLine(run, width, 0), body, clipLine(footer, width, 0),
	}, "\n"), width, height)
}

func (m TUIModel) renderBody() string {
	height := m.bodyHeight()
	if m.width < wideTUIWidth {
		return m.renderPanel(m.focus, m.width, height)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderPanel(panelDAG, m.panelWidth(panelDAG), height), " ",
		m.renderPanel(panelDetail, m.panelWidth(panelDetail), height), " ",
		m.renderPanel(panelTimeline, m.panelWidth(panelTimeline), height),
	)
}

func (m TUIModel) bodyHeight() int {
	height := m.height - 3
	if height < 1 {
		return 1
	}
	return height
}

func (m TUIModel) renderPanel(target panel, width, height int) string {
	title := panelName(target)
	if m.focus == target {
		title += " *"
	}
	content := ""
	switch target {
	case panelDAG:
		content = m.dagContent()
	case panelDetail:
		content = m.detailContent()
	case panelTimeline:
		content = m.timelineContent()
	}
	innerWidth := width - 2
	innerHeight := height - 2
	if innerWidth < 1 {
		innerWidth = 1
	}
	if innerHeight < 1 {
		innerHeight = 1
	}
	lines := visibleLines(content, m.scroll[target], innerWidth, innerHeight-1)
	panelContent := clipLine(title, innerWidth, 0)
	if innerHeight > 1 {
		panelContent += "\n" + strings.Join(lines, "\n")
	}
	borderColor := lipgloss.Color("240")
	if m.focus == target {
		borderColor = lipgloss.Color("39")
	}
	return lipgloss.NewStyle().
		Width(innerWidth).Height(innerHeight).
		Border(lipgloss.NormalBorder()).BorderForeground(borderColor).
		Render(panelContent)
}

func panelName(target panel) string {
	switch target {
	case panelDetail:
		return "Detail"
	case panelTimeline:
		return "Timeline"
	default:
		return "DAG"
	}
}

func (m TUIModel) visibleNodes() []Node {
	snapshot := m.projector.Snapshot()
	nodes := make([]Node, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		hidden := false
		parent := node.Parent
		for parent != "" {
			if m.collapsed[parent] {
				hidden = true
				break
			}
			parentNode, ok := snapshot.Node(parent)
			if !ok {
				break
			}
			parent = parentNode.Parent
		}
		if !hidden {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func (m TUIModel) dagContent() string {
	nodes := m.visibleNodes()
	lines := make([]string, 0, len(nodes))
	for index, node := range nodes {
		selected := "  "
		if index == m.selected {
			selected = "> "
		}
		indent := strings.Repeat("  ", nodeDepth(node, m.projector.Snapshot()))
		fold := ""
		if node.Kind == "module" {
			if m.collapsed[node.Address] {
				fold = "+ "
			} else {
				fold = "- "
			}
		}
		line := fmt.Sprintf("%s%s%s[%s] %s", selected, indent, fold, statusMarker(node.Status), terminalText(node.Address))
		if len(node.Dependencies) > 0 {
			dependencies := make([]string, len(node.Dependencies))
			for index, dependency := range node.Dependencies {
				dependencies[index] = terminalText(dependency)
			}
			line += " <- " + strings.Join(dependencies, ", ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func nodeDepth(node Node, snapshot Snapshot) int {
	depth := 0
	parent := node.Parent
	for parent != "" {
		depth++
		parentNode, ok := snapshot.Node(parent)
		if !ok {
			break
		}
		parent = parentNode.Parent
	}
	return depth
}

func statusMarker(status Status) string {
	switch status {
	case StatusRunning:
		return ">"
	case StatusCompleted:
		return "x"
	case StatusFailed:
		return "!"
	case StatusSkipped:
		return "-"
	default:
		return " "
	}
}

func (m TUIModel) selectedNode() (Node, bool) {
	nodes := m.visibleNodes()
	if m.selected < 0 || m.selected >= len(nodes) {
		return Node{}, false
	}
	return nodes[m.selected], true
}

func (m TUIModel) detailContent() string {
	node, ok := m.selectedNode()
	if !ok {
		return "No node selected"
	}
	phase := string(node.Phase)
	if phase == "" {
		phase = "not started"
	}
	lines := []string{
		"Address: " + terminalText(node.Address),
		"Kind: " + terminalText(node.Kind),
		"Status: " + string(node.Status),
		"Phase: " + phase,
		"Activity: " + string(node.Activity),
		fmt.Sprintf("Tokens: %d (input=%d output=%d reasoning=%d cache_read=%d cache_write=%d)",
			node.Usage.TotalTokens(), node.Usage.InputTokens, node.Usage.OutputTokens,
			node.Usage.ReasoningTokens, node.Usage.CacheReadTokens, node.Usage.CacheWriteTokens),
	}
	if len(node.Dependencies) > 0 {
		dependencies := make([]string, len(node.Dependencies))
		for index, dependency := range node.Dependencies {
			dependencies[index] = terminalText(dependency)
		}
		lines = append(lines, "Depends on: "+strings.Join(dependencies, ", "))
	}
	if node.ToolName != "" {
		lines = append(lines, "Tool: "+node.ToolName)
	}
	if node.Content != "" {
		lines = append(lines, "", "Current content:", node.Content)
	}
	return strings.Join(lines, "\n")
}

func (m TUIModel) timelineContent() string {
	node, ok := m.selectedNode()
	if !ok {
		return "No node selected"
	}
	snapshot := m.projector.Snapshot()
	lines := make([]string, 0)
	for _, entry := range snapshot.Timeline {
		if entry.Address != node.Address {
			continue
		}
		phase := string(entry.Session)
		if phase == "" {
			phase = "run"
		}
		line := fmt.Sprintf("[%s] %s", phase, strings.ToUpper(string(entry.Activity)))
		if entry.ToolName != "" {
			line += " " + entry.ToolName
		}
		if entry.Content != "" {
			line += " " + entry.Content
		}
		if entry.Error != "" {
			line += " ERROR: " + entry.Error
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "No session events yet"
	}
	return strings.Join(lines, "\n")
}

func visibleLines(content string, position scrollPosition, width, height int) []string {
	if height < 1 {
		return nil
	}
	all := strings.Split(content, "\n")
	if position.y >= len(all) {
		position.y = len(all) - 1
	}
	if position.y < 0 {
		position.y = 0
	}
	end := min(position.y+height, len(all))
	result := make([]string, 0, height)
	for _, line := range all[position.y:end] {
		result = append(result, clipLine(line, width, position.x))
	}
	for len(result) < height {
		result = append(result, "")
	}
	return result
}

func clipLine(line string, width, offset int) string {
	if width <= 0 {
		return ""
	}
	if offset >= ansi.StringWidth(line) {
		return ""
	}
	if offset < 0 {
		offset = 0
	}
	return ansi.Cut(line, offset, offset+width)
}

func fitBlock(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		if ansi.StringWidth(lines[index]) > width {
			lines[index] = ansi.Truncate(lines[index], width, "")
		}
	}
	return strings.Join(lines, "\n")
}

func RunTUI(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	projector *Projector,
	cancel context.CancelFunc,
	apply func() error,
) error {
	if projector == nil {
		return fmt.Errorf("tui projector is required")
	}
	if apply == nil {
		return fmt.Errorf("tui apply function is required")
	}
	options := []tea.ProgramOption{tea.WithAltScreen(), tea.WithOutput(output), tea.WithContext(ctx)}
	if input != os.Stdin {
		options = append(options, tea.WithInput(input))
	}
	program := tea.NewProgram(NewTUIModel(projector, cancel), options...)
	applyDone := make(chan error, 1)
	go func() {
		err := apply()
		applyDone <- err
		program.Send(applyFinishedMsg{})
	}()
	_, tuiErr := program.Run()
	if cancel != nil {
		cancel()
	}
	applyErr := <-applyDone
	return errors.Join(applyErr, tuiErr)
}
