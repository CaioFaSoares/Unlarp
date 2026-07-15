package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/git"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var (
	worktreeNewBranch bool
	worktreeNoTmux    bool
)

var worktreeCmd = &cobra.Command{
	Use:   "worktree",
	Short: "Worktrees git em projetos remotos (para agentes trabalharem em paralelo)",
}

var worktreeAddCmd = &cobra.Command{
	Use:   "add <project> <branch> [host]",
	Short: "Criar uma worktree no projeto remoto e abrir uma sessão tmux nela",
	Long: `Cria uma worktree git DENTRO do repositório remoto do projeto, em
<projeto>/.claude/worktree-<branch>, e abre uma sessão tmux apontando para ela.

Como a worktree fica dentro do repo, o sync já existente do projeto a espelha
automaticamente na pasta local vinculada — sem precisar de um sync novo. A
engine de sync reescreve os metadados git (gitdir) entre as máquinas, então
o git funciona dos dois lados.`,
	Args: cobra.RangeArgs(2, 3),
	RunE: runWorktreeAdd,
}

var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <project> <branch> [host]",
	Short: "Remover uma worktree criada com `unlarp worktree add` (e a sessão tmux dela)",
	Args:  cobra.RangeArgs(2, 3),
	RunE:  runWorktreeRemove,
}

func init() {
	rootCmd.AddCommand(worktreeCmd)
	worktreeCmd.AddCommand(worktreeAddCmd)
	worktreeCmd.AddCommand(worktreeRemoveCmd)
	worktreeAddCmd.Flags().BoolVarP(&worktreeNewBranch, "new-branch", "b", false, "criar a branch (git worktree add -b)")
	worktreeAddCmd.Flags().BoolVar(&worktreeNoTmux, "no-tmux", false, "não criar sessão tmux na worktree")
}

// findProject resolve um projeto cadastrado pelo nome no host.
func findProject(hostCfg *config.Host, name string) (config.Project, bool) {
	for _, p := range hostCfg.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return config.Project{}, false
}

func worktreePaths(proj config.Project, branch string) (remotePath, tmuxSession, localPath string) {
	rel := git.WorktreeRelPath(branch)
	remotePath = strings.TrimSuffix(proj.RemotePath, "/") + "/" + rel
	tmuxSession = proj.Name + "-wt-" + git.SanitizeWorktreeName(branch)
	if proj.LocalDir != "" {
		localPath = strings.TrimSuffix(proj.LocalDir, "/") + "/" + rel
	}
	return
}

