package cmd

import (
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/ui"
	"github.com/CaioFaSoares/unlarp/internal/watcher"
)

var (
	syncLocalDir    string
	syncRemoteDir   string
	syncMode        string
	syncInit        string
	syncStopAll     bool
	syncInteractive bool
	syncProject     string
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
	Use:     "status",
	Short:   "Mostrar status de sincronizações registradas",
	Aliases: []string{"ls"},
	RunE:    runSyncStatus,
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
	syncStartCmd.Flags().StringVar(&syncProject, "project", "", "nome do projeto cadastrado dono deste sync (marca o sync como sync de projeto)")

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

	// --project: valida contra os projetos cadastrados e, sem --remote-dir,
	// usa o caminho remoto do projeto como default.
	if syncProject != "" {
		found := false
		for _, p := range hostCfg.Projects {
			if p.Name == syncProject {
				found = true
				if syncRemoteDir == "" {
					syncRemoteDir = p.RemotePath
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("projeto '%s' não cadastrado no host %s (veja ~/.unlarp.yaml)", syncProject, displayName)
		}
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

	// Colisão de pasta local: dois syncs (mesmo de hosts diferentes) escrevendo
	// na mesma árvore local corromperiam a reconciliação
	if mgr, mgrErr := session.NewManager(); mgrErr == nil {
		if otherHost, other, found := mgr.FindSyncByLocalDir(absLocal, ""); found {
			return fmt.Errorf("a pasta local %s colide com o sync %s do host %s (%s) — pare-o antes com `unlarp sync stop %s`", absLocal, other.ID, otherHost, other.LocalDir, other.ID)
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

	if engine.StateWarning != "" {
		ui.Warn("%s", engine.StateWarning)
	}
	engine.OnConflict = func(path string, winner string) {
		ui.Warn("Conflito em %s — venceu %s (%s); trilha em %s", path, winner, engine.ConflictStrategy, engine.AuditPath)
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
			Project:   syncProject,
			PID:       os.Getpid(),
			Owner:     "cli",
		})
	}

	ui.Success("Sessão de sincronização %s criada", syncID)
	ui.Info("Local:  %s", absLocal)
	ui.Info("Remoto: %s", remoteDir)

	// Detecta o unlarp-agent no container antes do sync inicial: o snapshot
	// remoto via agent acelera também o primeiro ciclo (reconnect).
	// atomic.Pointer para o redial trocar o client sem corrida com SyncExec.
	var agentRef atomic.Pointer[agent.Client]
	agentClient := agent.Detect(sshClient)
	if agentClient != nil {
		agentRef.Store(agentClient)
		engine.RemoteSnapshotFn = func() (internalsync.Snapshot, error) {
			return agentRef.Load().Snapshot(remoteDir, globalIgnores)
		}
	}

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

	// Configura canal de trigger para a engine.
	// Buffer 1 + send com default é um coalescer intencional (dirty flag), não uma
	// fila: SyncExec é reconciliação full-state, então um único token pendente
	// cobre todas as mudanças que chegarem durante um ciclo. Não trocar por fila.
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

	// redial derruba e recria SSH+SFTP quando um watcher remoto perde a conexão
	redial := func() (*internalssh.Client, *internalssh.SFTPClient, error) {
		newSSH, err := internalssh.NewClient(hostCfg)
		if err != nil {
			return nil, nil, err
		}
		if err := newSSH.Connect(); err != nil {
			return nil, nil, err
		}
		newSFTP, err := internalssh.NewSFTPClient(newSSH)
		if err != nil {
			newSSH.Close()
			return nil, nil, err
		}
		sftpClient.Close()
		sshClient.Close()
		sshClient = newSSH
		sftpClient = newSFTP
		engine.UpdateSFTPClient(newSFTP.Inner())
		return newSSH, newSFTP, nil
	}

	onRemoteChange := func() {
		select {
		case triggerSync <- "mudança remota":
		default:
		}
	}

	// Watcher remoto: unlarp-agent (inotify no container) quando disponível,
	// senão o polling SFTP de sempre — tudo funciona igual sem o agent
	var remoteWatcher watcher.Stopper
	if agentClient != nil {
		aw := watcher.NewAgentWatcher(remoteDir, agentClient, globalIgnores, func(msg string) {
			ui.Warn("%s", msg)
		}, func() (*agent.Client, error) {
			newSSH, _, err := redial()
			if err != nil {
				return nil, err
			}
			newAgent := agent.New(newSSH)
			agentRef.Store(newAgent)
			return newAgent, nil
		}, onRemoteChange)
		if err := aw.Start(); err != nil {
			ui.Warn("Agent detectado mas falhou ao observar (%v); usando polling SFTP.", err)
		} else {
			remoteWatcher = aw
			ui.Success("Monitorando diretório remoto via unlarp-agent (inotify)...")
		}
	}
	if remoteWatcher == nil {
		rw := watcher.NewRemoteWatcher(remoteDir, sftpClient.Inner(), pollInterval, engine.IgnoreMatcher(), func(msg string) {
			ui.Warn("%s", msg)
		}, func() (*sftp.Client, error) {
			_, newSFTP, err := redial()
			if err != nil {
				return nil, err
			}
			return newSFTP.Inner(), nil
		}, onRemoteChange)
		rw.Start()
		remoteWatcher = rw
		ui.Success("Monitorando diretório remoto (polling %s)...", pollInterval)
	}
	defer remoteWatcher.Stop()

	// Cobre edições feitas na janela entre o sync inicial e o start dos watchers
	select {
	case triggerSync <- "startup":
	default:
	}

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

	// Não fecha triggerSync: os watchers (parados só nos defers, depois daqui)
	// ainda podem enviar um último evento — send em canal fechado panicaria.
	// A goroutine de sync morre com o processo.
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
	table.Header("SESSION ID", "HOST", "LOCAL DIR", "REMOTE DIR", "MODE", "STATUS", "LAST SYNC")

	for name, sess := range sessions {
		for _, s := range sess.Syncs {
			hasSyncs = true
			status := "morto"
			if s.Alive() {
				status = fmt.Sprintf("ativo (pid %d)", s.PID)
			}
			lastSync := "—"
			if !s.LastSync.IsZero() {
				lastSync = s.LastSync.Format("02/01 15:04")
			}
			table.Append(s.ID, name, s.LocalDir, s.RemoteDir, s.Mode, status, lastSync)
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
				// Para de fato o processo dono quando é um `sync start` do CLI.
				// Syncs da TUI não são sinalizados: matar o PID derrubaria a TUI
				// inteira — nesse caso o stop é pela própria TUI (tecla x).
				if s.Alive() && s.PID != os.Getpid() {
					if s.Owner == "cli" {
						if proc, err := os.FindProcess(s.PID); err == nil {
							_ = proc.Signal(syscall.SIGTERM)
							ui.Info("Processo de sync (pid %d) sinalizado para parar.", s.PID)
						}
					} else {
						ui.Warn("Sync gerenciado pela TUI (pid %d) — pare pela aba Syncs; removendo só o registro.", s.PID)
					}
				}
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
