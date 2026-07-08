package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// renderMainPanel desenha a barra de abas e o conteúdo da aba selecionada
func (m AppModel) renderMainPanel(width, height int) string {
	tabs := []string{"Dashboard", "Syncs", "Túneis", "Logs"}
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
	contentHeight := height - lipgloss.Height(tabRow) - 1
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

	if m.promptActive {
		var promptTitle string
		switch m.promptType {
		case "sync":
			promptTitle = "Iniciar Sincronização em Tempo Real"
		case "tunnel":
			promptTitle = "Configurar Novo Túnel SSH"
		case "new_tmux_session":
			promptTitle = "Criar Nova Sessão Tmux Remota"
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
	case 0:
		content = m.renderDashboard(width, contentHeight)
	case 1:
		content = m.renderSyncs(width, contentHeight)
	case 2:
		content = m.renderTunnels(width, contentHeight)
	case 3:
		content = m.renderLogs(width, contentHeight)
	default:
		content = "Aba desconhecida"
	}

	return lipgloss.JoinVertical(lipgloss.Left, tabRow, content)
}

func (m AppModel) renderDashboard(width, height int) string {
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

	if len(m.tmuxSessions) == 0 {
		sb.WriteString(" Nenhuma sessão Tmux ativa encontrada.\n")
		sb.WriteString(fmt.Sprintf(" Pressione %s para criar e conectar à sessão padrão 'unlarp'.\n", styles.KeyStyle.Render("c")))
	} else {
		for i, s := range m.tmuxSessions {
			statusStr := "DETACHED"
			if s.Attached {
				statusStr = "ATTACHED"
			}

			line := fmt.Sprintf("  ● %-12s (%d janelas) [%s]", s.Name, s.Windows, statusStr)

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
	sb.WriteString(fmt.Sprintf("  %s - Mudar aba (Dashboard/Syncs/Túneis/Logs)\n", styles.KeyStyle.Render("←/→")))

	return sb.String()
}
