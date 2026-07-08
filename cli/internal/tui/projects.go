package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/CaioFaSoares/unlarp/internal/session"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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

func (m *AppModel) renderProjects(width, height int) string {
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

	headerStr := styles.TableHeaderStyle.Width(width).Render(
		fmt.Sprintf("  %-24s %-16s %-10s", "PROJETO / SESSÃO", "BRANCH / JANELAS", "SYNC / STATUS"),
	)
	sb.WriteString(headerStr)
	sb.WriteString("\n")

	headerHeight := lipgloss.Height(headerStr) + 1

	projectListHeight := 0
	items := m.buildProjectTree()
	for i, item := range items {
		var line string
		if item.IsProject {
			marker := " "
			syncState := "—"
			if s := matchProjectSync(syncs, item.Project.Path); s != nil {
				marker = "★"
				syncState = s.ID
			}

			indicator := "▸"
			if m.expandedProjects[item.Project.Path] {
				indicator = "▾"
			}
			
			nameCol := fmt.Sprintf("%s %s", indicator, item.Project.Name)
			branchStr := item.Project.Branch

			var hasAlert bool
			if s := matchProjectSync(syncs, item.Project.Path); s != nil {
				if _, ok := m.gitAlerts[s.ID]; ok {
					hasAlert = true
					marker = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true).Render("⚠")
				}
			}

			if hasAlert {
				nameCol = nameCol + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true).Render("[DIVERGIU]")
			}

			line = fmt.Sprintf("%s %-24s %-16s %-10s", marker, nameCol, branchStr, syncState)
		} else if item.Session != nil {
			indent := "    "
			nameCol := fmt.Sprintf("%s└─ %s", indent, item.Session.Name)
			windowsStr := fmt.Sprintf("%d janelas", item.Session.Windows)
			if item.Session.Windows == 1 {
				windowsStr = "1 janela"
			}
			statusStr := "Detached"
			if item.Session.Attached {
				statusStr = "Attached"
			}
			line = fmt.Sprintf("  %-24s %-16s %-10s", nameCol, windowsStr, statusStr)
		}

		if i == m.selectedProjectRow && !m.sidebarFocus {
			sb.WriteString(styles.HostSelectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
		projectListHeight++

		// Render placeholder if it's an expanded project with no sessions
		if item.IsProject && m.expandedProjects[item.Project.Path] && len(m.getProjectSessions(item.Project)) == 0 {
			placeholderLine := "      (Nenhuma sessão ativa - pressione 'n' para criar)"
			sb.WriteString(styles.HelpStyle.Render(placeholderLine))
			sb.WriteString("\n")
			projectListHeight++
		}
	}

	sb.WriteString("\n")

	// Calcular altura disponível para o output
	detailHeaderStr := styles.TableHeaderStyle.Width(width).Render("Detalhes do Projeto")
	detailHeaderHeight := lipgloss.Height(detailHeaderStr) + 1

	metadataHeight := 3 // Caminho remoto, Git Info (Remote), Sync
	
	gitAlert := ""
	if len(items) > 0 && m.selectedProjectRow < len(items) {
		selectedItem := items[m.selectedProjectRow]
		if s := matchProjectSync(syncs, selectedItem.Project.Path); s != nil {
			if alert, ok := m.gitAlerts[s.ID]; ok {
				gitAlert = alert
			}
		}
		
		if !selectedItem.IsProject && selectedItem.Session != nil {
			metadataHeight += 3 // Sessão Tmux, Janelas, Attached
			if selectedItem.Session.Command != "" {
				metadataHeight++ // Comando
			}
		} else {
			metadataHeight += 2 // "Últimos comandos: ..." + newline (no leading newline)
		}
	}

	totalUsedHeight := headerHeight + projectListHeight + 1 + detailHeaderHeight + metadataHeight
	if gitAlert != "" {
		totalUsedHeight += 9 // alerta + opções + newlines
	}

	maxOutputLines := height - totalUsedHeight
	if maxOutputLines < 0 {
		maxOutputLines = 0
	}
	if maxOutputLines > 5 {
		maxOutputLines = 5
	}

	sb.WriteString(m.renderProjectDetail(width, maxOutputLines, syncs))

	return strings.TrimSuffix(sb.String(), "\n")
}

