package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/session"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// SyncTreeItem representa um item na visualização em árvore/acordeão dos syncs
type SyncTreeItem struct {
	IsSync       bool
	Sync         session.SyncEntry
	PendingSync  *pendingSync
	FileProgress *internalsync.FileProgress
}

func (m *AppModel) buildSyncTree() []SyncTreeItem {
	var items []SyncTreeItem

	sess, sessOk := m.sessMgr.GetSession(m.activeHost)
	pending := m.pendingSyncs[m.activeHost]

	if sessOk {
		for _, s := range sess.Syncs {
			items = append(items, SyncTreeItem{
				IsSync: true,
				Sync:   s,
			})

			if m.expandedSyncs[s.ID] {
				// Encontra a sessão de sync ativa para obter o progresso
				var progress internalsync.SyncProgress
				activeHostSyncs, exists := m.syncSessions[m.activeHost]
				if exists {
					sessCtx, exists := activeHostSyncs[s.ID]
					if exists && sessCtx.engine != nil {
						progress = sessCtx.engine.GetProgress()
					}
				}

				// Coleta os 10 arquivos mais relevantes seguindo a prioridade:
				// 1. Em progresso (SyncingFiles)
				// 2. Pendentes (PendingFiles)
				// 3. Concluídos recentemente (CompletedFiles)
				var displayFiles []internalsync.FileProgress

				// Adiciona arquivos ativamente sincronizando
				displayFiles = append(displayFiles, progress.SyncingFiles...)

				// Adiciona pendentes até completar 10
				if len(displayFiles) < 10 {
					needed := 10 - len(displayFiles)
					if needed > len(progress.PendingFiles) {
						needed = len(progress.PendingFiles)
					}
					displayFiles = append(displayFiles, progress.PendingFiles[:needed]...)
				}

				// Adiciona concluídos recentemente (em ordem reversa para mostrar os mais novos) até completar 10
				if len(displayFiles) < 10 {
					needed := 10 - len(displayFiles)
					if needed > len(progress.CompletedFiles) {
						needed = len(progress.CompletedFiles)
					}
					compCount := len(progress.CompletedFiles)
					for i := compCount - 1; i >= compCount-needed && i >= 0; i-- {
						displayFiles = append(displayFiles, progress.CompletedFiles[i])
					}
				}

				for i := range displayFiles {
					items = append(items, SyncTreeItem{
						IsSync:       false,
						Sync:         s,
						FileProgress: &displayFiles[i],
					})
				}
			}
		}
	}

	for _, p := range pending {
		pCopy := p
		items = append(items, SyncTreeItem{
			IsSync:      true,
			PendingSync: &pCopy,
		})
	}

	return items
}

func renderProgressBar(percent float64, caseType int, width int) string {
	if width <= 0 {
		width = 15
	}
	filledWidth := int(float64(width) * percent / 100.0)
	if filledWidth < 0 {
		filledWidth = 0
	}
	if filledWidth > width {
		filledWidth = width
	}
	emptyWidth := width - filledWidth

	filledChar := "█"
	emptyChar := "░"

	var filledPart, emptyPart string
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	grayStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	yellowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	switch caseType {
	case 1:
		// Caso 1: local tem mais, remoto tem menos. x% verde (já carregado), resto cinza.
		filledPart = greenStyle.Render(strings.Repeat(filledChar, filledWidth))
		emptyPart = grayStyle.Render(strings.Repeat(emptyChar, emptyWidth))
	case 2:
		// Caso 2: local mais atualizado. y% vermelho (enviando), resto verde (ok).
		filledPart = redStyle.Render(strings.Repeat(filledChar, filledWidth))
		emptyPart = greenStyle.Render(strings.Repeat(filledChar, emptyWidth))
	case 3:
		// Caso 3: remoto mais atualizado. z% amarelo (baixando), resto verde (ok).
		filledPart = yellowStyle.Render(strings.Repeat(filledChar, filledWidth))
		emptyPart = greenStyle.Render(strings.Repeat(filledChar, emptyWidth))
	default:
		filledPart = greenStyle.Render(strings.Repeat(filledChar, filledWidth))
		emptyPart = grayStyle.Render(strings.Repeat(emptyChar, emptyWidth))
	}

	return fmt.Sprintf("[%s%s] %3.0f%%", filledPart, emptyPart, percent)
}

