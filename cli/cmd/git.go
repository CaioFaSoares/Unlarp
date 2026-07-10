package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	"github.com/CaioFaSoares/unlarp/internal/git"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var (
	gitProject   string
	gitRemoteDir string
)

var gitCmd = &cobra.Command{
	Use:   "git",
	Short: "Operações git no workspace remoto",
}

var gitSwitchCmd = &cobra.Command{
	Use:   "switch <branch> [host]",
	Short: "Trocar a branch de um projeto remoto",
	Long: `Faz checkout de uma branch no projeto remoto via unlarp-agent (ou SSH,
sem o agent). Syncs ativos do projeto detectam a mudança pelo GitGuard e
pausam para resolução na TUI — para a troca orquestrada (pause/resume
automático), use a tecla "b" na aba Projects da TUI.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runGitSwitch,
}

func init() {
	rootCmd.AddCommand(gitCmd)
	gitCmd.AddCommand(gitSwitchCmd)
	gitSwitchCmd.Flags().StringVar(&gitProject, "project", "", "nome do projeto cadastrado no host")
	gitSwitchCmd.Flags().StringVar(&gitRemoteDir, "remote-dir", "", "diretório remoto do repositório")
}

func runGitSwitch(cmd *cobra.Command, args []string) error {
	branch := args[0]
	hostArgs := args[1:]

	sshClient, displayName, err := connectForAgent(hostArgs)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	// Resolve o diretório remoto: --remote-dir direto ou --project cadastrado
	dir := gitRemoteDir
	if dir == "" && gitProject != "" {
		hostCfg, err := getHostConfig(strings.Join(hostArgs, ""))
		if err != nil {
			return err
		}
		for _, p := range hostCfg.Projects {
			if p.Name == gitProject {
				dir = p.RemotePath
				break
			}
		}
		if dir == "" {
			return fmt.Errorf("projeto '%s' não cadastrado em %s", gitProject, displayName)
		}
	}
	if dir == "" {
		return fmt.Errorf("informe --project ou --remote-dir")
	}

	var newBranch, newCommit string
	if ac := agent.Detect(sshClient); ac != nil {
		resp, err := ac.GitOp(dir, agentapi.GitOpCheckout, []string{branch})
		if err != nil {
			return fmt.Errorf("checkout via agent falhou: %w", err)
		}
		newBranch, newCommit = resp.Branch, resp.Commit
	} else {
		quoted := "'" + strings.ReplaceAll(dir, "'", "'\\''") + "'"
		quotedBranch := "'" + strings.ReplaceAll(branch, "'", "'\\''") + "'"
		_, stderr, err := sshClient.RunCommand(fmt.Sprintf("git -C %s checkout %s", quoted, quotedBranch))
		if err != nil {
			return fmt.Errorf("checkout falhou: %s: %w", strings.TrimSpace(stderr), err)
		}
		if info, err := git.GetRemoteGitInfo(sshClient, dir); err == nil && info.IsGitRepo {
			newBranch, newCommit = info.Branch, info.CommitHash
		}
	}

	ui.Success("Branch de %s agora é %s (%s)", dir, newBranch, newCommit)
	ui.Info("Syncs ativos deste projeto vão pausar via GitGuard — resolva na TUI (aba Projects) ou use a tecla 'b' lá para trocas orquestradas.")
	return nil
}
