// Package daemon implementa o `unlarp daemon`: o processo único por usuário
// que hospeda as engines de sync (e, na fase 4, os tunnel managers), servido
// por HTTP sobre unix socket (internal/daemonapi define o contrato). É opt-in
// nesta fase — TUI e CLI continuam sabendo rodar in-process quando o daemon
// não está ativo (ver DAEMON.md, seção 5).
package daemon

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
	"github.com/CaioFaSoares/unlarp/internal/session"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
	"github.com/CaioFaSoares/unlarp/internal/tunnel"
	"github.com/CaioFaSoares/unlarp/internal/watcher"
)

// runningSync é uma engine de sync viva sob o daemon, dona de sua conexão SSH
// e dos seus watchers — o mesmo conjunto que a TUI guarda em liveSyncSession,
// só que fora do Bubble Tea.
type runningSync struct {
	id            string
	host          string
	project       string
	mode          string
	engine        *internalsync.Engine
	sshClient     *internalssh.Client
	sftpClient    *internalssh.SFTPClient
	localWatcher  *watcher.LocalWatcher
	remoteWatcher watcher.Stopper
	trigger       chan string
}

// Daemon é o dono único das engines de sync e do state.json enquanto ativo.
type Daemon struct {
	store     *config.Store
	sessMgr   *session.Manager
	log       *logBuf
	startedAt time.Time

	mu      sync.Mutex
	syncs   map[string]*runningSync
	tunnels map[string]*tunnel.Manager // por host — reusa o SSH client do Manager entre túneis do mesmo host
}

// New cria o daemon com seus próprios Store/Manager e logger — nenhum
// estado é compartilhado com o processo que o auto-start disparou.
func New() (*Daemon, error) {
	sessMgr, err := session.NewManager()
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir session manager: %w", err)
	}
	log, err := newLogBuf()
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir log do daemon: %w", err)
	}
	return &Daemon{
		store:     config.NewStore(),
		sessMgr:   sessMgr,
		log:       log,
		startedAt: time.Now(),
		syncs:     make(map[string]*runningSync),
		tunnels:   make(map[string]*tunnel.Manager),
	}, nil
}

func (d *Daemon) logf(level, format string, args ...any) {
	d.log.Append(level, format, args...)
}

func (d *Daemon) Info() daemonapi.InfoResponse {
	d.mu.Lock()
	n := len(d.syncs)
	d.mu.Unlock()
	return daemonapi.InfoResponse{
		Version:       daemonapi.Version,
		Protocol:      daemonapi.Protocol,
		PID:           os.Getpid(),
		UptimeSeconds: int64(time.Since(d.startedAt).Seconds()),
		SyncCount:     n,
	}
}

func (d *Daemon) Logs(since uint64) daemonapi.LogsResponse {
	entries, latest := d.log.Since(since)
	out := make([]daemonapi.LogEntry, len(entries))
	copy(out, entries)
	return daemonapi.LogsResponse{Entries: out, Latest: latest}
}

func generateSyncID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "s-" + string(b)
}

func toSyncInfo(rs *runningSync) daemonapi.SyncInfo {
	return daemonapi.SyncInfo{
		ID:        rs.id,
		Host:      rs.host,
		LocalDir:  rs.engine.LocalDir,
		RemoteDir: rs.engine.RemoteDir,
		Mode:      rs.mode,
		Project:   rs.project,
		Progress:  rs.engine.GetProgress(),
		GitGuard:  rs.engine.GetGitGuard(),
	}
}

// ListSyncs devolve os syncs vivos no daemon — GET /v1/syncs. O daemon é o
// único escritor do state.json para os syncs que criou, então esta lista (em
// memória) já é a fonte de verdade, sem reler o disco.
func (d *Daemon) ListSyncs() []daemonapi.SyncInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]daemonapi.SyncInfo, 0, len(d.syncs))
	for _, rs := range d.syncs {
		out = append(out, toSyncInfo(rs))
	}
	return out
}

