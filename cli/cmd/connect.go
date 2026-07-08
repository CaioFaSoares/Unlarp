package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var connectShell string

var connectCmd = &cobra.Command{
	Use:   "connect [name]",
	Short: "Conectar ao workspace via SSH interativo",
	Long: `Abre uma sessão SSH interativa com o workspace remoto.
Sem argumento, conecta ao host padrão/ativo.

Exemplos:
  unlarp connect              # Conecta ao host padrão
  unlarp connect coolify-prod # Conecta ao perfil "coolify-prod"
  unlarp connect --shell zsh  # Força shell específico`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostName := ""
		if len(args) > 0 {
			hostName = args[0]
		}

		hostCfg, err := getHostConfig(hostName)
		if err != nil {
			return err
		}

		// Resolve nome para display
		displayName := hostName
		if displayName == "" {
			displayName = getActiveHost()
		}

		spin := ui.NewSpinner("Conectando a " + displayName + " (" + hostCfg.Address() + ")...")
		spin.Start()

		client, err := internalssh.NewClient(hostCfg)
		if err != nil {
			spin.StopWithError("Falha ao configurar conexão: " + err.Error())
			return err
		}

		if err := client.Connect(); err != nil {
			spin.StopWithError("Falha ao conectar: " + err.Error())
			return err
		}
		defer client.Close()

		spin.StopWithSuccess("Conectado a " + displayName)

		if connectUseTmux {
			// Verifica se tmux está instalado
			_, _, err := client.RunCommand("command -v tmux")
			if err != nil {
				ui.Dim("Tmux não encontrado no host remoto. Tentando instalar...")
				// Tenta instalar silenciosamente via apt-get ou apk
				_, _, _ = client.RunCommand("apt-get update && apt-get install -y tmux || apk add tmux")

				// Verifica novamente se agora existe
				_, _, err = client.RunCommand("command -v tmux")
				if err != nil {
					ui.Warn("Não foi possível instalar o Tmux no host remoto.")
					ui.Warn("Fazendo fallback automático para o shell comum (/bin/bash)...")
					connectUseTmux = false
				} else {
					ui.Success("Tmux instalado com sucesso no host remoto!")
				}
			}
		}

		shellCmd := connectShell
		if connectUseTmux {
			if connectTmuxSession == "" {
				connectTmuxSession = "unlarp"
			}
			newSessionFlags := ""
			if connectCwd != "" {
				newSessionFlags = " -c " + shellQuote(connectCwd)
			}
			// Garante a sessão em background, configura Ctrl+G como atalho de detach rápido sem prefixo, e atacha (com -u para forçar UTF-8 e status-bar customizada)
			shellCmd = fmt.Sprintf(
				"export LANG=C.UTF-8; export LC_ALL=C.UTF-8; "+
				"tmux -u new-session -d -s %[1]s%[2]s 2>/dev/null; "+
				"tmux -u bind-key -n C-g detach-client 2>/dev/null; "+
				"tmux -u set-option -t %[1]s status-left-length 50 2>/dev/null; "+
				"tmux -u set-option -t %[1]s status-left '#[fg=magenta,bold] unlarp #[fg=cyan]⬡ #[fg=white,bold]#S #[fg=cyan]⬢ ' 2>/dev/null; "+
				"tmux -u set-option -t %[1]s status-right '#[fg=magenta,bold]Ctrl+G: Detach #[fg=cyan]| #[fg=white]%%H:%%M ' 2>/dev/null; "+
				"tmux -u set-option -t %[1]s status-style 'bg=black,fg=white' 2>/dev/null; "+
				"tmux -u set-option -t %[1]s window-status-current-style 'fg=magenta,bold' 2>/dev/null; "+
				"tmux -u attach-session -t %[1]s",
				connectTmuxSession, newSessionFlags,
			)
			ui.Dim("Iniciando Tmux (Sessão: %s)...", connectTmuxSession)
			ui.Dim("Use Ctrl+G para desanexar de forma rápida (ou o clássico Ctrl+B d)")
		} else {
			ui.Dim("Pressione 'exit' ou Ctrl+D para desconectar")
		}
		fmt.Println()

		return client.InteractiveShell(shellCmd)
	},
}

var (
	connectUseTmux     bool
	connectTmuxSession string
	connectCwd         string
)

func init() {
	rootCmd.AddCommand(connectCmd)

	connectCmd.Flags().StringVar(&connectShell, "shell", "", "shell a ser usado (default: /bin/bash)")
	connectCmd.Flags().BoolVarP(&connectUseTmux, "tmux", "t", false, "iniciar sessão dentro do Tmux persistente")
	connectCmd.Flags().StringVar(&connectTmuxSession, "tmux-session", "unlarp", "nome da sessão Tmux a ser usada")
	connectCmd.Flags().StringVar(&connectCwd, "cwd", "", "diretório de trabalho inicial da sessão Tmux (apenas na criação)")
}

// shellQuote coloca uma string entre aspas simples seguras para uso em shell POSIX
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
