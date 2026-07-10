package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/compose"
	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/tunnel"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var composeCmd = &cobra.Command{
	Use:   "compose",
	Short: "Gerenciar o docker compose de um projeto cadastrado",
	Long: `Sobe, derruba e inspeciona o docker compose de um projeto cadastrado no host.

Por padrão roda no host remoto e cria túneis automáticos (ssh -L) para cada
porta publicada pelos serviços — as portas do projeto ficam acessíveis em
localhost como se rodassem na sua máquina. Com --local, roda o compose na
pasta local vinculada do projeto (sem túneis).

Exemplos:
  unlarp compose up api            # sobe remoto e tunela as portas publicadas
  unlarp compose up api --local    # sobe na pasta local vinculada
  unlarp compose ps api            # estado dos serviços
  unlarp compose logs api          # últimas linhas de log
  unlarp compose down api          # derruba os serviços`,
}

var composeUpCmd = &cobra.Command{
	Use:   "up <projeto> [host]",
	Short: "Subir os serviços do projeto (e tunelar portas se remoto)",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runComposeUp,
}

var composeDownCmd = &cobra.Command{
	Use:   "down <projeto> [host]",
	Short: "Derrubar os serviços do projeto",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runComposeDown,
}

var composePsCmd = &cobra.Command{
	Use:   "ps <projeto> [host]",
	Short: "Estado dos serviços do projeto",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runComposePs,
}

var composeLogsCmd = &cobra.Command{
	Use:   "logs <projeto> [host]",
	Short: "Últimas linhas de log dos serviços",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runComposeLogs,
}

var (
	composeLocal    bool
	composeNoTunnel bool
	composeTail     int
)

func init() {
	rootCmd.AddCommand(composeCmd)
	composeCmd.AddCommand(composeUpCmd)
	composeCmd.AddCommand(composeDownCmd)
	composeCmd.AddCommand(composePsCmd)
	composeCmd.AddCommand(composeLogsCmd)

	composeCmd.PersistentFlags().BoolVarP(&composeLocal, "local", "l", false, "rodar o compose na pasta local vinculada do projeto (sem túneis)")
	composeUpCmd.Flags().BoolVar(&composeNoTunnel, "no-tunnel", false, "não criar túneis automáticos para as portas publicadas")
	composeLogsCmd.Flags().IntVar(&composeTail, "tail", 100, "quantidade de linhas de log")
}

// resolveComposeProject resolve host + projeto pelos args do comando
func resolveComposeProject(args []string) (string, *config.Host, *config.Project, error) {
	hostName := ""
	if len(args) > 1 {
		hostName = args[1]
	}
	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return "", nil, nil, err
	}
	if hostName == "" {
		hostName = getActiveHost()
	}

	projName := args[0]
	for i := range hostCfg.Projects {
		if hostCfg.Projects[i].Name == projName {
			return hostName, hostCfg, &hostCfg.Projects[i], nil
		}
	}
	return "", nil, nil, fmt.Errorf("projeto '%s' não encontrado no host '%s' (cadastre pela TUI ou edite ~/.unlarp.yaml)", projName, hostName)
}

