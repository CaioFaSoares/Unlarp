// Package daemonapi define o contrato compartilhado entre os clientes (CLI e
// TUI) e o `unlarp daemon`. Só DTOs e constantes — sem lógica — espelhando
// internal/agentapi (o mesmo padrão usado pelo unlarp-agent no container).
package daemonapi

import (
	"fmt"
	"os"
	"path/filepath"

	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
)

const (
	// Protocol é incrementado a cada mudança incompatível na API. O cliente
	// trata mismatch como "daemon ausente/desatualizado" e cai no fallback
	// in-process (o daemon é opt-in nesta fase, então o fallback já é o
	// comportamento default de qualquer forma).
	Protocol = 1

	// Version do binário do daemon (informativa, exibida em `unlarp daemon status`)
	Version = "0.1.0"
)

// unlarpDir resolve ~/.unlarp, criando-o se necessário. Mesmo diretório usado
// por session.Manager para state.json — o daemon é só mais um dono desse
// diretório, nunca de um sync root.
func unlarpDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".unlarp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// SocketPath retorna o caminho do unix socket local do daemon.
func SocketPath() (string, error) {
	dir, err := unlarpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// LockPath retorna o caminho do lockfile usado pelo auto-start para garantir
// um único daemon por usuário mesmo com clientes concorrentes.
func LockPath() (string, error) {
	dir, err := unlarpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// PIDPath retorna o caminho do arquivo de PID do daemon em execução.
func PIDPath() (string, error) {
	dir, err := unlarpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// LogPath retorna o caminho do arquivo de log do daemon (RFC3339, uma linha
// por evento, rotação simples por tamanho — ver internal/daemon/logbuf.go).
func LogPath() (string, error) {
	dir, err := unlarpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

type InfoResponse struct {
	Version       string `json:"version"`
	Protocol      int    `json:"protocol"`
	PID           int    `json:"pid"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	SyncCount     int    `json:"sync_count"`
}

// CreateSyncRequest pede a criação de um sync gerenciado pelo daemon.
// InitialSync: "full" (default) executa uma sincronização completa antes de
// religar os watchers; "none" pula (espelha a flag --initial-sync do CLI).
type CreateSyncRequest struct {
	Host        string `json:"host"`
	LocalDir    string `json:"local_dir"`
	RemoteDir   string `json:"remote_dir"`
	Mode        string `json:"mode,omitempty"`
	Project     string `json:"project,omitempty"`
	InitialSync string `json:"initial_sync,omitempty"`
}

// SyncInfo descreve um sync vivo no daemon — o suficiente para `sync status`
// e para a aba Syncs da TUI renderizarem sem outra chamada.
type SyncInfo struct {
	ID        string                    `json:"id"`
	Host      string                    `json:"host"`
	LocalDir  string                    `json:"local_dir"`
	RemoteDir string                    `json:"remote_dir"`
	Mode      string                    `json:"mode,omitempty"`
	Project   string                    `json:"project,omitempty"`
	Progress  internalsync.SyncProgress `json:"progress"`
	GitGuard  internalsync.GitGuard     `json:"git_guard"`
}

type SyncsResponse struct {
	Syncs []SyncInfo `json:"syncs"`
}

// CreateTunnelRequest pede a criação de um túnel SSH (port forwarding)
// gerenciado pelo daemon. Direction: "remote" (padrão, ssh -R) | "local" (ssh -L).
type CreateTunnelRequest struct {
	Host       string `json:"host"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Direction  string `json:"direction,omitempty"`
}

// TunnelInfo descreve um túnel vivo no daemon.
type TunnelInfo struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Direction  string `json:"direction"`
}

type TunnelsResponse struct {
	Tunnels []TunnelInfo `json:"tunnels"`
}

type LogEntry struct {
	Seq   uint64 `json:"seq"`
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type LogsResponse struct {
	Entries []LogEntry `json:"entries"`
	Latest  uint64     `json:"latest"`
}

// ErrNotFound é usado pelo servidor para responder 404 de forma consistente;
// o cliente reconhece a mensagem para diferenciar "sync não existe" de outros erros.
func ErrNotFound(kind, id string) error {
	return fmt.Errorf("%s '%s' não encontrado", kind, id)
}
