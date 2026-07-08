package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/tunnel"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var tunnelBackground bool
var tunnelLocal bool

var tunnelCmd = &cobra.Command{
	Use:   "tunnel <portas> [name]",
	Short: "Criar túnel SSH (port forwarding)",
	Long: `Cria túneis SSH para encaminhar portas entre sua máquina local e o workspace remoto.
Substitui o script tunnel.sh com suporte a múltiplos túneis simultâneos e reconexão automática.

Por padrão o túnel escuta no host REMOTO e encaminha para sua máquina local
(equivalente a "ssh -R"), útil para expor um serviço local (ex: dev server)
através do host remoto. Use --local para inverter: escuta na sua máquina e
encaminha para o host remoto (equivalente a "ssh -L"), útil para acessar um
serviço que já roda remotamente (ex: Postgres num container).

Sintaxe de portas:
  5432         — Porta remota 5432 ↔ local 5432 (mesma porta)
  3000:8080    — Porta remota 3000 ↔ local 8080
  5432,3000    — Múltiplas portas de uma vez

Exemplos:
  unlarp tunnel 3000                    # Dev server local:3000 exposto no remoto:3000
  unlarp tunnel 5432 --local            # Postgres remoto:5432 acessível em localhost:5432
  unlarp tunnel 3000:8080               # Remoto 3000 ↔ local 8080
  unlarp tunnel 5432,3000,6379          # Múltiplos túneis
  unlarp tunnel 5432 coolify-prod       # Host específico
  unlarp tunnel 5432 -b                 # Background mode`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runTunnel,
}

var tunnelListCmd = &cobra.Command{
	Use:   "list",
	Short: "Listar túneis ativos",
	Aliases: []string{"ls"},
	RunE: runTunnelList,
}

var tunnelStopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Parar túneis",
	Long: `Para um túnel específico pelo ID ou todos os túneis com --all.

Exemplos:
  unlarp tunnel stop t-a1b2    # Para um túnel específico
  unlarp tunnel stop --all     # Para todos`,
	RunE: runTunnelStop,
}

var tunnelStopAll bool

func init() {
	rootCmd.AddCommand(tunnelCmd)
	tunnelCmd.AddCommand(tunnelListCmd)
	tunnelCmd.AddCommand(tunnelStopCmd)

	tunnelCmd.Flags().BoolVarP(&tunnelBackground, "background", "b", false, "rodar em background")
	tunnelCmd.Flags().BoolVarP(&tunnelLocal, "local", "l", false, "escutar na máquina local e encaminhar para o host remoto (ssh -L), em vez do padrão (ssh -R)")
	tunnelStopCmd.Flags().BoolVar(&tunnelStopAll, "all", false, "parar todos os túneis")
}

// portMapping representa um mapeamento de porta parseado
type portMapping struct {
	RemotePort int
	LocalPort  int
}

// parsePortMappings parseia a string de portas: "5432", "3000:8080", "5432,3000:8080"
func parsePortMappings(input string) ([]portMapping, error) {
	var mappings []portMapping

	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, ":") {
			// Formato remote:local
			sides := strings.SplitN(part, ":", 2)
			remote, err := strconv.Atoi(sides[0])
			if err != nil {
				return nil, fmt.Errorf("porta remota inválida '%s': %w", sides[0], err)
			}
			local, err := strconv.Atoi(sides[1])
			if err != nil {
				return nil, fmt.Errorf("porta local inválida '%s': %w", sides[1], err)
			}
			mappings = append(mappings, portMapping{RemotePort: remote, LocalPort: local})
		} else {
			// Formato simples: mesma porta
			port, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("porta inválida '%s': %w", part, err)
			}
			mappings = append(mappings, portMapping{RemotePort: port, LocalPort: port})
		}
	}

	if len(mappings) == 0 {
		return nil, fmt.Errorf("nenhuma porta especificada")
	}

	return mappings, nil
}

