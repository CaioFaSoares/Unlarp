package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// accountAddedMsg é o resultado do cadastro de conta feito em background (SSH)
type accountAddedMsg struct {
	name string
	dir  string
	err  error
}

// addAccountCmd cria o diretório remoto da conta e a registra no config.
// dir vazio → $HOME/.claude-accounts/<name> resolvido no remoto.
func (m *AppModel) addAccountCmd(name, dir string) tea.Cmd {
	host := m.activeHost
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return accountAddedMsg{name: name, err: fmt.Errorf("host '%s' não encontrado", host)}
		}
		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			return accountAddedMsg{name: name, err: err}
		}
		if dir == "" || strings.HasPrefix(dir, "~") {
			home, _, err := client.RunCommand("echo $HOME")
			if err != nil {
				return accountAddedMsg{name: name, err: fmt.Errorf("resolver $HOME remoto: %w", err)}
			}
			home = strings.TrimSpace(home)
			if dir == "" {
				dir = home + "/.claude-accounts/" + name
			} else {
				dir = home + strings.TrimPrefix(dir, "~")
			}
		}
		if _, stderr, err := client.RunCommand("mkdir -p " + shellQuote(dir)); err != nil {
			return accountAddedMsg{name: name, err: fmt.Errorf("mkdir remoto: %s: %w", strings.TrimSpace(stderr), err)}
		}
		if err := config.NewStore().AddAccount(host, name, dir); err != nil {
			return accountAddedMsg{name: name, err: err}
		}
		return accountAddedMsg{name: name, dir: dir}
	}
}

// renderAccounts desenha a aba Contas: contas Claude Code do host ativo e
// os projetos vinculados a cada uma.
func (m *AppModel) renderAccounts(width, height int) string {
	dimStyle := lipgloss.NewStyle().Foreground(styles.ColorDim)
	var sb strings.Builder

	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		return dimStyle.Render("Nenhum host ativo.")
	}

	names := m.hostAccounts()
	if len(names) == 0 {
		sb.WriteString(dimStyle.Render("Nenhuma conta Claude Code cadastrada neste host.") + "\n\n")
		sb.WriteString("Contas isolam credenciais do Claude Code via CLAUDE_CONFIG_DIR:\n")
		sb.WriteString("cada projeto pode apontar para uma conta (ex.: pessoal vs. empresa).\n\n")
		sb.WriteString(styles.KeyStyle.Render("a") + " cadastrar conta   " +
			dimStyle.Render("(login depois com: unlarp account login <nome>)"))
		return lipgloss.NewStyle().Padding(1, 2).Width(width - 4).Render(sb.String())
	}

	if m.selectedAccountRow >= len(names) {
		m.selectedAccountRow = len(names) - 1
	}

	sb.WriteString(styles.HostActiveStyle.Render(fmt.Sprintf("Contas Claude Code — %s", m.activeHost)) + "\n\n")
	for i, name := range names {
		var projs []string
		for _, p := range hostCfg.Projects {
			if p.Account == name {
				projs = append(projs, p.Name)
			}
		}
		line := fmt.Sprintf("%s  %s", name, dimStyle.Render(hostCfg.Accounts[name]))
		if len(projs) > 0 {
			line += "  " + dimStyle.Render("projetos: "+strings.Join(projs, ", "))
		}
		if i == m.selectedAccountRow {
			sb.WriteString(styles.HostSelectedStyle.Render("> "+line) + "\n")
		} else {
			sb.WriteString("  " + line + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("Projeto sem conta usa o ~/.claude padrão do remoto. Login: unlarp account login <nome>"))

	return lipgloss.NewStyle().Padding(1, 2).Width(width - 4).Render(sb.String())
}
