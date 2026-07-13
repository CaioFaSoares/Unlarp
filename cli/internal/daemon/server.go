package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
)

// Server expõe o Daemon via HTTP sobre unix socket — mesmo padrão do
// unlarp-agent (agent/server.go), só que local (sem SSH streamlocal).
type Server struct {
	d        *Daemon
	ln       net.Listener
	sockPath string
}

// NewServer sobe o listener no socket de ~/.unlarp/daemon.sock, removendo um
// socket órfão de uma execução anterior. Permissão 0600: só o dono do
// processo (mesmo usuário) consegue conectar.
func NewServer(d *Daemon) (*Server, error) {
	sockPath, err := daemonapi.SocketPath()
	if err != nil {
		return nil, err
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return &Server{d: d, ln: ln, sockPath: sockPath}, nil
}

func (s *Server) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/info", s.handleInfo)
	m.HandleFunc("GET /v1/syncs", s.handleListSyncs)
	m.HandleFunc("POST /v1/syncs", s.handleCreateSync)
	m.HandleFunc("DELETE /v1/syncs/{id}", s.handleDeleteSync)
	m.HandleFunc("POST /v1/syncs/{id}/pause", s.handlePauseSync)
	m.HandleFunc("POST /v1/syncs/{id}/resume", s.handleResumeSync)
	m.HandleFunc("GET /v1/logs", s.handleLogs)
	m.HandleFunc("GET /v1/tunnels", s.handleListTunnels)
	m.HandleFunc("POST /v1/tunnels", s.handleCreateTunnel)
	m.HandleFunc("DELETE /v1/tunnels/{id}", s.handleDeleteTunnel)
	return m
}

// Serve bloqueia servindo HTTP no socket — o caller (cmd/daemon.go) chama em
// goroutine própria ou como último passo antes do shutdown.
func (s *Server) Serve() error {
	return http.Serve(s.ln, s.mux())
}

// Close fecha o listener e remove o socket do disco.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.sockPath)
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.d.Info())
}

func (s *Server) handleListSyncs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, daemonapi.SyncsResponse{Syncs: s.d.ListSyncs()})
}

func (s *Server) handleCreateSync(w http.ResponseWriter, r *http.Request) {
	var req daemonapi.CreateSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" || req.LocalDir == "" || req.RemoteDir == "" {
		http.Error(w, "requisição inválida: host, local_dir e remote_dir são obrigatórios", http.StatusBadRequest)
		return
	}
	info, err := s.d.CreateSync(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, info)
}

func (s *Server) handleDeleteSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.DeleteSync(id); err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePauseSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.d.PauseSync(id, req.Reason); err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResumeSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.ResumeSync(id); err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, s.d.Logs(since))
}

func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, daemonapi.TunnelsResponse{Tunnels: s.d.ListTunnels()})
}

func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req daemonapi.CreateTunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" || req.LocalPort == 0 || req.RemotePort == 0 {
		http.Error(w, "requisição inválida: host, local_port e remote_port são obrigatórios", http.StatusBadRequest)
		return
	}
	info, err := s.d.CreateTunnel(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, info)
}

func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.DeleteTunnel(id); err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeNotFoundOrError(w http.ResponseWriter, err error) {
	// ponytail: sem tipo de erro estruturado — ErrNotFound é a única distinção
	// que os clientes precisam (sync já não existe vs. falha genérica).
	http.Error(w, err.Error(), http.StatusNotFound)
}
