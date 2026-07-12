package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

func (m *AppModel) renderLogs(width, height int) string {
	logs := m.snapshotLogs()
	if len(logs) == 0 {
		return "Nenhum log registrado."
	}

	if width < 10 {
		width = 10
	}
	if height < 1 {
		height = 1
	}

	// Wrap each log line to the current panel width
	var wrappedLines []string
	style := lipgloss.NewStyle().Width(width)
	for _, logLine := range logs {
		wrapped := style.Render(logLine)
		subLines := strings.Split(wrapped, "\n")
		wrappedLines = append(wrappedLines, subLines...)
	}

	totalLines := len(wrappedLines)

	// Determine vertical layout. If total lines exceed height, reserve 1 line for the status bar
	showHeight := height
	hasStatus := totalLines > height
	if hasStatus {
		showHeight = height - 1
	}
	if showHeight < 1 {
		showHeight = 1
	}

	// Handle auto-scroll boundary
	if m.logAutoScroll {
		m.logScrollOffset = totalLines - showHeight
	}
	if m.logScrollOffset > totalLines-showHeight {
		m.logScrollOffset = totalLines - showHeight
	}
	if m.logScrollOffset < 0 {
		m.logScrollOffset = 0
	}

	// Slice wrapped logs to show based on scroll offset
	end := m.logScrollOffset + showHeight
	if end > totalLines {
		end = totalLines
	}

	// Safe bounds check
	start := m.logScrollOffset
	if start > totalLines {
		start = totalLines
	}

	displayLines := wrappedLines[start:end]
	logsContent := strings.Join(displayLines, "\n")

	// If there are more logs than can fit, show a nice status bar at the bottom
	if hasStatus {
		statusText := ""
		currentPos := end
		if m.logAutoScroll {
			statusText = fmt.Sprintf("▲ %d/%d logs | Auto-Scroll: Ativo (use ↑/k para rolar)", currentPos, totalLines)
		} else {
			statusText = fmt.Sprintf("▼ %d/%d logs | Auto-Scroll: Pausado (pressione 'G' ou role até o fim)", currentPos, totalLines)
		}

		statusStyled := lipgloss.NewStyle().
			Foreground(styles.ColorDim).
			Bold(true).
			Render(statusText)

		// Auto-enable auto-scroll when user reaches the bottom
		if currentPos == totalLines {
			m.logAutoScroll = true
		}

		return logsContent + "\n" + statusStyled
	}

	return logsContent
}
