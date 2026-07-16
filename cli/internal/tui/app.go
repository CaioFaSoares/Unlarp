package tui

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/daemon"
	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
	"github.com/CaioFaSoares/unlarp/internal/git"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
	"github.com/CaioFaSoares/unlarp/internal/tunnel"
	"github.com/CaioFaSoares/unlarp/internal/ui"
	"github.com/CaioFaSoares/unlarp/internal/watcher"
)

// tickMsg disparado periodicamente para atualização de UI
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type liveSyncSession struct {
	id            string
	engine        *internalsync.Engine
	localWatcher  *watcher.LocalWatcher
	remoteWatcher watcher.Stopper // RemoteWatcher (SFTP poll) ou AgentWatcher (unlarp-agent)
	stopChan      chan struct{}
}

// pendingSync representa um sync que o usuário acabou de confirmar mas cuja
// conexão/engine/watchers ainda estão sendo verificados em background —
// mostrado otimisticamente na lista de Syncs antes da confirmação definitiva
// (syncStartedMsg), para que o usuário nunca veja "nenhum sync" logo após
// cadastrar um.
type pendingSync struct {
	id        string
	localDir  string
	remoteDir string
}

// TmuxSession representa metadados de uma sessão Tmux remota
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
	Command  string    // comando em execução no painel ativo
	Path     string    // diretório onde o comando está rodando
	PaneDead bool      // processo do painel ativo terminou (remain-on-exit)
	Activity time.Time // última atividade registrada na janela ativa
}

// shellCommands são comandos que indicam que só o shell está rodando no
// painel — ou seja, o agente/processo que rodava ali terminou (idle).
var shellCommands = map[string]bool{
	"zsh": true, "bash": true, "sh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true,
}

// State infere o estado do painel ativo da sessão: "rodando", "idle" ou "morto".
func (s TmuxSession) State() string {
	if s.PaneDead {
		return "morto"
	}
	if s.Command == "" || shellCommands[s.Command] {
		return "idle"
	}
	return "rodando"
}

// StateIcon retorna o indicador visual do estado: ● rodando, ◌ idle, ✗ morto.
func (s TmuxSession) StateIcon() string {
	switch s.State() {
	case "rodando":
		return "●"
	case "morto":
		return "✗"
	default:
		return "◌"
	}
}

// IdleFor retorna há quanto tempo a janela ativa está sem atividade
// (zero se desconhecido). Sujeito a pequeno skew de relógio local vs remoto.
func (s TmuxSession) IdleFor() time.Duration {
	if s.Activity.IsZero() {
		return 0
	}
	d := time.Since(s.Activity)
	if d < 0 {
		return 0
	}
	return d
}

type tmuxSessionsMsg []TmuxSession

// Project representa um projeto cadastrado manualmente no host (ver config.Project)
type Project struct {
	Path     string
	Name     string
	Branch   string
	LocalDir string // pasta local vinculada, se um sync foi criado no cadastro
	Account  string // conta Claude Code do projeto (chave de Host.Accounts), "" = padrão
}

type projectsMsg []Project

// projectOutputMsg carrega o tail do pane Tmux de um projeto (tmux capture-pane)
type projectOutputMsg struct {
	name   string
	output string
}

type resumeTuiMsg struct {
	err error
}

type obSetupFinishedMsg struct {
	err error
}

type obVpsPasswordRequiredMsg struct{}

// syncStartedMsg carrega o resultado (ou erro) da criação assíncrona de um sync,
// já que conectar/varrer o diretório remoto via SFTP pode ser lento e não pode
// travar o loop de eventos da TUI.
type syncStartedMsg struct {
	err           error
	host          string
	syncID        string
	localDir      string
	remoteDir     string
	engine        *internalsync.Engine
	localWatcher  *watcher.LocalWatcher
	remoteWatcher watcher.Stopper
	restored      bool // true quando religado automaticamente a partir do state persistido, não criado pelo usuário agora
	daemon        bool // true quando criado no daemon (ver DAEMON.md) — sem engine/watcher local, entry já persistida pelo daemon
}

// daemonLogsMsg carrega uma página de GET /v1/logs?since= do daemon local,
// mesclada na aba Logs junto dos eventos da própria TUI (ver DAEMON.md).
type daemonLogsMsg struct {
	entries []daemonapi.LogEntry
	latest  uint64
}

// fetchDaemonLogsCmd busca novas entradas do log do daemon desde o último
// cursor visto. Silencioso se o daemon não está de pé (opt-in) ou desativado.
func (m *AppModel) fetchDaemonLogsCmd() tea.Cmd {
	if !m.cfg.Daemon.Enabled {
		return nil
	}
	since := m.daemonLogCursor
	return func() tea.Msg {
		client, err := daemon.NewClient()
		if err != nil {
			return nil
		}
		resp, err := client.Logs(since)
		if err != nil {
			return nil
		}
		return daemonLogsMsg{entries: resp.Entries, latest: resp.Latest}
	}
}

// Índices das abas do painel principal — Dashboard e Projetos ficam lado a lado
// por serem as mais usadas no dia a dia.
const (
	tabDashboard = iota
	tabProjects
	tabSyncs
	tabTunnels
	tabLogs
	tabWatch
	tabAccounts
	tabCount
)

// AppModel é o modelo central do Bubble Tea
type AppModel struct {
	width     int
	height    int
	isExiting bool

	// Configs e Estado
	cfg          *config.Config
	hostNames    []string
	selectedHost int // Index na barra lateral
	activeHost   string

	// Abas e Foco
	activeTab    int  // ver consts tabDashboard/tabProjects/tabSyncs/tabTunnels/tabLogs
	sidebarFocus bool // true: foco na barra lateral, false: no painel principal

	// Seleção de linhas nas abas
	selectedSyncRow   int
	selectedTunnelRow int
	selectedTmuxRow   int

	// Prompts interativos para criação interna
	promptActive bool
	promptType   string // "sync" | "tunnel_direction" | "tunnel"
	textInput    textinput.Model

	// Direção escolhida no prompt "tunnel_direction", usada ao criar o túnel
	// no prompt "tunnel" subsequente (padrão: tunnel.DirectionLocal)
	pendingTunnelDirection tunnel.Direction

	// Clientes e Managers ativos por HostName. sshMu protege sshClients e
	// sftpClients, que são lidos/escritos tanto no goroutine principal do
	// Update() quanto em tea.Cmd assíncronos (checkTmuxCmd, createSyncLiveCmd
	// etc.) — sem essa trava, dois goroutines escrevendo ao mesmo tempo
	// derrubam o processo inteiro com "fatal error: concurrent map writes".
	sshMu          *sync.Mutex
	sshClients     map[string]*internalssh.Client
	sftpClients    map[string]*sftp.Client
	agentClients   map[string]*agent.Client // nil = detectado como ausente (cache negativo)
	tunnelManagers map[string]*tunnel.Manager
	syncSessions   map[string]map[string]*liveSyncSession
	pendingSyncs   map[string][]pendingSync
	// healedHosts marca hosts onde já disparamos a cura proativa de git
	// (git.HealAllProjects) nesta sessão da TUI — evita repetir a cada tick.
	healedHosts map[string]bool

	// Sessões Tmux
	tmuxSessions []TmuxSession

	// Projetos (cadastrados manualmente por host, ver config.Project)
	projects           []Project
	selectedProjectRow int
	projectOutput      map[string]string
	expandedProjects   map[string]bool
	expandedSyncs      map[string]bool
	pendingProject     Project

	gitInfo         map[string]git.RemoteGitInfo // chave: RemotePath/RemoteDir do projeto
	gitAlerts       map[string]string            // chave: syncID ou RemotePath -> motivo do bloqueio
	gitTickCounter  int
	syncTickCounter int

	// Hosts cujos syncs persistidos já foram religados nesta execução da TUI
	restoredHosts map[string]bool

	// Componentes e Sub-sistemas
	spinner spinner.Model
	sessMgr *session.Manager

	// Onboarding Wizard
	isOnboarding   bool
	onboardingStep int
	obProfileName  string
	obHost         string
	obPort         int
	obUser         string
	obWorkspace    string
	obSetupKey     bool
	obPassword     string

	// DirPicker
	dirPicker    *ui.DirPicker
	pickerActive bool
	pickerStage  string // "local" | "remote"
	chosenLocal  string
	chosenRemote string

	// Picker de conta Claude Code — aparece ao criar sessão de projeto sem conta
	accountPickerActive bool
	accountPickerCursor int
	accountPickerSave   bool // Enter também grava a conta no projeto (tecla s alterna)
	accountPickerEdit   bool // modo edição (tecla e na aba Projetos): só grava, não cria sessão
	pendingSessionName  string

	// Aba Contas
	selectedAccountRow int
	pendingAccount     string // conta alvo do prompt de exclusão

	// Logs
	logs            []string
	logScrollOffset int
	logAutoScroll   bool
	logsMu          sync.Mutex // addLog é chamado de goroutines de background
	daemonLogCursor uint64     // último Seq visto de GET /v1/logs (ver DAEMON.md)

	// Syncs cujo restore falhou nesta execução (não re-tentar a cada reload)
	failedRestores map[string]bool

	// Estado inferido dos agentes Claude Code por sessão tmux (claude_status.go)
	claudeStatus      map[string]string
	claudeTickCounter int

	// Watch-commands configurados no yaml (watch.go); chave: host + "/" + nome
	watcherLastRun map[string]time.Time
	watcherRunning map[string]bool
	watcherOutput  map[string]watcherResult
}

// NewAppModel inicializa o modelo do aplicativo
func NewAppModel() (*AppModel, error) {
	store := config.NewStore()
	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}

	sessMgr, err := session.NewManager()
	if err != nil {
		return nil, err
	}

	// Ordena hosts (range de map é não determinístico — a sidebar pulava de
	// ordem a cada execução)
	var hostNames []string
	for name := range cfg.Hosts {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)

	selected := 0
	active := cfg.DefaultHost
	for i, name := range hostNames {
		if name == active {
			selected = i
		}
	}

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.ColorPrimary)

	ti := textinput.New()
	ti.Placeholder = "Digite os parâmetros..."
	ti.CharLimit = 156
	ti.Width = 50

	isOnboarding := len(hostNames) == 0

	return &AppModel{
		cfg:                    cfg,
		hostNames:              hostNames,
		selectedHost:           selected,
		activeHost:             active,
		activeTab:              tabDashboard,
		sidebarFocus:           true,
		promptActive:           false,
		pendingTunnelDirection: tunnel.DirectionLocal,
		textInput:              ti,
		sshMu:                  &sync.Mutex{},
		sshClients:             make(map[string]*internalssh.Client),
		sftpClients:            make(map[string]*sftp.Client),
		healedHosts:            make(map[string]bool),
		agentClients:           make(map[string]*agent.Client),
		tunnelManagers:         make(map[string]*tunnel.Manager),
		syncSessions:           make(map[string]map[string]*liveSyncSession),
		pendingSyncs:           make(map[string][]pendingSync),
		projectOutput:          make(map[string]string),
		expandedProjects:       make(map[string]bool),
		expandedSyncs:          make(map[string]bool),
		gitInfo:                make(map[string]git.RemoteGitInfo),
		gitAlerts:              make(map[string]string),
		gitTickCounter:         0,
		syncTickCounter:        0,
		restoredHosts:          make(map[string]bool),
		isOnboarding:           isOnboarding,
		onboardingStep:         0,
		obProfileName:          "",
		obPort:                 2222,
		obUser:                 "root",
		obWorkspace:            "/workspace",
		spinner:                s,
		sessMgr:                sessMgr,
		logs: []string{
			"unlarp TUI inicializada com sucesso.",
			"Configuração carregada de ~/.unlarp.yaml.",
			"Abra a barra lateral (Tab) ou alterne abas (left/right).",
		},
		logScrollOffset: 0,
		logAutoScroll:   true,
		failedRestores:  make(map[string]bool),
		claudeStatus:    make(map[string]string),
		watcherLastRun:  make(map[string]time.Time),
		watcherRunning:  make(map[string]bool),
		watcherOutput:   make(map[string]watcherResult),
	}, nil
}

// cleanupFinishedMsg é enviada quando a limpeza em background é concluída
type cleanupFinishedMsg struct{}

func (m *AppModel) cleanupCmd() tea.Cmd {
	return func() tea.Msg {
		done := make(chan struct{})
		go func() {
			m.Cleanup()
			close(done)
		}()

		select {
		case <-done:
			// Limpeza finalizada normalmente
		case <-time.After(5 * time.Second):
			// Timeout de segurança atingido para evitar travamento da TUI
			// (Close de SSH/SFTP em rede lenta pode exceder 1,5s e deixaria watchers vivos)
		}
		return cleanupFinishedMsg{}
	}
}

// Init inicializa a aplicação Bubble Tea
func (m *AppModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tickCmd())
}

// Cleanup encerra todas as conexões e watchers ativos de background
func (m *AppModel) Cleanup() {
	// 1. Fechar todas as conexões SFTP e SSH primeiro para interromper chamadas de rede bloqueantes
	m.sshMu.Lock()
	for _, sftpCli := range m.sftpClients {
		if sftpCli != nil {
			_ = sftpCli.Close()
		}
	}
	for _, client := range m.sshClients {
		if client != nil {
			_ = client.Close()
		}
	}
	m.sshMu.Unlock()

	// 2. Parar os watchers ativos
	for _, hostSyncs := range m.syncSessions {
		for _, s := range hostSyncs {
			if s.localWatcher != nil {
				s.localWatcher.Stop()
			}
			if s.remoteWatcher != nil {
				s.remoteWatcher.Stop()
			}
			if s.stopChan != nil {
				close(s.stopChan)
			}
		}
	}

	// 3. Fechar os gerenciadores de túnel
	for _, mgr := range m.tunnelManagers {
		if mgr != nil {
			mgr.Close()
		}
	}
}

