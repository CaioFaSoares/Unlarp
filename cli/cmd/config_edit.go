package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var (
	editHost      string
	editPort      int
	editUser      string
	editKey       string
	editWorkspace string
)

var configEditCmd = &cobra.Command{
	Use:   "edit [name]",
	Short: "Editar um host",
	Long: `Edita um perfil de host existente. Sem flags, abre o arquivo de configuração no editor.
Com flags, edita campos específicos inline.

Exemplos:
  unlarp config edit                      # Abre ~/.unlarp.yaml no $EDITOR
  unlarp config edit local --port 2223    # Altera porta do host "local"`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore()

		// Se sem argumentos, abre o editor
		if len(args) == 0 {
			return openEditor(store.Path())
		}

		name := args[0]
		cfg, err := store.Load()
		if err != nil {
			return err
		}

		host, ok := cfg.Hosts[name]
		if !ok {
			return fmt.Errorf("host '%s' não encontrado", name)
		}

		// Aplica apenas os campos que foram explicitamente setados
		changed := false
		if cmd.Flags().Changed("host") {
			host.Host = editHost
			changed = true
		}
		if cmd.Flags().Changed("port") {
			host.Port = editPort
			changed = true
		}
		if cmd.Flags().Changed("user") {
			host.User = editUser
			changed = true
		}
		if cmd.Flags().Changed("key") {
			host.Key = editKey
			changed = true
		}
		if cmd.Flags().Changed("workspace") {
			host.Workspace = editWorkspace
			changed = true
		}

		if !changed {
			ui.Info("Nenhum campo alterado. Use flags como --host, --port, etc.")
			return nil
		}

		if err := store.UpdateHost(name, host); err != nil {
			return err
		}

		ui.Success("Host '%s' atualizado", name)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configEditCmd)

	configEditCmd.Flags().StringVar(&editHost, "host", "", "endereço do host")
	configEditCmd.Flags().IntVar(&editPort, "port", 0, "porta SSH")
	configEditCmd.Flags().StringVar(&editUser, "user", "", "usuário SSH")
	configEditCmd.Flags().StringVar(&editKey, "key", "", "caminho da chave SSH")
	configEditCmd.Flags().StringVar(&editWorkspace, "workspace", "", "diretório workspace remoto")
}

// openEditor abre um arquivo no editor padrão do sistema
func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
