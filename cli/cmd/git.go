package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	"github.com/CaioFaSoares/unlarp/internal/git"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
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

var gitHealCmd = &cobra.Command{
	Use:   "heal [host]",
	Short: "Recuperar o repositório git remoto de todos os projetos cadastrados",
	Long: `Roda o bootstrap de git (bundle do histórico local) em todos os projetos
com diretório local vinculado nesse host — cria o repo remoto se não existir,
recupera um .git vazio/quebrado, ou avança o HEAD remoto pro local. Mesmo
mecanismo best-effort que já roda ao iniciar um sync; útil para curar
projetos sem sync ativo (ex: sincronizados antes dessa feature existir).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runGitHeal,
}

func init() {
	rootCmd.AddCommand(gitCmd)
	gitCmd.AddCommand(gitSwitchCmd)
	gitCmd.AddCommand(gitHealCmd)
	gitSwitchCmd.Flags().StringVar(&gitProject, "project", "", "nome do projeto cadastrado no host")
	gitSwitchCmd.Flags().StringVar(&gitRemoteDir, "remote-dir", "", "diretório remoto do repositório")
}

func runGitHeal(cmd *cobra.Command, args []string) error {
	sshClient, displayName, err := connectForAgent(args)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	hostCfg, err := getHostConfig(strings.Join(args, ""))
	if err != nil {
		return err
	}

	sftpC, err := internalssh.NewSFTPClient(sshClient)
	if err != nil {
		return fmt.Errorf("abrindo SFTP: %w", err)
	}
	defer sftpC.Close()

	var projects []git.HealProject
	for _, p := range hostCfg.Projects {
		projects = append(projects, git.HealProject{Name: p.Name, LocalDir: p.LocalDir, RemotePath: p.RemotePath})
	}

	results := git.HealAllProjects(sshClient, sftpC.Inner(), projects)
	if len(results) == 0 {
		ui.Info("Nenhum projeto com diretório local vinculado em %s", displayName)
		return nil
	}

	for _, r := range results {
		switch {
		case r.Err != nil:
			ui.Error("%s: falhou (%v)", r.Name, r.Err)
		case r.Action == git.BootstrapNone:
			ui.Dim("%s: ok", r.Name)
		default:
			ui.Success("%s: %s", r.Name, r.Action)
		}
	}
	return nil
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