// Update gerencia mensagens e entrada do usuário
func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Intercepta ctrl+c globalmente para desligamento gracioso
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+c" {
		m.isExiting = true
		return m, m.cleanupCmd()
	}

	if m.isExiting {
		// Se estiver desligando, ignora qualquer outra mensagem exceto cleanupFinishedMsg que efetiva a saída
		if _, ok := msg.(cleanupFinishedMsg); ok {
			return m, tea.Quit
		}
		return m, nil
	}

	var cmds []tea.Cmd

	// Mensagens de fundo auto-rearmáveis são tratadas ANTES de qualquer branch
	// modal (dirPicker): sem isso, o primeiro tickMsg/spinner.TickMsg que chega
	// com o picker aberto é engolido sem re-arme e a cadeia morre para o resto
	// da sessão — progresso de sync e logs congelam até o usuário teclar algo.
	switch bg := msg.(type) {
	case tickMsg:
		cmds = append(cmds, tickCmd())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(bg)
		cmds = append(cmds, cmd)
	case syncStartedMsg:
		// Engine recém-criada precisa ser registrada mesmo com picker aberto,
		// senão fica órfã (sem registro em syncSessions nem saída de pendingSyncs).
		m.handleSyncStarted(bg)
		return m, tea.Batch(cmds...)
	case daemonLogsMsg:
		if bg.latest > m.daemonLogCursor {
			m.daemonLogCursor = bg.latest
		}
		for _, e := range bg.entries {
			m.addLog(fmt.Sprintf("[daemon] %s: %s", e.Level, e.Msg))
		}
		return m, tea.Batch(cmds...)
	}

	if m.pickerActive {
		var cmd tea.Cmd
		var newModel tea.Model
		newModel, cmd = m.dirPicker.Update(msg)
		m.dirPicker = newModel.(*ui.DirPicker)
		cmds = append(cmds, cmd)

		if m.dirPicker.Confirmed {
			switch m.pickerStage {
			case "local":
				m.chosenLocal = m.dirPicker.SelectedPath
				m.pickerStage = "remote"
				m.addLog(fmt.Sprintf("Diretório local selecionado: %s. Conectando a %s para escolher o remoto...", m.chosenLocal, m.activeHost))

				// Obter cliente SFTP para o host ativo
				hostCfg, ok := m.cfg.Hosts[m.activeHost]
				if !ok {
					m.addLog("Erro: Host ativo não configurado.")
					m.pickerActive = false
					return m, nil
				}

				client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
				if err != nil {
					m.addLog(fmt.Sprintf("Erro SSH ao conectar em %s: %v", m.activeHost, err))
					m.pickerActive = false
					return m, nil
				}

				sftpCli, err := m.getOrCreateSFTPClient(m.activeHost, client)
				if err != nil {
					m.addLog(fmt.Sprintf("Erro SFTP ao conectar em %s: %v", m.activeHost, err))
					m.pickerActive = false
					return m, nil
				}

				m.addLog(fmt.Sprintf("Conectado a %s. Navegue e confirme (Tab/s) o diretório remoto.", m.activeHost))

				// Inicia picker remoto no diretório workspace configurado do host
				m.dirPicker = ui.NewDirPicker(true, sftpCli, hostCfg.Workspace)

			case "remote":
				// Confirmado remoto!
				m.chosenRemote = m.dirPicker.SelectedPath
				m.pickerActive = false
				m.addLog(fmt.Sprintf("Diretório remoto selecionado: %s. Conectando e iniciando sincronização (pode levar alguns segundos)...", m.chosenRemote))

				// Mostra o sync na lista imediatamente como "verificando...",
				// antes mesmo da conexão/engine confirmarem que está ativo.
				syncID := "s-" + generateRandomString(6)
				m.pendingSyncs[m.activeHost] = append(m.pendingSyncs[m.activeHost], pendingSync{
					id:        syncID,
					localDir:  m.chosenLocal,
					remoteDir: m.chosenRemote,
				})

				// Inicia o sync em tempo real em background, sem travar a TUI
				cmds = append(cmds, m.createSyncLiveCmd(syncID, fmt.Sprintf("%s:%s", m.chosenLocal, m.chosenRemote)))

			case "project_remote":
				// Cadastro de projeto: pasta remota escolhida, pergunta se já
				// quer vincular um sync antes de gravar no config.
				m.chosenRemote = m.dirPicker.SelectedPath
				m.pickerActive = false
				m.addLog(fmt.Sprintf("Pasta do projeto selecionada: %s", m.chosenRemote))

				m.promptActive = true
				m.promptType = "project_sync_confirm"
				m.textInput.SetValue("s") // Enter direto já confirma "sim", igual ao onboarding
				m.textInput.Placeholder = "s/n"
				m.textInput.Focus()
				cmds = append(cmds, textinput.Blink)

			case "project_local":
				// Cadastro de projeto com sync: pasta local escolhida, grava o
				// projeto e inicia o sync no mesmo fluxo já usado pela aba Syncs.
				m.chosenLocal = m.dirPicker.SelectedPath
				m.pickerActive = false
				if err := m.registerProject(m.chosenRemote, m.chosenLocal); err == nil {
					cmds = append(cmds, m.createSyncLiveCmd("s-"+generateRandomString(6), fmt.Sprintf("%s:%s", m.chosenLocal, m.chosenRemote)))
				}
			}
		} else if m.dirPicker.Cancelled {
			m.pickerActive = false
			m.addLog("Seleção de diretório cancelada.")
		}

		return m, tea.Batch(cmds...)
	}

	switch msg := msg.(type) {
	case cleanupFinishedMsg:
		return m, tea.Quit

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		// Re-arme do tick acontece no topo de Update (antes dos branches modais)
		if !m.isOnboarding {
			cmds = append(cmds, m.checkTmuxCmd())
			cmds = append(cmds, m.checkProjectsCmd())

			cmds = append(cmds, m.dispatchWatchersCmd()...)

			if cmd := m.fetchDaemonLogsCmd(); cmd != nil {
				cmds = append(cmds, cmd)
			}

			m.claudeTickCounter++
			if m.claudeTickCounter >= 3 {
				m.claudeTickCounter = 0
				if cmd := m.checkClaudeStatusCmd(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

			m.gitTickCounter++
			if m.gitTickCounter >= 5 {
				m.gitTickCounter = 0
				if cmd := m.checkGitCmd(); cmd != nil {
					cmds = append(cmds, cmd)
				}

				// Relê o state.json para absorver syncs criados/removidos por
				// outros processos (ex: `unlarp sync start` no CLI) e re-agenda
				// o restore de todos os hosts — os guards em restoreSyncsCmd
				// (engine viva, dono externo vivo, falha anterior) garantem que
				// isso é barato e não duplica engines nem spamma logs.
				m.sessMgr.Reload()
				for _, host := range m.hostNames {
					m.restoredHosts[host] = true
					if restoreCmd := m.restoreSyncsCmd(host); restoreCmd != nil {
						cmds = append(cmds, restoreCmd)
					}
				}
			}

			m.syncTickCounter++
			if m.syncTickCounter >= 15 {
				m.syncTickCounter = 0
				// Dispara reconciliação de consistência periódica para todas as sessões de sync ativas em background
				for _, hostSyncs := range m.syncSessions {
					for _, s := range hostSyncs {
						if s.engine != nil {
							go func(engine *internalsync.Engine) {
								_, _ = engine.SyncExec()
							}(s.engine)
						}
					}
				}
			}

			// Religa, de forma invisível, os syncs persistidos do host ativo assim
			// que ele é visto pela primeira vez nesta execução (cobre tanto o
			// boot da TUI quanto a troca de host ativo).
			if m.activeHost != "" && !m.restoredHosts[m.activeHost] {
				m.restoredHosts[m.activeHost] = true
				if restoreCmd := m.restoreSyncsCmd(m.activeHost); restoreCmd != nil {
					cmds = append(cmds, restoreCmd)
				}
			}

			if m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 {
					idx := m.selectedProjectRow
					if idx >= len(items) {
						idx = 0
					}
					item := items[idx]
					sessionName := item.Project.Name
					if !item.IsProject && item.Session != nil {
						sessionName = item.Session.Name
					}
					cmds = append(cmds, m.captureProjectCmd(sessionName))
				}
			}
		}

	case tmuxSessionsMsg:
		// Loga transições de estado dos agentes (rodando -> idle/morto e
		// sessões finalizadas). Só compara quando a nova listagem tem conteúdo,
		// para não confundir erro transitório de SSH (msg nil) com "tudo sumiu".
		if len(msg) > 0 && len(m.tmuxSessions) > 0 {
			previous := make(map[string]TmuxSession, len(m.tmuxSessions))
			for _, s := range m.tmuxSessions {
				previous[s.Name] = s
			}
			for _, s := range msg {
				if prev, ok := previous[s.Name]; ok {
					if prev.State() == "rodando" && s.State() != "rodando" {
						branchNote := ""
						if info, ok := m.gitInfo[s.Path]; ok && info.IsGitRepo && info.Branch != "" {
							branchNote = fmt.Sprintf(" (branch %s)", info.Branch)
						}
						m.addLog(fmt.Sprintf("Sessão %s: '%s' terminou — agora %s%s", s.Name, prev.Command, s.State(), branchNote))
					}
					delete(previous, s.Name)
				}
			}
			for name := range previous {
				m.addLog(fmt.Sprintf("Sessão tmux %s finalizada", name))
			}
		}
		m.tmuxSessions = msg
		if m.selectedTmuxRow >= len(m.tmuxSessions) {
			m.selectedTmuxRow = 0
		}
		if items := m.buildProjectTree(); len(items) > 0 && m.selectedProjectRow >= len(items) {
			m.selectedProjectRow = 0
		}

	case accountAddedMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro ao cadastrar conta '%s': %v", msg.name, msg.err))
		} else {
			if cfg, err := config.NewStore().Load(); err == nil {
				m.cfg = cfg
			}
			m.addLog(fmt.Sprintf("Conta '%s' cadastrada → %s. Login: unlarp account login %s", msg.name, msg.dir, msg.name))
		}

	case projectsGitMsg:
		m.projects = msg.projects
		// Mescla as informações Git ricas coletadas
		for path, info := range msg.gitInfos {
			m.gitInfo[path] = info
		}

		// Projetos com sync ativo sobem para o topo — "ativos" separados do
		// resto sem precisar de um passo extra de curadoria manual.
		if sess, ok := m.sessMgr.GetSession(m.activeHost); ok {
			sort.SliceStable(m.projects, func(i, j int) bool {
				return matchProjectSync(sess.Syncs, m.projects[i]) != nil &&
					matchProjectSync(sess.Syncs, m.projects[j]) == nil
			})
		}
		if items := m.buildProjectTree(); len(items) > 0 && m.selectedProjectRow >= len(items) {
			m.selectedProjectRow = 0
		}

	case projectOutputMsg:
		m.projectOutput[msg.name] = msg.output

	case resumeTuiMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro ao retornar do SSH: %v", msg.err))
		} else {
			m.addLog("Retornou do terminal SSH.")
		}
		m.tmuxSessions = nil
		cmds = append(cmds, m.checkTmuxCmd())
		cmds = append(cmds, m.checkProjectsCmd())

	// syncStartedMsg e spinner.TickMsg são tratados no topo de Update,
	// antes dos branches modais.

	case claudeStatusMsg:
		if msg.host == m.activeHost {
			m.claudeStatus = msg.statuses
		}

	case watcherOutputMsg:
		m.watcherRunning[msg.key] = false
		m.watcherOutput[msg.key] = msg.result

	case gitCheckResultMsg:
		m.gitInfo[msg.projectPath] = msg.info
		if msg.diverged {
			m.gitAlerts[msg.syncID] = msg.reason
			m.addLog(fmt.Sprintf("ALERTA: Sincronização %s bloqueada: %s", msg.syncID, msg.reason))
		} else {
			if _, hasAlert := m.gitAlerts[msg.syncID]; hasAlert {
				delete(m.gitAlerts, msg.syncID)
				m.addLog(fmt.Sprintf("Sincronização %s liberada (alinhada com o Git).", msg.syncID))
				if activeHostSyncs, exists := m.syncSessions[m.activeHost]; exists {
					if sessCtx, exists := activeHostSyncs[msg.syncID]; exists && sessCtx.engine != nil {
						sessCtx.engine.Resume()
					}
				}
			}
		}

	case gitResolvedMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro ao resolver divergência Git via %s: %v", msg.action, msg.err))
		} else {
			delete(m.gitAlerts, msg.syncID)
			m.addLog(fmt.Sprintf("Divergência Git no sync %s resolvida via %s com sucesso.", msg.syncID, msg.action))
		}

	case obSetupFinishedMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro de setup: %v", msg.err))
			m.onboardingStep = 8 // se falhou feio mesmo com senha, vai pro fim (mas mostrando erro na TUI)
		} else {
			m.addLog("Chave SSH pública injetada com sucesso.")
			m.onboardingStep = 8
		}
		m.textInput.Blur()

	case obVpsPasswordRequiredMsg:
		m.onboardingStep = 9
		m.textInput.SetValue("")
		m.textInput.Placeholder = "Senha SSH da VPS"
		m.textInput.EchoMode = textinput.EchoPassword
		m.textInput.Focus()
		cmds = append(cmds, textinput.Blink)

	case tea.KeyMsg:
		// Se estiver no onboarding, processa separadamente
		if m.isOnboarding {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				// Só permite cancelar o onboarding se já houver pelo menos um host configurado
				if len(m.hostNames) > 0 {
					m.isOnboarding = false
					m.textInput.Blur()
					return m, nil
				}
			case "enter":
				cmd := m.handleOnboardingSubmit()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			default:
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			}
		}

		// Picker de conta Claude Code (criação de sessão de projeto)
		if m.accountPickerActive {
			names := m.hostAccounts()
			total := len(names) + 1 // opção 0 = "(sem conta — padrão)"
			switch msg.String() {
			case "esc":
				m.accountPickerActive = false
				m.accountPickerEdit = false
				return m, nil
			case "up", "k":
				m.accountPickerCursor = (m.accountPickerCursor - 1 + total) % total
				return m, nil
			case "down", "j":
				m.accountPickerCursor = (m.accountPickerCursor + 1) % total
				return m, nil
			case "s":
				if !m.accountPickerEdit {
					m.accountPickerSave = !m.accountPickerSave
				}
				return m, nil
			case "enter":
				m.accountPickerActive = false
				account := ""
				if m.accountPickerCursor > 0 {
					account = names[m.accountPickerCursor-1]
				}
				if m.accountPickerEdit {
					m.accountPickerEdit = false
					store := config.NewStore()
					if err := store.SetProjectAccount(m.activeHost, m.pendingProject.Path, account); err != nil {
						m.addLog("Erro ao definir conta do projeto: " + err.Error())
						return m, nil
					}
					if cfg, err := store.Load(); err == nil {
						m.cfg = cfg
					}
					if account == "" {
						m.addLog(fmt.Sprintf("Projeto '%s' sem conta (usa o ~/.claude padrão). Sessões existentes não mudam.", m.pendingProject.Name))
					} else {
						m.addLog(fmt.Sprintf("Projeto '%s' agora usa a conta '%s'. Vale para sessões novas; as existentes não mudam.", m.pendingProject.Name, account))
					}
					return m, m.checkProjectsCmd()
				}
				if m.accountPickerSave && account != "" {
					if err := config.NewStore().SetProjectAccount(m.activeHost, m.pendingProject.Path, account); err != nil {
						m.addLog("Erro ao salvar conta no projeto: " + err.Error())
					} else if cfg, err := config.NewStore().Load(); err == nil {
						m.cfg = cfg
						m.addLog(fmt.Sprintf("Conta '%s' salva no projeto '%s'", account, m.pendingProject.Name))
					}
				}
				return m, tea.Batch(
					m.createProjectSessionCmd(m.pendingSessionName, m.pendingProject.Path, m.accountDirFor(account)),
					m.checkProjectsCmd(),
				)
			}
			return m, nil
		}

		// Se o prompt estiver ativo, redireciona todas as teclas para o input
		if m.promptActive {
			switch msg.String() {
			case "esc":
				m.promptActive = false
				m.textInput.Blur()
				return m, nil
			case "enter":
				prevPromptType := m.promptType
				cmd := m.handlePromptSubmit()
				if prevPromptType == "tunnel_direction" {
					// Avança para o prompt de portas em vez de fechar o prompt
					return m, cmd
				}
				m.promptActive = false
				m.textInput.Blur()
				return m, cmd
			default:
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "q":
			m.isExiting = true
			return m, m.cleanupCmd()

		case "tab":
			m.sidebarFocus = !m.sidebarFocus

		case "left", "right":
			if !m.sidebarFocus {
				if msg.String() == "left" {
					m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
				} else {
					m.activeTab = (m.activeTab + 1) % tabCount
				}
				m.selectedSyncRow = 0
				m.selectedTunnelRow = 0
				m.selectedProjectRow = 0
			}

		case "up", "k":
			if m.sidebarFocus && len(m.hostNames) > 0 {
				m.selectedHost = (m.selectedHost - 1 + len(m.hostNames)) % len(m.hostNames)
			} else if !m.sidebarFocus {
				m.handleMainPanelUp()
			}

		case "down", "j":
			if m.sidebarFocus && len(m.hostNames) > 0 {
				m.selectedHost = (m.selectedHost + 1) % len(m.hostNames)
			} else if !m.sidebarFocus {
				m.handleMainPanelDown()
			}

		case "enter":
			if m.sidebarFocus && len(m.hostNames) > 0 {
				// Ativa o host selecionado
				m.activeHost = m.hostNames[m.selectedHost]
				m.sessMgr.SetActive(m.activeHost)
				store := config.NewStore()
				_ = store.SetDefault(m.activeHost)
				m.addLog(fmt.Sprintf("Sessão ativa alterada para: %s", m.activeHost))
			} else if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					item := items[m.selectedProjectRow]
					if item.IsProject {
						m.expandedProjects[item.Project.Path] = !m.expandedProjects[item.Project.Path]
					} else if item.Session != nil {
						exe, err := os.Executable()
						if err != nil {
							exe = "unlarp"
						}
						connectArgs := []string{"connect", m.activeHost, "--tmux", "--tmux-session", item.Session.Name}
						if item.Project.Path != "" {
							connectArgs = append(connectArgs, "--cwd", item.Project.Path)
						}
						if dir := m.accountDirFor(item.Project.Account); dir != "" {
							connectArgs = append(connectArgs, "--claude-config-dir", dir)
						}
						c := exec.Command(exe, connectArgs...)
						return m, tea.ExecProcess(c, func(err error) tea.Msg {
							return resumeTuiMsg{err: err}
						})
					}
				}
			} else if !m.sidebarFocus && m.activeTab == tabSyncs {
				items := m.buildSyncTree()
				if len(items) > 0 && m.selectedSyncRow < len(items) {
					item := items[m.selectedSyncRow]
					if item.IsSync && item.PendingSync == nil {
						m.expandedSyncs[item.Sync.ID] = !m.expandedSyncs[item.Sync.ID]
					}
				}
			}

		case "a":
			// Atalho para adicionar novo host via Onboarding Wizard (na barra lateral)
			if m.sidebarFocus {
				m.isOnboarding = true
				m.onboardingStep = 0
				m.obProfileName = ""
				m.obHost = ""
				m.obPort = 2222
				m.obUser = "root"
				m.obWorkspace = "/workspace"
				m.textInput.SetValue("")
				m.textInput.Placeholder = ""
				m.textInput.Blur()
				return m, nil
			}

			// Cadastrar conta Claude Code (aba Contas)
			if !m.sidebarFocus && m.activeTab == tabAccounts {
				m.promptType = "account_add"
				m.promptActive = true
				m.textInput.SetValue("")
				m.textInput.Placeholder = "nome [dir remoto] (ex: pessoal ou pessoal /root/.claude-accounts/pessoal)"
				m.textInput.Focus()
				return m, textinput.Blink
			}

			// Atalho para cadastrar um projeto (aba Projetos): abre o navegador
			// remoto na workspace do host para escolher a pasta do projeto.
			if !m.sidebarFocus && m.activeTab == tabProjects {
				hostCfg, ok := m.cfg.Hosts[m.activeHost]
				if !ok {
					m.addLog("Erro: Host ativo não configurado.")
					return m, nil
				}

				client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
				if err != nil {
					m.addLog(fmt.Sprintf("Erro SSH ao conectar em %s: %v", m.activeHost, err))
					return m, nil
				}

				sftpCli, err := m.getOrCreateSFTPClient(m.activeHost, client)
				if err != nil {
					m.addLog(fmt.Sprintf("Erro SFTP ao conectar em %s: %v", m.activeHost, err))
					return m, nil
				}

				m.addLog(fmt.Sprintf("Navegue até a pasta do projeto em %s e confirme (Tab/s).", m.activeHost))
				m.pickerActive = true
				m.pickerStage = "project_remote"
				m.dirPicker = ui.NewDirPicker(true, sftpCli, hostCfg.Workspace)
				return m, nil
			}

		case "d":
			// Desanexa do tmux local mantendo o processo rodando no background
			if os.Getenv("TMUX") != "" {
				_ = exec.Command("tmux", "detach-client").Run()
				return m, nil
			} else {
				m.addLog("Não está rodando sob uma sessão do Tmux.")
			}
			return m, nil

		case "D":
			// Toggle do daemon local (opt-in — ver DAEMON.md): novos syncs
			// passam a ser criados no daemon em vez de in-process. Syncs já
			// vivos não migram sozinhos.
			if !m.sidebarFocus && m.activeTab == tabDashboard {
				m.cfg.Daemon.Enabled = !m.cfg.Daemon.Enabled
				store := config.NewStore()
				if err := store.Save(m.cfg); err != nil {
					m.addLog(fmt.Sprintf("Erro ao salvar config do daemon: %v", err))
				} else if m.cfg.Daemon.Enabled {
					m.addLog("Daemon local ativado: novos syncs serão criados no daemon.")
				} else {
					m.addLog("Daemon local desativado: novos syncs voltam a rodar in-process.")
				}
			}
			return m, nil

		case "p", "f":
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					item := items[m.selectedProjectRow]
					if item.IsProject {
						sess, sessOk := m.sessMgr.GetSession(m.activeHost)
						if sessOk {
							syncEntry := matchProjectSync(sess.Syncs, item.Project)
							if syncEntry != nil {
								if _, hasAlert := m.gitAlerts[syncEntry.ID]; hasAlert {
									switch msg.String() {
									case "p":
										m.addLog(fmt.Sprintf("Executando git pull no repositório local %s...", syncEntry.LocalDir))
										return m, m.resolveGitCmd(syncEntry.ID, "pull", syncEntry.LocalDir, syncEntry.RemoteDir, syncEntry.GitBranch)
									case "f":
										m.addLog(fmt.Sprintf("Forçando alinhamento do sync %s com o remote...", syncEntry.ID))
										return m, m.resolveGitCmd(syncEntry.ID, "force", syncEntry.LocalDir, syncEntry.RemoteDir, "")
									}
								}
							}
						}
					}
				}
			}

		case "c":
			if !m.sidebarFocus && m.activeTab == tabDashboard && m.tmuxSessions == nil {
				m.addLog("Aguarde o carregamento das sessões Tmux...")
				return m, nil
			}
			// Atalho para abrir terminal interativo SSH (usando Tmux por padrão)
			sessionName := "unlarp"
			cwd := ""
			accDir := ""
			if !m.sidebarFocus {
				if m.activeTab == tabDashboard && len(m.tmuxSessions) > 0 {
					sessionName = m.tmuxSessions[m.selectedTmuxRow].Name
				} else if m.activeTab == tabProjects {
					items := m.buildProjectTree()
					if len(items) > 0 && m.selectedProjectRow < len(items) {
						item := items[m.selectedProjectRow]
						if item.IsProject {
							sessionName = item.Project.Name
							cwd = item.Project.Path
						} else if item.Session != nil {
							sessionName = item.Session.Name
							cwd = item.Project.Path
						}
						accDir = m.accountDirFor(item.Project.Account)
					}
				}
			}

			exe, err := os.Executable()
			if err != nil {
				exe = "unlarp"
			}

			connectArgs := []string{"connect", m.activeHost, "--tmux", "--tmux-session", sessionName}
			if cwd != "" {
				connectArgs = append(connectArgs, "--cwd", cwd)
			}
			if accDir != "" {
				connectArgs = append(connectArgs, "--claude-config-dir", accDir)
			}

			c := exec.Command(exe, connectArgs...)
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return resumeTuiMsg{err: err}
			})

		case "n":
			if !m.sidebarFocus {
				if m.activeTab == tabDashboard {
					m.promptType = "new_tmux_session"
					m.promptActive = true
					m.textInput.SetValue("")
					m.textInput.Placeholder = "nome-da-sessao (ex: api-worker)"
					m.textInput.Focus()
					return m, textinput.Blink
				} else if m.activeTab == tabProjects {
					items := m.buildProjectTree()
					if len(items) > 0 && m.selectedProjectRow < len(items) {
						proj := items[m.selectedProjectRow].Project
						m.pendingProject = proj
						m.promptType = "new_project_session"
						m.promptActive = true
						m.textInput.SetValue("")
						m.textInput.Placeholder = "sufixo/nome da sessão (ex: backend, worker)"
						m.textInput.Focus()
						return m, textinput.Blink
					}
				}
			}

		case "b":
			// Troca de branch orquestrada: pausa syncs do projeto, faz o
			// checkout remoto (agent quando presente), reconcilia e retoma
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					m.pendingProject = items[m.selectedProjectRow].Project
					m.promptType = "git_switch_branch"
					m.promptActive = true
					m.textInput.SetValue("")
					m.textInput.Placeholder = "branch de destino (ex: main, feature/x)"
					m.textInput.Focus()
					return m, textinput.Blink
				}
			}

		case "e":
			// Editar a conta Claude Code do projeto selecionado (aba Projetos)
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					names := m.hostAccounts()
					if len(names) == 0 {
						m.addLog("Nenhuma conta cadastrada. Cadastre na aba Contas (tecla a) ou com: unlarp account add <nome>")
						return m, nil
					}
					m.pendingProject = items[m.selectedProjectRow].Project
					m.accountPickerActive = true
					m.accountPickerEdit = true
					// cursor começa na conta atual do projeto (0 = sem conta)
					m.accountPickerCursor = 0
					for i, n := range names {
						if n == m.pendingProject.Account {
							m.accountPickerCursor = i + 1
							break
						}
					}
					return m, nil
				}
			}

		case "w":
			// Cria worktree no projeto selecionado (+ sessão tmux nela). A
			// worktree fica DENTRO do repo (.claude/worktree-<branch>), então o
			// sync do projeto já a espelha localmente — sem sync novo.
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) && items[m.selectedProjectRow].IsProject {
					m.pendingProject = items[m.selectedProjectRow].Project
					m.promptType = "worktree_add"
					m.promptActive = true
					m.textInput.SetValue("")
					m.textInput.Placeholder = "branch da worktree (nova branch se não existir)"
					m.textInput.Focus()
					return m, textinput.Blink
				}
			}

		case "g", "G":
			if !m.sidebarFocus && m.activeTab == tabLogs {
				if msg.String() == "g" {
					m.logAutoScroll = false
					m.logScrollOffset = 0
				} else {
					m.logAutoScroll = true
				}
				return m, nil
			}

		case "pageup", "ctrl+u":
			if !m.sidebarFocus && m.activeTab == tabLogs {
				m.logAutoScroll = false
				m.logScrollOffset -= 10
				if m.logScrollOffset < 0 {
					m.logScrollOffset = 0
				}
				return m, nil
			}

		case "pagedown", "ctrl+d":
			if !m.sidebarFocus && m.activeTab == tabLogs {
				m.logScrollOffset += 10
				return m, nil
			}

		case "s":
			// Atalho para iniciar sync (somente se não estiver na barra lateral).
			// Nota: 's' não togglea mais o auto-scroll de logs — rolar para cima
			// pausa, 'G' ou chegar ao fim religa. Um caminho a menos para se perder.
			if !m.sidebarFocus {
				m.addLog(fmt.Sprintf("Abrindo seletor de diretório local para host %s...", m.activeHost))
				m.pickerActive = true
				m.pickerStage = "local"
				m.dirPicker = ui.NewDirPicker(false, nil, ".")
				return m, nil
			}

		case "t":
			// Atalho para iniciar túnel (somente se não estiver na barra lateral).
			// Primeiro pergunta a direção, depois as portas.
			if !m.sidebarFocus {
				m.promptType = "tunnel_direction"
				m.promptActive = true
				m.textInput.SetValue("")
				m.textInput.Placeholder = "l=local→remoto, padrão / r=remoto→local (Enter = l)"
				m.textInput.Focus()
				return m, textinput.Blink
			}

		case "o":
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					item := items[m.selectedProjectRow]
					if item.IsProject {
						sess, sessOk := m.sessMgr.GetSession(m.activeHost)
						if sessOk {
							syncEntry := matchProjectSync(sess.Syncs, item.Project)
							if syncEntry != nil {
								if _, hasAlert := m.gitAlerts[syncEntry.ID]; hasAlert {
									m.addLog(fmt.Sprintf("Definindo sync %s para modo Download-Only temporário...", syncEntry.ID))
									return m, m.resolveGitCmd(syncEntry.ID, "download-only", syncEntry.LocalDir, syncEntry.RemoteDir, "")
								}
							}
						}
					}
				}
				return m, nil
			}

			// Atalho para abrir túnel no navegador (somente se não estiver na barra lateral e na aba de túneis)
			if !m.sidebarFocus && m.activeTab == tabTunnels {
				sess, ok := m.sessMgr.GetSession(m.activeHost)
				if ok && len(sess.Tunnels) > 0 && m.selectedTunnelRow < len(sess.Tunnels) {
					t := sess.Tunnels[m.selectedTunnelRow]
					url := fmt.Sprintf("http://localhost:%d", t.LocalPort)
					m.addLog(fmt.Sprintf("Abrindo navegador em %s...", url))
					go func() {
						_ = openBrowser(url)
					}()
				}
				return m, nil
			}

		case "x":
			// Atalho para excluir item selecionado na aba atual ou deletar host na barra lateral
			if !m.sidebarFocus {
				cmd := m.handleDeleteSelection()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else {
				// Deleta o host selecionado na barra lateral
				if len(m.hostNames) > 0 {
					hostToDelete := m.hostNames[m.selectedHost]
					store := config.NewStore()
					if err := store.RemoveHost(hostToDelete); err == nil {
						m.addLog(fmt.Sprintf("Host '%s' deletado com sucesso.", hostToDelete))

						cfg, errCfg := store.Load()
						if errCfg == nil {
							m.cfg = cfg
							var hostNames []string
							for name := range cfg.Hosts {
								hostNames = append(hostNames, name)
							}
							sort.Strings(hostNames)
							m.hostNames = hostNames

							if len(hostNames) == 0 {
								m.isOnboarding = true
								m.onboardingStep = 0
								m.obProfileName = ""
								m.obHost = ""
								m.obPort = 2222
								m.obUser = "root"
								m.obWorkspace = "/workspace"
								m.textInput.SetValue("")
								m.textInput.Placeholder = ""
								m.textInput.Blur()
							} else {
								m.selectedHost = 0
								m.activeHost = m.hostNames[0]
								m.sessMgr.SetActive(m.activeHost)
								_ = store.SetDefault(m.activeHost)
							}
						}
					} else {
						m.addLog(fmt.Sprintf("Erro ao deletar host: %v", err))
					}
				}
			}
		}
	}

	return m, tea.Batch(cmds...)
}

