package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var useCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Trocar a sessão/host ativo",
	Long: `Define qual host/sessão é o padrão para comandos subsequentes.

Exemplos:
  unlarp use local          # Define "local" como host ativo
  unlarp use coolify-prod   # Troca para "coolify-prod"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// Verifica se o host existe na config
		store := config.NewStore()
		cfg, err := store.Load()
		if err != nil {
			return err
		}

		if _, ok := cfg.Hosts[name]; !ok {
			return fmt.Errorf("host '%s' não encontrado. Use 'unlarp config list' para ver hosts disponíveis", name)
		}

		// Atualiza default na config
		if err := store.SetDefault(name); err != nil {
			return err
		}

		// Atualiza sessão ativa
		mgr, err := session.NewManager()
		if err != nil {
			ui.Warn("Não foi possível atualizar estado da sessão: %v", err)
		} else {
			mgr.SetActive(name)
		}

		host := cfg.Hosts[name]
		ui.Success("Sessão ativa: %s (%s)", name, host.Address())

		return nil
	},
}

func init() {
	rootCmd.AddCommand(useCmd)
}
