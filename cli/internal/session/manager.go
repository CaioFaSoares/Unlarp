package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/fsutil"
)

// State representa o estado persistido de todas as sessões
type State struct {
	ActiveSession string              `json:"active_session"`
	Sessions      map[string]*Session `json:"sessions"`
}

// Session representa uma sessão de conexão com um host
type Session struct {
	Name        string        `json:"name"`
	ConnectedAt *time.Time    `json:"connected_at,omitempty"`
	Syncs       []SyncEntry   `json:"syncs,omitempty"`
	Tunnels     []TunnelEntry `json:"tunnels,omitempty"`
}

// SyncEntry representa uma sessão de sync ativa
type SyncEntry struct {
	ID             string    `json:"id"`
	LocalDir       string    `json:"local_dir"`
	RemoteDir      string    `json:"remote_dir"`
	Mode           string    `json:"mode"` // "bidirectional" | "push" | "pull"
	LastSync       time.Time `json:"last_sync"`
	GitBranch      string    `json:"git_branch,omitempty"`
	GitCommit      string    `json:"git_commit,omitempty"`
	GitGuardActive bool      `json:"git_guard_active,omitempty"`
}

// TunnelEntry representa um túnel ativo
type TunnelEntry struct {
	ID         string `json:"id"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
	Direction  string `json:"direction,omitempty"` // "remote" (padrão) | "local"
}

// Manager gerencia múltiplas sessões com persistência
type Manager struct {
	state     *State
	statePath string
	mu        sync.RWMutex
}

// NewManager cria um novo gerenciador de sessões
func NewManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	stateDir := filepath.Join(home, ".unlarp")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("erro ao criar diretório ~/.unlarp: %w", err)
	}

	m := &Manager{
		statePath: filepath.Join(stateDir, "state.json"),
	}

	if err := m.loadState(); err != nil {
		// Se o arquivo não existe, inicia com estado vazio
		m.state = &State{
			Sessions: make(map[string]*Session),
		}
	}

	return m, nil
}

// ActiveSession retorna o nome da sessão ativa
func (m *Manager) ActiveSession() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.ActiveSession
}

// SetActive define a sessão ativa
func (m *Manager) SetActive(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.ActiveSession = name

	// Garante que a sessão existe no state
	if _, exists := m.state.Sessions[name]; !exists {
		now := time.Now()
		m.state.Sessions[name] = &Session{
			Name:        name,
			ConnectedAt: &now,
		}
	}

	return m.saveState()
}

// GetSession retorna uma sessão pelo nome
func (m *Manager) GetSession(name string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.state.Sessions[name]
	return session, ok
}

// GetActiveSession retorna a sessão ativa
func (m *Manager) GetActiveSession() (*Session, bool) {
	return m.GetSession(m.ActiveSession())
}

// ListSessions retorna todas as sessões
func (m *Manager) ListSessions() map[string]*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copia para evitar data races
	result := make(map[string]*Session)
	for k, v := range m.state.Sessions {
		result[k] = v
	}
	return result
}

// getOrCreateSession retorna a sessão existente ou cria uma nova.
// Chamador deve segurar m.mu.Lock().
func (m *Manager) getOrCreateSession(name string) *Session {
	session, ok := m.state.Sessions[name]
	if !ok {
		now := time.Now()
		session = &Session{Name: name, ConnectedAt: &now}
		m.state.Sessions[name] = session
	}
	return session
}

// AddSync adiciona uma entrada de sync a uma sessão
func (m *Manager) AddSync(sessionName string, entry SyncEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.getOrCreateSession(sessionName)
	session.Syncs = append(session.Syncs, entry)
	return m.saveState()
}

// RemoveSync remove uma entrada de sync de uma sessão
func (m *Manager) RemoveSync(sessionName, syncID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.state.Sessions[sessionName]
	if !ok {
		return fmt.Errorf("sessão '%s' não encontrada", sessionName)
	}

	filtered := make([]SyncEntry, 0)
	for _, s := range session.Syncs {
		if s.ID != syncID {
			filtered = append(filtered, s)
		}
	}
	session.Syncs = filtered

	return m.saveState()
}

// AddTunnel adiciona um túnel a uma sessão
func (m *Manager) AddTunnel(sessionName string, entry TunnelEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.getOrCreateSession(sessionName)
	session.Tunnels = append(session.Tunnels, entry)
	return m.saveState()
}

// RemoveTunnel remove um túnel de uma sessão
func (m *Manager) RemoveTunnel(sessionName, tunnelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.state.Sessions[sessionName]
	if !ok {
		return fmt.Errorf("sessão '%s' não encontrada", sessionName)
	}

	filtered := make([]TunnelEntry, 0)
	for _, t := range session.Tunnels {
		if t.ID != tunnelID {
			filtered = append(filtered, t)
		}
	}
	session.Tunnels = filtered

	return m.saveState()
}

// RemoveSession remove uma sessão completamente
func (m *Manager) RemoveSession(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.state.Sessions, name)

	if m.state.ActiveSession == name {
		m.state.ActiveSession = ""
		// Tenta definir outra sessão como ativa
		for n := range m.state.Sessions {
			m.state.ActiveSession = n
			break
		}
	}

	return m.saveState()
}

// loadState carrega o estado do disco
func (m *Manager) loadState() error {
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	if state.Sessions == nil {
		state.Sessions = make(map[string]*Session)
	}

	m.state = &state
	return nil
}

// saveState salva o estado no disco
func (m *Manager) saveState() error {
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(m.statePath, data, 0600)
}
