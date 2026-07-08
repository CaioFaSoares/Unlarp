package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var statusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Health check do workspace remoto",
	Long: `Mostra o status de conexão e informações do workspace remoto.
Sem argumento, mostra todos os hosts configurados.

Exemplos:
  unlarp status              # Status de todos os hosts
  unlarp status local        # Status de um host específico`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore()
		cfg, err := store.Load()
		if err != nil {
			return err
		}

		if len(cfg.Hosts) == 0 {
			ui.Warn("Nenhum host configurado.")
			return nil
		}

		// Se especificou um host, mostra só ele
		if len(args) > 0 {
			name := args[0]
			host, ok := cfg.Hosts[name]
			if !ok {
				return fmt.Errorf("host '%s' não encontrado", name)
			}
			return showHostStatus(name, &host, cfg.DefaultHost == name)
		}

		// Mostra todos
		for name, host := range cfg.Hosts {
			h := host // Evita captura por referência
			isDefault := cfg.DefaultHost == name
			if err := showHostStatus(name, &h, isDefault); err != nil {
				// Não interrompe por causa de um host offline
				continue
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func showHostStatus(name string, host *config.Host, isActive bool) error {
	activeLabel := ""
	if isActive {
		activeLabel = " — ACTIVE"
	}

	icon := ui.IconCircle
	if isActive {
		icon = ui.IconDot
	}

	fmt.Printf("\n  %s %s (%s)%s\n", icon, name, host.Address(), activeLabel)

	// Testa conectividade TCP
	latency, err := internalssh.TestConnection(host)
	if err != nil {
		ui.StatusLine("SSH:", "Não acessível", false)
		return nil
	}
	ui.StatusLine("SSH:", fmt.Sprintf("Acessível (latência: %s)", latency.Round(1e6)), true)

	// Tenta conectar SSH completo e obter informações
	client, err := internalssh.NewClient(host)
	if err != nil {
		ui.StatusLine("Auth:", "Erro ao configurar autenticação", false)
		return nil
	}

	if err := client.Connect(); err != nil {
		ui.StatusLine("Auth:", "Falha na autenticação SSH", false)
		return nil
	}
	defer client.Close()

	ui.StatusLine("Auth:", "Conectado", true)

	// Docker
	dockerOut, _, dockerErr := client.RunCommand("docker --version 2>/dev/null")
	if dockerErr == nil && dockerOut != "" {
		version := strings.TrimSpace(dockerOut)
		// Extrai apenas a versão
		if parts := strings.Split(version, ","); len(parts) > 0 {
			version = strings.TrimPrefix(parts[0], "Docker version ")
		}
		ui.StatusLine("Docker:", fmt.Sprintf("Disponível (%s)", version), true)
	} else {
		ui.StatusLine("Docker:", "Não disponível", false)
	}

	// Nix
	nixOut, _, nixErr := client.RunCommand("nix --version 2>/dev/null")
	if nixErr == nil && nixOut != "" {
		version := strings.TrimSpace(nixOut)
		ui.StatusLine("Nix:", fmt.Sprintf("Disponível (%s)", version), true)
	} else {
		ui.StatusLine("Nix:", "Não disponível", false)
	}

	// Disco
	diskOut, _, diskErr := client.RunCommand(fmt.Sprintf("df -h %s 2>/dev/null | tail -1 | awk '{print $4}'", host.Workspace))
	if diskErr == nil && diskOut != "" {
		ui.StatusLine("Disco:", fmt.Sprintf("%s livre em %s", strings.TrimSpace(diskOut), host.Workspace), true)
	}

	// Uptime
	uptimeOut, _, uptimeErr := client.RunCommand("uptime -p 2>/dev/null || uptime")
	if uptimeErr == nil && uptimeOut != "" {
		ui.StatusLine("Uptime:", strings.TrimSpace(uptimeOut), true)
	}

	fmt.Println()
	return nil
}
