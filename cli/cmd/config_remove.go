package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var configRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remover um host",
	Long:  `Remove um perfil de host do arquivo de configuração.`,
	Aliases: []string{"rm"},
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store := config.NewStore()
		if err := store.RemoveHost(name); err != nil {
			return err
		}

		ui.Success("Host '%s' removido", name)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configRemoveCmd)
}
