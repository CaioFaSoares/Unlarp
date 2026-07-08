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
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/config"
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
	remoteWatcher *watcher.RemoteWatcher
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
	Command  string // comando em execução no painel ativo
	Path     string // diretório onde o comando está rodando
}

type tmuxSessionsMsg []TmuxSession

// Project representa um projeto cadastrado manualmente no host (ver config.Project)
type Project struct {
	Path     string
	Name     string
	Branch   string
	LocalDir string // pasta local vinculada, se um sync foi criado no cadastro
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
	remoteWatcher *watcher.RemoteWatcher
	restored      bool // true quando religado automaticamente a partir do state persistido, não criado pelo usuário agora
}

// Índices das abas do painel principal — Dashboard e Projetos ficam lado a lado
// por serem as mais usadas no dia a dia.
const (
	tabDashboard = iota
	tabProjects
	tabSyncs
	tabTunnels
	tabLogs
	tabCount
)

// AppModel é o modelo central do Bubble Tea
type AppModel struct {
	width  int
	height int
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
	tunnelManagers map[string]*tunnel.Manager
	syncSessions   map[string]map[string]*liveSyncSession
	pendingSyncs   map[string][]pendingSync

	// Sessões Tmux
	tmuxSessions []TmuxSession

	// Projetos (cadastrados manualmente por host, ver config.Project)
	projects           []Project
	selectedProjectRow int
	projectOutput      map[string]string
	expandedProjects   map[string]bool
	expandedSyncs      map[string]bool
	pendingProject     Project

	gitInfo        map[string]git.RemoteGitInfo // chave: RemotePath/RemoteDir do projeto
	gitAlerts      map[string]string            // chave: syncID ou RemotePath -> motivo do bloqueio
	gitTickCounter int

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
	dirPicker      *ui.DirPicker
	pickerActive   bool
	pickerStage    string // "local" | "remote"
	chosenLocal    string
	chosenRemote   string

	// Logs
	logs []string
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

	// Ordena hosts
	var hostNames []string
	selected := 0
	active := cfg.DefaultHost

	i := 0
	for name := range cfg.Hosts {
		hostNames = append(hostNames, name)
		if name == active {
			selected = i
		}
		i++
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
		cfg:            cfg,
		hostNames:      hostNames,
		selectedHost:   selected,
		activeHost:     active,
		activeTab:      tabDashboard,
		sidebarFocus:   true,
		promptActive:           false,
		pendingTunnelDirection: tunnel.DirectionLocal,
		textInput:              ti,
		sshMu:          &sync.Mutex{},
		sshClients:     make(map[string]*internalssh.Client),
		sftpClients:    make(map[string]*sftp.Client),
		tunnelManagers: make(map[string]*tunnel.Manager),
		syncSessions:   make(map[string]map[string]*liveSyncSession),
		pendingSyncs:   make(map[string][]pendingSync),
		projectOutput:    make(map[string]string),
		expandedProjects: make(map[string]bool),
		expandedSyncs:    make(map[string]bool),
		gitInfo:          make(map[string]git.RemoteGitInfo),
		gitAlerts:        make(map[string]string),
		gitTickCounter:   0,
		restoredHosts:    make(map[string]bool),
		isOnboarding:     isOnboarding,
		onboardingStep: 0,
		obProfileName:  "",
		obPort:         2222,
		obUser:         "root",
		obWorkspace:    "/workspace",
		spinner:        s,
		sessMgr:        sessMgr,
		logs: []string{
			"unlarp TUI inicializada com sucesso.",
			"Configuração carregada de ~/.unlarp.yaml.",
			"Abra a barra lateral (Tab) ou alterne abas (left/right).",
		},
	}, nil
}

// cleanupFinishedMsg é enviada quando a limpeza em background é concluída
type cleanupFinishedMsg struct{}

func (m *AppModel) cleanupCmd() tea.Cmd {
	return func() tea.Msg {
		m.Cleanup()
		return cleanupFinishedMsg{}
	}
}

// Init inicializa a aplicação Bubble Tea
func (m *AppModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tickCmd())
}

