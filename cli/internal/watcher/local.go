package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
)

// LocalWatcher observa mudanças no sistema de arquivos local
type LocalWatcher struct {
	dir        string
	watcher    *fsnotify.Watcher
	onChange   func()
	onWarn     func(string)
	debounce   time.Duration
	matcher    *internalsync.IgnoreMatcher
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	watchList  map[string]bool
}

// NewLocalWatcher cria um novo observador de diretório local. matcher pode ser
// nil, mas sem ele diretórios como node_modules/.git são observados por
// completo, o que é lento e pode esgotar os file descriptors do processo.
// onWarn recebe avisos não fatais (ex: symlink quebrado); se nil, eles vão
// para os.Stderr. Em apps de terminal em alt-screen (como a TUI, que usa
// Bubble Tea), escrever direto em os.Stderr corrompe visualmente a tela —
// nesse caso, passe um onWarn que roteia para o log interno do app.
func NewLocalWatcher(dir string, debounce time.Duration, matcher *internalsync.IgnoreMatcher, onWarn func(string), onChange func()) (*LocalWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if onWarn == nil {
		onWarn = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &LocalWatcher{
		dir:       dir,
		watcher:   watcher,
		onChange:  onChange,
		onWarn:    onWarn,
		debounce:  debounce,
		matcher:   matcher,
		ctx:       ctx,
		cancel:    cancel,
		watchList: make(map[string]bool),
	}, nil
}

// Start inicia a observação recursiva em segundo plano
func (w *LocalWatcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Adiciona recursivamente o diretório raiz e subdiretórios
	err := w.addRecursive(w.dir)
	if err != nil {
		w.watcher.Close()
		return err
	}

	w.wg.Add(1)
	go w.watchLoop()

	return nil
}

// Stop para o watcher e limpa recursos
func (w *LocalWatcher) Stop() {
	w.cancel()
	w.watcher.Close()
	w.wg.Wait()
}

func (w *LocalWatcher) watchLoop() {
	defer w.wg.Done()

	var (
		timer     *time.Timer
		eventChan = make(chan struct{})
	)

	// Goroutine separada para debouncing
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-eventChan:
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(w.debounce, func() {
					select {
					case <-w.ctx.Done():
						return
					default:
						w.onChange()
					}
				})
			}
		}
	}()

	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Ignora eventos irrelevantes (Chmod)
			if event.Has(fsnotify.Chmod) {
				continue
			}

			// Se um novo diretório foi criado, adiciona ao watcher
			if event.Has(fsnotify.Create) {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					w.mu.Lock()
					_ = w.addRecursive(event.Name)
					w.mu.Unlock()
				}
			}

			// Sinaliza evento para o debouncer
			select {
			case eventChan <- struct{}{}:
			case <-w.ctx.Done():
				return
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.onWarn(fmt.Sprintf("Erro no watcher local: %v", err))
		}
	}
}

func (w *LocalWatcher) addRecursive(path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			// Normaliza o path
			cleanPath := filepath.Clean(p)
			if w.watchList[cleanPath] {
				return nil
			}

			if w.matcher != nil && cleanPath != filepath.Clean(w.dir) {
				if rel, relErr := filepath.Rel(w.dir, cleanPath); relErr == nil && w.matcher.Matches(rel, true) {
					return filepath.SkipDir
				}
			}

			err = w.watcher.Add(cleanPath)
			if err != nil {
				// No macOS/kqueue, watcher.Add tenta abrir cada entrada do
				// diretório para observá-la individualmente; um symlink
				// quebrado (aponta pra um alvo inexistente) faz open()
				// falhar e abortaria a árvore inteira. Uma pasta com
				// symlink quebrado não deve impedir o sync do resto.
				w.onWarn(fmt.Sprintf("Aviso: não foi possível observar %q: %v", cleanPath, err))
				return nil
			}
			w.watchList[cleanPath] = true
		}
		return nil
	})
}
