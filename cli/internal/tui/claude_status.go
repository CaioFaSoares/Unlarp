package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// claudeStatusMsg carrega o estado inferido dos agentes (Claude Code) por
// nome de sessão tmux: "trabalhando" | "esperando" | "idle".
type claudeStatusMsg struct {
	host     string
	statuses map[string]string
}

// claudeCommands são os comandos de pane que indicam um Claude Code rodando
// (o binário roda via node; "claude" cobre wrappers).
var claudeCommands = map[string]bool{"node": true, "claude": true}

// classifyClaudePane infere o estado do Claude Code a partir do tail do pane.
// Heurística sobre strings da UI do claude code — se a UI mudar entre versões,
// devolve "" e a TUI degrada para o status genérico de tmux (falha suave).
func classifyClaudePane(tail string) string {
	t := strings.ToLower(tail)

	// Trabalhando: o rodapé de interrupção só aparece com o agente ativo
	if strings.Contains(t, "esc to interrupt") || strings.Contains(t, "ctrl+b to run in background") {
		return "trabalhando"
	}

	// Esperando você: diálogo de permissão/escolha ou pergunta aberta
	waiting := []string{"do you want", "❯ 1.", "(y/n)", "waiting for your input", "esc to cancel"}
	for _, w := range waiting {
		if strings.Contains(t, w) {
			return "esperando"
		}
	}

	// Idle: prompt do claude visível, sem trabalho em andamento
	if strings.Contains(t, "? for shortcuts") || strings.Contains(t, "bypass permissions") || strings.Contains(t, "plan mode") {
		return "idle"
	}

	return ""
}

// checkClaudeStatusCmd captura o tail dos panes das sessões com Claude Code em
// UM RunCommand (1 roundtrip SSH para N sessões) e classifica cada uma.
// ponytail: cadência de 3s para todas as sessões node; filtrar por aba visível se pesar.
func (m *AppModel) checkClaudeStatusCmd() tea.Cmd {
	host := m.activeHost
	var names []string
	for _, s := range m.tmuxSessions {
		if claudeCommands[s.Command] {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return nil
		}
		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			return nil
		}

		var sb strings.Builder
		for _, n := range names {
			q := shellQuote(n)
			sb.WriteString(fmt.Sprintf("echo ==UNLARP==%s; tmux capture-pane -pt %s -S -12 2>/dev/null; ", q, q))
		}
		stdout, _, err := client.RunCommand(sb.String())
		if err != nil {
			return nil
		}

		statuses := make(map[string]string)
		var current string
		var buf strings.Builder
		flush := func() {
			if current != "" {
				if st := classifyClaudePane(buf.String()); st != "" {
					statuses[current] = st
				}
			}
			buf.Reset()
		}
		for _, line := range strings.Split(stdout, "\n") {
			if name, ok := strings.CutPrefix(line, "==UNLARP=="); ok {
				flush()
				current = strings.Trim(name, "'")
				continue
			}
			buf.WriteString(line)
			buf.WriteString("\n")
		}
		flush()

		return claudeStatusMsg{host: host, statuses: statuses}
	}
}

// claudeStatusLabel devolve o rótulo para a sessão, ou "" se não é um agente
// claude ou a heurística não reconheceu a tela.
func (m *AppModel) claudeStatusLabel(sessionName string) string {
	switch m.claudeStatus[sessionName] {
	case "trabalhando":
		return "✳ trabalhando"
	case "esperando":
		return "⌛ esperando você"
	case "idle":
		return "◌ idle"
	}
	return ""
}