// Cleanup encerra todas as conexões e watchers ativos de background
func (m *AppModel) Cleanup() {
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
	for _, mgr := range m.tunnelManagers {
		mgr.Close()
	}

	m.sshMu.Lock()
	for _, client := range m.sshClients {
		client.Close()
	}
	m.sshMu.Unlock()
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
		// Dispara tick recorrente
		cmds = append(cmds, tickCmd())
		if !m.isOnboarding {
			cmds = append(cmds, m.checkTmuxCmd())
			cmds = append(cmds, m.checkProjectsCmd())

			m.gitTickCounter++
			if m.gitTickCounter >= 5 {
				m.gitTickCounter = 0
				if cmd := m.checkGitCmd(); cmd != nil {
					cmds = append(cmds, cmd)
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
		m.tmuxSessions = msg
		if m.selectedTmuxRow >= len(m.tmuxSessions) {
			m.selectedTmuxRow = 0
		}
		if items := m.buildProjectTree(); len(items) > 0 && m.selectedProjectRow >= len(items) {
			m.selectedProjectRow = 0
		}

	case projectsMsg:
		m.projects = msg
		// Projetos com sync ativo sobem para o topo — "ativos" separados do
		// resto sem precisar de um passo extra de curadoria manual.
		if sess, ok := m.sessMgr.GetSession(m.activeHost); ok {
			sort.SliceStable(m.projects, func(i, j int) bool {
				return matchProjectSync(sess.Syncs, m.projects[i].Path) != nil &&
					matchProjectSync(sess.Syncs, m.projects[j].Path) == nil
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
		cmds = append(cmds, m.checkTmuxCmd())

	case syncStartedMsg:
		m.handleSyncStarted(msg)

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

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

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

		case "p", "f":
			if !m.sidebarFocus && m.activeTab == tabProjects {
				items := m.buildProjectTree()
				if len(items) > 0 && m.selectedProjectRow < len(items) {
					item := items[m.selectedProjectRow]
					if item.IsProject {
						sess, sessOk := m.sessMgr.GetSession(m.activeHost)
						if sessOk {
							syncEntry := matchProjectSync(sess.Syncs, item.Project.Path)
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
			// Atalho para abrir terminal interativo SSH (usando Tmux por padrão)
			sessionName := "unlarp"
			cwd := ""
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

		case "s":
			// Atalho para iniciar sync (somente se não estiver na barra lateral)
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
							syncEntry := matchProjectSync(sess.Syncs, item.Project.Path)
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

func (m *AppModel) addLog(msg string) {
	timestamp := time.Now().Format("15:04:05")
	m.logs = append(m.logs, fmt.Sprintf("[%s] %s", timestamp, msg))
	if len(m.logs) > 100 {
		m.logs = m.logs[1:] // Mantém cap de 100
	}
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
			} else if m.activeTab == tabProjects {
				actions = "%s Navegar | %s Cadastrar | %s Expandir/Conectar | %s Nova Sessão | %s Remover/Kill | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("a"),
					styles.KeyStyle.Render("Enter/c"),
					styles.KeyStyle.Render("n"),
					styles.KeyStyle.Render("x"),
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
		return m.createProjectSessionCmd(sessionName, m.pendingProject.Path)
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
func (m *AppModel) startSyncEngine(host, syncID, absLocal, remoteDir string, client *internalssh.Client, sftpCli *sftp.Client) (*internalsync.Engine, *watcher.LocalWatcher, *watcher.RemoteWatcher, error) {
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

	// Watcher local. onWarn vai para m.addLog (aba Logs) em vez de
	// os.Stderr, que corromperia visualmente a TUI em alt-screen.
	localWatcher, err := watcher.NewLocalWatcher(absLocal, 200*time.Millisecond, engine.IgnoreMatcher(), m.addLog, func() {
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

	// Watcher remoto: Start() faz uma varredura SFTP completa e síncrona do
	// diretório remoto — por isso esta função sempre deve rodar fora de
	// Update() (dentro de um tea.Cmd), nunca no goroutine principal da TUI.
	pollInterval := m.cfg.Sync.PollIntervalDuration()
	remoteWatcher := watcher.NewRemoteWatcher(remoteDir, sftpCli, pollInterval, engine.IgnoreMatcher(), m.addLog, func() (*sftp.Client, error) {
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
	}, func() {
		go func() {
			_, err := engine.SyncExec()
			if err != nil {
				m.addLog(fmt.Sprintf("[%s] Erro ao sincronizar remoto: %v", syncID, err))
			}
		}()
	})
	remoteWatcher.Start()

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
		m.addLog(fmt.Sprintf("Erro ao criar sincronização: %v", msg.err))
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
	if m.activeTab == tabProjects {
		items := m.buildProjectTree()
		if len(items) == 0 || m.selectedProjectRow >= len(items) {
			return nil
		}
		item := items[m.selectedProjectRow]
		if item.IsProject {
			proj := item.Project
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
				
				// Clamp selectedProjectRow
				newItems := m.buildProjectTree()
				if len(newItems) > 0 && m.selectedProjectRow >= len(newItems) {
					m.selectedProjectRow = len(newItems) - 1
				} else if len(newItems) == 0 {
					m.selectedProjectRow = 0
				}
				m.addLog(fmt.Sprintf("Projeto '%s' removido.", proj.Name))
			}
			return nil
		} else if item.Session != nil {
			name := item.Session.Name
			m.addLog(fmt.Sprintf("Finalizando sessão Tmux %s...", name))
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
		return m.killTmuxSessionCmd(name)
	}

	if m.activeTab == tabSyncs && len(sess.Syncs) > 0 {
		// Para o sync selecionado
		s := sess.Syncs[m.selectedSyncRow]
		
		// Encerra watchers se estiver rodando na live session
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
	defer m.sshMu.Unlock()

	client, exists := m.sshClients[hostName]
	if exists && client.IsConnected() {
		return client, nil
	}

	// Cria e conecta
	newClient, err := internalssh.NewClient(hostCfg)
	if err != nil {
		return nil, err
	}

	if err := newClient.Connect(); err != nil {
		return nil, err
	}

	m.sshClients[hostName] = newClient
	// A conexão SSH foi recriada, então qualquer *sftp.Client em cache está
	// preso à conexão morta anterior — descarta para forçar a recriação.
	delete(m.sftpClients, hostName)
	return newClient, nil
}

func (m *AppModel) getOrCreateSFTPClient(hostName string, client *internalssh.Client) (*sftp.Client, error) {
	m.sshMu.Lock()
	defer m.sshMu.Unlock()

	sftpCli, exists := m.sftpClients[hostName]
	if exists {
		return sftpCli, nil
	}

	newSFTP, err := internalssh.NewSFTPClient(client)
	if err != nil {
		return nil, err
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

		// Uma linha por sessão: dados da sessão + comando/diretório do painel ativo
		// da janela ativa (o que está de fato rodando ali no momento).
		stdout, _, err := client.RunCommand("tmux list-panes -a -f '#{&&:#{window_active},#{pane_active}}' -F '#{session_name}|#{session_windows}|#{?session_attached,1,0}|#{pane_current_command}|#{pane_current_path}' 2>/dev/null")
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

			sessions = append(sessions, session)
		}
		return tmuxSessionsMsg(sessions)
	}
}

// checkProjectsCmd retorna um comando assíncrono que atualiza a branch atual
// de cada projeto já cadastrado (config.Host.Projects) no host ativo.
func (m *AppModel) checkProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		hostCfg, ok := m.cfg.Hosts[m.activeHost]
		if !ok || len(hostCfg.Projects) == 0 {
			return projectsMsg(nil)
		}

		// Branca é best-effort: se a conexão ou o comando falharem, os projetos
		// cadastrados ainda devem aparecer (só sem a branch preenchida) — um
		// projeto não deixa de existir por não ser repositório git ou por um
		// hiccup de rede.
		branches := make(map[string]string)
		if client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg); err == nil {
			var paths strings.Builder
			for _, p := range hostCfg.Projects {
				paths.WriteString(shellQuote(p.RemotePath))
				paths.WriteString(" ")
			}
			branchCmd := fmt.Sprintf(
				`for p in %s; do echo "$p|$(git -C "$p" rev-parse --abbrev-ref HEAD 2>/dev/null)"; done`,
				strings.TrimSpace(paths.String()),
			)

			if stdout, _, err := client.RunCommand(branchCmd); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
					parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
					if len(parts) == 2 {
						branches[parts[0]] = strings.TrimSpace(parts[1])
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
			})
		}
		return projectsMsg(projects)
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

// createProjectSessionCmd cria uma sessão tmux remota com o diretório de trabalho especificado
func (m *AppModel) createProjectSessionCmd(name, path string) tea.Cmd {
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
		cmdStr := fmt.Sprintf("export LANG=C.UTF-8; export LC_ALL=C.UTF-8; tmux -u new-session -d -s %s -c %s", shellQuote(name), shellQuote(path))
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
