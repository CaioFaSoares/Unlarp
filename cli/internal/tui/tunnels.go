package tui

import (
	"fmt"
	"strings"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

func (m AppModel) renderTunnels(width, height int) string {
	var sb strings.Builder

	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok || len(sess.Tunnels) == 0 {
		sb.WriteString("Nenhum túnel de port forwarding ativo registrado neste host.\n\n")
		sb.WriteString(fmt.Sprintf("Pressione %s para criar um novo túnel SSH direto por aqui.", styles.KeyStyle.Render("t")))
		return sb.String()
	}

	sb.WriteString(styles.TableHeaderStyle.Width(width).Render(
		fmt.Sprintf("  %-8s %-12s %-12s %-10s", "TUNNEL ID", "REMOTE PORT", "LOCAL PORT", "STATUS"),
	))
	sb.WriteString("\n")

	for i, t := range sess.Tunnels {
		line := fmt.Sprintf("  %-8s %-12d %-12d %-10s",
			t.ID,
			t.RemotePort,
			t.LocalPort,
			"Active",
		)

		if i == m.selectedTunnelRow && !m.sidebarFocus {
			sb.WriteString(styles.HostSelectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