func (m *AppModel) renderProjectDetail(width, maxOutputLines int, syncs []session.SyncEntry) string {
	items := m.buildProjectTree()
	if len(items) == 0 || m.selectedProjectRow >= len(items) {
		return ""
	}
	item := items[m.selectedProjectRow]
	proj := item.Project

	var sb strings.Builder
	sb.WriteString(styles.TableHeaderStyle.Width(width).Render("Detalhes do Projeto"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Caminho remoto:"), proj.Path))

	gitInfoStr := proj.Branch
	if info, ok := m.gitInfo[proj.Path]; ok && info.IsGitRepo {
		dirtyStr := ""
		if info.IsDirty {
			dirtyStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Render(" *dirty*")
		}
		
		abStr := ""
		if info.AheadBehind.Ahead > 0 || info.AheadBehind.Behind > 0 {
			abStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#F1FA8C")).Render(fmt.Sprintf(" (↑%d ↓%d)", info.AheadBehind.Ahead, info.AheadBehind.Behind))
		}
		
		gitInfoStr = fmt.Sprintf("%s [%s]%s%s", info.Branch, info.CommitHash, dirtyStr, abStr)
		if info.CommitMessage != "" {
			gitInfoStr += fmt.Sprintf(" - %s", info.CommitMessage)
		}
	}
	sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Git Info (Remote):"), gitInfoStr))

	var gitAlert string
	var syncID string
	if s := matchProjectSync(syncs, proj.Path); s != nil {
		syncID = s.ID
		if alert, ok := m.gitAlerts[s.ID]; ok {
			gitAlert = alert
		}
		sb.WriteString(fmt.Sprintf(" %s %s -> %s\n", styles.StatusLabelStyle.Render("Sync:"), s.ID, truncatePath(s.LocalDir, 30)))
	} else if proj.LocalDir != "" {
		sb.WriteString(fmt.Sprintf(" %s pasta local vinculada (%s), sem sync ativo no momento\n", styles.StatusLabelStyle.Render("Sync:"), truncatePath(proj.LocalDir, 30)))
	} else {
		sb.WriteString(fmt.Sprintf(" %s nenhuma sincronização ativa\n", styles.StatusLabelStyle.Render("Sync:")))
	}

	if gitAlert != "" {
		alertStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5555")).
			Bold(true).
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#FF5555")).
			Padding(0, 1)
		
		sb.WriteString("\n")
		sb.WriteString(alertStyle.Render(fmt.Sprintf("⚠ ALERTA DE DIVERGÊNCIA GIT (Sync %s): %s", syncID, gitAlert)))
		sb.WriteString("\n\n")
		
		keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B")).Bold(true)
		sb.WriteString(" Opções de Resolução (pressione a tecla correspondente):\n")
		sb.WriteString(fmt.Sprintf("  [%s] Puxar mudanças para a máquina local (git pull)\n", keyStyle.Render("p")))
		sb.WriteString(fmt.Sprintf("  [%s] Forçar upload do código local (sobrescrever remote)\n", keyStyle.Render("f")))
		sb.WriteString(fmt.Sprintf("  [%s] Mudar para modo Download-Only temporário\n", keyStyle.Render("o")))
		sb.WriteString("\n")
	}

	// Se não sobrou espaço para o output, terminamos aqui sem renderizar o cabeçalho de Últimos Comandos
	if maxOutputLines <= 0 {
		return sb.String()
	}

	if !item.IsProject && item.Session != nil {
		sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Sessão Tmux:"), item.Session.Name))
		sb.WriteString(fmt.Sprintf(" %s %d\n", styles.StatusLabelStyle.Render("Janelas:"), item.Session.Windows))
		sb.WriteString(fmt.Sprintf(" %s %t\n", styles.StatusLabelStyle.Render("Attached:"), item.Session.Attached))
		if item.Session.Command != "" {
			sb.WriteString(fmt.Sprintf(" %s %s\n", styles.StatusLabelStyle.Render("Comando:"), item.Session.Command))
		}
	} else {
		sb.WriteString(fmt.Sprintf(" %s (sessão Tmux padrão: %s)\n", styles.StatusLabelStyle.Render("Últimos comandos:"), proj.Name))
	}

	sessionName := proj.Name
	if !item.IsProject && item.Session != nil {
		sessionName = item.Session.Name
	}

	output, ok := m.projectOutput[sessionName]
	if !ok || strings.TrimSpace(output) == "" {
		sb.WriteString(fmt.Sprintf(" Nenhum output capturado ainda. Pressione %s para abrir esta sessão.\n", styles.KeyStyle.Render("c")))
		for i := 1; i < maxOutputLines; i++ {
			sb.WriteString("\n")
		}
	} else {
		lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		
		// Limpa as linhas (tabs, espaços no fim) e detecta se são em branco usando isBlank
		var cleanedLines []string
		for _, l := range lines {
			cleaned := strings.ReplaceAll(l, "\t", "    ")
			if isBlank(cleaned) {
				cleanedLines = append(cleanedLines, "")
			} else {
				cleanedLines = append(cleanedLines, strings.TrimRight(cleaned, " \r\n"))
			}
		}
		
		// Remove linhas vazias no início do output
		for len(cleanedLines) > 0 && cleanedLines[0] == "" {
			cleanedLines = cleanedLines[1:]
		}

		// Remove linhas vazias no fim do output
		for len(cleanedLines) > 0 && cleanedLines[len(cleanedLines)-1] == "" {
			cleanedLines = cleanedLines[:len(cleanedLines)-1]
		}

		if len(cleanedLines) == 0 {
			sb.WriteString(fmt.Sprintf(" Nenhum output capturado ainda. Pressione %s para abrir esta sessão.\n", styles.KeyStyle.Render("c")))
			for i := 1; i < maxOutputLines; i++ {
				sb.WriteString("\n")
			}
		} else {
			start := 0
			if len(cleanedLines) > maxOutputLines {
				start = len(cleanedLines) - maxOutputLines
			}
			
			renderedCount := len(cleanedLines[start:])
			
			// Trunca horizontalmente as linhas para evitar que o terminal dê wrap delas
			// Reduzido para width - 5 para compensar o espaço inicial e evitar qualquer wrap horizontal de 1 caractere
			maxLineLen := width - 5
			if maxLineLen < 10 {
				maxLineLen = 10
			}

			for _, l := range cleanedLines[start:] {
				truncated := truncateLine(l, maxLineLen)
				sb.WriteString(" " + truncated + "\n")
			}
			
			// Preenche com novas linhas até atingir exatamente maxOutputLines
			for i := renderedCount; i < maxOutputLines; i++ {
				sb.WriteString("\n")
			}
		}
	}

	return sb.String()
}

// truncateLine trunca visualmente uma linha para maxLen caracteres visuais
func truncateLine(line string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if runewidth.StringWidth(line) <= maxLen {
		return line
	}
	if maxLen <= 3 {
		return runewidth.Truncate(line, maxLen, "")
	}
	return runewidth.Truncate(line, maxLen-3, "") + "..."
}

// isBlank verifica se uma linha é composta apenas por caracteres de espaço ou não imprimíveis
func isBlank(line string) bool {
	for _, r := range line {
		if !unicode.IsSpace(r) && unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ProjectTreeItem representa um item na visualização em árvore/acordeão
type ProjectTreeItem struct {
	IsProject bool
	Project   Project
	Session   *TmuxSession
}

func (m *AppModel) getProjectSessions(proj Project) []TmuxSession {
	var matches []TmuxSession
	for _, ts := range m.tmuxSessions {
		nameMatch := ts.Name == proj.Name || strings.HasPrefix(ts.Name, proj.Name+"-")
		pathMatch := false
		if ts.Path != "" && proj.Path != "" {
			pathMatch = ts.Path == proj.Path || strings.HasPrefix(ts.Path, proj.Path+"/")
		}
		if nameMatch || pathMatch {
			matches = append(matches, ts)
		}
	}
	return matches
}

func (m *AppModel) buildProjectTree() []ProjectTreeItem {
	var items []ProjectTreeItem
	for _, p := range m.projects {
		items = append(items, ProjectTreeItem{
			IsProject: true,
			Project:   p,
		})

		if m.expandedProjects[p.Path] {
			sessions := m.getProjectSessions(p)
			for i := range sessions {
				items = append(items, ProjectTreeItem{
					IsProject: false,
					Project:   p,
					Session:   &sessions[i],
				})
			}
		}
	}
	return items
}
