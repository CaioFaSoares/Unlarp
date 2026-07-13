package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/daemon"
	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Rodar o daemon local do unlarp em foreground",
	Long: `O daemon local hospeda as engines de sync (e, futuramente, os túneis)
num processo único que sobrevive ao fechamento do terminal/TUI — opt-in via
--daemon em 'sync start' ou pelo toggle de config na TUI. Normalmente é
subido automaticamente (auto-start); rodar 'unlarp daemon' diretamente é
útil sob um supervisor de processos.`,
	RunE: runDaemon,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Parar o daemon local",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Inspecionar o daemon local (versão, uptime, syncs ativos)",
	RunE:  runDaemonStatus,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
}

// readPID lê o PID file e diz se o processo ainda está vivo (signal 0) —
// mesma técnica de session.SyncEntry.Alive().
func readPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	return pid, proc.Signal(syscall.Signal(0)) == nil
}

func runDaemon(cmd *cobra.Command, args []string) error {
	pidPath, err := daemonapi.PIDPath()
	if err != nil {
		return err
	}
	if pid, alive := readPID(pidPath); alive {
		return fmt.Errorf("daemon já está rodando (pid %d)", pid)
	}

	d, err := daemon.New()
	if err != nil {
		return fmt.Errorf("erro ao iniciar daemon: %w", err)
	}
	srv, err := daemon.NewServer(d)
	if err != nil {
		return fmt.Errorf("erro ao escutar no socket: %w", err)
	}

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return fmt.Errorf("erro ao escrever pid file: %w", err)
	}
	defer os.Remove(pidPath)

	d.AdoptOrphans()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	shuttingDown := make(chan struct{})
	go func() {
		<-sigCh
		close(shuttingDown)
		d.Shutdown()
		srv.Close()
	}()

	fmt.Printf("unlarp daemon %s (protocol %d) pid %d\n", daemonapi.Version, daemonapi.Protocol, os.Getpid())
	err = srv.Serve()
	select {
	case <-shuttingDown:
		return nil
	default:
		return err
	}
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	pidPath, err := daemonapi.PIDPath()
	if err != nil {
		return err
	}
	pid, alive := readPID(pidPath)
	if !alive {
		ui.Info("daemon não está rodando")
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("erro ao sinalizar pid %d: %w", pid, err)
	}

	sockPath, err := daemonapi.SocketPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	ui.Success("daemon parado (pid %d)", pid)
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	client, err := daemon.NewClient()
	if err != nil {
		return err
	}
	info, err := client.Info(2 * time.Second)
	if err != nil {
		ui.Warn("daemon não está rodando ou não responde: %v", err)
		return nil
	}
	ui.Success("unlarp daemon v%s (protocol %d) pid %d, uptime %ds, %d sync(s)",
		info.Version, info.Protocol, info.PID, info.UptimeSeconds, info.SyncCount)
	return nil
}
