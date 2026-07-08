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
	pollInterval time.Duration
	matcher      *internalsync.IgnoreMatcher
	lastState    map[string]remoteFileState
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
}

// NewRemoteWatcher cria um novo observador de diretório remoto
func NewRemoteWatcher(dir string, sftpClient *sftp.Client, pollInterval time.Duration, matcher *internalsync.IgnoreMatcher, onChange func()) *RemoteWatcher {
	ctx, cancel := context.WithCancel(context.Background())

	return &RemoteWatcher{
		dir:          dir,
		sftpClient:   sftpClient,
		onChange:     onChange,
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
		// Loga o erro mas continua tentando no próximo tick
		fmt.Fprintf(os.Stderr, "Erro ao varrer diretório remoto para polling: %v\n", err)
		return
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
