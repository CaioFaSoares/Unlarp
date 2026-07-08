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
			if err == nil && sessName == "unlarp-tui" {
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

		var execArgs []string
		if hasSess {
			// Se existe, anexa
			execArgs = []string{"tmux", "attach-session", "-t", "unlarp-tui"}
		} else {
			// Se não existe, cria a sessão executando a TUI sem tmux interno
			self, err := os.Executable()
			if err != nil {
				self = "unlarp"
			}
			execArgs = []string{"tmux", "new-session", "-s", "unlarp-tui", self, "tui", "--no-tmux"}
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
	hasSess := exec.Command("tmux", "has-session", "-t", "unlarp-tui").Run() == nil
	if !hasSess {
		fmt.Println("Nenhuma sessão persistente do Unlarp ('unlarp-tui') está rodando.")
		return nil
	}

	err := exec.Command("tmux", "kill-session", "-t", "unlarp-tui").Run()
	if err != nil {
		return fmt.Errorf("erro ao encerrar sessão: %w", err)
	}

	fmt.Println("Sessão persistente do Unlarp ('unlarp-tui') encerrada com sucesso.")
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
