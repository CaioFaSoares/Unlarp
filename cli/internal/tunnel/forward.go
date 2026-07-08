package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// ForwarderStatus representa o estado de um forwarder
type ForwarderStatus int

const (
	StatusStopped      ForwarderStatus = iota
	StatusRunning
	StatusReconnecting
	StatusError
)

func (s ForwarderStatus) String() string {
	switch s {
	case StatusStopped:
		return "Stopped"
	case StatusRunning:
		return "Running"
	case StatusReconnecting:
		return "Reconnecting"
	case StatusError:
		return "Error"
	default:
		return "Unknown"
	}
}

// Forwarder encapsula um único túnel local → remoto via SSH
type Forwarder struct {
	ID         string
	LocalPort  int
	RemotePort int
	RemoteHost string // default "localhost" (dentro do container)

	listener  net.Listener
	sshClient *ssh.Client
	ctx       context.Context
	cancel    context.CancelFunc

	status      ForwarderStatus
	statusMu    sync.RWMutex
	lastError   error
	bytesIn     atomic.Int64
	bytesOut    atomic.Int64
	connections atomic.Int32
	startedAt   time.Time
}

// NewForwarder cria um novo forwarder
func NewForwarder(id string, localPort, remotePort int, sshClient *ssh.Client) *Forwarder {
	remoteHost := "localhost"
	return &Forwarder{
		ID:         id,
		LocalPort:  localPort,
		RemotePort: remotePort,
		RemoteHost: remoteHost,
		sshClient:  sshClient,
		status:     StatusStopped,
	}
}

// Start inicia o forwarder: abre listener local e aceita conexões
func (f *Forwarder) Start() error {
	addr := fmt.Sprintf("localhost:%d", f.LocalPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		f.setStatus(StatusError)
		f.lastError = fmt.Errorf("erro ao abrir porta local %d: %w", f.LocalPort, err)
		return f.lastError
	}

	f.listener = listener
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.startedAt = time.Now()
	f.setStatus(StatusRunning)

	// Aceita conexões em goroutine
	go f.acceptLoop()

	return nil
}

// Stop encerra o forwarder e todas as conexões ativas
func (f *Forwarder) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
	if f.listener != nil {
		f.listener.Close()
	}
	f.setStatus(StatusStopped)
}

// Status retorna o status atual
func (f *Forwarder) Status() ForwarderStatus {
	f.statusMu.RLock()
	defer f.statusMu.RUnlock()
	return f.status
}

// LastError retorna o último erro
func (f *Forwarder) LastError() error {
	f.statusMu.RLock()
	defer f.statusMu.RUnlock()
	return f.lastError
}

// BytesIn retorna o total de bytes recebidos
func (f *Forwarder) BytesIn() int64 {
	return f.bytesIn.Load()
}

// BytesOut retorna o total de bytes enviados
func (f *Forwarder) BytesOut() int64 {
	return f.bytesOut.Load()
}

// Connections retorna o número de conexões ativas
func (f *Forwarder) Connections() int32 {
	return f.connections.Load()
}

// StartedAt retorna quando o forwarder foi iniciado
func (f *Forwarder) StartedAt() time.Time {
	return f.startedAt
}

// Mapping retorna a string de mapeamento (ex: "5432 → 5432")
func (f *Forwarder) Mapping() string {
	if f.LocalPort == f.RemotePort {
		return fmt.Sprintf("%d → %d", f.RemotePort, f.LocalPort)
	}
	return fmt.Sprintf("%d → %d", f.RemotePort, f.LocalPort)
}

// UpdateSSHClient atualiza o cliente SSH (para reconexão)
func (f *Forwarder) UpdateSSHClient(client *ssh.Client) {
	f.sshClient = client
}

// Restart re-inicia o forwarder após reconexão
func (f *Forwarder) Restart() error {
	f.Stop()
	return f.Start()
}

func (f *Forwarder) setStatus(s ForwarderStatus) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()
	f.status = s
}

func (f *Forwarder) acceptLoop() {
	for {
		select {
		case <-f.ctx.Done():
			return
		default:
		}

		conn, err := f.listener.Accept()
		if err != nil {
			// Se o contexto foi cancelado, é um shutdown normal
			select {
			case <-f.ctx.Done():
				return
			default:
				// Erro real
				f.setStatus(StatusError)
				f.lastError = err
				return
			}
		}

		go f.handleConnection(conn)
	}
}

func (f *Forwarder) handleConnection(localConn net.Conn) {
	f.connections.Add(1)
	defer func() {
		f.connections.Add(-1)
		localConn.Close()
	}()

	// Conecta ao lado remoto via SSH channel
	remoteAddr := fmt.Sprintf("%s:%d", f.RemoteHost, f.RemotePort)
	remoteConn, err := f.sshClient.Dial("tcp", remoteAddr)
	if err != nil {
		f.statusMu.Lock()
		f.lastError = fmt.Errorf("erro ao conectar a %s: %w", remoteAddr, err)
		f.statusMu.Unlock()
		return
	}
	defer remoteConn.Close()

	// Bridge bidirecional com contagem de bytes
	done := make(chan struct{}, 2)

	// local → remote
	go func() {
		n, _ := io.Copy(remoteConn, localConn)
		f.bytesOut.Add(n)
		done <- struct{}{}
	}()

	// remote → local
	go func() {
		n, _ := io.Copy(localConn, remoteConn)
		f.bytesIn.Add(n)
		done <- struct{}{}
	}()

	// Espera qualquer direção terminar
	select {
	case <-done:
	case <-f.ctx.Done():
	}
}