// View desenha a TUI
func (m *AppModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Inicializando terminal..."
	}

	if m.isOnboarding {
		return m.renderOnboarding(m.width, m.height)
	}

	// Título do App
	header := styles.TitleStyle.Render("UNLARP — Remote Workspace Manager")
	exitingStr := ""
	if m.isExiting {
		exitingStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000")).Bold(true).Render(" — exiting...")
	}
	spinnerStr := m.spinner.View()
	headerRow := lipgloss.JoinHorizontal(lipgloss.Center, header, exitingStr, spinnerStr)

	// Sidebar
	sidebarWidth := 25
	sidebarHeight := m.height - 5
	sidebarContent := m.renderSidebar(sidebarHeight)

	sidebarBox := styles.SidebarStyle.Width(sidebarWidth).Height(sidebarHeight)
	if m.sidebarFocus {
		sidebarBox = styles.SidebarFocusedStyle.Width(sidebarWidth).Height(sidebarHeight)
	}
	sidebarView := sidebarBox.Render(sidebarContent)

	// Painel Principal
	mainWidth := m.width - sidebarWidth - 6
	mainHeight := m.height - 5
	mainContent := m.renderMainPanel(mainWidth, mainHeight)

	mainBox := styles.MainPanelStyle.Width(mainWidth).Height(mainHeight)
	if !m.sidebarFocus {
		mainBox = styles.MainPanelFocusedStyle.Width(mainWidth).Height(mainHeight)
	}
	mainView := mainBox.Render(mainContent)

	// Layout Completo Horizontal
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, mainView)

	// Footer de ajuda
	footer := m.renderFooter()

	return styles.AppStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			headerRow,
			body,
			footer,
		),
	)
}