func (m *AppModel) renderSyncs(width, height int) string {
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
		fmt.Sprintf("  %-8s %-18s %-18s %-10s %-12s %-20s", "SESSION", "LOCAL DIR", "REMOTE DIR", "MODE", "PROJECT", "PROGRESS"),
	))
	sb.WriteString("\n")

	items := m.buildSyncTree()
	for i, item := range items {
		var line string
		if item.IsSync {
			if item.PendingSync != nil {
				// Syncs recém-cadastrados cuja conexão/engine ainda estão sendo verificadas
				localDir := truncatePath(item.PendingSync.localDir, 18)
				remoteDir := truncatePath(item.PendingSync.remoteDir, 18)
				line = fmt.Sprintf("  %-8s %-18s %-18s %-10s %-12s %-20s",
					item.PendingSync.id,
					localDir,
					remoteDir,
					"verif...",
					"—",
					"verificando...",
				)
				pendingStyle := lipgloss.NewStyle().Foreground(styles.ColorDim).Italic(true)
				line = pendingStyle.Render(line)
			} else {
				localDir := truncatePath(item.Sync.LocalDir, 18)
				remoteDir := truncatePath(item.Sync.RemoteDir, 18)

				projectName := matchProjectName(m.projects, item.Sync.RemoteDir)
				if projectName == "" {
					projectName = "—"
				}

				// Pega o progresso, caso e se está pausado
				percent := 100.0
				caseType := 1
				isPaused := false
				conflicts := 0
				activeHostSyncs, exists := m.syncSessions[m.activeHost]
				if exists {
					sessCtx, exists := activeHostSyncs[item.Sync.ID]
					if exists && sessCtx.engine != nil {
						prog := sessCtx.engine.GetProgress()
						percent = prog.Percent
						caseType = prog.Case
						conflicts = prog.ConflictsResolved
						isPaused, _ = sessCtx.engine.IsPaused()
					}
				}

				barHtml := renderProgressBar(percent, caseType, 12)
				if isPaused {
					redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true)
					barHtml = redStyle.Render("🔒 Bloqueado (ver Projects)")
				} else {
					if info, hasInfo := m.gitInfo[item.Sync.RemoteDir]; hasInfo && info.IsGitRepo && info.Branch != "" {
						branchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")).Italic(true)
						barHtml = fmt.Sprintf("%s %s", barHtml, branchStyle.Render("("+info.Branch+")"))
					}
					if conflicts > 0 {
						conflictStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")).Bold(true)
						barHtml = fmt.Sprintf("%s %s", barHtml, conflictStyle.Render(fmt.Sprintf("⚡%d conflito(s)", conflicts)))
					}
				}

				indicator := "▸ "
				if m.expandedSyncs[item.Sync.ID] {
					indicator = "▾ "
				}

				line = fmt.Sprintf("  %s%-6s %-18s %-18s %-10s %-12s %s",
					indicator,
					item.Sync.ID,
					localDir,
					remoteDir,
					item.Sync.Mode,
					projectName,
					barHtml,
				)
			}
		} else if item.FileProgress != nil {
			indent := "      "
			icon := "⚙"
			statusText := ""
			statusStyle := lipgloss.NewStyle()

			switch item.FileProgress.Status {
			case internalsync.StatusSyncing:
				icon = "⏳"
				statusText = "SYNCING"
				statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF79C6")).Bold(true)
			case internalsync.StatusPending:
				icon = "💤"
				statusText = "PENDING"
				statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD"))
			case internalsync.StatusCompleted:
				icon = "✓"
				statusText = "DONE"
				statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B"))
			case internalsync.StatusFailed:
				icon = "✗"
				statusText = "FAILED"
				statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true)
			}

			var direction string
			switch item.FileProgress.Action {
			case "upload", "remote_delete":
				direction = "LOCAL->REMOTE"
			case "download", "local_delete":
				direction = "REMOTE->LOCAL"
			}

			actionStr := "enviando"
			if item.FileProgress.Action == "download" {
				actionStr = "baixando"
			} else if item.FileProgress.Action == "local_delete" || item.FileProgress.Action == "remote_delete" {
				actionStr = "deletando"
			}

			fileLine := fmt.Sprintf("%s└─ %s [%s] %s (%s): %s",
				indent,
				icon,
				statusStyle.Render(statusText),
				actionStr,
				direction,
				item.FileProgress.Path,
			)
			if item.FileProgress.Status == internalsync.StatusFailed && item.FileProgress.Error != nil {
				fileLine += fmt.Sprintf(" (%v)", item.FileProgress.Error)
			}
			line = fileLine
		}

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
