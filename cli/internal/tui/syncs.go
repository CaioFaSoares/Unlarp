package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

func (m AppModel) renderSyncs(width, height int) string {
	var sb strings.Builder

	sess, sessOk := m.sessMgr.GetSession(m.activeHost)
	pending := m.pendingSyncs[m.activeHost]

	realCount := 0
	if sessOk {
		realCount = len(sess.Syncs)
	}
	if realCount == 0 && len(pending) == 0 {
		sb.WriteString("Nenhuma sincronização de arquivos ativa registrada neste host.\n\n")
		sb.WriteString(fmt.Sprintf("Pressione %s para iniciar uma nova sincronização direto por aqui.", styles.KeyStyle.Render("s")))
		return sb.String()
	}

	sb.WriteString(styles.TableHeaderStyle.Width(width).Render(
		fmt.Sprintf("  %-8s %-18s %-18s %-14s %-14s", "SESSION", "LOCAL DIR", "REMOTE DIR", "MODE", "PROJECT"),
	))
	sb.WriteString("\n")

	if sessOk {
		for i, s := range sess.Syncs {
			// Trunca caminhos longos para caber na TUI
			localDir := truncatePath(s.LocalDir, 18)
			remoteDir := truncatePath(s.RemoteDir, 18)

			projectName := matchProjectName(m.projects, s.RemoteDir)
			if projectName == "" {
				projectName = "—"
			}

			line := fmt.Sprintf("  %-8s %-18s %-18s %-14s %-14s",
				s.ID,
				localDir,
				remoteDir,
				s.Mode,
				projectName,
			)

			if i == m.selectedSyncRow && !m.sidebarFocus {
				sb.WriteString(styles.HostSelectedStyle.Render(line))
			} else {
				sb.WriteString(line)
			}
			sb.WriteString("\n")
		}
	}

	// Syncs recém-cadastrados cuja conexão/engine ainda estão sendo
	// verificadas em background — aparecem na lista imediatamente, mas sem
	// poder ser selecionados/removidos até virarem um sync de verdade.
	pendingStyle := lipgloss.NewStyle().Foreground(styles.ColorDim).Italic(true)
	for _, p := range pending {
		localDir := truncatePath(p.localDir, 18)
		remoteDir := truncatePath(p.remoteDir, 18)

		line := fmt.Sprintf("  %-8s %-18s %-18s %-14s %-14s",
			p.id,
			localDir,
			remoteDir,
			"verificando...",
			"—",
		)
		sb.WriteString(pendingStyle.Render(line))
		sb.WriteString("\n")
	}

	return sb.String()
}

func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen+3:]
}
