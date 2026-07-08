package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

// renderOnboarding desenha a tela de onboarding sequencial
func (m *AppModel) renderOnboarding(width, height int) string {
	var sb strings.Builder

	// Centralização
	boxWidth := 60
	boxHeight := 15

	title := styles.TitleStyle.Render("UNLARP — Assistente de Configuração")
	
	var content string
	var stepText string

	switch m.onboardingStep {
	case 0:
		stepText = "Boas-vindas"
		if len(m.hostNames) > 0 {
			content = "Adicionar Novo Host ao Unlarp!\n\nEste assistente irá guiar você para configurar e registrar\num novo workspace de desenvolvimento remoto.\n\nPressione [Enter] para começar ou [Esc] para voltar."
		} else {
			content = "Bem-vindo ao Unlarp!\n\nNão detectamos nenhum host configurado.\nEste assistente irá guiar você para conectar e sincronizar\nseu workspace de desenvolvimento remoto.\n\nPressione [Enter] para começar."
		}
	case 1:
		stepText = "Etapa 1 de 6: Nome do Perfil"
		content = "Digite um nome/alias para este host (ex: coolify-prod):\n\n" + m.textInput.View()
	case 2:
		stepText = "Etapa 2 de 6: Endereço do Host"
		content = "Digite o endereço IP ou domínio do servidor remoto:\n\n" + m.textInput.View()
	case 3:
		stepText = "Etapa 3 de 6: Porta SSH"
		content = "Digite a porta SSH (default 2222 para DinD):\n\n" + m.textInput.View()
	case 4:
		stepText = "Etapa 4 de 6: Usuário SSH"
		content = "Digite o usuário SSH (default root):\n\n" + m.textInput.View()
	case 5:
		stepText = "Etapa 5 de 6: Diretório Workspace"
		content = "Digite o caminho do diretório workspace no servidor\n(default /workspace):\n\n" + m.textInput.View()
	case 6:
		stepText = "Etapa 6 de 6: Chave SSH"
		content = "Gostaria de injetar sua chave SSH pública local para login sem senha?\nDigite s (sim) ou n (não):\n\n" + m.textInput.View()
	case 7:
		stepText = "Processando..."
		content = fmt.Sprintf("Conectando ao host e configurando o workspace...\n\n%s Por favor, aguarde.", m.spinner.View())
	case 8:
		stepText = "Onboarding Completo"
		content = "Configuração concluída com sucesso!\n\nSeu host foi salvo em ~/.unlarp.yaml.\n\nPressione [Enter] para acessar a dashboard principal."
	case 9:
		stepText = "Autenticação por Senha da VPS"
		content = "A conexão por chave SSH à VPS falhou.\nPor favor, digite a senha SSH da VPS para injetar a chave automaticamente:\n\n" + m.textInput.View()
	}

	// Moldura do onboarding
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorSecondary).
		Padding(1, 2).
		Width(boxWidth).
		Height(boxHeight)

	boxContent := lipgloss.JoinVertical(
		lipgloss.Left,
		styles.HostActiveStyle.Render(stepText),
		"",
		content,
	)

	renderedBox := boxStyle.Render(boxContent)

	// Centraliza o box horizontal e verticalmente na tela
	topPadding := (height - boxHeight) / 2
	leftPadding := (width - boxWidth) / 2

	if topPadding < 0 {
		topPadding = 0
	}
	if leftPadding < 0 {
		leftPadding = 0
	}

	sb.WriteString(strings.Repeat("\n", topPadding))
	
	lines := strings.Split(renderedBox, "\n")
	for _, line := range lines {
		sb.WriteString(strings.Repeat(" ", leftPadding))
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		sb.String(),
	)
}
