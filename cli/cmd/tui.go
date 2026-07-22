package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/tui"
)

var (
	tuiNoTmux bool
	tuiKill   bool
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Interface interativa baseada em terminal",
	Long:  `Abre um painel interativo (TUI) para gerenciar todas as sessões, monitorar logs e status.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if tuiKill {
			return killTuiSession()
		}

		// Se o usuário pedir --no-tmux ou tmux não estiver instalado, roda normal
		tmuxPath, errLook := exec.LookPath("tmux")
		if tuiNoTmux || errLook != nil {
			return runDirectTUI()
		}

		// Checa se já estamos no tmux na sessão unlarp-tui
		inSession := false
		if os.Getenv("TMUX") != "" {
			sessName, err := getTmuxSessionName()
			if err == nil && strings.HasPrefix(sessName, "unlarp-tui") {
				inSession = true
			}
		}

		if inSession {
			// Já estamos na sessão do tmux unlarp-tui, roda a TUI diretamente
			return runDirectTUI()
		}

		// Caso contrário, gerencia a sessão
		// 1. Checa se existe a sessão unlarp-tui
		hasSess := exec.Command("tmux", "has-session", "-t", "unlarp-tui").Run() == nil

		if !hasSess {
			// Se não existe, cria a sessão (detached) executando a TUI sem tmux interno
			self, err := os.Executable()
			if err != nil {
				self = "unlarp"
			}
			if err := exec.Command("tmux", "new-session", "-d", "-s", "unlarp-tui", self, "tui", "--no-tmux").Run(); err != nil {
				return fmt.Errorf("falha ao criar sessão tmux local: %w", err)
			}
		}

		// Habilita scroll com mouse só nesta sessão (não mexe no tmux.conf global do usuário)
		_ = exec.Command("tmux", "set-option", "-t", "unlarp-tui", "mouse", "on").Run()

		// Cria uma grouped session (view independente das windows compartilhadas)
		// para que cada terminal possa navegar entre windows de forma independente.
		// destroy-unattached garante limpeza automática ao detach.
		groupedName := fmt.Sprintf("unlarp-tui-%d", os.Getpid())
		execArgs := []string{"tmux",
			"new-session", "-t", "unlarp-tui", "-s", groupedName,
			";", "set", "destroy-unattached", "on",
			";", "set", "mouse", "on",
		}
		env := os.Environ()
		execErr := syscall.Exec(tmuxPath, execArgs, env)
		if execErr != nil {
			return fmt.Errorf("falha ao executar tmux: %w", execErr)
		}
		return nil
	},
}

var tuiKillCmd = &cobra.Command{
	Use:   "kill",
	Short: "Encerra a sessão persistente do tmux local",
	RunE: func(cmd *cobra.Command, args []string) error {
		return killTuiSession()
	},
}

func killTuiSession() error {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		fmt.Println("Nenhuma sessão persistente do Unlarp ('unlarp-tui') está rodando.")
		return nil
	}

	killed := false
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(name, "unlarp-tui") {
			_ = exec.Command("tmux", "kill-session", "-t", name).Run()
			killed = true
		}
	}

	if !killed {
		fmt.Println("Nenhuma sessão persistente do Unlarp ('unlarp-tui') está rodando.")
	} else {
		fmt.Println("Sessões persistentes do Unlarp ('unlarp-tui') encerradas com sucesso.")
	}
	return nil
}

func runDirectTUI() error {
	model, err := tui.NewAppModel()
	if err != nil {
		return err
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("erro ao iniciar a TUI: %w", err)
	}

	return nil
}

func getTmuxSessionName() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "#S").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiNoTmux, "no-tmux", false, "Desabilita a inicialização automática dentro do tmux local")
	tuiCmd.Flags().BoolVar(&tuiKill, "kill", false, "Encerra a sessão persistente do tmux local ('unlarp-tui')")
	tuiCmd.AddCommand(tuiKillCmd)
	rootCmd.AddCommand(tuiCmd)
}
