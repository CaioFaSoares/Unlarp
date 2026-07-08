package cmd

import (
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Gerenciar hosts e configurações",
	Long:  `Gerencia os perfis de hosts remotos, incluindo adicionar, listar, editar e remover workspaces.`,
}

func init() {
	rootCmd.AddCommand(configCmd)
}
