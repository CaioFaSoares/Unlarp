package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	"github.com/CaioFaSoares/unlarp/internal/fsutil"
	"github.com/CaioFaSoares/unlarp/internal/git"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/watcher"
)

// dirWatch é um diretório observado: um LocalWatcher (o FS aqui é local ao
// container) e um cursor monotônico que os clientes acompanham por long-poll.
type dirWatch struct {
	GlobalIgnores []string `json:"global_ignores"`
	Seq           uint64   `json:"seq"`

	lw     *watcher.LocalWatcher `json:"-"`
	notify chan struct{}         `json:"-"` // fechado e trocado a cada mudança
}

type server struct {
	stateDir string

	mu      sync.Mutex
	watches map[string]*dirWatch

	persistTimer *time.Timer
}

func newServer(stateDir string) *server {
	return &server{
		stateDir: stateDir,
		watches:  make(map[string]*dirWatch),
	}
}

func (s *server) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/info", s.handleInfo)
	m.HandleFunc("POST /v1/watch", s.handleWatch)
	m.HandleFunc("GET /v1/events", s.handleEvents)
	m.HandleFunc("POST /v1/snapshot", s.handleSnapshot)
	m.HandleFunc("POST /v1/projects", s.handleProjects)
	m.HandleFunc("POST /v1/git/op", s.handleGitOp)
	return m
}

// gitOps mapeia cada op permitida para o prefixo real do comando git.
var gitOps = map[string][]string{
	agentapi.GitOpCheckout:       {"checkout"},
	agentapi.GitOpWorktreeAdd:    {"worktree", "add"},
	agentapi.GitOpWorktreeRemove: {"worktree", "remove"},
}

// handleGitOp executa uma operação git da allowlist e devolve o estado
// resultante. O caller (CLI/TUI) orquestra pause/resume do sync em volta.
func (s *server) handleGitOp(w http.ResponseWriter, r *http.Request) {
	var req agentapi.GitOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" || len(req.Args) == 0 {
		http.Error(w, "requisição inválida: dir e args são obrigatórios", http.StatusBadRequest)
		return
	}
	base, ok := gitOps[req.Op]
	if !ok {
		http.Error(w, "operação não permitida: "+req.Op, http.StatusBadRequest)
		return
	}
	// Sem flags nos args (fronteira de confiança): a única exceção é -b no
	// checkout e no worktree add, para criar branch nova
	for _, a := range req.Args {
		allowB := (req.Op == agentapi.GitOpCheckout || req.Op == agentapi.GitOpWorktreeAdd) && a == "-b"
		if strings.HasPrefix(a, "-") && !allowB {
			http.Error(w, "argumento não permitido: "+a, http.StatusBadRequest)
			return
		}
	}
	dir := filepath.Clean(req.Dir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "diretório inexistente: "+dir, http.StatusNotFound)
		return
	}

	args := append([]string{"-C", dir}, append(append([]string{}, base...), req.Args...)...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		http.Error(w, "git "+strings.Join(base, " ")+" falhou: "+strings.TrimSpace(string(out)), http.StatusUnprocessableEntity)
		return
	}

	info := git.LocalInfo(dir)
	log.Printf("git op %s em %s -> %s@%s", req.Op, dir, info.Branch, info.CommitHash)
	writeJSON(w, agentapi.GitOpResponse{Branch: info.Branch, Commit: info.CommitHash, Output: strings.TrimSpace(string(out))})
}

// handleProjects devolve o estado do workspace numa resposta só: git por
// projeto, sessões tmux e containers — substitui N execs SSH da TUI.
func (s *server) handleProjects(w http.ResponseWriter, r *http.Request) {
	var req agentapi.ProjectsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "requisição inválida: "+err.Error(), http.StatusBadRequest)
		return
	}

	paths := req.Paths
	if len(paths) == 0 && req.Root != "" {
		if entries, err := os.ReadDir(req.Root); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					paths = append(paths, filepath.Join(req.Root, e.Name()))
				}
			}
		}
	}

	var resp agentapi.ProjectsResponse
	for _, p := range paths {
		resp.Projects = append(resp.Projects, agentapi.ProjectInfo{Path: p, Git: git.LocalInfo(p)})
	}
	resp.Tmux = listTmuxSessions()
	resp.Containers = listContainers()
	writeJSON(w, resp)
}

// listTmuxSessions usa o mesmo formato da TUI (checkTmuxCmd): uma linha por
// sessão com o painel ativo da janela ativa.
func listTmuxSessions() []agentapi.TmuxSessionInfo {
	out, err := exec.Command("tmux", "list-panes", "-a",
		"-f", "#{&&:#{window_active},#{pane_active}}",
		"-F", "#{session_name}|#{session_windows}|#{?session_attached,1,0}|#{pane_current_command}|#{pane_current_path}|#{pane_dead}|#{window_activity}").Output()
	if err != nil {
		return nil // tmux ausente ou sem servidor rodando: sem sessões
	}

	var sessions []agentapi.TmuxSessionInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) < 7 {
			continue
		}
		windows, _ := strconv.Atoi(parts[1])
		activity, _ := strconv.ParseInt(parts[6], 10, 64)
		sessions = append(sessions, agentapi.TmuxSessionInfo{
			Name:         parts[0],
			Windows:      windows,
			Attached:     parts[2] == "1",
			Command:      parts[3],
			Path:         parts[4],
			PaneDead:     parts[5] == "1",
			ActivityUnix: activity,
		})
	}
	return sessions
}

func listContainers() []agentapi.ContainerInfo {
	out, err := exec.Command("docker", "ps", "--format", "{{.Names}}|{{.Image}}|{{.Status}}").Output()
	if err != nil {
		return nil // docker indisponível: sem containers
	}
	var containers []agentapi.ContainerInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 {
			continue
		}
		containers = append(containers, agentapi.ContainerInfo{Name: parts[0], Image: parts[1], Status: parts[2]})
	}
	return containers
}

