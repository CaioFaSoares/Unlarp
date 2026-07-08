package tui

import (
	"fmt"
	"strings"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

func (m AppModel) renderSyncs(width, height int) string {
	var sb strings.Builder

	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok || len(sess.Syncs) == 0 {
		sb.WriteString("Nenhuma sincronização de arquivos ativa registrada neste host.\n\n")
		sb.WriteString(fmt.Sprintf("Pressione %s para iniciar uma nova sincronização direto por aqui.", styles.KeyStyle.Render("s")))
		return sb.String()
	}

	sb.WriteString(styles.TableHeaderStyle.Width(width).Render(
		fmt.Sprintf("  %-8s %-20s %-20s %-8s", "SESSION", "LOCAL DIR", "REMOTE DIR", "MODE"),
	))
	sb.WriteString("\n")

	for i, s := range sess.Syncs {
		// Trunca caminhos longos para caber na TUI
		localDir := truncatePath(s.LocalDir, 20)
		remoteDir := truncatePath(s.RemoteDir, 20)

		line := fmt.Sprintf("  %-8s %-20s %-20s %-8s",
			s.ID,
			localDir,
			remoteDir,
			s.Mode,
		)

		if i == m.selectedSyncRow && !m.sidebarFocus {
			sb.WriteString(styles.HostSelectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
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
