package tui

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
	"github.com/CaioFaSoares/unlarp/internal/tunnel"
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

// TmuxSession representa metadados de uma sessão Tmux remota
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
}

type tmuxSessionsMsg []TmuxSession

type resumeTuiMsg struct {
	err error
}

type obSetupFinishedMsg struct {
	err error
}

type obSetupFinishedMsgType struct {
	err error
}

// AppModel é o modelo central do Bubble Tea
type AppModel struct {
	width  int
	height int

	// Configs e Estado
	cfg          *config.Config
	hostNames    []string
	selectedHost int // Index na barra lateral
	activeHost   string

	// Abas e Foco
	activeTab    int  // 0: Dashboard, 1: Syncs, 2: Tunnels, 3: Logs
	sidebarFocus bool // true: foco na barra lateral, false: no painel principal

	// Seleção de linhas nas abas
	selectedSyncRow   int
	selectedTunnelRow int
	selectedTmuxRow   int

	// Prompts interativos para criação interna
	promptActive bool
	promptType   string // "sync" | "tunnel"
	textInput    textinput.Model

	// Clientes e Managers ativos por HostName
	sshClients     map[string]*internalssh.Client
	sftpClients    map[string]*sftp.Client
	tunnelManagers map[string]*tunnel.Manager
	syncSessions   map[string]map[string]*liveSyncSession

	// Sessões Tmux
	tmuxSessions []TmuxSession

	// Componentes e Sub-sistemas
	spinner spinner.Model
	sessMgr *session.Manager

	// Onboarding Wizard
	isOnboarding   bool
	onboardingStep int
	obHost         string
	obPort         int
	obUser         string
	obWorkspace    string
	obSetupKey     bool

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
		activeTab:      0,
		sidebarFocus:   true,
		promptActive:   false,
		textInput:      ti,
		sshClients:     make(map[string]*internalssh.Client),
		sftpClients:    make(map[string]*sftp.Client),
		tunnelManagers: make(map[string]*tunnel.Manager),
		syncSessions:   make(map[string]map[string]*liveSyncSession),
		isOnboarding:   isOnboarding,
		onboardingStep: 0,
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

// Init inicializa a aplicação Bubble Tea
func (m AppModel) Init() tea.Cmd {
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
	for _, client := range m.sshClients {
		client.Close()
	}
}

// Update gerencia mensagens e entrada do usuário
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		// Dispara tick recorrente
		cmds = append(cmds, tickCmd())
		if !m.isOnboarding {
			cmds = append(cmds, m.checkTmuxCmd())
		}

	case tmuxSessionsMsg:
		m.tmuxSessions = msg
		if m.selectedTmuxRow >= len(m.tmuxSessions) {
			m.selectedTmuxRow = 0
		}

	case resumeTuiMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro ao retornar do SSH: %v", msg.err))
		} else {
			m.addLog("Retornou do terminal SSH.")
		}
		cmds = append(cmds, m.checkTmuxCmd())

	case obSetupFinishedMsg:
		if msg.err != nil {
			m.addLog(fmt.Sprintf("Erro de setup: %v", msg.err))
		} else {
			m.addLog("Chave SSH pública injetada com sucesso.")
		}
		m.onboardingStep = 7
		m.textInput.Blur()

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
				cmd := m.handlePromptSubmit()
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
		case "q", "ctrl+c":
			m.Cleanup()
			return m, tea.Quit

		case "tab":
			m.sidebarFocus = !m.sidebarFocus

		case "left", "right":
			if !m.sidebarFocus {
				if msg.String() == "left" {
					m.activeTab = (m.activeTab - 1 + 4) % 4
				} else {
					m.activeTab = (m.activeTab + 1) % 4
				}
				m.selectedSyncRow = 0
				m.selectedTunnelRow = 0
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
			}

		case "c":
			// Atalho para abrir terminal interativo SSH (usando Tmux por padrão)
			sessionName := "unlarp"
			if !m.sidebarFocus && m.activeTab == 0 && len(m.tmuxSessions) > 0 {
				sessionName = m.tmuxSessions[m.selectedTmuxRow].Name
			}

			exe, err := os.Executable()
			if err != nil {
				exe = "unlarp"
			}

			c := exec.Command(exe, "connect", m.activeHost, "--tmux", "--tmux-session", sessionName)
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return resumeTuiMsg{err: err}
			})

		case "n":
			// Atalho para criar nova sessão Tmux remota (somente na aba Dashboard e fora da barra lateral)
			if !m.sidebarFocus && m.activeTab == 0 {
				m.promptType = "new_tmux_session"
				m.promptActive = true
				m.textInput.SetValue("")
				m.textInput.Placeholder = "nome-da-sessao (ex: api-worker)"
				m.textInput.Focus()
				return m, textinput.Blink
			}

		case "s":
			// Atalho para iniciar sync (somente se não estiver na barra lateral)
			if !m.sidebarFocus {
				m.promptType = "sync"
				m.promptActive = true
				m.textInput.SetValue("")
				m.textInput.Placeholder = "local_dir:remote_dir (ex: .:/workspace/app)"
				m.textInput.Focus()
				return m, textinput.Blink
			}

		case "t":
			// Atalho para iniciar túnel (somente se não estiver na barra lateral)
			if !m.sidebarFocus {
				m.promptType = "tunnel"
				m.promptActive = true
				m.textInput.SetValue("")
				m.textInput.Placeholder = "porta_remota[:porta_local] (ex: 5432 ou 3000:8080)"
				m.textInput.Focus()
				return m, textinput.Blink
			}

		case "x":
			// Atalho para excluir item selecionado na aba atual
			if !m.sidebarFocus {
				cmd := m.handleDeleteSelection()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	}

	return m, tea.Batch(cmds...)
}