// addLog é chamado também de goroutines de background (watchers, callbacks da
// engine de sync), então m.logs precisa de lock próprio.
func (m *AppModel) addLog(msg string) {
	m.logsMu.Lock()
	defer m.logsMu.Unlock()
	timestamp := time.Now().Format("15:04:05")
	m.logs = append(m.logs, fmt.Sprintf("[%s] %s", timestamp, msg))
	if len(m.logs) > 500 {
		m.logs = m.logs[1:] // Mantém cap de 500
	}
}

// snapshotLogs devolve uma cópia estável para render sem segurar o lock.
func (m *AppModel) snapshotLogs() []string {
	m.logsMu.Lock()
	defer m.logsMu.Unlock()
	return append([]string(nil), m.logs...)
}

func (m *AppModel) renderSidebar(height int) string {
	var sb strings.Builder
	sb.WriteString(styles.SidebarTitleStyle.Render("Sessões / Hosts"))
	sb.WriteString("\n\n")

	for i, name := range m.hostNames {
		indicator := "  "
		if name == m.activeHost {
			indicator = "● "
		}

		line := fmt.Sprintf("%s%s", indicator, name)

		if i == m.selectedHost && m.sidebarFocus {
			sb.WriteString(styles.HostSelectedStyle.Render(line))
		} else if name == m.activeHost {
			sb.WriteString(styles.HostActiveStyle.Render(line))
		} else {
			sb.WriteString(styles.HostIdleStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	// Adiciona logs rápidos na barra lateral se couber
	if height > 15 {
		sb.WriteString("\n")
		sb.WriteString(styles.SidebarTitleStyle.Render("Atividades"))
		sb.WriteString("\n")
		sess, ok := m.sessMgr.GetSession(m.activeHost)
		if ok {
			sb.WriteString(fmt.Sprintf(" ↔ Syncs:   %d\n", len(sess.Syncs)))
			sb.WriteString(fmt.Sprintf(" 🔌 Túneis:  %d\n", len(sess.Tunnels)))
		} else {
			sb.WriteString(" Nenhuma atividade\n")
		}
	}

	return sb.String()
}

func (m *AppModel) renderFooter() string {
	var sb strings.Builder
	if m.promptActive {
		sb.WriteString(styles.HelpStyle.Render(
			fmt.Sprintf(
				"%s Confirmar | %s Cancelar",
				styles.KeyStyle.Render("Enter"),
				styles.KeyStyle.Render("Esc"),
			),
		))
	} else {
		// Footer contextualizado
		actions := "%s Navegar | %s Confirmar | %s Adicionar Host | %s Deletar Host | %s Mudar Foco | %s Sair"
		keys := []string{
			styles.KeyStyle.Render("↑/↓/j/k"),
			styles.KeyStyle.Render("Enter"),
			styles.KeyStyle.Render("a"),
			styles.KeyStyle.Render("x"),
			styles.KeyStyle.Render("Tab"),
			styles.KeyStyle.Render("q"),
		}

		if !m.sidebarFocus {
			if m.activeTab == tabDashboard {
				actions = "%s Navegar | %s SSH Attach | %s Nova Sessão | %s Destruir Sessão | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("c"),
					styles.KeyStyle.Render("n"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == tabSyncs {
				actions = "%s Navegar | %s Novo Sync | %s Parar Sync | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("s"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == tabTunnels {
				actions = "%s Navegar | %s Novo Túnel | %s Parar Túnel | %s Abrir no Navegador | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("t"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("o"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == tabAccounts {
				actions = "%s Navegar | %s Cadastrar Conta | %s Remover | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("a"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == tabProjects {
				actions = "%s Navegar | %s Cadastrar | %s Expandir/Conectar | %s Nova Sessão | %s Worktree | %s Conta | %s Remover/Kill | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("a"),
					styles.KeyStyle.Render("Enter/c"),
					styles.KeyStyle.Render("n"),
					styles.KeyStyle.Render("w"),
					styles.KeyStyle.Render("e"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == tabLogs {
				actions = "%s Rolar (G: fim + auto-scroll) | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k/g/G"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			}
		}

		if os.Getenv("TMUX") != "" {
			actions = strings.Replace(actions, "| %s Sair", "| %s Desanexar | %s Sair", 1)
			if len(keys) > 0 {
				lastIdx := len(keys) - 1
				qKey := keys[lastIdx]
				dKey := styles.KeyStyle.Render("d")
				keys[lastIdx] = dKey
				keys = append(keys, qKey)
			}
		}

		// Converte slice de strings para interface slice
		interfaceKeys := make([]interface{}, len(keys))
		for i, v := range keys {
			interfaceKeys[i] = v
		}

		sb.WriteString(styles.HelpStyle.Render(
			fmt.Sprintf(actions, interfaceKeys...),
		))
	}
	return sb.String()
}

func (m *AppModel) handleMainPanelUp() {
	if m.activeTab == tabProjects {
		items := m.buildProjectTree()
		if len(items) > 0 {
			m.selectedProjectRow = (m.selectedProjectRow - 1 + len(items)) % len(items)
		}
		return
	} else if m.activeTab == tabSyncs {
		items := m.buildSyncTree()
		if len(items) > 0 {
			m.selectedSyncRow = (m.selectedSyncRow - 1 + len(items)) % len(items)
		}
		return
	} else if m.activeTab == tabLogs {
		m.logAutoScroll = false
		m.logScrollOffset--
		if m.logScrollOffset < 0 {
			m.logScrollOffset = 0
		}
		return
	} else if m.activeTab == tabAccounts {
		if n := len(m.hostAccounts()); n > 0 {
			m.selectedAccountRow = (m.selectedAccountRow - 1 + n) % n
		}
		return
	}

	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return
	}

	if m.activeTab == tabDashboard && len(m.tmuxSessions) > 0 {
		m.selectedTmuxRow = (m.selectedTmuxRow - 1 + len(m.tmuxSessions)) % len(m.tmuxSessions)
	} else if m.activeTab == tabTunnels && len(sess.Tunnels) > 0 {
		m.selectedTunnelRow = (m.selectedTunnelRow - 1 + len(sess.Tunnels)) % len(sess.Tunnels)
	}
}

func (m *AppModel) handleMainPanelDown() {
	if m.activeTab == tabProjects {
		items := m.buildProjectTree()
		if len(items) > 0 {
			m.selectedProjectRow = (m.selectedProjectRow + 1) % len(items)
		}
		return
	} else if m.activeTab == tabSyncs {
		items := m.buildSyncTree()
		if len(items) > 0 {
			m.selectedSyncRow = (m.selectedSyncRow + 1) % len(items)
		}
		return
	} else if m.activeTab == tabLogs {
		m.logScrollOffset++
		return
	} else if m.activeTab == tabAccounts {
		if n := len(m.hostAccounts()); n > 0 {
			m.selectedAccountRow = (m.selectedAccountRow + 1) % n
		}
		return
	}

	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return
	}

	if m.activeTab == tabDashboard && len(m.tmuxSessions) > 0 {
		m.selectedTmuxRow = (m.selectedTmuxRow + 1) % len(m.tmuxSessions)
	} else if m.activeTab == tabTunnels && len(sess.Tunnels) > 0 {
		m.selectedTunnelRow = (m.selectedTunnelRow + 1) % len(sess.Tunnels)
	}
}

// handlePromptSubmit executa a ação de criação no background baseado no input
func (m *AppModel) handlePromptSubmit() tea.Cmd {
	val := strings.TrimSpace(m.textInput.Value())

	// "tunnel_direction" aceita Enter vazio (usa o padrão remote), diferente
	// dos demais prompts que exigem valor.
	if val == "" && m.promptType != "tunnel_direction" {
		return nil
	}

	switch m.promptType {
	case "tunnel_direction":
		m.pendingTunnelDirection = tunnel.DirectionLocal
		if v := strings.ToLower(val); v == "r" || v == "remote" {
			m.pendingTunnelDirection = tunnel.DirectionRemote
		}

		// Avança para o prompt de portas, mantendo o mesmo fluxo de input
		m.promptType = "tunnel"
		m.textInput.SetValue("")
		m.textInput.Placeholder = "porta_remota[:porta_local] (ex: 5432 ou 3000:8080)"
		m.textInput.Focus()
		return textinput.Blink
	case "tunnel":
		m.createTunnelLive(val, m.pendingTunnelDirection)
	case "sync":
		return m.createSyncLiveCmd("s-"+generateRandomString(6), val)
	case "new_tmux_session":
		return m.createTmuxSessionCmd(val)
	case "account_add":
		parts := strings.Fields(val)
		name := parts[0]
		dir := ""
		if len(parts) > 1 {
			dir = parts[1]
		}
		return m.addAccountCmd(name, dir)
	case "account_delete_confirm":
		if !strings.HasPrefix(strings.ToLower(val), "s") {
			return nil
		}
		store := config.NewStore()
		if err := store.RemoveAccount(m.activeHost, m.pendingAccount); err != nil {
			m.addLog("Erro ao remover conta: " + err.Error())
			return nil
		}
		if cfg, err := store.Load(); err == nil {
			m.cfg = cfg
		}
		m.selectedAccountRow = 0
		m.addLog(fmt.Sprintf("Conta '%s' removida (diretório remoto mantido).", m.pendingAccount))
		return nil
	case "new_project_session":
		suffix := strings.TrimSpace(val)
		if suffix == "" {
			m.addLog("Nome de sessão inválido.")
			return nil
		}
		sessionName := m.pendingProject.Name
		if !strings.HasPrefix(suffix, m.pendingProject.Name) {
			sessionName = fmt.Sprintf("%s-%s", m.pendingProject.Name, suffix)
		} else {
			sessionName = suffix
		}
		m.expandedProjects[m.pendingProject.Path] = true
		// Projeto sem conta e host com contas cadastradas → abre o picker antes de criar
		if m.pendingProject.Account == "" && len(m.hostAccounts()) > 0 {
			m.pendingSessionName = sessionName
			m.accountPickerActive = true
			m.accountPickerCursor = 0
			m.accountPickerSave = false
			return nil
		}
		return m.createProjectSessionCmd(sessionName, m.pendingProject.Path, m.accountDirFor(m.pendingProject.Account))
	case "git_switch_branch":
		return m.switchBranchCmd(m.pendingProject.Path, val)
	case "worktree_add":
		m.expandedProjects[m.pendingProject.Path] = true
		return m.worktreeAddCmd(m.pendingProject, val)
	case "project_sync_confirm":
		if strings.HasPrefix(strings.ToLower(val), "s") {
			// Quer sync junto: abre o picker local para escolher a pasta que
			// vai sincronizar com o projeto (fluxo ideal do cadastro).
			m.pickerActive = true
			m.pickerStage = "project_local"
			m.dirPicker = ui.NewDirPicker(false, nil, ".")
		} else {
			m.registerProject(m.chosenRemote, "")
		}
	case "project_delete_confirm":
		if strings.HasPrefix(strings.ToLower(val), "s") || strings.HasPrefix(strings.ToLower(val), "y") {
			proj := m.pendingProject
			store := config.NewStore()
			if err := store.RemoveProject(m.activeHost, proj.Path); err != nil {
				m.addLog(fmt.Sprintf("Erro ao remover projeto: %v", err))
			} else {
				if cfg, err := store.Load(); err == nil {
					m.cfg = cfg
				}
				// Remove from memory list immediately
				projIdx := -1
				for i, p := range m.projects {
					if p.Path == proj.Path {
						projIdx = i
						break
					}
				}
				if projIdx != -1 {
					m.projects = append(m.projects[:projIdx], m.projects[projIdx+1:]...)
				}

				// Remove any sync sessions linked to this project
				if sess, ok := m.sessMgr.GetSession(m.activeHost); ok && sess != nil {
					for _, s := range sess.Syncs {
						if s.RemoteDir == proj.Path || strings.HasPrefix(s.RemoteDir, proj.Path+"/") {
							// Stop watchers if running in live session
							if hostSyncs, exists := m.syncSessions[m.activeHost]; exists {
								if live, exists := hostSyncs[s.ID]; exists {
									if live.localWatcher != nil {
										live.localWatcher.Stop()
									}
									if live.remoteWatcher != nil {
										live.remoteWatcher.Stop()
									}
									if live.stopChan != nil {
										close(live.stopChan)
									}
									delete(hostSyncs, s.ID)
								}
							}
							_ = m.sessMgr.RemoveSync(m.activeHost, s.ID)
							m.addLog(fmt.Sprintf("Sync %s atrelado ao projeto '%s' parado e removido.", s.ID, proj.Name))
						}
					}
					m.selectedSyncRow = 0
				}

				// Clamp selectedProjectRow
				newItems := m.buildProjectTree()
				if len(newItems) > 0 && m.selectedProjectRow >= len(newItems) {
					m.selectedProjectRow = len(newItems) - 1
				} else if len(newItems) == 0 {
					m.selectedProjectRow = 0
				}
				m.addLog(fmt.Sprintf("Projeto '%s' removido.", proj.Name))
			}
		} else {
			m.addLog("Exclusão do projeto cancelada.")
		}
	}
	return nil
}

// registerProject cadastra um projeto (pasta remota, opcionalmente vinculada a
// uma pasta local sincronizada) no host ativo e recarrega a config em memória.
// Retorna erro se o cadastro falhar (ex: projeto duplicado), para que quem
// chama possa evitar disparar ações subsequentes (como iniciar um sync) para
// um projeto que não foi de fato gravado.
func (m *AppModel) registerProject(remotePath, localDir string) error {
	name := filepath.Base(remotePath)
	store := config.NewStore()
	if err := store.AddProject(m.activeHost, config.Project{
		Name:       name,
		RemotePath: remotePath,
		LocalDir:   localDir,
	}); err != nil {
		m.addLog(fmt.Sprintf("Erro ao cadastrar projeto: %v", err))
		return err
	}

	if cfg, err := store.Load(); err == nil {
		m.cfg = cfg
	}

	// Adiciona à lista em memória imediatamente — sem isso o projeto só
	// apareceria na aba Projetos (e vinculado a um sync na aba Syncs) no
	// próximo tick do checkProjectsCmd, até 1s depois do cadastro.
	m.projects = append(m.projects, Project{
		Path:     remotePath,
		Name:     name,
		LocalDir: localDir,
	})

	m.addLog(fmt.Sprintf("Projeto '%s' cadastrado em %s.", name, m.activeHost))
	return nil
}

// createTunnelLive inicializa o túnel em background na TUI
func (m *AppModel) createTunnelLive(val string, direction tunnel.Direction) {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		m.addLog("Erro: Config de host ativa não encontrada")
		return
	}

	// Parseia portas
	parts := strings.Split(val, ":")
	var remotePort, localPort int
	var err error

	if len(parts) == 2 {
		remotePort, err = strconv.Atoi(parts[0])
		if err != nil {
			m.addLog(fmt.Sprintf("Erro ao parsear porta remota: %v", err))
			return
		}
		localPort, err = strconv.Atoi(parts[1])
		if err != nil {
			m.addLog(fmt.Sprintf("Erro ao parsear porta local: %v", err))
			return
		}
	} else {
		remotePort, err = strconv.Atoi(val)
		if err != nil {
			m.addLog(fmt.Sprintf("Erro ao parsear porta: %v", err))
			return
		}
		localPort = remotePort
	}

	// Garante cliente SSH conectado
	client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro de conexão: %v", err))
		return
	}

	// Garante tunnel manager
	mgr, exists := m.tunnelManagers[m.activeHost]
	if !exists {
		mgr = tunnel.NewManager(client, &hostCfg, m.activeHost, m.cfg.Tunnel.AutoReconnect, m.cfg.Tunnel.ReconnectDelayDuration())
		m.tunnelManagers[m.activeHost] = mgr
	}

	// Inicia túnel
	id, err := mgr.Add(localPort, remotePort, direction)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro ao iniciar túnel: %v", err))
		return
	}

	// Salva no session manager
	_ = m.sessMgr.AddTunnel(m.activeHost, session.TunnelEntry{
		ID:         id,
		RemotePort: remotePort,
		LocalPort:  localPort,
		Direction:  direction.String(),
	})

	if direction == tunnel.DirectionLocal {
		m.addLog(fmt.Sprintf("Túnel %s criado com sucesso: local:%d -> remoto:%d", id, localPort, remotePort))
	} else {
		m.addLog(fmt.Sprintf("Túnel %s criado com sucesso: remoto:%d -> local:%d", id, remotePort, localPort))
	}
}

// createSyncLiveCmd prepara a conexão, a engine e os watchers de uma nova
// sincronização em background (tea.Cmd), sem bloquear o loop de eventos da
// TUI. A varredura inicial do diretório remoto via SFTP (feita dentro de
// RemoteWatcher.Start) pode levar vários segundos em hosts reais — se isso
// rodasse direto dentro de Update(), a TUI inteira travaria sem feedback
// nenhum até terminar. syncID é gerado e mostrado como pendente ANTES desta
// chamada (ver o confirm do dirpicker em Update()), para que o sync apareça
// na lista imediatamente com um status de "verificando..." em vez de só
// surgir (ou nem isso, em caso de erro) quando a conexão terminar.
func (m *AppModel) createSyncLiveCmd(syncID, val string) tea.Cmd {
	host := m.activeHost

	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return syncStartedMsg{err: fmt.Errorf("host ativo não configurado"), host: host, syncID: syncID}
		}

		parts := strings.Split(val, ":")
		var localDir, remoteDir string

		if len(parts) == 2 {
			localDir = strings.TrimSpace(parts[0])
			remoteDir = strings.TrimSpace(parts[1])
		} else {
			localDir = "."
			remoteDir = strings.TrimSpace(val)
		}

		if remoteDir == "" {
			return syncStartedMsg{err: fmt.Errorf("diretório remoto é obrigatório"), host: host, syncID: syncID}
		}

		absLocal, err := filepath.Abs(localDir)
		if err != nil {
			return syncStartedMsg{err: fmt.Errorf("caminho local inválido: %w", err), host: host, syncID: syncID}
		}

		// Colisão de pasta local: dois syncs (mesmo de hosts diferentes)
		// escrevendo na mesma árvore local corromperiam a reconciliação
		if otherHost, other, found := m.sessMgr.FindSyncByLocalDir(absLocal, syncID); found {
			return syncStartedMsg{err: fmt.Errorf("pasta local %s colide com o sync %s do host %s (%s)", absLocal, other.ID, otherHost, other.LocalDir), host: host, syncID: syncID}
		}

		if m.cfg.Daemon.Enabled {
			daemonClient, err := daemon.Connect()
			if err != nil {
				return syncStartedMsg{err: fmt.Errorf("erro ao conectar no daemon: %w", err), host: host, syncID: syncID}
			}
			if _, err := daemonClient.CreateSync(daemonapi.CreateSyncRequest{
				Host:        host,
				LocalDir:    absLocal,
				RemoteDir:   remoteDir,
				Mode:        "bidirectional",
				Project:     matchProjectName(m.projects, remoteDir),
				InitialSync: m.cfg.Sync.InitialSync,
			}); err != nil {
				return syncStartedMsg{err: fmt.Errorf("erro ao criar sync no daemon: %w", err), host: host, syncID: syncID}
			}
			return syncStartedMsg{host: host, syncID: syncID, localDir: absLocal, remoteDir: remoteDir, daemon: true}
		}

		// Garante conexão SSH e SFTP
		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			return syncStartedMsg{err: fmt.Errorf("erro SSH: %w", err), host: host, syncID: syncID}
		}

		sftpCli, err := m.getOrCreateSFTPClient(host, client)
		if err != nil {
			return syncStartedMsg{err: fmt.Errorf("erro SFTP: %w", err), host: host, syncID: syncID}
		}

		engine, localWatcher, remoteWatcher, err := m.startSyncEngine(host, syncID, absLocal, remoteDir, client, sftpCli)
		if err != nil {
			return syncStartedMsg{err: err, host: host, syncID: syncID}
		}

		return syncStartedMsg{
			host:          host,
			syncID:        syncID,
			localDir:      absLocal,
			remoteDir:     remoteDir,
			engine:        engine,
			localWatcher:  localWatcher,
			remoteWatcher: remoteWatcher,
		}
	}
}

