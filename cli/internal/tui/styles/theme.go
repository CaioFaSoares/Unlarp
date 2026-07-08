package styles

import "github.com/charmbracelet/lipgloss"

// Temas de cores do Unlarp TUI
var (
	ColorPrimary   = lipgloss.Color("86")  // Cyan/Teal
	ColorSecondary = lipgloss.Color("99")  // Purple
	ColorSuccess   = lipgloss.Color("78")  // Verde
	ColorWarning   = lipgloss.Color("220") // Amarelo
	ColorError     = lipgloss.Color("203") // Vermelho/Coral
	ColorDim       = lipgloss.Color("242") // Cinza escuro
	ColorWhite     = lipgloss.Color("255") // Branco
	ColorFocus     = lipgloss.Color("81")  // Light Blue
)

// Estilos de componentes Lip Gloss
var (
	// Layout Geral
	AppStyle = lipgloss.NewStyle().
			Padding(0, 1)

	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Padding(0, 1)

	// Barra Lateral (Sidebar)
	SidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(0, 1)

	SidebarFocusedStyle = SidebarStyle.
				BorderForeground(ColorFocus)

	SidebarTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorWhite).
				MarginBottom(1)

	HostActiveStyle = lipgloss.NewStyle().
			Foreground(ColorSuccess).
			Bold(true)

	HostIdleStyle = lipgloss.NewStyle().
			Foreground(ColorWhite)

	HostSelectedStyle = lipgloss.NewStyle().
				Background(ColorSecondary).
				Foreground(ColorWhite).
				Bold(true)

	// Painel Principal
	MainPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(0, 1)

	MainPanelFocusedStyle = MainPanelStyle.
				BorderForeground(ColorPrimary)

	// Abas (Tabs)
	TabRowStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(ColorDim).
			MarginBottom(1)

	TabStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(ColorDim)

	TabActiveStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true).
			Foreground(ColorPrimary).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(ColorPrimary)

	// Status e Tabelas
	StatusLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorSecondary).
				Width(12)

	StatusValueStyle = lipgloss.NewStyle().
				Foreground(ColorWhite)

	TableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary).
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(ColorDim)

	TableCellStyle = lipgloss.NewStyle().
			Padding(0, 1)

	TableCellSelectedStyle = TableCellStyle.
				Background(ColorPrimary).
				Foreground(lipgloss.Color("0")).
				Bold(true)

	// Barra de Ajuda / Rodapé
	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorDim).
			Padding(0, 1).
			MarginTop(1)

	KeyStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)
)