// View desenha a TUI
func (m AppModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Inicializando terminal..."
	}

	if m.isOnboarding {
		return m.renderOnboarding(m.width, m.height)
	}

	// Título do App
	header := styles.TitleStyle.Render("UNLARP — Remote Workspace Manager")
	spinnerStr := m.spinner.View()
	headerRow := lipgloss.JoinHorizontal(lipgloss.Center, header, spinnerStr)

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

func (m AppModel) renderSidebar(height int) string {
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

func (m AppModel) renderFooter() string {
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
		actions := "%s Navegar | %s Confirmar | %s Mudar Foco | %s Sair"
		keys := []string{
			styles.KeyStyle.Render("↑/↓/j/k"),
			styles.KeyStyle.Render("Enter"),
			styles.KeyStyle.Render("Tab"),
			styles.KeyStyle.Render("q"),
		}

		if !m.sidebarFocus {
			if m.activeTab == 0 {
				actions = "%s Navegar | %s SSH Attach | %s Nova Sessão | %s Destruir Sessão | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("c"),
					styles.KeyStyle.Render("n"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == 1 {
				actions = "%s Navegar | %s Novo Sync | %s Parar Sync | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("s"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
			} else if m.activeTab == 2 {
				actions = "%s Navegar | %s Novo Túnel | %s Parar Túnel | %s Mudar Foco | %s Sair"
				keys = []string{
					styles.KeyStyle.Render("↑/↓/j/k"),
					styles.KeyStyle.Render("t"),
					styles.KeyStyle.Render("x"),
					styles.KeyStyle.Render("Tab"),
					styles.KeyStyle.Render("q"),
				}
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
	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return
	}

	if m.activeTab == 0 && len(m.tmuxSessions) > 0 {
		m.selectedTmuxRow = (m.selectedTmuxRow - 1 + len(m.tmuxSessions)) % len(m.tmuxSessions)
	} else if m.activeTab == 1 && len(sess.Syncs) > 0 {
		m.selectedSyncRow = (m.selectedSyncRow - 1 + len(sess.Syncs)) % len(sess.Syncs)
	} else if m.activeTab == 2 && len(sess.Tunnels) > 0 {
		m.selectedTunnelRow = (m.selectedTunnelRow - 1 + len(sess.Tunnels)) % len(sess.Tunnels)
	}
}

func (m *AppModel) handleMainPanelDown() {
	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return
	}

	if m.activeTab == 0 && len(m.tmuxSessions) > 0 {
		m.selectedTmuxRow = (m.selectedTmuxRow + 1) % len(m.tmuxSessions)
	} else if m.activeTab == 1 && len(sess.Syncs) > 0 {
		m.selectedSyncRow = (m.selectedSyncRow + 1) % len(sess.Syncs)
	} else if m.activeTab == 2 && len(sess.Tunnels) > 0 {
		m.selectedTunnelRow = (m.selectedTunnelRow + 1) % len(sess.Tunnels)
	}
}

// handlePromptSubmit executa a ação de criação no background baseado no input
func (m *AppModel) handlePromptSubmit() tea.Cmd {
	val := strings.TrimSpace(m.textInput.Value())
	if val == "" {
		return nil
	}

	switch m.promptType {
	case "tunnel":
		m.createTunnelLive(val)
	case "sync":
		m.createSyncLive(val)
	case "new_tmux_session":
		return m.createTmuxSessionCmd(val)
	}
	return nil
}

// createTunnelLive inicializa o túnel em background na TUI
func (m *AppModel) createTunnelLive(val string) {
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
	id, err := mgr.Add(localPort, remotePort)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro ao iniciar túnel: %v", err))
		return
	}

	// Salva no session manager
	_ = m.sessMgr.AddTunnel(m.activeHost, session.TunnelEntry{
		ID:         id,
		RemotePort: remotePort,
		LocalPort:  localPort,
	})

	m.addLog(fmt.Sprintf("Túnel %s criado com sucesso: local:%d -> remoto:%d", id, localPort, remotePort))
}

// createSyncLive inicializa a sincronização em tempo real em background na TUI
func (m *AppModel) createSyncLive(val string) {
	hostCfg, ok := m.cfg.Hosts[m.activeHost]
	if !ok {
		m.addLog("Erro: Host ativo não configurado.")
		return
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
		m.addLog("Erro: Diretório remoto é obrigatório.")
		return
	}

	// Configura caminhos
	absLocal, err := filepath.Abs(localDir)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro no caminho local: %v", err))
		return
	}

	// Garante conexão SSH e SFTP
	client, err := m.getOrCreateSSHClient(m.activeHost, &hostCfg)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro SSH: %v", err))
		return
	}

	sftpCli, err := m.getOrCreateSFTPClient(m.activeHost, client)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro SFTP: %v", err))
		return
	}

	syncID := "s-" + generateRandomString(6)

	// Cria Engine
	engine, err := internalsync.NewEngine(
		syncID,
		absLocal,
		remoteDir,
		m.activeHost,
		m.cfg.Sync.IgnorePatterns,
		internalsync.ConflictStrategy(m.cfg.Sync.ConflictStrategy),
		client,
		sftpCli,
	)
	if err != nil {
		m.addLog(fmt.Sprintf("Erro ao iniciar engine de sync: %v", err))
		return
	}

	// Executa sync inicial no background para não congelar a TUI
	go func() {
		m.addLog(fmt.Sprintf("[%s] Iniciando reconciliação de arquivos...", syncID))
		count, err := engine.SyncExec()
		if err != nil {
			m.addLog(fmt.Sprintf("[%s] Erro no sync inicial: %v", syncID, err))
		} else {
			m.addLog(fmt.Sprintf("[%s] Sync inicial finalizado. %d modificações aplicadas.", syncID, count))
		}
	}()

	// Watcher local
	localWatcher, err := watcher.NewLocalWatcher(absLocal, 200*time.Millisecond, func() {
		go func() {
			_, err := engine.SyncExec()
			if err != nil {
				m.addLog(fmt.Sprintf("[%s] Erro ao sincronizar local: %v", syncID, err))
			}
		}()
	})
	if err != nil {
		m.addLog(fmt.Sprintf("Erro ao criar watcher local: %v", err))
		return
	}

	err = localWatcher.Start()
	if err != nil {
		m.addLog(fmt.Sprintf("Erro ao iniciar watcher local: %v", err))
		return
	}

	// Watcher remoto
	pollInterval := m.cfg.Sync.PollIntervalDuration()
	remoteWatcher := watcher.NewRemoteWatcher(remoteDir, sftpCli, pollInterval, engine.IgnoreMatcher(), func() {
		go func() {
			_, err := engine.SyncExec()
			if err != nil {
				m.addLog(fmt.Sprintf("[%s] Erro ao sincronizar remoto: %v", syncID, err))
			}
		}()
	})
	remoteWatcher.Start()

	stopChan := make(chan struct{})

	// Registra live session
	if _, exists := m.syncSessions[m.activeHost]; !exists {
		m.syncSessions[m.activeHost] = make(map[string]*liveSyncSession)
	}
	m.syncSessions[m.activeHost][syncID] = &liveSyncSession{
		id:            syncID,
		engine:        engine,
		localWatcher:  localWatcher,
		remoteWatcher: remoteWatcher,
		stopChan:      stopChan,
	}

	// Adiciona ao session manager persistente
	_ = m.sessMgr.AddSync(m.activeHost, session.SyncEntry{
		ID:        syncID,
		LocalDir:  absLocal,
		RemoteDir: remoteDir,
		Mode:      "bidirectional",
		LastSync:  time.Now(),
	})

	m.addLog(fmt.Sprintf("Sincronização %s iniciada com sucesso.", syncID))
}