// startSyncEngine cria a engine de sync e liga os watchers local/remoto para
// um (host, syncID, dirs) já resolvidos. Usado tanto para sincronizações
// novas (createSyncLiveCmd) quanto para religar as já persistidas no boot
// (restoreSyncsCmd).
func (m *AppModel) startSyncEngine(host, syncID, absLocal, remoteDir string, client *internalssh.Client, sftpCli *sftp.Client) (*internalsync.Engine, *watcher.LocalWatcher, watcher.Stopper, error) {
	engine, err := internalsync.NewEngine(
		syncID,
		absLocal,
		remoteDir,
		host,
		m.cfg.Sync.IgnorePatterns,
		internalsync.ConflictStrategy(m.cfg.Sync.ConflictStrategy),
		client,
		sftpCli,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("erro ao iniciar engine de sync: %w", err)
	}

	engine.OnFileSuccess = func(path string, action string) {
		var direction string
		switch action {
		case "upload", "remote_delete":
			direction = "LOCAL -> REMOTE"
		case "download", "local_delete":
			direction = "REMOTE -> LOCAL"
		}
		m.addLog(fmt.Sprintf("[%s] %s: %s", syncID, direction, path))
	}

	engine.OnConflict = func(path string, winner string) {
		m.addLog(fmt.Sprintf("[%s] ⚡ CONFLITO em %s — venceu %s (%s)", syncID, path, winner, engine.ConflictStrategy))
	}

	if engine.StateWarning != "" {
		m.addLog(fmt.Sprintf("[%s] ⚠ %s", syncID, engine.StateWarning))
	}

	// Watcher local. onWarn vai para m.addLog (aba Logs) em vez de
	// os.Stderr, que corromperia visualmente a TUI em alt-screen.
	localWatcher, err := watcher.NewLocalWatcher(engine.LocalDir, 200*time.Millisecond, engine.IgnoreMatcher(), m.addLog, func() {
		go func() {
			_, err := engine.SyncExec()
			if err != nil {
				m.addLog(fmt.Sprintf("[%s] Erro ao sincronizar local: %v", syncID, err))
			}
		}()
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("erro ao criar watcher local: %w", err)
	}

	if err := localWatcher.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("erro ao iniciar watcher local: %w", err)
	}

	onRemoteChange := func() {
		go func() {
			_, err := engine.SyncExec()
			if err != nil {
				m.addLog(fmt.Sprintf("[%s] Erro ao sincronizar remoto: %v", syncID, err))
			}
		}()
	}

	// Watcher remoto: unlarp-agent (inotify no container) quando disponível,
	// senão polling SFTP. Detect e Start fazem I/O de rede — assim como o
	// RemoteWatcher.Start (varredura SFTP síncrona), esta função sempre deve
	// rodar fora de Update() (dentro de um tea.Cmd), nunca no goroutine
	// principal da TUI.
	var remoteWatcher watcher.Stopper
	if agentClient := m.getOrCreateAgentClient(host, client); agentClient != nil {
		// atomic.Pointer: o redial troca o client sem corrida com o SyncExec
		// que usa o RemoteSnapshotFn em outro goroutine
		var agentRef atomic.Pointer[agent.Client]
		agentRef.Store(agentClient)
		aw := watcher.NewAgentWatcher(remoteDir, agentClient, m.cfg.Sync.IgnorePatterns, m.addLog, func() (*agent.Client, error) {
			hostCfg, ok := m.cfg.Hosts[host]
			if !ok {
				return nil, fmt.Errorf("host '%s' não configurado", host)
			}
			newClient, err := m.getOrCreateSSHClient(host, &hostCfg)
			if err != nil {
				return nil, err
			}
			if newSFTP, err := m.getOrCreateSFTPClient(host, newClient); err == nil {
				engine.UpdateSFTPClient(newSFTP)
			}
			newAgent := agent.New(newClient)
			agentRef.Store(newAgent)
			return newAgent, nil
		}, onRemoteChange)
		if err := aw.Start(); err != nil {
			m.addLog(fmt.Sprintf("[%s] Agent detectado mas falhou ao observar (%v); usando polling SFTP.", syncID, err))
		} else {
			remoteWatcher = aw
			engine.RemoteSnapshotFn = func() (internalsync.Snapshot, error) {
				return agentRef.Load().Snapshot(remoteDir, m.cfg.Sync.IgnorePatterns)
			}
			m.addLog(fmt.Sprintf("[%s] Monitorando remoto via unlarp-agent (inotify).", syncID))
		}
	}
	if remoteWatcher == nil {
		pollInterval := m.cfg.Sync.PollIntervalDuration()
		rw := watcher.NewRemoteWatcher(remoteDir, sftpCli, pollInterval, engine.IgnoreMatcher(), m.addLog, func() (*sftp.Client, error) {
			hostCfg, ok := m.cfg.Hosts[host]
			if !ok {
				return nil, fmt.Errorf("host '%s' não configurado", host)
			}
			newClient, err := m.getOrCreateSSHClient(host, &hostCfg)
			if err != nil {
				return nil, err
			}
			newSFTP, err := m.getOrCreateSFTPClient(host, newClient)
			if err != nil {
				return nil, err
			}
			engine.UpdateSFTPClient(newSFTP)
			return newSFTP, nil
		}, onRemoteChange)
		rw.Start()
		remoteWatcher = rw
	}

	return engine, localWatcher, remoteWatcher, nil
}

