package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// watcherResult é o último output de um watch-command configurado no yaml.
type watcherResult struct {
	output string
	err    error
	at     time.Time
}

type watcherOutputMsg struct {
	key    string // host + "/" + nome
	name   string
	result watcherResult
}

// dispatchWatchersCmd dispara (no tick de 1s) os watchers do host ativo cujo
// intervalo venceu. Guard `watcherRunning` evita sobrepor execuções do mesmo
// watcher quando o comando demora mais que o intervalo.
func (m *AppModel) dispatchWatchersCmd() []tea.Cmd {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok || len(hostCfg.Watchers) == 0 {
		return nil
	}
	host := m.activeHost

	var cmds []tea.Cmd
	for _, w := range hostCfg.Watchers {
		if w.Name == "" || w.Cmd == "" {
			continue
		}
		key := host + "/" + w.Name
		if m.watcherRunning[key] {
			continue
		}
		if last, ok := m.watcherLastRun[key]; ok && time.Since(last) < w.IntervalDuration() {
			continue
		}
		m.watcherLastRun[key] = time.Now()
		m.watcherRunning[key] = true

		watcher := w
		cmds = append(cmds, func() tea.Msg {
			cfg := hostCfg
			client, err := m.getOrCreateSSHClient(host, &cfg)
			if err != nil {
				return watcherOutputMsg{key: key, name: watcher.Name, result: watcherResult{err: err, at: time.Now()}}
			}
			stdout, stderr, err := client.RunCommand(watcher.Cmd)
			out := strings.TrimRight(stdout, "\n")
			if err != nil && strings.TrimSpace(stderr) != "" {
				out = strings.TrimRight(stderr, "\n")
			}
			return watcherOutputMsg{key: key, name: watcher.Name, result: watcherResult{output: out, err: err, at: time.Now()}}
		})
	}
	return cmds
}

// renderWatch mostra um bloco por watcher configurado: nome, idade do output
// e o output truncado à altura disponível.
// ponytail: sem scroll; adicionar se um watcher precisar de mais de uma tela.
func (m *AppModel) renderWatch(width, height int) string {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok || len(hostCfg.Watchers) == 0 {
		var sb strings.Builder
		sb.WriteString("Nenhum watch-command configurado para este host.\n\n")
		sb.WriteString("Adicione no ~/.unlarp.yaml (via `unlarp config edit`):\n\n")
		sb.WriteString(styles.HelpStyle.Render(
			"  hosts:\n" +
				"    " + m.activeHost + ":\n" +
				"      watchers:\n" +
				"        - name: claude-usage\n" +
				"          cmd: npx -y ccusage@latest blocks --active\n" +
				"          interval: 5m\n" +
				"        - name: disco\n" +
				"          cmd: df -h /workspace | tail -1\n" +
				"          interval: 1m"))
		return sb.String()
	}

	dimStyle := lipgloss.NewStyle().Foreground(styles.ColorDim)
	// Altura de output por watcher: divide o espaço, mínimo 3 linhas
	perWatcher := height/len(hostCfg.Watchers) - 3
	if perWatcher < 3 {
		perWatcher = 3
	}

	var sb strings.Builder
	for _, w := range hostCfg.Watchers {
		key := m.activeHost + "/" + w.Name
		res, hasRes := m.watcherOutput[key]

		title := fmt.Sprintf(" %s", w.Name)
		if hasRes {
			title += dimStyle.Render(fmt.Sprintf("  (há %s, a cada %s)", formatIdle(time.Since(res.at)), w.IntervalDuration()))
		} else if m.watcherRunning[key] {
			title += dimStyle.Render("  (executando...)")
		} else {
			title += dimStyle.Render("  (aguardando primeira execução)")
		}
		sb.WriteString(styles.TableHeaderStyle.Width(width).Render(title))
		sb.WriteString("\n")

		if hasRes {
			if res.err != nil && res.output == "" {
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Render(fmt.Sprintf(" erro: %v", res.err)))
				sb.WriteString("\n")
			} else {
				lines := strings.Split(res.output, "\n")
				if len(lines) > perWatcher {
					lines = lines[len(lines)-perWatcher:]
				}
				for _, l := range lines {
					if len(l) > width-2 {
						l = l[:width-2]
					}
					sb.WriteString(" " + l + "\n")
				}
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
