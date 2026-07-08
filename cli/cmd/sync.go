package cmd

import (
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/ui"
	"github.com/CaioFaSoares/unlarp/internal/watcher"
)

var (
	syncLocalDir   string
	syncRemoteDir  string
	syncMode       string
	syncInit       string
	syncStopAll    bool
	syncInteractive bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sincronização bidirecional de arquivos",
	Long:  `Sincroniza arquivos em tempo real entre uma pasta local do seu Mac e o workspace remoto via SFTP.`,
}

var syncStartCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "Iniciar sincronização de arquivos em tempo real",
	Long: `Inicia o monitoramento e sincronização de arquivos. Por padrão roda em foreground.

Exemplos:
  unlarp sync start --local-dir . --remote-dir /workspace/meu-projeto
  unlarp sync start coolify-prod --local-dir ~/Projects/api --remote-dir /workspace/api
  unlarp sync start -i`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSyncStart,
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Mostrar status de sincronizações registradas",
	Aliases: []string{"ls"},
	RunE: runSyncStatus,
}

var syncStopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Parar sessões de sincronização",
	Long: `Para a sincronização de arquivos e remove o registro da sessão.
Use --all para parar todos os registros.`,
	RunE: runSyncStop,
}

func init() {
	rootCmd.AddCommand(syncCmd)
	syncCmd.AddCommand(syncStartCmd)
	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.AddCommand(syncStopCmd)

	// Flags para start
	syncStartCmd.Flags().StringVar(&syncLocalDir, "local-dir", "", "diretório local no Mac (default: diretório atual)")
	syncStartCmd.Flags().StringVar(&syncRemoteDir, "remote-dir", "", "diretório remoto no workspace")
	syncStartCmd.Flags().StringVar(&syncMode, "mode", "bidirectional", "modo: bidirectional | push | pull")
	syncStartCmd.Flags().StringVar(&syncInit, "initial-sync", "full", "sync inicial: full | none")
	syncStartCmd.Flags().BoolVarP(&syncInteractive, "interactive", "i", false, "iniciar seletor interativo de pastas")

	// Flags para stop
	syncStopCmd.Flags().BoolVar(&syncStopAll, "all", false, "parar todas as sessões de sync")
}

