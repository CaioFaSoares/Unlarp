package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/tui"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Interface interativa baseada em terminal",
	Long:  `Abre um painel interativo (TUI) para gerenciar todas as sessões, monitorar logs e status.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		model, err := tui.NewAppModel()
		if err != nil {
			return err
		}

		p := tea.NewProgram(model, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			return fmt.Errorf("erro ao iniciar a TUI: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
