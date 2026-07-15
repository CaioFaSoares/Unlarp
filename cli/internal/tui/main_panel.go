package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// renderMainPanel desenha a barra de abas e o conteúdo da aba selecionada
func (m *AppModel) renderMainPanel(width, height int) string {
	tabs := []string{"Dashboard", "Projetos", "Syncs", "Túneis", "Logs", "Watch", "Contas"}
	var renderedTabs []string

	for i, t := range tabs {
		var style lipgloss.Style
		if i == m.activeTab {
			style = styles.TabActiveStyle
		} else {
			style = styles.TabStyle
		}
		renderedTabs = append(renderedTabs, style.Render(t))
	}

	tabRow := styles.TabRowStyle.Width(width).Render(
		lipgloss.JoinHorizontal(lipgloss.Top, renderedTabs...),
	)

	// Altura restante para o conteúdo da aba
	contentHeight := height - lipgloss.Height(tabRow) - 2
	var content string

	if m.pickerActive {
		pickerBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorSecondary).
			Padding(1, 2).
			Width(width - 6).
			Render(m.dirPicker.View())

		return lipgloss.JoinVertical(lipgloss.Left, tabRow, pickerBox)
	}

	if m.accountPickerActive {
		var sb strings.Builder
		sb.WriteString(styles.HostActiveStyle.Render(fmt.Sprintf("Conta Claude Code para a sessão '%s'", m.pendingSessionName)))
		sb.WriteString("\n\n")
		options := append([]string{"(sem conta — padrão do remoto)"}, m.hostAccounts()...)
		for i, opt := range options {
			line := "  " + opt
			if i == m.accountPickerCursor {
				line = styles.HostSelectedStyle.Render("> " + opt)
			}
			sb.WriteString(line + "\n")
		}
		saveMark := "[ ]"
		if m.accountPickerSave {
			saveMark = "[x]"
		}
		sb.WriteString(fmt.Sprintf("\n%s salvar como conta do projeto (%s alterna)\n", saveMark, styles.KeyStyle.Render("s")))
		sb.WriteString(styles.KeyStyle.Render("↑/↓") + " navegar  " + styles.KeyStyle.Render("Enter") + " criar sessão  " + styles.KeyStyle.Render("Esc") + " cancelar")

		pickerBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorSecondary).
			Padding(1, 2).
			Width(width - 6).
			Render(sb.String())

		return lipgloss.JoinVertical(lipgloss.Left, tabRow, pickerBox)
	}

	if m.promptActive {
		var promptTitle string
		switch m.promptType {
		case "sync":
			promptTitle = "Iniciar Sincronização em Tempo Real"
		case "tunnel_direction":
			promptTitle = "Direção do Túnel SSH"
		case "tunnel":
			promptTitle = "Configurar Novo Túnel SSH"
		case "new_tmux_session":
			promptTitle = "Criar Nova Sessão Tmux Remota"
		case "project_sync_confirm":
			promptTitle = "Cadastrar Projeto — Sincronizar Agora?"
		case "project_delete_confirm":
			promptTitle = fmt.Sprintf("Confirmar exclusão do projeto '%s'? (s/n)", m.pendingProject.Name)
		case "git_switch_branch":
			promptTitle = fmt.Sprintf("Trocar branch do projeto '%s'", m.pendingProject.Name)
		case "worktree_add":
			promptTitle = fmt.Sprintf("Nova worktree em '%s' — nome da branch", m.pendingProject.Name)
		case "account_add":
			promptTitle = "Cadastrar Conta Claude Code — nome [dir remoto]"
		case "account_delete_confirm":
			promptTitle = fmt.Sprintf("Remover conta '%s' do config? O diretório remoto é mantido. (s/n)", m.pendingAccount)
		}

		promptBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorSecondary).
			Padding(1, 2).
			Width(width - 6).
			Render(
				lipgloss.JoinVertical(lipgloss.Left,
					styles.HostActiveStyle.Render(promptTitle),
					"",
					m.textInput.View(),
				),
			)

		return lipgloss.JoinVertical(lipgloss.Left, tabRow, promptBox)
	}

	switch m.activeTab {
	case tabDashboard:
		content = m.renderDashboard(width, contentHeight)
	case tabProjects:
		content = m.renderProjects(width, contentHeight)
	case tabSyncs:
		content = m.renderSyncs(width, contentHeight)
	case tabTunnels:
		content = m.renderTunnels(width, contentHeight)
	case tabLogs:
		content = m.renderLogs(width, contentHeight)
	case tabWatch:
		content = m.renderWatch(width, contentHeight)
	case tabAccounts:
		content = m.renderAccounts(width, contentHeight)
	default:
		content = "Aba desconhecida"
	}

	return lipgloss.JoinVertical(lipgloss.Left, tabRow, content)
}

