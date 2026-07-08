package tui

import (
	"strings"
)

func (m AppModel) renderLogs(width, height int) string {
	if len(m.logs) == 0 {
		return "Nenhum log registrado."
	}

	// Filtra as linhas para caber exatamente na altura
	displayLogs := m.logs
	if len(displayLogs) > height {
		displayLogs = displayLogs[len(displayLogs)-height:]
	}

	return strings.Join(displayLogs, "\n")
}
