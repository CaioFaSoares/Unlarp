package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
)

// remoteFileState armazena metadados simplificados para detecção rápida de alteração
type remoteFileState struct {
	Size    int64
	ModTime time.Time
}

// RemoteWatcher monitora mudanças no sistema de arquivos remoto via polling SFTP
type RemoteWatcher struct {
	dir          string
	sftpClient   *sftp.Client
	onChange     func()
	onWarn       func(string)
	reconnect    func() (*sftp.Client, error)
	connected    bool
	pollInterval time.Duration
	matcher      *internalsync.IgnoreMatcher
	lastState    map[string]remoteFileState
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
}

// NewRemoteWatcher cria um novo observador de diretório remoto.
// onWarn recebe avisos não fatais (ex: conexão perdida); se nil, vão para
// os.Stderr. Em apps de terminal em alt-screen (como a TUI), escrever direto
// em os.Stderr corrompe visualmente a tela — nesse caso, passe um onWarn que
// roteia para o log interno do app (mesma convenção de NewLocalWatcher).
// reconnect é opcional: quando um tick de polling falha, é chamado para obter
// um novo *sftp.Client (ex: redial da conexão SSH); nil desativa a
// reconexão automática e o watcher só continua tentando com o client atual.
func NewRemoteWatcher(dir string, sftpClient *sftp.Client, pollInterval time.Duration, matcher *internalsync.IgnoreMatcher, onWarn func(string), reconnect func() (*sftp.Client, error), onChange func()) *RemoteWatcher {
	if onWarn == nil {
		onWarn = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &RemoteWatcher{
		dir:          dir,
		sftpClient:   sftpClient,
		onChange:     onChange,
		onWarn:       onWarn,
		reconnect:    reconnect,
		connected:    true,
		pollInterval: pollInterval,
		matcher:      matcher,
		lastState:    make(map[string]remoteFileState),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start inicia o loop de polling em background
func (w *RemoteWatcher) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Gera o estado inicial
	if state, err := w.scanRemote(); err == nil {
		w.lastState = state
	}

	w.wg.Add(1)
	go w.pollLoop()
}

// Stop encerra o watcher e aguarda a finalização
func (w *RemoteWatcher) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *RemoteWatcher) pollLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.checkChanges()
		}
	}
}

func (w *RemoteWatcher) checkChanges() {
	w.mu.Lock()
	defer w.mu.Unlock()

	currentState, err := w.scanRemote()
	if err != nil {
		// Avisa uma única vez na transição para não inundar o log a cada
		// tick, e tenta reconectar em segundo plano se um hook foi fornecido.
		if w.connected {
			w.connected = false
			w.onWarn(fmt.Sprintf("Conexão remota perdida (%v); tentando reconectar em segundo plano...", err))
		}
		if w.reconnect != nil {
			if newClient, rerr := w.reconnect(); rerr == nil {
				w.sftpClient = newClient
			}
		}
		return
	}

	if !w.connected {
		w.connected = true
		w.onWarn("Conexão remota reestabelecida.")
	}

	changed := false

	// Verifica arquivos alterados ou novos
	for path, current := range currentState {
		last, exists := w.lastState[path]
		if !exists || last.Size != current.Size || !last.ModTime.Equal(current.ModTime) {
			changed = true
			break
		}
	}

	// Verifica se algum arquivo foi removido
	if !changed {
		for path := range w.lastState {
			if _, exists := currentState[path]; !exists {
				changed = true
				break
			}
		}
	}

	if changed {
		w.lastState = currentState
		w.onChange()
	}
}

func (w *RemoteWatcher) scanRemote() (map[string]remoteFileState, error) {
	state := make(map[string]remoteFileState)
	cleanRoot := filepath.ToSlash(filepath.Clean(w.dir))

	walker := w.sftpClient.Walk(cleanRoot)
	for walker.Step() {
		if walker.Err() != nil {
			return nil, walker.Err()
		}

		path := filepath.ToSlash(walker.Path())
		if path == cleanRoot {
			continue
		}

		relPath := strings.TrimPrefix(path, cleanRoot)
		relPath = strings.TrimPrefix(relPath, "/")

		if relPath == "" {
			continue
		}

		stat := walker.Stat()
		isDir := stat.IsDir()

		// Verifica ignore rules
		if w.matcher != nil && w.matcher.Matches(relPath, isDir) {
			if isDir {
				walker.SkipDir()
			}
			continue
		}

		// Armazena apenas arquivos (diretórios mudam de mtime com frequência no Linux e causam falso positivo)
		if !isDir {
			state[relPath] = remoteFileState{
				Size:    stat.Size(),
				ModTime: stat.ModTime().UTC(),
			}
		}
	}

	return state, nil
}
