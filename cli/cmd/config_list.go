package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Listar hosts configurados",
	Long:  `Lista todos os perfis de hosts configurados em ~/.unlarp.yaml.`,
	Aliases: []string{"ls"},
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore()
		cfg, err := store.Load()
		if err != nil {
			return err
		}

		if len(cfg.Hosts) == 0 {
			ui.Warn("Nenhum host configurado. Use 'unlarp config add' para adicionar um host.")
			return nil
		}

		// Ordena nomes para output consistente
		names := make([]string, 0, len(cfg.Hosts))
		for name := range cfg.Hosts {
			names = append(names, name)
		}
		sort.Strings(names)

		table := tablewriter.NewTable(os.Stdout)
		table.Header("NAME", "HOST", "PORT", "USER", "WORKSPACE", "STATUS")

		for _, name := range names {
			host := cfg.Hosts[name]
			status := "○ idle"
			if name == cfg.DefaultHost {
				status = "● default"
			}

			table.Append(
				name,
				host.Host,
				fmt.Sprintf("%d", host.Port),
				host.User,
				host.Workspace,
				status,
			)
		}

		table.Render()
		return nil
	},
}

func init() {
	configCmd.AddCommand(configListCmd)
}