func runTunnel(cmd *cobra.Command, args []string) error {
	// Parseia portas
	mappings, err := parsePortMappings(args[0])
	if err != nil {
		return err
	}

	// Determina host
	hostName := ""
	if len(args) > 1 {
		hostName = args[1]
	}

	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return err
	}

	displayName := hostName
	if displayName == "" {
		displayName = getActiveHost()
	}

	// Lê config de tunnel para auto_reconnect
	store := config.NewStore()
	cfg, _ := store.Load()
	autoReconnect := cfg.Tunnel.AutoReconnect
	reconnectDelay := cfg.Tunnel.ReconnectDelayDuration()

	// Conecta SSH
	spin := ui.NewSpinner("Conectando a " + displayName + "...")
	spin.Start()

	sshClient, err := internalssh.NewClient(hostCfg)
	if err != nil {
		spin.StopWithError("Falha ao configurar conexão")
		return err
	}

	if err := sshClient.Connect(); err != nil {
		spin.StopWithError("Falha ao conectar")
		return err
	}
	spin.StopWithSuccess("Conectado a " + displayName)

	// Cria tunnel manager
	mgr := tunnel.NewManager(sshClient, hostCfg, displayName, autoReconnect, reconnectDelay)

	// Session manager para persistência
	sessMgr, _ := session.NewManager()

	direction := tunnel.DirectionRemote
	if tunnelLocal {
		direction = tunnel.DirectionLocal
	}

	// Inicia túneis
	var tunnelIDs []string
	for _, m := range mappings {
		id, err := mgr.Add(m.LocalPort, m.RemotePort, direction)
		if err != nil {
			ui.Error("Falha ao criar túnel %d → %d: %v", m.RemotePort, m.LocalPort, err)
			continue
		}

		tunnelIDs = append(tunnelIDs, id)
		if direction == tunnel.DirectionLocal {
			ui.Success("Túnel %s: localhost:%d → workspace:%d", id, m.LocalPort, m.RemotePort)
		} else {
			ui.Success("Túnel %s: workspace:%d → localhost:%d", id, m.RemotePort, m.LocalPort)
		}

		// Registra no session state
		if sessMgr != nil && displayName != "" {
			sessMgr.AddTunnel(displayName, session.TunnelEntry{
				ID:         id,
				RemotePort: m.RemotePort,
				LocalPort:  m.LocalPort,
				Direction:  direction.String(),
			})
		}
	}

	if len(tunnelIDs) == 0 {
		sshClient.Close()
		return fmt.Errorf("nenhum túnel foi criado")
	}

	if tunnelBackground {
		// Background mode: retorna IDs e mantém rodando
		fmt.Println()
		ui.Info("Túneis rodando em background. Use 'unlarp tunnel list' para ver status.")
		ui.Dim("Para encerrar: unlarp tunnel stop --all")
		// Nota: em background real precisaríamos de um daemon — por agora mantém o processo
		// mas retorna o controle. Em implementação futura (Fase 5) teremos daemon mode.
		select {} // Bloqueia para manter as goroutines vivas
	}

	// Foreground mode: mostra status e espera Ctrl+C
	fmt.Println()
	ui.Dim("Túneis ativos (Ctrl+C para encerrar)")
	fmt.Println()

	// Mostra status periodicamente
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Mostra status inicial
	printTunnelStatus(mgr)

	for {
		select {
		case <-sigCh:
			fmt.Println()
			ui.Info("Encerrando túneis...")
			mgr.Close()
			sshClient.Close()

			// Remove do session state
			if sessMgr != nil && displayName != "" {
				for _, id := range tunnelIDs {
					sessMgr.RemoveTunnel(displayName, id)
				}
			}

			ui.Success("Todos os túneis encerrados")
			return nil

		case <-ticker.C:
			// Atualiza status
			fmt.Print("\033[F\033[K") // Move cursor para cima e limpa (por túnel)
			for range mappings {
				fmt.Print("\033[F\033[K")
			}
			printTunnelStatus(mgr)
		}
	}
}

func printTunnelStatus(mgr *tunnel.Manager) {
	infos := mgr.List()
	for _, info := range infos {
		statusIcon := "●"
		statusColor := "\033[32m" // Verde
		if info.Status == tunnel.StatusReconnecting {
			statusIcon = "◐"
			statusColor = "\033[33m" // Amarelo
		} else if info.Status == tunnel.StatusError || info.Status == tunnel.StatusStopped {
			statusIcon = "○"
			statusColor = "\033[31m" // Vermelho
		}

		totalBytes := tunnel.FormatBytes(info.BytesIn + info.BytesOut)
		fmt.Printf("   %s%s\033[0m %d → %d (%s)  %d conn  %s\n",
			statusColor, statusIcon,
			info.RemotePort, info.LocalPort, info.Direction,
			info.Connections, totalBytes,
		)
	}
}

func runTunnelList(cmd *cobra.Command, args []string) error {
	sessMgr, err := session.NewManager()
	if err != nil {
		return err
	}

	sessions := sessMgr.ListSessions()
	hasTunnels := false

	table := tablewriter.NewTable(os.Stdout)
	table.Header("ID", "HOST", "MAPPING", "DIRECTION", "STATUS")

	for name, sess := range sessions {
		for _, t := range sess.Tunnels {
			hasTunnels = true
			mapping := fmt.Sprintf("%d → %d", t.RemotePort, t.LocalPort)
			direction := t.Direction
			if direction == "" {
				direction = tunnel.DirectionRemote.String()
			}
			table.Append(t.ID, name, mapping, direction, "● Registered")
		}
	}

	if !hasTunnels {
		ui.Info("Nenhum túnel registrado. Use 'unlarp tunnel <portas>' para criar.")
		return nil
	}

	table.Render()
	return nil
}

func runTunnelStop(cmd *cobra.Command, args []string) error {
	sessMgr, err := session.NewManager()
	if err != nil {
		return err
	}

	if tunnelStopAll {
		sessions := sessMgr.ListSessions()
		count := 0
		for name, sess := range sessions {
			for _, t := range sess.Tunnels {
				sessMgr.RemoveTunnel(name, t.ID)
				count++
			}
		}
		if count == 0 {
			ui.Info("Nenhum túnel para encerrar")
		} else {
			ui.Success("%d túnel(is) removido(s) do registro", count)
		}
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("especifique o ID do túnel ou use --all")
	}

	id := args[0]
	sessions := sessMgr.ListSessions()
	for name, sess := range sessions {
		for _, t := range sess.Tunnels {
			if t.ID == id {
				sessMgr.RemoveTunnel(name, id)
				ui.Success("Túnel '%s' removido (%d → %d)", id, t.RemotePort, t.LocalPort)
				return nil
			}
		}
	}

	return fmt.Errorf("túnel '%s' não encontrado", id)
}