// CreateSync conecta ao host, sobe a engine e os watchers, e registra o sync
// como vivo. É o mesmo corpo que hoje existe duplicado em cmd/sync.go
// (runSyncStart) e internal/tui/app.go (startSyncEngine) — o daemon
// centraliza essa sequência uma vez só.
func (d *Daemon) CreateSync(req daemonapi.CreateSyncRequest) (daemonapi.SyncInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cfg, err := d.store.Load()
	if err != nil {
		return daemonapi.SyncInfo{}, err
	}
	hostCfg, ok := cfg.Hosts[req.Host]
	if !ok {
		return daemonapi.SyncInfo{}, fmt.Errorf("host '%s' não configurado", req.Host)
	}

	// Colisão de pasta local: dois syncs (mesmo de hosts diferentes)
	// escrevendo na mesma árvore local corromperiam a reconciliação.
	if otherHost, other, found := d.sessMgr.FindSyncByLocalDir(req.LocalDir, ""); found {
		return daemonapi.SyncInfo{}, fmt.Errorf("pasta local %s colide com o sync %s do host %s (%s)", req.LocalDir, other.ID, otherHost, other.LocalDir)
	}

	sshClient, err := internalssh.NewClient(&hostCfg)
	if err != nil {
		return daemonapi.SyncInfo{}, err
	}
	if err := sshClient.Connect(); err != nil {
		return daemonapi.SyncInfo{}, fmt.Errorf("erro SSH: %w", err)
	}
	sftpClient, err := internalssh.NewSFTPClient(sshClient)
	if err != nil {
		sshClient.Close()
		return daemonapi.SyncInfo{}, fmt.Errorf("erro SFTP: %w", err)
	}

	syncID := generateSyncID()
	strategy := internalsync.ConflictStrategy(cfg.Sync.ConflictStrategy)
	engine, err := internalsync.NewEngine(syncID, req.LocalDir, req.RemoteDir, req.Host, cfg.Sync.IgnorePatterns, strategy, sshClient, sftpClient.Inner())
	if err != nil {
		sftpClient.Close()
		sshClient.Close()
		return daemonapi.SyncInfo{}, fmt.Errorf("erro ao iniciar engine de sync: %w", err)
	}
	if engine.StateWarning != "" {
		d.logf("WARN", "[%s] %s", syncID, engine.StateWarning)
	}
	engine.OnFileSuccess = func(path, action string) {
		var direction string
		switch action {
		case "upload", "remote_delete":
			direction = "LOCAL -> REMOTE"
		case "download", "local_delete":
			direction = "REMOTE -> LOCAL"
		}
		d.logf("INFO", "[%s] %s: %s", syncID, direction, path)
	}
	engine.OnConflict = func(path, winner string) {
		d.logf("WARN", "[%s] conflito em %s — venceu %s (%s)", syncID, path, winner, engine.ConflictStrategy)
	}

	rs := &runningSync{
		id: syncID, host: req.Host, project: req.Project, mode: req.Mode,
		engine: engine, sshClient: sshClient, sftpClient: sftpClient,
		trigger: make(chan string, 1),
	}

	// unlarp-agent (inotify no container), quando disponível, acelera o
	// snapshot remoto — igual ao caminho in-process de hoje.
	var agentClient *agent.Client
	if ac := agent.Detect(sshClient); ac != nil {
		agentClient = ac
		globalIgnores := cfg.Sync.IgnorePatterns
		remoteDir := req.RemoteDir
		engine.RemoteSnapshotFn = func() (internalsync.Snapshot, error) {
			return agentClient.Snapshot(remoteDir, globalIgnores)
		}
	}

	if req.InitialSync != "none" {
		if _, err := engine.SyncExec(); err != nil {
			d.logf("ERROR", "[%s] sync inicial falhou: %v", syncID, err)
		}
	}

	// Consumidor único do canal de trigger — coalescer intencional (buffer 1
	// + send com default nos watchers), não uma fila: não trocar por chan
	// maior nem por worker pool.
	go func() {
		for reason := range rs.trigger {
			d.logf("DEBUG", "[%s] sync disparado por: %s", syncID, reason)
			count, err := engine.SyncExec()
			if err != nil {
				d.logf("ERROR", "[%s] erro ao sincronizar: %v", syncID, err)
			} else if count > 0 {
				d.logf("INFO", "[%s] %d alteração(ões) propagada(s)", syncID, count)
			}
		}
	}()

	localWatcher, err := watcher.NewLocalWatcher(req.LocalDir, 200*time.Millisecond, engine.IgnoreMatcher(),
		func(msg string) { d.logf("WARN", "[%s] %s", syncID, msg) },
		func() {
			select {
			case rs.trigger <- "mudança local":
			default:
			}
		})
	if err != nil {
		sftpClient.Close()
		sshClient.Close()
		return daemonapi.SyncInfo{}, fmt.Errorf("erro ao criar watcher local: %w", err)
	}
	if err := localWatcher.Start(); err != nil {
		sftpClient.Close()
		sshClient.Close()
		return daemonapi.SyncInfo{}, fmt.Errorf("erro ao iniciar watcher local: %w", err)
	}
	rs.localWatcher = localWatcher

	// redial derruba e recria SSH+SFTP quando um watcher remoto perde conexão
	// — mesma lógica de cmd/sync.go e tui/app.go.
	redial := func() (*internalssh.Client, *internalssh.SFTPClient, error) {
		newSSH, err := internalssh.NewClient(&hostCfg)
		if err != nil {
			return nil, nil, err
		}
		if err := newSSH.Connect(); err != nil {
			return nil, nil, err
		}
		newSFTP, err := internalssh.NewSFTPClient(newSSH)
		if err != nil {
			newSSH.Close()
			return nil, nil, err
		}
		rs.sftpClient.Close()
		rs.sshClient.Close()
		rs.sshClient = newSSH
		rs.sftpClient = newSFTP
		engine.UpdateSFTPClient(newSFTP.Inner())
		return newSSH, newSFTP, nil
	}
	onRemoteChange := func() {
		select {
		case rs.trigger <- "mudança remota":
		default:
		}
	}

	var remoteWatcher watcher.Stopper
	if agentClient != nil {
		aw := watcher.NewAgentWatcher(req.RemoteDir, agentClient, cfg.Sync.IgnorePatterns,
			func(msg string) { d.logf("WARN", "[%s] %s", syncID, msg) },
			func() (*agent.Client, error) {
				newSSH, _, err := redial()
				if err != nil {
					return nil, err
				}
				agentClient = agent.New(newSSH)
				return agentClient, nil
			}, onRemoteChange)
		if err := aw.Start(); err != nil {
			d.logf("WARN", "[%s] agent detectado mas falhou ao observar (%v); usando polling SFTP.", syncID, err)
		} else {
			remoteWatcher = aw
			d.logf("INFO", "[%s] monitorando remoto via unlarp-agent (inotify).", syncID)
		}
	}
	if remoteWatcher == nil {
		pollInterval := cfg.Sync.PollIntervalDuration()
		rw := watcher.NewRemoteWatcher(req.RemoteDir, sftpClient.Inner(), pollInterval, engine.IgnoreMatcher(),
			func(msg string) { d.logf("WARN", "[%s] %s", syncID, msg) },
			func() (*sftp.Client, error) {
				_, newSFTP, err := redial()
				if err != nil {
					return nil, err
				}
				return newSFTP.Inner(), nil
			}, onRemoteChange)
		rw.Start()
		remoteWatcher = rw
	}
	rs.remoteWatcher = remoteWatcher

	d.syncs[syncID] = rs

	entry := session.SyncEntry{
		ID: syncID, LocalDir: req.LocalDir, RemoteDir: req.RemoteDir, Mode: req.Mode,
		LastSync: time.Now(), Project: req.Project, PID: os.Getpid(), Owner: "daemon",
	}
	if err := d.sessMgr.AddSync(req.Host, entry); err != nil {
		d.logf("ERROR", "[%s] falha ao persistir sync em state.json: %v", syncID, err)
	}

	d.logf("INFO", "[%s] sync criado: %s -> %s@%s", syncID, req.LocalDir, req.Host, req.RemoteDir)

	return toSyncInfo(rs), nil
}