// runLocalCompose executa docker compose na pasta local vinculada do projeto
func runLocalCompose(proj *config.Project, args ...string) (string, error) {
	if proj.LocalDir == "" {
		return "", fmt.Errorf("projeto '%s' não tem pasta local vinculada (local_dir) para rodar com --local", proj.Name)
	}
	cargs := []string{"compose"}
	if proj.Compose != "" {
		cargs = append(cargs, "-f", proj.Compose)
	}
	cargs = append(cargs, args...)
	cmd := exec.Command("docker", cargs...)
	cmd.Dir = proj.LocalDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// composeExec roda uma ação do compose no lugar certo (remoto via SSH ou local)
func composeExec(client *internalssh.Client, proj *config.Project, action string) (string, error) {
	if composeLocal {
		return runLocalCompose(proj, strings.Fields(action)...)
	}
	stdout, stderr, err := client.RunCommand(compose.CommandFor(proj.RemotePath, proj.Compose, action))
	if err != nil {
		return stdout, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

// connectForCompose abre a conexão SSH quando a ação é remota (nil se --local)
func connectForCompose(hostName string, hostCfg *config.Host) (*internalssh.Client, error) {
	if composeLocal {
		return nil, nil
	}
	spin := ui.NewSpinner("Conectando a " + hostName + "...")
	spin.Start()
	client, err := internalssh.NewClient(hostCfg)
	if err == nil {
		err = client.Connect()
	}
	if err != nil {
		spin.StopWithError("Falha ao conectar")
		return nil, err
	}
	spin.StopWithSuccess("Conectado a " + hostName)
	return client, nil
}

func printComposeServices(svcs []compose.Service) {
	table := tablewriter.NewTable(os.Stdout)
	table.Header("SERVICE", "STATE", "PORTS")
	for _, s := range svcs {
		var ports []string
		for _, p := range s.Publishers {
			if p.PublishedPort > 0 {
				ports = append(ports, fmt.Sprintf("%d->%d/%s", p.PublishedPort, p.TargetPort, p.Protocol))
			}
		}
		portStr := strings.Join(ports, ", ")
		if portStr == "" {
			portStr = "—"
		}
		table.Append(s.Service, s.State, portStr)
	}
	table.Render()
}

func runComposeUp(cmd *cobra.Command, args []string) error {
	hostName, hostCfg, proj, err := resolveComposeProject(args)
	if err != nil {
		return err
	}

	client, err := connectForCompose(hostName, hostCfg)
	if err != nil {
		return err
	}
	if client != nil {
		defer func() {
			if client != nil {
				client.Close()
			}
		}()
	}

	where := "remoto"
	if composeLocal {
		where = "local"
	}
	spin := ui.NewSpinner(fmt.Sprintf("Subindo compose de '%s' (%s)...", proj.Name, where))
	spin.Start()
	if out, err := composeExec(client, proj, "up -d"); err != nil {
		spin.StopWithError("Falha no compose up")
		fmt.Println(strings.TrimSpace(out))
		return err
	}
	spin.StopWithSuccess("Serviços no ar")

	// Fixa restart policy nos containers para voltarem sozinhos após reboot do servidor
	if !composeLocal {
		if _, stderr, err := client.RunCommand(compose.EnsureRestart(proj.RemotePath, proj.Compose)); err != nil {
			ui.Warn("Não foi possível fixar restart policy: %v %s", err, strings.TrimSpace(stderr))
		}
	}

	// Descobre serviços/portas — com retries curtos, containers demoram a publicar
	var svcs []compose.Service
	for attempt := 0; attempt < 5; attempt++ {
		out, err := composeExec(client, proj, "ps --format json")
		if err == nil {
			if svcs, err = compose.ParsePS(out); err == nil && len(compose.PublishedPorts(svcs)) > 0 {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if len(svcs) > 0 {
		printComposeServices(svcs)
	}

	// Local ou sem túnel: portas já são locais / usuário não quer forward
	if composeLocal || composeNoTunnel {
		return nil
	}

	ports := compose.PublishedPorts(svcs)
	if len(ports) == 0 {
		ui.Warn("Nenhuma porta publicada detectada — nada para tunelar.")
		return nil
	}

	// Auto-tunnel: cada porta publicada vira um ssh -L porta->porta
	store := config.NewStore()
	cfg, _ := store.Load()
	mgr := tunnel.NewManager(client, hostCfg, hostName, cfg.Tunnel.AutoReconnect, cfg.Tunnel.ReconnectDelayDuration())
	sessMgr, _ := session.NewManager()
	origin := "compose:" + proj.Name

	var tunnelIDs []string
	for _, port := range ports {
		id, err := mgr.Add(port, port, tunnel.DirectionLocal)
		if err != nil {
			// Porta local ocupada não aborta o resto — avisa e segue
			ui.Warn("Porta %d não tunelada: %v", port, err)
			continue
		}
		tunnelIDs = append(tunnelIDs, id)
		ui.Success("Túnel %s: localhost:%d → %s:%d", id, port, hostName, port)
		if sessMgr != nil {
			sessMgr.AddTunnel(hostName, session.TunnelEntry{
				ID:         id,
				RemotePort: port,
				LocalPort:  port,
				Direction:  tunnel.DirectionLocal.String(),
				Origin:     origin,
			})
		}
	}

	if len(tunnelIDs) == 0 {
		ui.Warn("Nenhum túnel criado — serviços seguem rodando no remoto.")
		return nil
	}

	fmt.Println()
	ui.Dim("Túneis ativos (Ctrl+C encerra os túneis; os serviços continuam no remoto)")
	fmt.Println()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	printTunnelStatus(mgr)
	for {
		select {
		case <-sigCh:
			fmt.Println()
			ui.Info("Encerrando túneis...")
			mgr.Close()
			client.Close()
			client = nil
			if sessMgr != nil {
				for _, id := range tunnelIDs {
					sessMgr.RemoveTunnel(hostName, id)
				}
			}
			ui.Success("Túneis encerrados. Use 'unlarp compose down %s' para derrubar os serviços.", proj.Name)
			return nil
		case <-ticker.C:
			for range tunnelIDs {
				fmt.Print("\033[F\033[K")
			}
			printTunnelStatus(mgr)
		}
	}
}

func runComposeDown(cmd *cobra.Command, args []string) error {
	hostName, hostCfg, proj, err := resolveComposeProject(args)
	if err != nil {
		return err
	}

	client, err := connectForCompose(hostName, hostCfg)
	if err != nil {
		return err
	}
	if client != nil {
		defer client.Close()
	}

	spin := ui.NewSpinner(fmt.Sprintf("Derrubando compose de '%s'...", proj.Name))
	spin.Start()
	if out, err := composeExec(client, proj, "down"); err != nil {
		spin.StopWithError("Falha no compose down")
		fmt.Println(strings.TrimSpace(out))
		return err
	}
	spin.StopWithSuccess("Serviços derrubados")

	// Limpa do registro os túneis criados pelo compose up deste projeto.
	// Túneis vivos morrem com o processo do `compose up` (Ctrl+C lá).
	if sessMgr, _ := session.NewManager(); sessMgr != nil {
		origin := "compose:" + proj.Name
		if sess, ok := sessMgr.GetSession(hostName); ok {
			for _, t := range sess.Tunnels {
				if t.Origin == origin {
					sessMgr.RemoveTunnel(hostName, t.ID)
				}
			}
		}
	}

	return nil
}

func runComposePs(cmd *cobra.Command, args []string) error {
	hostName, hostCfg, proj, err := resolveComposeProject(args)
	if err != nil {
		return err
	}

	client, err := connectForCompose(hostName, hostCfg)
	if err != nil {
		return err
	}
	if client != nil {
		defer client.Close()
	}

	out, err := composeExec(client, proj, "ps --format json")
	if err != nil {
		return err
	}
	svcs, err := compose.ParsePS(out)
	if err != nil {
		return err
	}
	if len(svcs) == 0 {
		ui.Info("Nenhum serviço rodando para o projeto '%s'.", proj.Name)
		return nil
	}
	printComposeServices(svcs)
	return nil
}

func runComposeLogs(cmd *cobra.Command, args []string) error {
	hostName, hostCfg, proj, err := resolveComposeProject(args)
	if err != nil {
		return err
	}

	client, err := connectForCompose(hostName, hostCfg)
	if err != nil {
		return err
	}
	if client != nil {
		defer client.Close()
	}

	// ponytail: tail one-shot; logs -f (stream) quando houver demanda real
	out, err := composeExec(client, proj, fmt.Sprintf("logs --tail=%d", composeTail))
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimSpace(out))
	return nil
}
