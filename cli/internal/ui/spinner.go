package ui

import (
	"fmt"
	"time"

	"github.com/fatih/color"
)

// Spinner exibe um spinner animado no terminal
type Spinner struct {
	frames  []string
	message string
	done    chan bool
	running bool
}

// NewSpinner cria um novo spinner com mensagem
func NewSpinner(message string) *Spinner {
	return &Spinner{
		frames:  []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		message: message,
		done:    make(chan bool),
	}
}

// Start inicia o spinner em uma goroutine
func (s *Spinner) Start() {
	s.running = true
	go func() {
		i := 0
		cyan := color.New(color.FgCyan)
		for {
			select {
			case <-s.done:
				return
			default:
				fmt.Printf("\r %s %s", cyan.Sprint(s.frames[i%len(s.frames)]), s.message)
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

// Stop para o spinner e limpa a linha
func (s *Spinner) Stop() {
	if s.running {
		s.done <- true
		s.running = false
		fmt.Print("\r\033[K") // Limpa a linha
	}
}

// StopWithSuccess para o spinner e mostra mensagem de sucesso
func (s *Spinner) StopWithSuccess(message string) {
	s.Stop()
	Success(message)
}

// StopWithError para o spinner e mostra mensagem de erro
func (s *Spinner) StopWithError(message string) {
	s.Stop()
	Error(message)
}