// DeleteSync para watchers e conexões de um sync vivo e remove seu registro
// de state.json — substitui o SIGTERM em PID do modelo antigo (DELETE
// /v1/syncs/{id} não precisa mais do caso especial "owner é a TUI, não mate").
func (d *Daemon) DeleteSync(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	rs, ok := d.syncs[id]
	if !ok {
		return daemonapi.ErrNotFound("sync", id)
	}

	if rs.localWatcher != nil {
		rs.localWatcher.Stop()
	}
	if rs.remoteWatcher != nil {
		rs.remoteWatcher.Stop()
	}
	rs.sftpClient.Close()
	rs.sshClient.Close()
	delete(d.syncs, id)

	if err := d.sessMgr.RemoveSync(rs.host, id); err != nil {
		d.logf("ERROR", "[%s] falha ao remover de state.json: %v", id, err)
	}
	d.logf("INFO", "[%s] sync parado e removido", id)
	return nil
}

// PauseSync/ResumeSync delegam ao GitGuard da engine (o mesmo mecanismo de
// pausa/resolução de hoje) — a rota HTTP só expõe o que a engine já sabe fazer.
func (d *Daemon) PauseSync(id, reason string) error {
	d.mu.Lock()
	rs, ok := d.syncs[id]
	d.mu.Unlock()
	if !ok {
		return daemonapi.ErrNotFound("sync", id)
	}
	rs.engine.Pause(reason)
	d.logf("INFO", "[%s] pausado: %s", id, reason)
	return nil
}

