package watcher

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/agent"
)

// Stopper é o denominador comum entre RemoteWatcher e AgentWatcher — os
// callers (TUI, CLI) só precisam parar o watcher remoto, seja ele qual for.
type Stopper interface {
	Stop()
}

// AgentWatcher observa um diretório remoto via long-poll no unlarp-agent do
// container (inotify server-side), no lugar do polling SFTP do RemoteWatcher.
// Mesma shape: onChange coalescido pelo caller, onWarn para avisos não fatais.
type AgentWatcher struct {
	dir           string
	client        *agent.Client
	globalIgnores []string
	onChange      func()
	onWarn        func(string)
	reconnect     func() (*agent.Client, error)
	pollTimeout   time.Duration
	connected     bool
	failCount     int
	since         uint64
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewAgentWatcher segue a convenção de NewRemoteWatcher: onWarn nil vai para
// os.Stderr (em TUIs alt-screen, passe um onWarn que roteia para o log do app).
// reconnect é opcional: chamado quando o long-poll falha repetidamente, para
// obter um novo client (ex: redial da conexão SSH subjacente); nil desativa.
func NewAgentWatcher(dir string, client *agent.Client, globalIgnores []string, onWarn func(string), reconnect func() (*agent.Client, error), onChange func()) *AgentWatcher {
	if onWarn == nil {
		onWarn = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &AgentWatcher{
		dir:           dir,
		client:        client,
		globalIgnores: globalIgnores,
		onChange:      onChange,
		onWarn:        onWarn,
		reconnect:     reconnect,
		pollTimeout:   25 * time.Second,
		connected:     true,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start registra o watch no agent e inicia o loop de long-poll.
func (w *AgentWatcher) Start() error {
	seq, err := w.client.Watch(w.dir, w.globalIgnores)
	if err != nil {
		return fmt.Errorf("falha ao registrar watch no agent: %w", err)
	}
	w.since = seq

	w.wg.Add(1)
	go w.pollLoop()
	return nil
}

// Stop encerra o watcher e aguarda a finalização
func (w *AgentWatcher) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *AgentWatcher) pollLoop() {
	defer w.wg.Done()

	for {
		if w.ctx.Err() != nil {
			return
		}

		resp, err := w.client.Events(w.dir, w.since, w.pollTimeout)
		if err != nil {
			if w.ctx.Err() != nil {
				return
			}
			if w.connected {
				w.connected = false
				w.onWarn(fmt.Sprintf("unlarp-agent indisponível (%v); tentando novamente...", err))
			}
			w.failCount++
			select {
			case <-w.ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			// ponytail: mesmo padrão do RemoteWatcher — redial SSH só a cada
			// 5 falhas; falha isolada costuma ser só o agent reiniciando (~2s)
			if w.reconnect != nil && w.failCount%5 == 0 {
				if newClient, rerr := w.reconnect(); rerr == nil {
					w.client = newClient
				}
			}
			// Re-registra (idempotente): cobre restart do agent pelo
			// entrypoint, que recarrega os watches persistidos
			if seq, werr := w.client.Watch(w.dir, w.globalIgnores); werr == nil {
				w.since = seq
				w.connected = true
				w.failCount = 0
				w.onWarn("unlarp-agent reestabelecido.")
				// Mudanças durante a indisponibilidade não geraram evento:
				// força um ciclo de reconciliação para não perder nada
				w.onChange()
			}
			continue
		}
		w.failCount = 0

		if !w.connected {
			w.connected = true
			w.onWarn("unlarp-agent reestabelecido.")
		}

		if resp.Changed {
			w.since = resp.Seq
			w.onChange()
		}
	}
}