func runSyncStart(cmd *cobra.Command, args []string) error {
	hostName := ""
	if len(args) > 0 {
		hostName = args[0]
	}

	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return err
	}

	displayName := hostName
	if displayName == "" {
		displayName = getActiveHost()
	}

	// Verifica se deve rodar no modo interativo (caso as pastas não sejam fornecidas ou forçado por flag)
	isInteractive := syncInteractive || (syncLocalDir == "" && syncRemoteDir == "")

	var absLocal string
	if isInteractive {
		localStart := syncLocalDir
		if localStart == "" {
			localStart = "."
		}
		absLocal, err = ui.ChooseLocalDir(localStart)
		if err != nil {
			return err
		}
	} else {
		localPath := syncLocalDir
		if localPath == "" {
			localPath = "."
		}
		absLocal, err = filepath.Abs(localPath)
		if err != nil {
			return fmt.Errorf("caminho local inválido: %w", err)
		}
	}

	// Cria e configura o cliente SSH e SFTP
	spin := ui.NewSpinner("Conectando a " + displayName + " para sincronização...")
	spin.Start()

	sshClient, err := internalssh.NewClient(hostCfg)
	if err != nil {
		spin.StopWithError("Falha ao configurar conexão SSH")
		return err
	}

	if err := sshClient.Connect(); err != nil {
		spin.StopWithError("Falha ao conectar via SSH")
		return err
	}

	sftpClient, err := internalssh.NewSFTPClient(sshClient)
	if err != nil {
		sshClient.Close()
		spin.StopWithError("Falha ao inicializar SFTP")
		return err
	}
	spin.StopWithSuccess("Conectado a " + displayName)

	// Resolve remoteDir
	remoteDir := syncRemoteDir
	if isInteractive {
		remoteStart := syncRemoteDir
		if remoteStart == "" {
			remoteStart = hostCfg.Workspace
		}
		remoteDir, err = ui.ChooseRemoteDir(sftpClient.Inner(), remoteStart)
		if err != nil {
			sftpClient.Close()
			sshClient.Close()
			return err
		}
	}

	if remoteDir == "" {
		sftpClient.Close()
		sshClient.Close()
		return fmt.Errorf("diretório remoto é obrigatório")
	}

	// Carrega as configurações globais de sync
	store := config.NewStore()
	cfg, _ := store.Load()
	globalIgnores := cfg.Sync.IgnorePatterns
	pollInterval := cfg.Sync.PollIntervalDuration()

	syncID := generateSyncID()

	// Inicializa a Engine de Sincronização
	engine, err := internalsync.NewEngine(
		syncID,
		absLocal,
		remoteDir,
		displayName,
		globalIgnores,
		internalsync.ConflictStrategy(cfg.Sync.ConflictStrategy),
		sshClient,
		sftpClient.Inner(),
	)
	if err != nil {
		sftpClient.Close()
		sshClient.Close()
		return err
	}

	// Registra no session state
	sessMgr, _ := session.NewManager()
	if sessMgr != nil && displayName != "" {
		_ = sessMgr.AddSync(displayName, session.SyncEntry{
			ID:        syncID,
			LocalDir:  absLocal,
			RemoteDir: remoteDir,
			Mode:      syncMode,
			LastSync:  time.Now(),
		})
	}

	ui.Success("Sessão de sincronização %s criada", syncID)
	ui.Info("Local:  %s", absLocal)
	ui.Info("Remoto: %s", remoteDir)

	// Executa sincronização inicial completa se configurado
	if syncInit == "full" {
		spinSync := ui.NewSpinner("Executando sincronização inicial...")
		spinSync.Start()
		count, err := engine.SyncExec()
		if err != nil {
			spinSync.StopWithError("Erro na sincronização inicial: " + err.Error())
		} else {
			spinSync.StopWithSuccess(fmt.Sprintf("Sincronização inicial completa. %d alteração(ões) aplicada(s).", count))
		}
	}

	// Configura canal de trigger para a engine
	triggerSync := make(chan string, 1)

	// Goroutine principal do loop de sincronização com debouncer e lock interno
	go func() {
		for reason := range triggerSync {
			if verbose {
				ui.Dim("Iniciando sync disparado por: %s", reason)
			}
			count, err := engine.SyncExec()
			if err != nil {
				ui.Error("Erro ao sincronizar: %v", err)
			} else if count > 0 {
				ui.Success("Sincronizado: %d alteração(ões) propagada(s) (%s)", count, time.Now().Format("15:04:05"))
			}
		}
	}()

	// Inicia o Local Watcher (fsnotify)
	localWatcher, err := watcher.NewLocalWatcher(absLocal, 200*time.Millisecond, engine.IgnoreMatcher(), func(msg string) {
		ui.Warn("%s", msg)
	}, func() {
		select {
		case triggerSync <- "mudança local":
		default:
		}
	})
	if err != nil {
		sftpClient.Close()
		sshClient.Close()
		return fmt.Errorf("falha ao criar watcher local: %w", err)
	}

	err = localWatcher.Start()
	if err != nil {
		sftpClient.Close()
		sshClient.Close()
		return fmt.Errorf("falha ao iniciar watcher local: %w", err)
	}
	defer localWatcher.Stop()
	ui.Success("Monitorando diretório local recursivamente...")

	// Inicia o Remote Watcher (SFTP poll)
	// Como precisamos de um ignore matcher que seja gerado localmente, passamos o matcher da engine
	remoteWatcher := watcher.NewRemoteWatcher(remoteDir, sftpClient.Inner(), pollInterval, engine.IgnoreMatcher(), func() {
		select {
		case triggerSync <- "mudança remota":
		default:
		}
	})
	remoteWatcher.Start()
	defer remoteWatcher.Stop()
	ui.Success("Monitorando diretório remoto (polling %s)...", pollInterval)

	fmt.Println()
	ui.Warn("Mantenha este processo ativo para continuar sincronizando. Ctrl+C para parar.")
	fmt.Println()

	// Aguarda sinal de saída para encerrar
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println()
	ui.Info("Finalizando monitoramento e limpando recursos...")

	// Remove do session state ao parar
	if sessMgr != nil && displayName != "" {
		_ = sessMgr.RemoveSync(displayName, syncID)
	}

	close(triggerSync)
	sftpClient.Close()
	sshClient.Close()
	ui.Success("Sincronização parada com sucesso")

	return nil
}

func runSyncStatus(cmd *cobra.Command, args []string) error {
	sessMgr, err := session.NewManager()
	if err != nil {
		return err
	}

	sessions := sessMgr.ListSessions()
	hasSyncs := false

	table := tablewriter.NewTable(os.Stdout)
	table.Header("SESSION ID", "HOST", "LOCAL DIR", "REMOTE DIR", "MODE")

	for name, sess := range sessions {
		for _, s := range sess.Syncs {
			hasSyncs = true
			table.Append(s.ID, name, s.LocalDir, s.RemoteDir, s.Mode)
		}
	}

	if !hasSyncs {
		ui.Info("Nenhuma sessão de sincronização ativa registrada.")
		return nil
	}

	table.Render()
	return nil
}

func runSyncStop(cmd *cobra.Command, args []string) error {
	sessMgr, err := session.NewManager()
	if err != nil {
		return err
	}

	if syncStopAll {
		sessions := sessMgr.ListSessions()
		count := 0
		for name, sess := range sessions {
			for _, s := range sess.Syncs {
				_ = sessMgr.RemoveSync(name, s.ID)
				count++
			}
		}
		if count == 0 {
			ui.Info("Nenhuma sessão de sincronização registrada para parar.")
		} else {
			ui.Success("%d sessão(ões) de sincronização removida(s).", count)
		}
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("especifique o ID da sessão de sync ou use --all")
	}

	id := args[0]
	sessions := sessMgr.ListSessions()
	for name, sess := range sessions {
		for _, s := range sess.Syncs {
			if s.ID == id {
				_ = sessMgr.RemoveSync(name, id)
				ui.Success("Sessão de sincronização '%s' removida do registro.", id)
				return nil
			}
		}
	}

	return fmt.Errorf("sessão de sincronização '%s' não encontrada", id)
}

func generateSyncID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "s-" + string(b)
}