func (d *Daemon) ResumeSync(id string) error {
	d.mu.Lock()
	rs, ok := d.syncs[id]
	d.mu.Unlock()
	if !ok {
		return daemonapi.ErrNotFound("sync", id)
	}
	rs.engine.Resume()
	d.logf("INFO", "[%s] resumido", id)
	return nil
}

// AdoptOrphans religa, no boot do daemon, os syncs persistidos com Owner
// antigo ("cli"/"tui") cujo processo dono não está mais vivo — mesma
// semântica do restoreSyncsCmd da TUI, agora centralizada aqui. Syncs cujo
// dono ainda está vivo (outro processo `sync start` ou uma TUI em pé) não são
// tocados: adotá-los duplicaria a engine.
func (d *Daemon) AdoptOrphans() {
	for host, sess := range d.sessMgr.ListSessions() {
		for _, entry := range sess.Syncs {
			if entry.Alive() {
				continue
			}
			req := daemonapi.CreateSyncRequest{
				Host: host, LocalDir: entry.LocalDir, RemoteDir: entry.RemoteDir,
				Mode: entry.Mode, Project: entry.Project, InitialSync: "full",
			}
			// Remove a entrada órfã antes de recriar — CreateSync vai
			// adicionar uma nova com Owner=daemon; sem isso ficaria duplicada.
			_ = d.sessMgr.RemoveSync(host, entry.ID)
			if _, err := d.CreateSync(req); err != nil {
				d.logf("WARN", "falha ao adotar sync órfão %s (%s): %v", entry.ID, host, err)
			} else {
				d.logf("INFO", "sync órfão %s adotado de %s", entry.ID, host)
			}
		}
	}
}

