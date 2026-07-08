package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var execCmd = &cobra.Command{
	Use:   "exec [name] -- <command>",
	Short: "Executar comando no workspace remoto",
	Long: `Executa um comando no workspace remoto e retorna stdout/stderr.
Use -- para separar os argumentos do unlarp do comando remoto.

Exemplos:
  unlarp exec -- ls -la /workspace
  unlarp exec coolify-prod -- docker ps
  unlarp exec -- nix develop --command "node --version"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Determina se o primeiro argumento é um host ou parte do comando
		hostName := ""
		commandArgs := args

		// Se existem args antes do --, o primeiro pode ser o nome do host
		if len(args) > 1 {
			// Verifica se o primeiro argumento é um host configurado
			hostCfg, err := getHostConfig(args[0])
			if err == nil && hostCfg != nil {
				hostName = args[0]
				commandArgs = args[1:]
			}
		}

		if len(commandArgs) == 0 {
			return fmt.Errorf("comando é obrigatório. Uso: unlarp exec [host] -- <command>")
		}

		hostCfg, err := getHostConfig(hostName)
		if err != nil {
			return err
		}

		command := strings.Join(commandArgs, " ")

		client, err := internalssh.NewClient(hostCfg)
		if err != nil {
			return err
		}

		if err := client.Connect(); err != nil {
			return fmt.Errorf("falha ao conectar: %w", err)
		}
		defer client.Close()

		stdout, stderr, err := client.RunCommand(command)

		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(cmd.ErrOrStderr(), stderr)
		}

		if err != nil {
			return fmt.Errorf("comando falhou: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(execCmd)
}

// Versão verbosa do exec que mostra informação extra
var _ = func() {
	execCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if verbose {
			ui.Dim("Executando remotamente: %s", strings.Join(args, " "))
		}
	}
}
