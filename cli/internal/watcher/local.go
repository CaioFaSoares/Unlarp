package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// LocalWatcher observa mudanças no sistema de arquivos local
type LocalWatcher struct {
	dir        string
	watcher    *fsnotify.Watcher
	onChange   func()
	debounce   time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	watchList  map[string]bool
}

// NewLocalWatcher cria um novo observador de diretório local
func NewLocalWatcher(dir string, debounce time.Duration, onChange func()) (*LocalWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &LocalWatcher{
		dir:       dir,
		watcher:   watcher,
		onChange:  onChange,
		debounce:  debounce,
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
			fmt.Fprintf(os.Stderr, "Erro no watcher local: %v\n", err)
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

			err = w.watcher.Add(cleanPath)
			if err != nil {
				return err
			}
			w.watchList[cleanPath] = true
		}
		return nil
	})
}
