package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/config"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var accountCmd = &cobra.Command{
	Use:   "account",
	Short: "Contas Claude Code no host remoto (isoladas via CLAUDE_CONFIG_DIR)",
	Long: `Gerencia múltiplas contas do Claude Code no host remoto. Cada conta é um
diretório de configuração isolado (CLAUDE_CONFIG_DIR): credenciais, histórico e
settings próprios. Projetos podem apontar para uma conta (campo 'account' no
~/.unlarp.yaml); sessões tmux criadas para o projeto recebem a variável
automaticamente. Projeto sem conta usa o ~/.claude padrão do remoto.`,
}

var accountAddCmd = &cobra.Command{
	Use:   "add <nome> [dir] [host]",
	Short: "Cadastrar uma conta e criar o diretório remoto dela",
	Long: `Cadastra uma conta no host. Sem [dir], usa $HOME/.claude-accounts/<nome>
no remoto (resolvido e salvo como path absoluto). O diretório é criado na hora.`,
	Args: cobra.RangeArgs(1, 3),
	RunE: runAccountAdd,
}

var accountListCmd = &cobra.Command{
	Use:   "list [host]",
	Short: "Listar contas cadastradas e os projetos vinculados",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAccountList,
}

var accountRemoveCmd = &cobra.Command{
	Use:   "remove <nome> [host]",
	Short: "Remover uma conta do config (o diretório remoto é mantido)",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runAccountRemove,
}

var accountLoginCmd = &cobra.Command{
	Use:   "login <nome> [host]",
	Short: "Fazer login do Claude Code nessa conta (interativo, via SSH)",
	Long: `Abre um shell interativo no remoto com CLAUDE_CONFIG_DIR apontando para o
diretório da conta e roda 'claude /login'. Autentique no browser local colando a
URL/código exibidos. O login fica gravado no diretório da conta.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAccountLogin,
}

func init() {
	rootCmd.AddCommand(accountCmd)
	accountCmd.AddCommand(accountAddCmd)
	accountCmd.AddCommand(accountListCmd)
	accountCmd.AddCommand(accountRemoveCmd)
	accountCmd.AddCommand(accountLoginCmd)
}

// resolveHostName resolve o nome efetivo do host (arg explícito > override > default)
func resolveHostName(name string) (string, error) {
	if name == "" {
		name = getActiveHost()
	}
	if name == "" {
		return "", fmt.Errorf("nenhum host configurado. Use 'unlarp config add' para adicionar um host")
	}
	return name, nil
}

func runAccountAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	dir := ""
	hostArg := ""
	// add <nome> [dir] [host]: 2º arg começando com / ou ~ é dir, senão é host
	if len(args) >= 2 {
		if strings.HasPrefix(args[1], "/") || strings.HasPrefix(args[1], "~") {
			dir = args[1]
		} else {
			hostArg = args[1]
		}
	}
	if len(args) == 3 {
		hostArg = args[2]
	}

	hostName, err := resolveHostName(hostArg)
	if err != nil {
		return err
	}

	sshClient, displayName, err := connectForAgent([]string{hostName})
	if err != nil {
		return err
	}
	defer sshClient.Close()

	if dir == "" || strings.HasPrefix(dir, "~") {
		home, _, err := sshClient.RunCommand("echo $HOME")
		if err != nil {
			return fmt.Errorf("não consegui resolver $HOME remoto: %w", err)
		}
		home = strings.TrimSpace(home)
		if dir == "" {
			dir = home + "/.claude-accounts/" + name
		} else {
			dir = home + strings.TrimPrefix(dir, "~")
		}
	}

	if _, stderr, err := sshClient.RunCommand("mkdir -p " + shellQuoteArg(dir)); err != nil {
		return fmt.Errorf("mkdir remoto falhou: %s: %w", strings.TrimSpace(stderr), err)
	}

	store := config.NewStore()
	if err := store.AddAccount(hostName, name, dir); err != nil {
		return err
	}

	ui.Success("Conta '%s' cadastrada em %s → %s", name, displayName, dir)
	ui.Info("Faça login com: unlarp account login %s", name)
	return nil
}

func runAccountList(cmd *cobra.Command, args []string) error {
	hostName, err := resolveHostName(strings.Join(args, ""))
	if err != nil {
		return err
	}
	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return err
	}

	if len(hostCfg.Accounts) == 0 {
		ui.Info("Nenhuma conta cadastrada em %s. Use 'unlarp account add <nome>'.", hostName)
		return nil
	}

	for _, name := range sortedAccountNames(hostCfg.Accounts) {
		var projs []string
		for _, p := range hostCfg.Projects {
			if p.Account == name {
				projs = append(projs, p.Name)
			}
		}
		line := fmt.Sprintf("%s → %s", name, hostCfg.Accounts[name])
		if len(projs) > 0 {
			line += fmt.Sprintf("  (projetos: %s)", strings.Join(projs, ", "))
		}
		fmt.Println(line)
	}
	return nil
}

func runAccountRemove(cmd *cobra.Command, args []string) error {
	hostName, err := resolveHostName(strings.Join(args[1:], ""))
	if err != nil {
		return err
	}
	store := config.NewStore()
	if err := store.RemoveAccount(hostName, args[0]); err != nil {
		return err
	}
	ui.Success("Conta '%s' removida do config.", args[0])
	ui.Info("O diretório remoto com as credenciais foi mantido.")
	return nil
}

func runAccountLogin(cmd *cobra.Command, args []string) error {
	name := args[0]
	hostName, err := resolveHostName(strings.Join(args[1:], ""))
	if err != nil {
		return err
	}
	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return err
	}
	dir, ok := hostCfg.AccountDir(name)
	if !ok {
		return fmt.Errorf("conta '%s' não cadastrada em %s. Use 'unlarp account add %s'", name, hostName, name)
	}

	sshClient, displayName, err := connectForAgent([]string{hostName})
	if err != nil {
		return err
	}
	defer sshClient.Close()

	ui.Info("Abrindo login do Claude Code na conta '%s' em %s...", name, displayName)
	shell := fmt.Sprintf("mkdir -p %[1]s; export CLAUDE_CONFIG_DIR=%[1]s; claude /login", shellQuoteArg(dir))
	return sshClient.InteractiveShell(shell)
}

func sortedAccountNames(accounts map[string]string) []string {
	names := make([]string, 0, len(accounts))
	for n := range accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
