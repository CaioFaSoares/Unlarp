package tui

import (
	"fmt"
	"strings"

	"github.com/CaioFaSoares/unlarp/internal/session"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// matchProjectSync encontra o sync (se houver) cujo RemoteDir está dentro do
// caminho do projeto, para cruzar o estado de sincronização por projeto.
func matchProjectSync(syncs []session.SyncEntry, projectPath string) *session.SyncEntry {
	for i, s := range syncs {
		if s.RemoteDir == projectPath || strings.HasPrefix(s.RemoteDir, projectPath+"/") {
			return &syncs[i]
		}
	}
	return nil
}

// matchProjectName encontra o nome do projeto (se houver) cujo caminho contém
// o RemoteDir informado — usado para anotar a aba Syncs com o projeto dono.
func matchProjectName(projects []Project, remoteDir string) string {
	for _, p := range projects {
		if remoteDir == p.Path || strings.HasPrefix(remoteDir, p.Path+"/") {
			return p.Name
		}
	}
	return ""
}

func (m AppModel) renderProjects(width, height int) string {
	var sb strings.Builder

	if len(m.projects) == 0 {
		sb.WriteString("Nenhum projeto cadastrado neste host.\n\n")
		sb.WriteString(fmt.Sprintf("Pressione %s para navegar até a pasta do projeto na workspace remota e cadastrá-la\n", styles.KeyStyle.Render("a")))
		sb.WriteString("(você pode já vincular uma sincronização no mesmo passo).")
		return sb.String()
	}

	sess, _ := m.sessMgr.GetSession(m.activeHost)
	var syncs []session.SyncEntry
	if sess != nil {
		syncs = sess.Syncs
	}

	sb.WriteString(styles.TableHeaderStyle.Width(width).Render(
		fmt.Sprintf("  %-24s %-16s %-10s", "PROJETO", "BRANCH", "SYNC"),
	))
	sb.WriteString("\n")

	// m.projects já vem com os projetos com sync ativo no topo (ver o handler
	// de projectsMsg em app.go) — aqui só marcamos com ★ quem está sincronizando.
	for i, p := range m.projects {
		marker := " "
		syncState := "—"
		if s := matchProjectSync(syncs, p.Path); s != nil {
			marker = "★"
			syncState = s.ID
		}

		line := fmt.Sprintf("%s %-24s %-16s %-10s", marker, p.Name, p.Branch, syncState)

		if i == m.selectedProjectRow && !m.sidebarFocus {
			sb.WriteString(styles.HostSelectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(m.renderProjectDetail(width, syncs))

	return sb.String()
}

func (m AppModel) renderProjectDetail(width int, syncs []session.SyncEntry) string {
	if m.selectedProjectRow >= len(m.projects) {
		return ""
	}
	proj := m.projects[m.selectedProjectRow]

	var sb strings.Builder
	sb.WriteString(styles.TableHeaderStyle.Width(width).Render("Detalhes do Projeto"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Caminho remoto:"), proj.Path))
	sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Branch:"), proj.Branch))

	if s := matchProjectSync(syncs, proj.Path); s != nil {
		sb.WriteString(fmt.Sprintf(" %s %s -> %s\n", styles.StatusLabelStyle.Render("Sync:"), s.ID, truncatePath(s.LocalDir, 30)))
	} else if proj.LocalDir != "" {
		sb.WriteString(fmt.Sprintf(" %s pasta local vinculada (%s), sem sync ativo no momento\n", styles.StatusLabelStyle.Render("Sync:"), truncatePath(proj.LocalDir, 30)))
	} else {
		sb.WriteString(fmt.Sprintf(" %s nenhuma sincronização ativa\n", styles.StatusLabelStyle.Render("Sync:")))
	}

	sb.WriteString(fmt.Sprintf("\n %s (sessão Tmux: %s)\n", styles.StatusLabelStyle.Render("Últimos comandos:"), proj.Name))

	output, ok := m.projectOutput[proj.Name]
	if !ok || strings.TrimSpace(output) == "" {
		sb.WriteString(fmt.Sprintf(" Nenhum output capturado ainda. Pressione %s para abrir uma sessão neste projeto.\n", styles.KeyStyle.Render("c")))
	} else {
		lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		start := 0
		if len(lines) > 10 {
			start = len(lines) - 10
		}
		for _, l := range lines[start:] {
			sb.WriteString(" " + l + "\n")
		}
	}

	return sb.String()
}