// restoreSyncsCmd religa, de forma automática e invisível, os syncs
// persistidos de um host (session.SyncEntry em ~/.unlarp/state.json) que
// ainda não têm engine/watchers vivos nesta execução da TUI — cobre tanto o
// boot do app quanto a troca para um host que não tinha sido ativado ainda.
func (m *AppModel) restoreSyncsCmd(host string) tea.Cmd {
	sess, ok := m.sessMgr.GetSession(host)
	if !ok || len(sess.Syncs) == 0 {
		return nil
	}

	var cmds []tea.Cmd
	for _, entry := range sess.Syncs {
		if hostSyncs, exists := m.syncSessions[host]; exists {
			if _, alreadyLive := hostSyncs[entry.ID]; alreadyLive {
				continue
			}
		}

		// Sync de outro processo vivo (ex: `unlarp sync start` num terminal):
		// religar aqui duplicaria a engine. Só exibe (renderSyncs mostra "externo").
		if entry.Alive() && entry.PID != os.Getpid() {
			continue
		}

		// Restore é re-tentado periodicamente (tick de reload); não insistir
		// em entradas que já falharam nesta execução para não spammar os logs.
		if m.failedRestores[entry.ID] {
			continue
		}

		entry := entry
		cmds = append(cmds, func() tea.Msg {
			hostCfg, ok := m.cfg.Hosts[host]
			if !ok {
				return syncStartedMsg{err: fmt.Errorf("host '%s' não configurado", host), host: host, syncID: entry.ID, restored: true}
			}

			client, err := m.getOrCreateSSHClient(host, &hostCfg)
			if err != nil {
				return syncStartedMsg{err: fmt.Errorf("erro SSH ao restaurar sync %s: %w", entry.ID, err), host: host, syncID: entry.ID, restored: true}
			}

			sftpCli, err := m.getOrCreateSFTPClient(host, client)
			if err != nil {
				return syncStartedMsg{err: fmt.Errorf("erro SFTP ao restaurar sync %s: %w", entry.ID, err), host: host, syncID: entry.ID, restored: true}
			}

			engine, localWatcher, remoteWatcher, err := m.startSyncEngine(host, entry.ID, entry.LocalDir, entry.RemoteDir, client, sftpCli)
			if err != nil {
				return syncStartedMsg{err: err, host: host, syncID: entry.ID, restored: true}
			}

			return syncStartedMsg{
				host:          host,
				syncID:        entry.ID,
				localDir:      entry.LocalDir,
				remoteDir:     entry.RemoteDir,
				engine:        engine,
				localWatcher:  localWatcher,
				remoteWatcher: remoteWatcher,
				restored:      true,
			}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleSyncStarted processa o resultado assíncrono de createSyncLiveCmd ou
// restoreSyncsCmd: registra a sessão viva, persiste no session manager
// (exceto quando restaurado — já está persistido) e dispara a reconciliação
// inicial de arquivos.
func (m *AppModel) handleSyncStarted(msg syncStartedMsg) {
	m.removePendingSync(msg.host, msg.syncID)

	if msg.err != nil {
		if msg.restored {
			// Não re-tentar a cada tick de reload — uma falha basta por execução.
			m.failedRestores[msg.syncID] = true
		}
		m.addLog(fmt.Sprintf("Erro ao criar sincronização: %v", msg.err))
		return
	}

	if msg.daemon {
		// Sem engine local: o daemon já persistiu a entry (Owner=daemon) em
		// state.json. Ela aparece sozinha no próximo tick de reload, exibida
		// como "externo" pelo mesmo guard que outros processos externos usam.
		m.addLog(fmt.Sprintf("Sincronização criada no daemon local em %s.", msg.host))
		return
	}

	if _, exists := m.syncSessions[msg.host]; !exists {
		m.syncSessions[msg.host] = make(map[string]*liveSyncSession)
	}
	if _, alreadyLive := m.syncSessions[msg.host][msg.syncID]; alreadyLive {
		return
	}

	stopChan := make(chan struct{})
	m.syncSessions[msg.host][msg.syncID] = &liveSyncSession{
		id:            msg.syncID,
		engine:        msg.engine,
		localWatcher:  msg.localWatcher,
		remoteWatcher: msg.remoteWatcher,
		stopChan:      stopChan,
	}

	if msg.restored {
		m.addLog(fmt.Sprintf("Sincronização %s restaurada automaticamente em %s.", msg.syncID, msg.host))
	} else {
		if err := m.sessMgr.AddSync(msg.host, session.SyncEntry{
			ID:        msg.syncID,
			LocalDir:  msg.localDir,
			RemoteDir: msg.remoteDir,
			Mode:      "bidirectional",
			LastSync:  time.Now(),
			Project:   matchProjectName(m.projects, msg.remoteDir),
			PID:       os.Getpid(),
			Owner:     "tui",
		}); err != nil {
			m.addLog(fmt.Sprintf("Erro ao salvar registro de sync: %v", err))
		}
		m.addLog(fmt.Sprintf("Sincronização %s iniciada com sucesso em %s.", msg.syncID, msg.host))
	}

	engine := msg.engine
	syncID := msg.syncID
	go func() {
		m.addLog(fmt.Sprintf("[%s] Iniciando reconciliação de arquivos...", syncID))
		count, err := engine.SyncExec()
		if err != nil {
			m.addLog(fmt.Sprintf("[%s] Erro no sync inicial: %v", syncID, err))
		} else {
			m.addLog(fmt.Sprintf("[%s] Sync inicial finalizado. %d modificações aplicadas.", syncID, count))
		}
	}()
}

// handleDeleteSelection deleta o item que está selecionado na view ativa
func (m *AppModel) handleDeleteSelection() tea.Cmd {
	if m.activeTab == tabAccounts {
		names := m.hostAccounts()
		if len(names) == 0 || m.selectedAccountRow >= len(names) {
			return nil
		}
		m.pendingAccount = names[m.selectedAccountRow]
		m.promptType = "account_delete_confirm"
		m.promptActive = true
		m.textInput.SetValue("n")
		m.textInput.Placeholder = "s/n"
		m.textInput.Focus()
		return textinput.Blink
	}

	if m.activeTab == tabProjects {
		items := m.buildProjectTree()
		if len(items) == 0 || m.selectedProjectRow >= len(items) {
			return nil
		}
		item := items[m.selectedProjectRow]
		if item.IsProject {
			proj := item.Project
			m.pendingProject = proj
			m.promptType = "project_delete_confirm"
			m.promptActive = true
			m.textInput.SetValue("n")
			m.textInput.Placeholder = "s/n"
			m.textInput.Focus()
			return textinput.Blink
		} else if item.Session != nil {
			name := item.Session.Name
			m.addLog(fmt.Sprintf("Finalizando sessão Tmux %s...", name))
			m.tmuxSessions = nil
			return m.killTmuxSessionCmd(name)
		}
	}

	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return nil
	}

	if m.activeTab == tabDashboard && len(m.tmuxSessions) > 0 {
		name := m.tmuxSessions[m.selectedTmuxRow].Name
		m.addLog(fmt.Sprintf("Finalizando sessão Tmux %s...", name))
		m.selectedTmuxRow = 0
		m.tmuxSessions = nil
		return m.killTmuxSessionCmd(name)
	}

	if m.activeTab == tabSyncs {
		// Resolve o sync pelo item da árvore (a árvore inclui linhas de arquivos
		// expandidos e pendings — indexar sess.Syncs direto pegava a entry errada)
		items := m.buildSyncTree()
		if len(items) == 0 || m.selectedSyncRow >= len(items) {
			return nil
		}
		item := items[m.selectedSyncRow]
		if !item.IsSync || item.PendingSync != nil {
			return nil
		}
		s := item.Sync

		// Sync de outro processo vivo: só remove o registro (matar o PID de uma
		// TUI/CLI alheia por aqui seria surpresa demais; use `unlarp sync stop`)
		if s.Alive() && s.PID != os.Getpid() {
			_ = m.sessMgr.RemoveSync(item.Host, s.ID)
			m.addLog(fmt.Sprintf("Registro do sync externo %s removido (processo dono pid %d segue rodando; use `unlarp sync stop %s`).", s.ID, s.PID, s.ID))
			m.selectedSyncRow = 0
			return nil
		}

		// Encerra watchers se estiver rodando na live session
		if hostSyncs, exists := m.syncSessions[item.Host]; exists {
			if live, exists := hostSyncs[s.ID]; exists {
				if live.localWatcher != nil {
					live.localWatcher.Stop()
				}
				if live.remoteWatcher != nil {
					live.remoteWatcher.Stop()
				}
				if live.stopChan != nil {
					close(live.stopChan)
				}
				delete(hostSyncs, s.ID)
			}
		}

		_ = m.sessMgr.RemoveSync(item.Host, s.ID)
		m.addLog(fmt.Sprintf("Sessão de sync %s parada e removida.", s.ID))
		m.selectedSyncRow = 0

	} else if m.activeTab == tabTunnels && len(sess.Tunnels) > 0 {
		// Para o túnel selecionado
		t := sess.Tunnels[m.selectedTunnelRow]

		// Para o listener live
		if mgr, exists := m.tunnelManagers[m.activeHost]; exists {
			_ = mgr.Remove(t.ID)
		}

		_ = m.sessMgr.RemoveTunnel(m.activeHost, t.ID)
		m.addLog(fmt.Sprintf("Túnel %s parado e removido.", t.ID))
		m.selectedTunnelRow = 0
	}
	return nil
}

func (m *AppModel) getOrCreateSSHClient(hostName string, hostCfg *config.Host) (*internalssh.Client, error) {
	m.sshMu.Lock()
	client, exists := m.sshClients[hostName]
	if exists && client.IsConnected() {
		m.sshMu.Unlock()
		return client, nil
	}
	m.sshMu.Unlock()

	// Cria e conecta fora do mutex lock para evitar contenção e travamento ao sair
	newClient, err := internalssh.NewClient(hostCfg)
	if err != nil {
		return nil, err
	}

	if err := newClient.Connect(); err != nil {
		return nil, err
	}

	m.sshMu.Lock()
	defer m.sshMu.Unlock()

	// Verifica se outra goroutine criou um cliente válido nesse meio tempo
	existingClient, exists := m.sshClients[hostName]
	if exists && existingClient.IsConnected() {
		_ = newClient.Close()
		return existingClient, nil
	}

	m.sshClients[hostName] = newClient
	// A conexão SSH foi recriada, então qualquer *sftp.Client ou *agent.Client
	// em cache está preso à conexão morta anterior — descarta para recriar.
	delete(m.sftpClients, hostName)
	delete(m.agentClients, hostName)
	return newClient, nil
}

// getOrCreateAgentClient detecta o unlarp-agent uma vez por host e cacheia o
// resultado (inclusive a ausência, para não pagar o handshake a cada tick).
// O cache é invalidado junto com a conexão SSH em getOrCreateSSHClient.
func (m *AppModel) getOrCreateAgentClient(hostName string, client *internalssh.Client) *agent.Client {
	m.sshMu.Lock()
	ac, checked := m.agentClients[hostName]
	m.sshMu.Unlock()
	if checked {
		return ac
	}

	ac = agent.Detect(client) // fora do mutex: handshake com timeout de 2s

	m.sshMu.Lock()
	m.agentClients[hostName] = ac
	m.sshMu.Unlock()
	return ac
}

func (m *AppModel) getOrCreateSFTPClient(hostName string, client *internalssh.Client) (*sftp.Client, error) {
	m.sshMu.Lock()
	sftpCli, exists := m.sftpClients[hostName]
	if exists {
		m.sshMu.Unlock()
		return sftpCli, nil
	}
	m.sshMu.Unlock()

	// Cria fora do mutex lock para evitar travar a TUI se a rede estiver lenta
	newSFTP, err := internalssh.NewSFTPClient(client)
	if err != nil {
		return nil, err
	}

	m.sshMu.Lock()
	defer m.sshMu.Unlock()

	// Checa dupla criação
	existingSFTP, exists := m.sftpClients[hostName]
	if exists {
		_ = newSFTP.Inner().Close()
		return existingSFTP, nil
	}

	m.sftpClients[hostName] = newSFTP.Inner()
	return newSFTP.Inner(), nil
}

// removePendingSync tira um sync da lista otimista de "verificando..." —
// chamado quando syncStartedMsg chega (sucesso ou erro), já que a partir daí
// ele passa a existir de verdade (persistido) ou nunca existiu.
func (m *AppModel) removePendingSync(host, syncID string) {
	list := m.pendingSyncs[host]
	for i, p := range list {
		if p.id == syncID {
			m.pendingSyncs[host] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

func generateRandomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// checkTmuxCmd retorna um comando assíncrono para listar sessões tmux remotas
func (m *AppModel) checkTmuxCmd() tea.Cmd {
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok {
			return tmuxSessionsMsg(nil)
		}

		client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
		if err != nil {
			return tmuxSessionsMsg(nil)
		}

		// Via agent quando presente: resposta estruturada, sem parsing de shell
		if ac := m.getOrCreateAgentClient(m.activeHost, client); ac != nil {
			if resp, err := ac.Projects("", nil); err == nil {
				sessions := make([]TmuxSession, 0, len(resp.Tmux))
				for _, t := range resp.Tmux {
					s := TmuxSession{
						Name:     t.Name,
						Windows:  t.Windows,
						Attached: t.Attached,
						Command:  t.Command,
						Path:     t.Path,
						PaneDead: t.PaneDead,
					}
					if t.ActivityUnix > 0 {
						s.Activity = time.Unix(t.ActivityUnix, 0)
					}
					sessions = append(sessions, s)
				}
				return tmuxSessionsMsg(sessions)
			}
		}

		// Uma linha por sessão: dados da sessão + comando/diretório do painel ativo
		// da janela ativa (o que está de fato rodando ali no momento).
		stdout, _, err := client.RunCommand("tmux list-panes -a -f '#{&&:#{window_active},#{pane_active}}' -F '#{session_name}|#{session_windows}|#{?session_attached,1,0}|#{pane_current_command}|#{pane_current_path}|#{pane_dead}|#{window_activity}' 2>/dev/null")
		if err != nil {
			return tmuxSessionsMsg(nil)
		}

		var sessions []TmuxSession
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) < 3 {
				continue
			}

			name := parts[0]
			windows, _ := strconv.Atoi(parts[1])
			attached := parts[2] == "1"

			session := TmuxSession{
				Name:     name,
				Windows:  windows,
				Attached: attached,
			}
			if len(parts) >= 5 {
				session.Command = parts[3]
				session.Path = parts[4]
			}
			if len(parts) >= 7 {
				session.PaneDead = parts[5] == "1"
				if ts, err := strconv.ParseInt(parts[6], 10, 64); err == nil && ts > 0 {
					session.Activity = time.Unix(ts, 0)
				}
			}

			sessions = append(sessions, session)
		}
		return tmuxSessionsMsg(sessions)
	}
}

// healProjectsOnce dispara git.HealAllProjects em background pra todos os
// projetos cadastrados em hostName, uma única vez por sessão da TUI (evita
// repetir o bootstrap a cada tick de checkProjectsCmd). Best-effort: erros
// só viram log de audit dentro de EnsureRemoteRepo, nunca bloqueiam a UI —
// cobre projetos sem sync ativo, como um remoto sincronizado antes dessa
// feature existir e que nunca ganhou um .git.
func (m *AppModel) healProjectsOnce(hostName string, hostCfg config.Host, client *internalssh.Client) {
	m.sshMu.Lock()
	if m.healedHosts[hostName] {
		m.sshMu.Unlock()
		return
	}
	m.healedHosts[hostName] = true
	m.sshMu.Unlock()

	sftpCli, err := m.getOrCreateSFTPClient(hostName, client)
	if err != nil {
		return
	}

	projects := make([]git.HealProject, 0, len(hostCfg.Projects))
	for _, p := range hostCfg.Projects {
		projects = append(projects, git.HealProject{Name: p.Name, LocalDir: p.LocalDir, RemotePath: p.RemotePath})
	}

	go git.HealAllProjects(client, sftpCli, projects)
}

// checkProjectsCmd retorna um comando assíncrono que atualiza o status rico do Git
// de cada projeto já cadastrado (config.Host.Projects) no host ativo de uma só vez.
func (m *AppModel) checkProjectsCmd() tea.Cmd {
	// Snapshot dos paths das sessões tmux fora do closure (que roda em outro
	// goroutine): eles entram no mesmo batch git dos projetos, para que a TUI
	// mostre branch/estado git por sessão (worktrees têm paths próprios).
	sessionPaths := make([]string, 0, len(m.tmuxSessions))
	seenPaths := make(map[string]bool)
	for _, ts := range m.tmuxSessions {
		if ts.Path != "" && !seenPaths[ts.Path] {
			seenPaths[ts.Path] = true
			sessionPaths = append(sessionPaths, ts.Path)
		}
	}

	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok || (len(hostCfg.Projects) == 0 && len(sessionPaths) == 0) {
			return projectsGitMsg{projects: nil, gitInfos: nil}
		}

		gitInfos := make(map[string]git.RemoteGitInfo)
		branches := make(map[string]string)

		if client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg); err == nil {
			m.healProjectsOnce(m.activeHost, hostCfg, client)

			allPaths := make([]string, 0, len(hostCfg.Projects)+len(sessionPaths))
			queried := make(map[string]bool)
			for _, p := range hostCfg.Projects {
				queried[p.RemotePath] = true
				allPaths = append(allPaths, p.RemotePath)
			}
			for _, sp := range sessionPaths {
				if !queried[sp] {
					allPaths = append(allPaths, sp)
				}
			}

			// Via agent quando presente: uma chamada HTTP com git estruturado
			// de todos os paths, sem parsing de shell
			agentDone := false
			if ac := m.getOrCreateAgentClient(m.activeHost, client); ac != nil {
				if resp, err := ac.Projects("", allPaths); err == nil {
					for _, pi := range resp.Projects {
						gitInfos[pi.Path] = pi.Git
						if pi.Git.IsGitRepo {
							branches[pi.Path] = pi.Git.Branch
						}
					}
					agentDone = true
				}
			}

			if !agentDone {
				var paths strings.Builder
				for _, p := range allPaths {
					paths.WriteString(shellQuote(p))
					paths.WriteString(" ")
				}

				gitCmd := fmt.Sprintf(
					`for p in %s; do `+
						`echo "PROJ|$p" && `+
						`cd "$p" 2>/dev/null && git rev-parse --is-inside-work-tree >/dev/null 2>&1 && `+
						`echo "REPO|true" && `+
						`echo "BRANCH|$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" && `+
						`echo "HASH|$(git rev-parse --short HEAD 2>/dev/null)" && `+
						`echo "MSG|$(git log -1 --format=%%%%s 2>/dev/null)" && `+
						`echo "TIME|$(git log -1 --format=%%%%aI 2>/dev/null)" && `+
						`echo "DIRTY|$(git status --porcelain 2>/dev/null | head -1)" && `+
						`REMOTE=$(git remote | grep -x "origin" || git remote | head -1) && `+
						`echo "REMOTE|$REMOTE" && `+
						`echo "URL|$(git remote get-url $REMOTE 2>/dev/null)" && `+
						`echo "AB|$(git rev-list --left-right --count HEAD...$REMOTE/$(git rev-parse --abbrev-ref HEAD) 2>/dev/null)" || `+
						`echo "REPO|false"; `+
						`done`,
					strings.TrimSpace(paths.String()),
				)

				if stdout, _, err := client.RunCommand(gitCmd); err == nil {
					var currentProj string
					var currentInfo git.RemoteGitInfo

					for _, line := range strings.Split(stdout, "\n") {
						line = strings.TrimSpace(line)
						if line == "" {
							continue
						}

						parts := strings.SplitN(line, "|", 2)
						if len(parts) != 2 {
							continue
						}

						key := parts[0]
						val := strings.TrimSpace(parts[1])

						switch key {
						case "PROJ":
							if currentProj != "" {
								gitInfos[currentProj] = currentInfo
							}
							currentProj = val
							currentInfo = git.RemoteGitInfo{}
						case "REPO":
							currentInfo.IsGitRepo = val == "true"
						case "BRANCH":
							currentInfo.Branch = val
							branches[currentProj] = val
						case "HASH":
							currentInfo.CommitHash = val
						case "MSG":
							currentInfo.CommitMessage = val
						case "TIME":
							if t, err := time.Parse(time.RFC3339, val); err == nil {
								currentInfo.CommitTime = t
							}
						case "DIRTY":
							currentInfo.IsDirty = val != ""
						case "REMOTE":
							currentInfo.RemoteName = val
						case "URL":
							currentInfo.RemoteURL = val
						case "AB":
							abParts := strings.Fields(val)
							if len(abParts) == 2 {
								currentInfo.AheadBehind.Ahead, _ = strconv.Atoi(abParts[0])
								currentInfo.AheadBehind.Behind, _ = strconv.Atoi(abParts[1])
							}
						}
					}
					if currentProj != "" {
						gitInfos[currentProj] = currentInfo
					}
				}
			}
		}

		projects := make([]Project, 0, len(hostCfg.Projects))
		for _, p := range hostCfg.Projects {
			projects = append(projects, Project{
				Path:     p.RemotePath,
				Name:     p.Name,
				Branch:   branches[p.RemotePath],
				LocalDir: p.LocalDir,
				Account:  p.Account,
			})
		}
		return projectsGitMsg{projects: projects, gitInfos: gitInfos}
	}
}

// captureProjectCmd busca o tail recente do pane Tmux de um projeto (a sessão
// tmux é nomeada igual ao projeto — ver o atalho "c" na aba Projetos).
func (m *AppModel) captureProjectCmd(name string) tea.Cmd {
	host := m.activeHost
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return nil
		}

		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			return nil
		}

		stdout, _, err := client.RunCommand(fmt.Sprintf("tmux capture-pane -pt %s -S -200 2>/dev/null", shellQuote(name)))
		if err != nil {
			return nil
		}

		return projectOutputMsg{name: name, output: stdout}
	}
}