func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func runWorktreeAdd(cmd *cobra.Command, args []string) error {
	projectName, branch := args[0], args[1]
	hostArgs := args[2:]

	sshClient, displayName, err := connectForAgent(hostArgs)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	hostCfg, err := getHostConfig(strings.Join(hostArgs, ""))
	if err != nil {
		return err
	}
	proj, ok := findProject(hostCfg, projectName)
	if !ok {
		return fmt.Errorf("projeto '%s' não cadastrado em %s", projectName, displayName)
	}

	wtPath, tmuxSession, localPath := worktreePaths(proj, branch)

	// Garante que o repo remoto exista e esteja em dia ANTES do worktree add:
	// este comando roda direto no remoto (ou via unlarp-agent) e não passa
	// pela engine de sync, então sem isso um repo remoto vazio/quebrado (ou
	// só defasado) faz o add falhar ou criar a worktree a partir de um HEAD
	// antigo. Best-effort e seguro para projetos sem LocalDir vinculado —
	// EnsureRemoteRepo é no-op quando o local não é repo git.
	if proj.LocalDir != "" {
		if sftpC, err := internalssh.NewSFTPClient(sshClient); err != nil {
			ui.Warn("não consegui abrir SFTP pra garantir o git remoto: %v", err)
		} else {
			_, bootstrapErr := git.EnsureRemoteRepo(sshClient, sftpC.Inner(), proj.LocalDir, proj.RemotePath)
			sftpC.Close()
			if bootstrapErr != nil {
				ui.Warn("não consegui garantir/atualizar o git remoto: %v", bootstrapErr)
			}
		}
	}

	// git worktree add [-b branch] <path> [branch]
	var gitArgs []string
	if worktreeNewBranch {
		gitArgs = []string{"-b", branch, wtPath}
	} else {
		gitArgs = []string{wtPath, branch}
	}

	if ac := agent.Detect(sshClient); ac != nil {
		if _, err := ac.GitOp(proj.RemotePath, agentapi.GitOpWorktreeAdd, gitArgs); err != nil {
			return fmt.Errorf("worktree add via agent falhou: %w", err)
		}
	} else {
		quoted := make([]string, len(gitArgs))
		for i, a := range gitArgs {
			quoted[i] = shellQuoteArg(a)
		}
		gitCmd := fmt.Sprintf("git -C %s worktree add %s", shellQuoteArg(proj.RemotePath), strings.Join(quoted, " "))
		if _, stderr, err := sshClient.RunCommand(gitCmd); err != nil {
			return fmt.Errorf("worktree add falhou: %s: %w", strings.TrimSpace(stderr), err)
		}
	}

	// Esconde as worktrees do `git status` do repo principal (info/exclude é
	// local ao repo, não versionado). Falha aqui não é fatal.
	excludeCmd := fmt.Sprintf(
		"grep -qxF '.claude/worktree-*' %[1]s/.git/info/exclude 2>/dev/null || echo '.claude/worktree-*' >> %[1]s/.git/info/exclude",
		shellQuoteArg(proj.RemotePath))
	_, _, _ = sshClient.RunCommand(excludeCmd)

	ui.Success("Worktree criada em %s (branch %s)", wtPath, branch)

	if !worktreeNoTmux {
		tmuxCmd := fmt.Sprintf("tmux -u new-session -d -s %s -c %s", shellQuoteArg(tmuxSession), shellQuoteArg(wtPath))
		if _, stderr, err := sshClient.RunCommand(tmuxCmd); err != nil {
			ui.Warn("Sessão tmux não criada: %s", strings.TrimSpace(stderr))
		} else {
			ui.Success("Sessão tmux '%s' criada na worktree", tmuxSession)
			ui.Info("Conecte com: unlarp connect %s --tmux --tmux-session %s", displayName, tmuxSession)
		}
	}

	if localPath != "" {
		ui.Info("Com o sync do projeto ativo, a worktree aparece localmente em: %s", localPath)
	}
	return nil
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	projectName, branch := args[0], args[1]
	hostArgs := args[2:]

	sshClient, displayName, err := connectForAgent(hostArgs)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	hostCfg, err := getHostConfig(strings.Join(hostArgs, ""))
	if err != nil {
		return err
	}
	proj, ok := findProject(hostCfg, projectName)
	if !ok {
		return fmt.Errorf("projeto '%s' não cadastrado em %s", projectName, displayName)
	}

	wtPath, tmuxSession, localPath := worktreePaths(proj, branch)

	// Mata a sessão tmux antes (processos com cwd na worktree impedem o remove)
	_, _, _ = sshClient.RunCommand("tmux kill-session -t " + shellQuoteArg(tmuxSession))

	if ac := agent.Detect(sshClient); ac != nil {
		if _, err := ac.GitOp(proj.RemotePath, agentapi.GitOpWorktreeRemove, []string{wtPath}); err != nil {
			// Sem --force na allowlist do agent: worktree suja falha — guardrail
			return fmt.Errorf("worktree remove via agent falhou (worktree suja? commit/descarte antes): %w", err)
		}
	} else {
		gitCmd := fmt.Sprintf("git -C %s worktree remove %s", shellQuoteArg(proj.RemotePath), shellQuoteArg(wtPath))
		if _, stderr, err := sshClient.RunCommand(gitCmd); err != nil {
			return fmt.Errorf("worktree remove falhou: %s: %w", strings.TrimSpace(stderr), err)
		}
	}

	ui.Success("Worktree %s removida (sessão tmux '%s' finalizada)", wtPath, tmuxSession)
	if localPath != "" {
		ui.Info("O sync do projeto propaga a remoção para %s em instantes.", localPath)
	}
	return nil
}
