package tunnel

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/config"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// Manager gerencia múltiplos forwarders para uma sessão
type Manager struct {
	forwarders map[string]*Forwarder
	sshClient  *internalssh.Client
	hostConfig *config.Host
	hostName   string

	autoReconnect  bool
	reconnectDelay time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

// ForwarderInfo contém informações de um forwarder para exibição
type ForwarderInfo struct {
	ID          string
	LocalPort   int
	RemotePort  int
	Status      ForwarderStatus
	Connections int32
	BytesIn     int64
	BytesOut    int64
	StartedAt   time.Time
	Error       error
}

// NewManager cria um novo tunnel manager
func NewManager(sshClient *internalssh.Client, hostConfig *config.Host, hostName string, autoReconnect bool, reconnectDelay time.Duration) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		forwarders:     make(map[string]*Forwarder),
		sshClient:      sshClient,
		hostConfig:     hostConfig,
		hostName:       hostName,
		autoReconnect:  autoReconnect,
		reconnectDelay: reconnectDelay,
		ctx:            ctx,
		cancel:         cancel,
	}

	if autoReconnect {
		go m.monitorReconnect()
	}

	return m
}

// Add cria e inicia um novo forwarder
func (m *Manager) Add(localPort, remotePort int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verifica se a porta local já está em uso por outro forwarder
	for _, f := range m.forwarders {
		if f.LocalPort == localPort {
			return "", fmt.Errorf("porta local %d já está em uso pelo túnel %s", localPort, f.ID)
		}
	}

	id := generateID()
	forwarder := NewForwarder(id, localPort, remotePort, m.sshClient.Conn())

	if err := forwarder.Start(); err != nil {
		return "", err
	}

	m.forwarders[id] = forwarder
	return id, nil
}

// Remove para e remove um forwarder
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	forwarder, ok := m.forwarders[id]
	if !ok {
		return fmt.Errorf("túnel '%s' não encontrado", id)
	}

	forwarder.Stop()
	delete(m.forwarders, id)
	return nil
}

// RemoveAll para e remove todos os forwarders
func (m *Manager) RemoveAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, f := range m.forwarders {
		f.Stop()
		delete(m.forwarders, id)
	}
}

// List retorna informações de todos os forwarders
func (m *Manager) List() []ForwarderInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]ForwarderInfo, 0, len(m.forwarders))
	for _, f := range m.forwarders {
		infos = append(infos, ForwarderInfo{
			ID:          f.ID,
			LocalPort:   f.LocalPort,
			RemotePort:  f.RemotePort,
			Status:      f.Status(),
			Connections: f.Connections(),
			BytesIn:     f.BytesIn(),
			BytesOut:    f.BytesOut(),
			StartedAt:   f.StartedAt(),
			Error:       f.LastError(),
		})
	}
	return infos
}

// Count retorna o número de túneis ativos
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.forwarders)
}

// Close encerra o manager e todos os forwarders
func (m *Manager) Close() {
	m.cancel()
	m.RemoveAll()
}

// HostName retorna o nome do host associado
func (m *Manager) HostName() string {
	return m.hostName
}

// monitorReconnect monitora a conexão SSH e reconecta se necessário
func (m *Manager) monitorReconnect() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if !m.sshClient.IsConnected() {
				m.reconnectWithBackoff()
			}
		}
	}
}

// reconnectWithBackoff tenta reconectar com backoff exponencial
func (m *Manager) reconnectWithBackoff() {
	m.mu.Lock()
	// Marca todos os forwarders como reconnecting
	for _, f := range m.forwarders {
		f.Stop()
		f.setStatus(StatusReconnecting)
	}
	m.mu.Unlock()

	delay := m.reconnectDelay
	maxDelay := 30 * time.Second
	maxAttempts := 50

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		// Tenta reconectar SSH
		newClient, err := internalssh.NewClient(m.hostConfig)
		if err != nil {
			time.Sleep(delay)
			delay = min(delay*2, maxDelay)
			continue
		}

		if err := newClient.Connect(); err != nil {
			time.Sleep(delay)
			delay = min(delay*2, maxDelay)
			continue
		}

		// Reconexão bem-sucedida — atualiza client e re-inicia forwarders
		m.sshClient.Close()
		m.sshClient = newClient

		m.mu.Lock()
		for _, f := range m.forwarders {
			f.UpdateSSHClient(newClient.Conn())
			if err := f.Start(); err != nil {
				f.setStatus(StatusError)
				f.lastError = err
			}
		}
		m.mu.Unlock()

		return
	}

	// Esgotou tentativas
	m.mu.Lock()
	for _, f := range m.forwarders {
		f.setStatus(StatusError)
		f.lastError = fmt.Errorf("reconexão falhou após %d tentativas", maxAttempts)
	}
	m.mu.Unlock()
}

// generateID gera um ID curto para o túnel (ex: "t-a1b2")
func generateID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 4)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "t-" + string(b)
}

// FormatBytes formata bytes em formato legível
func FormatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