// Shutdown para todos os syncs vivos — chamado no SIGINT/SIGTERM do processo
// do daemon.
// ListTunnels devolve todos os túneis vivos, de todos os hosts.
func (d *Daemon) ListTunnels() []daemonapi.TunnelInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []daemonapi.TunnelInfo
	for host, mgr := range d.tunnels {
		for _, fw := range mgr.List() {
			out = append(out, daemonapi.TunnelInfo{
				ID:         fw.ID,
				Host:       host,
				LocalPort:  fw.LocalPort,
				RemotePort: fw.RemotePort,
				Direction:  fw.Direction.String(),
			})
		}
	}
	return out
}

// CreateTunnel cria (ou reusa, se já existir um Manager vivo para o host) um
// túnel SSH. Um Manager por host reusa o mesmo SSH client entre túneis do
// mesmo host — ver DAEMON.md Fase 4.
func (d *Daemon) CreateTunnel(req daemonapi.CreateTunnelRequest) (daemonapi.TunnelInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cfg, err := d.store.Load()
	if err != nil {
		return daemonapi.TunnelInfo{}, err
	}
	hostCfg, ok := cfg.Hosts[req.Host]
	if !ok {
		return daemonapi.TunnelInfo{}, fmt.Errorf("host '%s' não configurado", req.Host)
	}

	mgr, exists := d.tunnels[req.Host]
	if !exists {
		sshClient, err := internalssh.NewClient(&hostCfg)
		if err != nil {
			return daemonapi.TunnelInfo{}, err
		}
		if err := sshClient.Connect(); err != nil {
			return daemonapi.TunnelInfo{}, fmt.Errorf("erro SSH: %w", err)
		}
		mgr = tunnel.NewManager(sshClient, &hostCfg, req.Host, cfg.Tunnel.AutoReconnect, cfg.Tunnel.ReconnectDelayDuration())
		d.tunnels[req.Host] = mgr
	}

	direction := tunnel.DirectionRemote
	if req.Direction == "local" {
		direction = tunnel.DirectionLocal
	}
	id, err := mgr.Add(req.LocalPort, req.RemotePort, direction)
	if err != nil {
		return daemonapi.TunnelInfo{}, fmt.Errorf("erro ao iniciar túnel: %w", err)
	}

	if err := d.sessMgr.AddTunnel(req.Host, session.TunnelEntry{
		ID:         id,
		RemotePort: req.RemotePort,
		LocalPort:  req.LocalPort,
		Direction:  direction.String(),
	}); err != nil {
		d.logf("WARN", "erro ao persistir túnel %s: %v", id, err)
	}

	d.logf("INFO", "túnel %s criado em %s (local:%d <-> remoto:%d, %s)", id, req.Host, req.LocalPort, req.RemotePort, direction)
	return daemonapi.TunnelInfo{ID: id, Host: req.Host, LocalPort: req.LocalPort, RemotePort: req.RemotePort, Direction: direction.String()}, nil
}

// DeleteTunnel para e remove um túnel pelo ID, procurando em todos os hosts.
func (d *Daemon) DeleteTunnel(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for host, mgr := range d.tunnels {
		if err := mgr.Remove(id); err == nil {
			_ = d.sessMgr.RemoveTunnel(host, id)
			d.logf("INFO", "túnel %s removido (%s)", id, host)
			return nil
		}
	}
	return daemonapi.ErrNotFound("tunnel", id)
}

func (d *Daemon) Shutdown() {
	d.mu.Lock()
	ids := make([]string, 0, len(d.syncs))
	for id := range d.syncs {
		ids = append(ids, id)
	}
	d.mu.Unlock()

	for _, id := range ids {
		if err := d.DeleteSync(id); err != nil {
			d.logf("ERROR", "erro ao parar %s no shutdown: %v", id, err)
		}
	}

	d.mu.Lock()
	for _, mgr := range d.tunnels {
		mgr.Close()
	}
	d.mu.Unlock()

	_ = d.log.Close()
}
