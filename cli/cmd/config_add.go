package cmd

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var (
	addHost      string
	addPort      int
	addUser      string
	addKey       string
	addWorkspace string
	addContainer string
	addFromSSH   string
)

var configAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Adicionar um novo host",
	Long: `Adiciona um novo perfil de host ao arquivo de configuração.
	
Exemplos:
  unlarp config add local --host localhost --port 2222 --user root --workspace /workspace
  unlarp config add coolify --host 192.168.1.100 --port 2222 --user root --workspace /workspace
  unlarp config add myserver --from-ssh-config unlarp`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		host := config.Host{
			Host:      addHost,
			Port:      addPort,
			User:      addUser,
			Key:       addKey,
			Workspace: addWorkspace,
			Container: addContainer,
		}

		// Se --from-ssh-config foi especificado, preenche defaults do ~/.ssh/config
		if addFromSSH != "" {
			entry, err := internalssh.ReadSSHConfig(addFromSSH)
			if err != nil {
				ui.Warn("Não foi possível ler ~/.ssh/config: %v", err)
			} else {
				if host.Host == "" && entry.HostName != "" {
					host.Host = entry.HostName
				}
				if host.Port == 0 && entry.Port != "" {
					if p, err := strconv.Atoi(entry.Port); err == nil {
						host.Port = p
					}
				}
				if host.User == "" && entry.User != "" {
					host.User = entry.User
				}
				if host.Key == "" && entry.KeyFile != "" {
					host.Key = entry.KeyFile
				}
				ui.Info("Campos preenchidos a partir de ~/.ssh/config (Host: %s)", addFromSSH)
			}
		}

		// Defaults
		if host.Port == 0 {
			host.Port = 2222
		}
		if host.User == "" {
			host.User = "root"
		}
		if host.Workspace == "" {
			host.Workspace = "/workspace"
		}

		// Validação
		if err := host.Validate(); err != nil {
			return err
		}

		store := config.NewStore()
		if err := store.AddHost(name, host); err != nil {
			return err
		}

		ui.Success("Host '%s' adicionado (%s:%d)", name, host.Host, host.Port)

		// Verifica se é o primeiro host e informa que foi definido como default
		cfg, _ := store.Load()
		if cfg.DefaultHost == name {
			ui.Dim("Definido como host padrão")
		}

		return nil
	},
}

func init() {
	configCmd.AddCommand(configAddCmd)

	configAddCmd.Flags().StringVar(&addHost, "host", "", "endereço do host (hostname ou IP)")
	configAddCmd.Flags().IntVar(&addPort, "port", 0, "porta SSH (default: 2222)")
	configAddCmd.Flags().StringVar(&addUser, "user", "", "usuário SSH (default: root)")
	configAddCmd.Flags().StringVar(&addKey, "key", "", "caminho da chave SSH privada")
	configAddCmd.Flags().StringVar(&addWorkspace, "workspace", "", "diretório workspace remoto (default: /workspace)")
	configAddCmd.Flags().StringVar(&addContainer, "container", "", "nome do container Docker (para operações locais)")
	configAddCmd.Flags().StringVar(&addFromSSH, "from-ssh-config", "", "importar config de um Host em ~/.ssh/config (read-only)")
}