// handleDeleteSelection deleta o item que está selecionado na view ativa
func (m *AppModel) handleDeleteSelection() tea.Cmd {
	sess, ok := m.sessMgr.GetSession(m.activeHost)
	if !ok {
		return nil
	}

	if m.activeTab == 0 && len(m.tmuxSessions) > 0 {
		name := m.tmuxSessions[m.selectedTmuxRow].Name
		m.addLog(fmt.Sprintf("Finalizando sessão Tmux %s...", name))
		m.selectedTmuxRow = 0
		return m.killTmuxSessionCmd(name)
	}

	if m.activeTab == 1 && len(sess.Syncs) > 0 {
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

	} else if m.activeTab == 2 && len(sess.Tunnels) > 0 {
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
	return newClient, nil
}

func (m *AppModel) getOrCreateSFTPClient(hostName string, client *internalssh.Client) (*sftp.Client, error) {
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

		// List sessions formatted: session_name|session_windows|attached_flag
		stdout, _, err := client.RunCommand("tmux list-sessions -F '#{session_name}|#{session_windows}|#{?session_attached,1,0}' 2>/dev/null")
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

			sessions = append(sessions, TmuxSession{
				Name:     name,
				Windows:  windows,
				Attached: attached,
			})
		}
		return tmuxSessionsMsg(sessions)
	}
}