func (m *AppModel) renderDashboard(width, height int) string {
	var sb strings.Builder

	host, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		return "Selecione uma sessão ativa para ver o status."
	}

	sb.WriteString(fmt.Sprintf("Status do host: %s\n\n", styles.HostActiveStyle.Render(m.activeHost)))

	// Tabela ou campos de status
	sb.WriteString(fmt.Sprintf("%s %s\n", styles.StatusLabelStyle.Render("Endereço:"), host.Address()))
	sb.WriteString(fmt.Sprintf("%s %s\n", styles.StatusLabelStyle.Render("Usuário:"), host.User))
	if host.Container != "" {
		sb.WriteString(fmt.Sprintf("%s %s\n", styles.StatusLabelStyle.Render("Container:"), host.Container))
	}
	sb.WriteString(fmt.Sprintf("%s %s\n", styles.StatusLabelStyle.Render("Workspace:"), host.Workspace))

	daemonState := "desativado"
	if m.cfg.Daemon.Enabled {
		daemonState = "ativado"
	}
	sb.WriteString(fmt.Sprintf("%s %s (%s para alternar)\n", styles.StatusLabelStyle.Render("Daemon local:"), daemonState, styles.KeyStyle.Render("D")))

	sb.WriteString("\n")
	sb.WriteString(styles.TableHeaderStyle.Width(width).Render("Visão Geral de Recursos Remotos"))
	sb.WriteString("\n")

	// Adiciona resumo das atividades
	sess, exists := m.sessMgr.GetSession(m.activeHost)
	if exists {
		sb.WriteString(fmt.Sprintf(" Sincronizações ativas: %d\n", len(sess.Syncs)))
		sb.WriteString(fmt.Sprintf(" Túneis SSH ativos:     %d\n", len(sess.Tunnels)))
	} else {
		sb.WriteString(" Nenhuma atividade registrada no momento.\n")
	}

	sb.WriteString("\n")
	sb.WriteString(styles.TableHeaderStyle.Width(width).Render("Sessões Remotas Persistentes (Tmux)"))
	sb.WriteString("\n")

	if m.tmuxSessions == nil {
		sb.WriteString(" Carregando sessões Tmux...\n")
	} else if len(m.tmuxSessions) == 0 {
		sb.WriteString(" Nenhuma sessão Tmux ativa encontrada.\n")
		sb.WriteString(fmt.Sprintf(" Pressione %s para criar e conectar à sessão padrão 'unlarp'.\n", styles.KeyStyle.Render("c")))
	} else {
		for i, s := range m.tmuxSessions {
			statusStr := "DETACHED"
			if s.Attached {
				statusStr = "ATTACHED"
			}

			displayName := s.Command
			if displayName == "" {
				displayName = s.Name
			}
			// Agente Claude Code: mostra o estado inferido do pane
			if label := m.claudeStatusLabel(s.Name); label != "" {
				displayName = label
			}

			// Branch da worktree onde a sessão roda (se for repo git)
			branchStr := "—"
			if info, ok := m.gitInfo[s.Path]; ok && info.IsGitRepo && info.Branch != "" {
				branchStr = info.Branch
				if info.IsDirty {
					branchStr += " *"
				}
			}

			line := fmt.Sprintf("  %s %-14s %-16s %-30s (%d janelas) [%s]", s.StateIcon(), displayName, branchStr, s.Path, s.Windows, statusStr)

			if i == m.selectedTmuxRow && !m.sidebarFocus {
				sb.WriteString(styles.HostSelectedStyle.Render(line))
			} else if s.Attached {
				sb.WriteString(styles.HostActiveStyle.Render(line))
			} else {
				sb.WriteString(line)
			}
			sb.WriteString("\n")
		}
	}

	// Atalhos rápidos no Dashboard
	sb.WriteString("\nAtalhos Rápidos:\n")
	sb.WriteString(fmt.Sprintf("  %s - Conectar / Atachar à sessão selecionada\n", styles.KeyStyle.Render("c")))
	sb.WriteString(fmt.Sprintf("  %s - Navegar pelas sessões / hosts\n", styles.KeyStyle.Render("↑/↓ / j/k")))
	sb.WriteString(fmt.Sprintf("  %s - Mudar aba (Dashboard/Projetos/Syncs/Túneis/Logs)\n", styles.KeyStyle.Render("←/→")))

	return sb.String()
}
