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

// Direction indica de que lado o túnel escuta por conexões
type Direction int

const (
	// DirectionRemote escuta no host remoto e encaminha para a máquina local
	// (equivalente a `ssh -R`). É o padrão: a maioria dos túneis expõe um
	// serviço local (ex: dev server) através do host remoto.
	DirectionRemote Direction = iota
	// DirectionLocal escuta na máquina local e encaminha para o host remoto
	// (equivalente a `ssh -L`). Útil para acessar um serviço que já roda no
	// host remoto (ex: Postgres num container) via uma porta local.
	DirectionLocal
)

func (d Direction) String() string {
	if d == DirectionLocal {
		return "local"
	}
	return "remote"
}

// Forwarder encapsula um único túnel SSH entre uma porta local e uma remota
type Forwarder struct {
	ID         string
	LocalPort  int
	RemotePort int
	RemoteHost string // default "localhost" (dentro do container, usado em DirectionLocal)
	Direction  Direction

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
func NewForwarder(id string, localPort, remotePort int, sshClient *ssh.Client, direction Direction) *Forwarder {
	return &Forwarder{
		ID:         id,
		LocalPort:  localPort,
		RemotePort: remotePort,
		RemoteHost: "localhost",
		Direction:  direction,
		sshClient:  sshClient,
		status:     StatusStopped,
	}
}

// Start inicia o forwarder: abre o listener do lado apropriado (local ou
// remoto via SSH, conforme Direction) e aceita conexões
func (f *Forwarder) Start() error {
	var listener net.Listener
	var err error

	if f.Direction == DirectionLocal {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.LocalPort))
		if err != nil {
			f.setStatus(StatusError)
			f.lastError = fmt.Errorf("erro ao abrir porta local %d: %w", f.LocalPort, err)
			return f.lastError
		}
	} else {
		listener, err = f.sshClient.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", f.RemotePort))
		if err != nil {
			f.setStatus(StatusError)
			f.lastError = fmt.Errorf("erro ao abrir porta remota %d: %w", f.RemotePort, err)
			return f.lastError
		}
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

// Mapping retorna a string de mapeamento (ex: "5432 → 5432 (remote)")
func (f *Forwarder) Mapping() string {
	return fmt.Sprintf("%d → %d (%s)", f.RemotePort, f.LocalPort, f.Direction)
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

// dialPeer conecta ao lado oposto do listener que recebeu a conexão: em
// DirectionLocal disca o host remoto via canal SSH; em DirectionRemote disca
// a porta local diretamente (o listener já é o lado remoto, via SSH).
func (f *Forwarder) dialPeer() (net.Conn, string, error) {
	if f.Direction == DirectionLocal {
		addr := fmt.Sprintf("%s:%d", f.RemoteHost, f.RemotePort)
		conn, err := f.sshClient.Dial("tcp", addr)
		return conn, addr, err
	}

	addr := fmt.Sprintf("127.0.0.1:%d", f.LocalPort)
	conn, err := net.Dial("tcp", addr)
	return conn, addr, err
}

func (f *Forwarder) handleConnection(inConn net.Conn) {
	f.connections.Add(1)
	defer func() {
		f.connections.Add(-1)
		inConn.Close()
	}()

	peerConn, peerAddr, err := f.dialPeer()
	if err != nil {
		f.statusMu.Lock()
		f.lastError = fmt.Errorf("erro ao conectar a %s: %w", peerAddr, err)
		f.statusMu.Unlock()
		return
	}
	defer peerConn.Close()

	// Bridge bidirecional com contagem de bytes
	done := make(chan struct{}, 2)

	go func() {
		n, _ := io.Copy(peerConn, inConn)
		f.bytesOut.Add(n)
		done <- struct{}{}
	}()

	go func() {
		n, _ := io.Copy(inConn, peerConn)
		f.bytesIn.Add(n)
		done <- struct{}{}
	}()

	// Espera qualquer direção terminar
	select {
	case <-done:
	case <-f.ctx.Done():
	}
}