// handleSnapshot gera o snapshot do diretório localmente (o FS é local ao
// container): mesmo BuildMatcherForDir + CreateLocalSnapshot da engine, então
// ModTime (UTC truncado a segundos) e ignores batem exatamente com o lado Mac.
func (s *server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var req agentapi.SnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" {
		http.Error(w, "requisição inválida: dir é obrigatório", http.StatusBadRequest)
		return
	}
	dir := filepath.Clean(req.Dir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "diretório inexistente: "+dir, http.StatusNotFound)
		return
	}

	matcher := internalsync.BuildMatcherForDir(dir, req.GlobalIgnores)
	snap, err := internalsync.CreateLocalSnapshot(dir, matcher)
	if err != nil {
		http.Error(w, "falha no snapshot de "+dir+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snap)
}

func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, agentapi.InfoResponse{Version: agentapi.Version, Protocol: agentapi.Protocol})
}

func (s *server) handleWatch(w http.ResponseWriter, r *http.Request) {
	var req agentapi.WatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" {
		http.Error(w, "requisição inválida: dir é obrigatório", http.StatusBadRequest)
		return
	}
	dir := filepath.Clean(req.Dir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "diretório inexistente: "+dir, http.StatusNotFound)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if dw, ok := s.watches[dir]; ok {
		writeJSON(w, agentapi.WatchResponse{Seq: dw.Seq})
		return
	}

	dw := &dirWatch{GlobalIgnores: req.GlobalIgnores}
	if err := s.startWatchLocked(dir, dw); err != nil {
		http.Error(w, "falha ao observar "+dir+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.watches[dir] = dw
	s.persistSoonLocked()
	log.Printf("observando %s (%d ignores globais)", dir, len(req.GlobalIgnores))
	writeJSON(w, agentapi.WatchResponse{Seq: dw.Seq})
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Clean(r.URL.Query().Get("dir"))
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	timeout, err := time.ParseDuration(r.URL.Query().Get("timeout"))
	if err != nil || timeout <= 0 || timeout > time.Minute {
		timeout = 25 * time.Second
	}

	s.mu.Lock()
	dw, ok := s.watches[dir]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "diretório não observado (chame POST /v1/watch)", http.StatusNotFound)
		return
	}
	if dw.Seq > since {
		resp := agentapi.EventsResponse{Seq: dw.Seq, Changed: true}
		s.mu.Unlock()
		writeJSON(w, resp)
		return
	}
	ch := dw.notify
	s.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(timeout):
	case <-r.Context().Done():
		return
	}

	s.mu.Lock()
	resp := agentapi.EventsResponse{Seq: dw.Seq, Changed: dw.Seq > since}
	s.mu.Unlock()
	writeJSON(w, resp)
}

// startWatchLocked sobe o LocalWatcher de um dir. Chamar com s.mu segura.
func (s *server) startWatchLocked(dir string, dw *dirWatch) error {
	dw.notify = make(chan struct{})

	// Mesmo matcher estático da engine: ignores globais + .unlarpignore do dir.
	// ponytail: sem .gitignore dinâmico aqui — falso positivo só custa um
	// ciclo no-op de SyncExec, que refiltra tudo com o matcher completo.
	matcher := internalsync.NewIgnoreMatcher(dw.GlobalIgnores, filepath.Join(dir, ".unlarpignore"))

	lw, err := watcher.NewLocalWatcher(dir, 200*time.Millisecond, matcher,
		func(msg string) { log.Printf("[%s] %s", dir, msg) },
		func() {
			s.mu.Lock()
			dw.Seq++
			close(dw.notify)
			dw.notify = make(chan struct{})
			s.persistSoonLocked()
			s.mu.Unlock()
		})
	if err != nil {
		return err
	}
	if err := lw.Start(); err != nil {
		return err
	}
	dw.lw = lw
	return nil
}

func (s *server) watchesPath() string {
	return filepath.Join(s.stateDir, "watches.json")
}

// loadWatches restaura os diretórios observados de execuções anteriores —
// é isso que garante que fechar o tmux no Mac não perde o journaling aqui.
func (s *server) loadWatches() error {
	data, err := os.ReadFile(s.watchesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var persisted map[string]*dirWatch
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for dir, dw := range persisted {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			log.Printf("descartando watch de %s: diretório não existe mais", dir)
			continue
		}
		// Bump no boot: mudanças ocorridas enquanto o agent esteve morto não
		// geraram evento — o cursor avançado força um ciclo de reconciliação
		// no próximo poll de qualquer cliente.
		dw.Seq++
		if err := s.startWatchLocked(dir, dw); err != nil {
			log.Printf("falha ao restaurar watch de %s: %v", dir, err)
			continue
		}
		s.watches[dir] = dw
		log.Printf("watch restaurado: %s (seq %d)", dir, dw.Seq)
	}
	return nil
}

// persistSoonLocked agenda um save debounced (~1s). Chamar com s.mu segura.
// Crash entre evento e persist subconta o seq — inofensivo: todo (re)connect
// de cliente começa com um SyncExec full de qualquer forma.
func (s *server) persistSoonLocked() {
	if s.persistTimer != nil {
		s.persistTimer.Stop()
	}
	s.persistTimer = time.AfterFunc(time.Second, func() {
		s.mu.Lock()
		data, err := json.MarshalIndent(s.watches, "", "  ")
		s.mu.Unlock()
		if err == nil {
			err = fsutil.WriteFileAtomic(s.watchesPath(), data, 0600)
		}
		if err != nil {
			log.Printf("falha ao persistir watches: %v", err)
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