// shellQuote coloca uma string entre aspas simples seguras para uso em shell POSIX
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// handleOnboardingSubmit processa a etapa do assistente de onboarding
func (m *AppModel) handleOnboardingSubmit() tea.Cmd {
	val := strings.TrimSpace(m.textInput.Value())

	switch m.onboardingStep {
	case 0: // Welcome screen
		m.onboardingStep = 1
		m.textInput.SetValue("")
		m.textInput.Placeholder = "dev-server"
		m.textInput.Focus()
	case 1: // Profile Name
		if val == "" {
			val = "dev-server"
		}
		m.obProfileName = val
		m.onboardingStep = 2
		m.textInput.SetValue("")
		m.textInput.Placeholder = "192.168.1.100"
		m.textInput.Focus()
	case 2: // Host IP
		if val == "" {
			return nil
		}
		m.obHost = val
		m.onboardingStep = 3
		m.textInput.SetValue("2222")
		m.textInput.Focus()
	case 3: // Port
		port := 2222
		if val != "" {
			if p, err := strconv.Atoi(val); err == nil {
				port = p
			}
		}
		m.obPort = port
		m.onboardingStep = 4
		m.textInput.SetValue("root")
		m.textInput.Focus()
	case 4: // User
		user := "root"
		if val != "" {
			user = val
		}
		m.obUser = user
		m.onboardingStep = 5
		m.textInput.SetValue("/workspace")
		m.textInput.Focus()
	case 5: // Workspace path
		workspace := "/workspace"
		if val != "" {
			workspace = val
		}
		m.obWorkspace = workspace
		m.onboardingStep = 6
		m.textInput.SetValue("s")
		m.textInput.Placeholder = "s/n"
		m.textInput.Focus()
	case 6: // Inject key Confirm
		m.obSetupKey = strings.ToLower(val) == "s" || strings.ToLower(val) == "sim" || val == ""
		if m.obSetupKey {
			m.onboardingStep = 7
			m.textInput.Blur()
			return m.runSSHKeySetupCmd()
		} else {
			m.onboardingStep = 8
			m.textInput.Blur()
		}
	case 8: // Finalizar e inicializar dashboard
		m.activeHost = m.obProfileName
		_ = m.finalizeOnboarding()
		m.sessMgr.SetActive(m.activeHost)
		m.isOnboarding = false
	case 9: // VPS Password submitted
		m.obPassword = val
		m.onboardingStep = 7 // Voltar para processando spinner
		m.textInput.Blur()
		m.textInput.EchoMode = textinput.EchoNormal // restaurar
		return m.runSSHKeySetupCmd()
	}
	return nil
}

// runSSHKeySetupCmd instala a chave pública no container de onboarding
func (m *AppModel) runSSHKeySetupCmd() tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return obSetupFinishedMsg{err: err}
		}

		candidates := []string{
			filepath.Join(home, ".ssh", "id_ed25519.pub"),
			filepath.Join(home, ".ssh", "id_ecdsa.pub"),
			filepath.Join(home, ".ssh", "id_rsa.pub"),
		}

		pubKeyPath := ""
		for _, path := range candidates {
			if _, err := os.Stat(path); err == nil {
				pubKeyPath = path
				break
			}
		}

		if pubKeyPath == "" {
			return obSetupFinishedMsg{err: fmt.Errorf("nenhuma chave pública local encontrada em ~/.ssh/")}
		}

		pubKey, err := os.ReadFile(pubKeyPath)
		if err != nil {
			return obSetupFinishedMsg{err: err}
		}
		pubKeyStr := strings.TrimSpace(string(pubKey))

		// Tenta conexão direta primeiro
		containerHost := config.Host{
			Host:      m.obHost,
			Port:      m.obPort,
			User:      m.obUser,
			Workspace: m.obWorkspace,
		}

		directClient, err := internalssh.NewClient(&containerHost)
		if err == nil {
			if err = directClient.Connect(); err == nil {
				defer directClient.Close()
				// Conexão direta funcionou! Instala a chave.
				installCmd := fmt.Sprintf(
					`mkdir -p ~/.ssh && chmod 700 ~/.ssh && `+
						`grep -qxF '%s' ~/.ssh/authorized_keys 2>/dev/null || echo '%s' >> ~/.ssh/authorized_keys && `+
						`chmod 600 ~/.ssh/authorized_keys && chown -R $(whoami):$(id -gn) ~/.ssh`,
					pubKeyStr, pubKeyStr,
				)
				_, _, err2 := directClient.RunCommand(installCmd)
				return obSetupFinishedMsg{err: err2}
			}
		}

		// Se a conexão direta falhou e for localhost/127.0.0.1, tenta rodar docker exec local
		if m.obHost == "localhost" || m.obHost == "127.0.0.1" {
			cmd := exec.Command("docker", "exec", "-i", "workspace_machine", "sh", "-c",
				"mkdir -p /root/.ssh && cat >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root")
			cmd.Stdin = strings.NewReader(pubKeyStr + "\n")
			if err := cmd.Run(); err != nil {
				cmdLabel := exec.Command("sh", "-c", "docker exec -i $(docker ps -qf label=com.docker.compose.service=workspace) sh -c 'mkdir -p /root/.ssh && cat >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root'")
				cmdLabel.Stdin = strings.NewReader(pubKeyStr + "\n")
				err = cmdLabel.Run()
			}
			return obSetupFinishedMsg{err: err}
		}

		// Para hosts remotos, tenta bootstrap via host SSH do VPS (porta 22)
		vpsHost := config.Host{
			Host:      m.obHost,
			Port:      22,
			User:      m.obUser,
			Workspace: m.obWorkspace,
			Password:  m.obPassword,
		}

		vpsClient, err := internalssh.NewClient(&vpsHost)
		if err != nil {
			return obSetupFinishedMsg{err: fmt.Errorf("falha ao configurar conexão com o host VPS: %w", err)}
		}

		if err = vpsClient.Connect(); err != nil {
			// Se falhar com o usuário padrão, tenta root
			vpsHost.User = "root"
			rootClient, errRoot := internalssh.NewClient(&vpsHost)
			if errRoot == nil {
				if errRoot = rootClient.Connect(); errRoot == nil {
					vpsClient = rootClient
					err = nil
				}
			}

			// Se ainda assim falhou e não temos senha fornecida, solicita a senha interativamente na TUI
			if err != nil && m.obPassword == "" {
				return obVpsPasswordRequiredMsg{}
			}

			if err != nil {
				return obSetupFinishedMsg{err: fmt.Errorf("conexão direta falhou e não foi possível conectar ao host VPS (porta 22): %w", err)}
			}
		}
		defer vpsClient.Close()

		// Conectado ao VPS! Roda docker exec para injetar a chave
		bootstrapCmd := fmt.Sprintf(
			`docker exec -i $(docker ps -qf label=com.docker.compose.service=workspace) sh -c `+
				`"mkdir -p /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root"`,
			pubKeyStr,
		)

		_, stderr, err := vpsClient.RunCommand(bootstrapCmd)
		if err != nil {
			// Fallback para nome padrão de container se a label falhar
			bootstrapCmdFallback := fmt.Sprintf(
				`docker exec -i workspace_machine sh -c `+
					`"mkdir -p /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root"`,
				pubKeyStr,
			)
			_, stderr, err = vpsClient.RunCommand(bootstrapCmdFallback)
			if err != nil {
				return obSetupFinishedMsg{err: fmt.Errorf("falha ao injetar chave via docker exec no VPS: %s (%w)", stderr, err)}
			}
		}

		return obSetupFinishedMsg{err: nil}
	}
}