// handleOnboardingSubmit processa a etapa do assistente de onboarding
func (m *AppModel) handleOnboardingSubmit() tea.Cmd {
	val := strings.TrimSpace(m.textInput.Value())

	switch m.onboardingStep {
	case 0: // Welcome screen
		m.onboardingStep = 1
		m.textInput.SetValue("")
		m.textInput.Placeholder = "192.168.1.100"
		m.textInput.Focus()
	case 1: // Host IP
		if val == "" {
			return nil
		}
		m.obHost = val
		m.onboardingStep = 2
		m.textInput.SetValue("2222")
		m.textInput.Focus()
	case 2: // Port
		port := 2222
		if val != "" {
			if p, err := strconv.Atoi(val); err == nil {
				port = p
			}
		}
		m.obPort = port
		m.onboardingStep = 3
		m.textInput.SetValue("root")
		m.textInput.Focus()
	case 3: // User
		user := "root"
		if val != "" {
			user = val
		}
		m.obUser = user
		m.onboardingStep = 4
		m.textInput.SetValue("/workspace")
		m.textInput.Focus()
	case 4: // Workspace path
		workspace := "/workspace"
		if val != "" {
			workspace = val
		}
		m.obWorkspace = workspace
		m.onboardingStep = 5
		m.textInput.SetValue("s")
		m.textInput.Placeholder = "s/n"
		m.textInput.Focus()
	case 5: // Inject key Confirm
		m.obSetupKey = strings.ToLower(val) == "s" || strings.ToLower(val) == "sim" || val == ""
		if m.obSetupKey {
			m.onboardingStep = 6
			m.textInput.Blur()
			return m.runSSHKeySetupCmd()
		} else {
			m.onboardingStep = 7
			m.textInput.Blur()
		}
	case 7: // Finalizar e inicializar dashboard
		m.activeHost = "remote-workspace"
		if m.obHost == "localhost" || m.obHost == "127.0.0.1" {
			m.activeHost = "local"
		}
		_ = m.finalizeOnboarding()
		m.sessMgr.SetActive(m.activeHost)
		m.isOnboarding = false
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

		host := config.Host{
			Host:      m.obHost,
			Port:      m.obPort,
			User:      m.obUser,
			Workspace: m.obWorkspace,
		}

		client, err := internalssh.NewClient(&host)
		if err != nil {
			return obSetupFinishedMsg{err: err}
		}

		if err := client.Connect(); err != nil {
			return obSetupFinishedMsg{err: err}
		}
		defer client.Close()

		installCmd := fmt.Sprintf(
			`mkdir -p ~/.ssh && chmod 700 ~/.ssh && `+
			`grep -qxF '%s' ~/.ssh/authorized_keys 2>/dev/null || echo '%s' >> ~/.ssh/authorized_keys && `+
			`chmod 600 ~/.ssh/authorized_keys && chown -R $(whoami):$(id -gn) ~/.ssh`,
			pubKeyStr, pubKeyStr,
		)

		_, _, err = client.RunCommand(installCmd)
		return obSetupFinishedMsg{err: err}
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

	err := store.AddHost(m.activeHost, h)
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