// finalizeOnboarding grava a configuração final no arquivo ~/.unlarp.yaml
func (m *AppModel) finalizeOnboarding() error {
	store := config.NewStore()
	h := config.Host{
		Host:      m.obHost,
		Port:      m.obPort,
		User:      m.obUser,
		Workspace: m.obWorkspace,
	}

	err := store.AddHost(m.obProfileName, h)
	if err != nil {
		return err
	}

	cfg, err := store.Load()
	if err != nil {
		return err
	}

	m.cfg = cfg

	// Recarrega hosts da barra lateral
	var hostNames []string
	for name := range cfg.Hosts {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)
	m.hostNames = hostNames
	m.selectedHost = 0

	// Altera a seleção na barra lateral para o novo host adicionado
	for idx, name := range hostNames {
		if name == m.obProfileName {
			m.selectedHost = idx
			break
		}
	}

	return nil
}

// killTmuxSessionCmd envia comando para matar sessão tmux remota
func (m *AppModel) killTmuxSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok {
			return nil
		}

		client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
		if err != nil {
			return nil
		}

		_, _, _ = client.RunCommand("tmux kill-session -t " + name)
		return m.checkTmuxCmd()()
	}
}

// createTmuxSessionCmd envia comando para criar sessão tmux remota desanexada
func (m *AppModel) createTmuxSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok {
			return nil
		}

		client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
		if err != nil {
			return nil
		}

		m.addLog(fmt.Sprintf("Criando sessão Tmux remota %s...", name))
		_, _, _ = client.RunCommand("export LANG=C.UTF-8; export LC_ALL=C.UTF-8; tmux -u new-session -d -s " + name)
		return m.checkTmuxCmd()()
	}
}

// hostAccounts retorna os nomes das contas Claude Code do host ativo, ordenados
func (m *AppModel) hostAccounts() []string {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(hostCfg.Accounts))
	for n := range hostCfg.Accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// accountDirFor resolve o CLAUDE_CONFIG_DIR de uma conta no host ativo ("" = sem injeção)
func (m *AppModel) accountDirFor(account string) string {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		return ""
	}
	dir, _ := hostCfg.AccountDir(account)
	return dir
}

// createProjectSessionCmd cria uma sessão tmux remota com o diretório de trabalho
// especificado; accountDir != "" injeta
// CLAUDE_CONFIG_DIR via `tmux -e` (export antes do tmux não propaga com server já ativo)
func (m *AppModel) createProjectSessionCmd(name, path, accountDir string) tea.Cmd {
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok {
			return nil
		}

		client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
		if err != nil {
			return nil
		}

		m.addLog(fmt.Sprintf("Criando sessão Tmux '%s' no diretório '%s'...", name, path))
		envFlag := ""
		if accountDir != "" {
			// mkdir -p protege contra volume resetado; -e fixa a env na sessão
			_, _, _ = client.RunCommand("mkdir -p " + shellQuote(accountDir))
			envFlag = " -e " + shellQuote("CLAUDE_CONFIG_DIR="+accountDir)
		}
		cmdStr := fmt.Sprintf("export LANG=C.UTF-8; export LC_ALL=C.UTF-8; tmux -u new-session -d -s %s -c %s%s", shellQuote(name), shellQuote(path), envFlag)
		_, _, _ = client.RunCommand(cmdStr)
		return m.checkTmuxCmd()()
	}
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default: // "linux", "freebsd", etc.
		cmd = "xdg-open"
		args = []string{url}
	}

	return exec.Command(cmd, args...).Start()
}

type gitCheckResultMsg struct {
	syncID      string
	projectPath string
	info        git.RemoteGitInfo
	diverged    bool
	reason      string
}

type gitResolvedMsg struct {
	syncID string
	err    error
	action string
}

type projectsGitMsg struct {
	projects []Project
	gitInfos map[string]git.RemoteGitInfo
}

// switchBranchCmd faz a troca de branch remota sem correr contra o sync:
// pausa as engines do projeto, executa o checkout (agent quando presente,
// SSH senão), atualiza o GitGuard com o novo estado, reconcilia e retoma.
// worktreeAddCmd cria uma worktree DENTRO do repo do projeto e abre uma sessão
// tmux nela. Sem pause/resume de sync: o HEAD do repo principal não muda
// (GitGuard não dispara) e a worktree entra no sync como arquivos novos.
// Tenta com branch existente; se não existir, cria com -b.
func (m *AppModel) worktreeAddCmd(proj Project, branch string) tea.Cmd {
	host := m.activeHost
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return nil
		}
		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			m.addLog(fmt.Sprintf("Worktree: erro SSH: %v", err))
			return nil
		}

		wtPath := strings.TrimSuffix(proj.Path, "/") + "/" + git.WorktreeRelPath(branch)
		sessionName := proj.Name + "-wt-" + git.SanitizeWorktreeName(branch)

		m.addLog(fmt.Sprintf("Criando worktree %s (branch %s)...", wtPath, branch))

		if ac := m.getOrCreateAgentClient(host, client); ac != nil {
			if _, err := ac.GitOp(proj.Path, agentapi.GitOpWorktreeAdd, []string{wtPath, branch}); err != nil {
				if _, err2 := ac.GitOp(proj.Path, agentapi.GitOpWorktreeAdd, []string{"-b", branch, wtPath}); err2 != nil {
					m.addLog(fmt.Sprintf("Worktree add falhou: %v", err2))
					return nil
				}
			}
		} else {
			cmdStr := fmt.Sprintf("git -C %s worktree add %s %s || git -C %s worktree add -b %s %s",
				shellQuote(proj.Path), shellQuote(wtPath), shellQuote(branch),
				shellQuote(proj.Path), shellQuote(branch), shellQuote(wtPath))
			if _, stderr, err := client.RunCommand(cmdStr); err != nil {
				m.addLog(fmt.Sprintf("Worktree add falhou: %s", strings.TrimSpace(stderr)))
				return nil
			}
		}

		// Esconde as worktrees do `git status` do repo principal (info/exclude
		// é local ao repo, não versionado). Falha aqui não é fatal.
		excludeCmd := fmt.Sprintf(
			"grep -qxF '.claude/worktree-*' %[1]s/.git/info/exclude 2>/dev/null || echo '.claude/worktree-*' >> %[1]s/.git/info/exclude",
			shellQuote(proj.Path))
		_, _, _ = client.RunCommand(excludeCmd)

		if proj.LocalDir != "" {
			m.addLog(fmt.Sprintf("Worktree criada — com o sync do projeto ativo ela aparece em %s/%s.", strings.TrimSuffix(proj.LocalDir, "/"), git.WorktreeRelPath(branch)))
		} else {
			m.addLog("Worktree criada.")
		}
		// Worktree herda a conta do projeto (sem picker)
		return m.createProjectSessionCmd(sessionName, wtPath, m.accountDirFor(proj.Account))()
	}
}

func (m *AppModel) switchBranchCmd(projPath, branch string) tea.Cmd {
	host := m.activeHost
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[host]
		if !ok {
			return nil
		}
		client, err := m.getOrCreateSSHClient(host, &hostCfg)
		if err != nil {
			m.addLog(fmt.Sprintf("Troca de branch: erro SSH: %v", err))
			return nil
		}

		// Engines sincronizando este projeto (ou subpastas dele)
		var engines []*internalsync.Engine
		if hostSyncs, ok := m.syncSessions[host]; ok {
			for _, sc := range hostSyncs {
				if sc.engine != nil && (sc.engine.RemoteDir == projPath || strings.HasPrefix(sc.engine.RemoteDir, projPath+"/")) {
					engines = append(engines, sc.engine)
				}
			}
		}

		for _, e := range engines {
			e.Pause("troca de branch em andamento")
		}
		resumeAll := func() {
			for _, e := range engines {
				e.Resume()
			}
		}

		m.addLog(fmt.Sprintf("Trocando branch de %s para %s (%d sync(s) pausado(s))...", projPath, branch, len(engines)))

		var newBranch, newCommit string
		if ac := m.getOrCreateAgentClient(host, client); ac != nil {
			resp, err := ac.GitOp(projPath, agentapi.GitOpCheckout, []string{branch})
			if err != nil {
				resumeAll()
				m.addLog(fmt.Sprintf("Checkout falhou: %v", err))
				return nil
			}
			newBranch, newCommit = resp.Branch, resp.Commit
		} else {
			_, stderr, err := client.RunCommand(fmt.Sprintf("git -C %s checkout %s", shellQuote(projPath), shellQuote(branch)))
			if err != nil {
				resumeAll()
				m.addLog(fmt.Sprintf("Checkout falhou: %s (%v)", strings.TrimSpace(stderr), err))
				return nil
			}
			if info, err := git.GetRemoteGitInfo(client, projPath); err == nil && info.IsGitRepo {
				newBranch, newCommit = info.Branch, info.CommitHash
			}
		}

		// Atualiza o GitGuard antes de retomar, para o check periódico não
		// interpretar a troca como divergência e pausar de novo
		for _, e := range engines {
			e.UpdateGitState(newCommit, newBranch)
		}
		resumeAll()

		// Um ciclo de reconciliação explícito propaga a árvore da nova branch
		for _, e := range engines {
			eng := e
			go func() {
				if _, err := eng.SyncExec(); err != nil {
					m.addLog(fmt.Sprintf("[%s] Erro ao reconciliar após troca de branch: %v", eng.ID, err))
				}
			}()
		}

		m.addLog(fmt.Sprintf("Branch de %s agora é %s (%s).", projPath, newBranch, newCommit))
		return m.checkProjectsCmd()()
	}
}

func (m *AppModel) checkGitCmd() tea.Cmd {
	host := m.activeHost
	hostCfg, ok := m.cfg.Hosts[host]
	if !ok {
		return nil
	}

	sess, ok := m.sessMgr.GetSession(host)
	if !ok || len(sess.Syncs) == 0 {
		return nil
	}

	var cmds []tea.Cmd
	for _, s := range sess.Syncs {
		syncEntry := s
		cmds = append(cmds, func() tea.Msg {
			client, err := m.getOrCreateSSHClient(host, &hostCfg)
			if err != nil {
				return nil
			}

			info, err := git.GetRemoteGitInfo(client, syncEntry.RemoteDir)
			if err != nil {
				return nil
			}

			if !info.IsGitRepo {
				return nil
			}

			var diverged bool
			var reason string

			activeHostSyncs, exists := m.syncSessions[host]
			if exists {
				sessCtx, exists := activeHostSyncs[syncEntry.ID]
				if exists && sessCtx.engine != nil {
					guard := sessCtx.engine.GetGitGuard()

					if guard.LastKnownCommit == "" {
						sessCtx.engine.UpdateGitState(info.CommitHash, info.Branch)
					} else {
						if guard.LastKnownBranch != info.Branch {
							diverged = true
							reason = fmt.Sprintf("branch mudou no remote (%s -> %s)", guard.LastKnownBranch, info.Branch)
						} else if guard.LastKnownCommit != info.CommitHash {
							diverged = true
							reason = fmt.Sprintf("remote avançou (%s -> %s)", guard.LastKnownCommit, info.CommitHash)
						}
					}
				}
			}

			if diverged {
				if exists {
					if sessCtx, exists := activeHostSyncs[syncEntry.ID]; exists && sessCtx.engine != nil {
						sessCtx.engine.Pause(reason)
					}
				}
			}

			return gitCheckResultMsg{
				syncID:      syncEntry.ID,
				projectPath: syncEntry.RemoteDir,
				info:        info,
				diverged:    diverged,
				reason:      reason,
			}
		})
	}

	return tea.Batch(cmds...)
}

func (m *AppModel) resolveGitCmd(syncID, action, localDir, remoteDir, branch string) tea.Cmd {
	host := m.activeHost
	return func() tea.Msg {
		activeHostSyncs, exists := m.syncSessions[host]
		if !exists {
			return gitResolvedMsg{syncID: syncID, action: action, err: fmt.Errorf("sessão de sync ativa não encontrada")}
		}
		sessCtx, exists := activeHostSyncs[syncID]
		if !exists || sessCtx.engine != nil {
			// Se o engine existe, a gente manipula
		}
		if !exists || sessCtx.engine == nil {
			return gitResolvedMsg{syncID: syncID, action: action, err: fmt.Errorf("engine de sync ativa não encontrada")}
		}

		switch action {
		case "pull":
			err := git.PullLocal(localDir, "origin", branch)
			if err != nil {
				return gitResolvedMsg{syncID: syncID, action: action, err: err}
			}

			hostCfg, ok := m.cfg.Hosts[host]
			if ok {
				client, err := m.getOrCreateSSHClient(host, &hostCfg)
				if err == nil {
					if info, err := git.GetRemoteGitInfo(client, remoteDir); err == nil && info.IsGitRepo {
						sessCtx.engine.UpdateGitState(info.CommitHash, info.Branch)
					}
				}
			}

			sessCtx.engine.Resume()

		case "force":
			hostCfg, ok := m.cfg.Hosts[host]
			if ok {
				client, err := m.getOrCreateSSHClient(host, &hostCfg)
				if err == nil {
					if info, err := git.GetRemoteGitInfo(client, remoteDir); err == nil && info.IsGitRepo {
						sessCtx.engine.UpdateGitState(info.CommitHash, info.Branch)
					}
				}
			}
			sessCtx.engine.Resume()

		case "download-only":
			sessCtx.engine.ConflictStrategy = internalsync.StrategyRemoteWins
			sessCtx.engine.Resume()
		}

		return gitResolvedMsg{syncID: syncID, action: action}
	}
}
