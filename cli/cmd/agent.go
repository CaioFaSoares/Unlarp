package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CaioFaSoares/unlarp/internal/agent"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Gerenciar o unlarp-agent no container remoto",
	Long: `O unlarp-agent é um servidor helper que roda dentro do container workspace:
detecção de mudanças via inotify (substitui o polling SFTP) e estado que
sobrevive a desconexões do Mac. Sem ele, tudo funciona no modo SFTP atual.`,
}

var agentStatusCmd = &cobra.Command{
	Use:   "status [host]",
	Short: "Verificar se o agent está instalado e respondendo",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAgentStatus,
}

var agentInstallCmd = &cobra.Command{
	Use:   "install [host]",
	Short: "Compilar e instalar o agent no container remoto",
	Long: `Cross-compila o unlarp-agent (GOOS=linux) e envia via SFTP para
/usr/local/bin/unlarp-agent no host remoto. Requer o toolchain Go e deve ser
executado de dentro do repositório do unlarp. Em containers criados com a
imagem atual o entrypoint já supervisiona o agent; em containers antigos um
loop supervisor é iniciado via nohup.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAgentInstall,
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentInstallCmd)
}

func connectForAgent(args []string) (*internalssh.Client, string, error) {
	hostName := ""
	if len(args) > 0 {
		hostName = args[0]
	}
	hostCfg, err := getHostConfig(hostName)
	if err != nil {
		return nil, "", err
	}
	displayName := hostName
	if displayName == "" {
		displayName = getActiveHost()
	}

	sshClient, err := internalssh.NewClient(hostCfg)
	if err != nil {
		return nil, "", err
	}
	if err := sshClient.Connect(); err != nil {
		return nil, "", err
	}
	return sshClient, displayName, nil
}

func runAgentStatus(cmd *cobra.Command, args []string) error {
	sshClient, displayName, err := connectForAgent(args)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	client := agent.New(sshClient)
	info, err := client.Info(2 * time.Second)
	if err != nil {
		ui.Warn("Agent não está respondendo em %s: %v", displayName, err)
		ui.Info("Instale com: unlarp agent install %s", displayName)
		return nil
	}

	ui.Success("unlarp-agent v%s (protocol %d) ativo em %s", info.Version, info.Protocol, displayName)
	return nil
}

func runAgentInstall(cmd *cobra.Command, args []string) error {
	sshClient, displayName, err := connectForAgent(args)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	// Mapeia a arquitetura do container para GOARCH
	unameOut, _, err := sshClient.RunCommand("uname -m")
	if err != nil {
		return fmt.Errorf("falha ao detectar arquitetura remota: %w", err)
	}
	var goarch string
	switch strings.TrimSpace(unameOut) {
	case "x86_64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return fmt.Errorf("arquitetura remota não suportada: %s", strings.TrimSpace(unameOut))
	}

	// Cross-compila a partir do repositório (requer go no PATH e cwd no módulo)
	tmpBin := filepath.Join(os.TempDir(), "unlarp-agent-"+goarch)
	spin := ui.NewSpinner(fmt.Sprintf("Compilando unlarp-agent (linux/%s)...", goarch))
	spin.Start()
	build := exec.Command("go", "build", "-o", tmpBin, "github.com/CaioFaSoares/unlarp/agent")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		spin.StopWithError("Falha na compilação")
		return fmt.Errorf("go build falhou (rode de dentro do repositório do unlarp): %s: %w", strings.TrimSpace(string(out)), err)
	}
	spin.StopWithSuccess("Binário compilado")
	defer os.Remove(tmpBin)

	sftpClient, err := internalssh.NewSFTPClient(sshClient)
	if err != nil {
		return err
	}
	defer sftpClient.Close()

	spin = ui.NewSpinner("Enviando e ativando o agent em " + displayName + "...")
	spin.Start()
	if err := sftpClient.Upload(tmpBin, "/usr/local/bin/unlarp-agent.new"); err != nil {
		spin.StopWithError("Falha no upload")
		return err
	}

	// Ativa: troca o binário e mata o processo antigo (o supervisor reinicia).
	// Em containers sem o loop no entrypoint, sobe um supervisor via nohup.
	activate := `chmod +x /usr/local/bin/unlarp-agent.new && ` +
		`mv -f /usr/local/bin/unlarp-agent.new /usr/local/bin/unlarp-agent && ` +
		`pkill -x unlarp-agent 2>/dev/null; sleep 3; ` +
		`if ! pgrep -x unlarp-agent >/dev/null; then ` +
		`nohup sh -c 'while true; do /usr/local/bin/unlarp-agent >> /var/log/unlarp-agent.log 2>&1; sleep 2; done' >/dev/null 2>&1 & ` +
		`sleep 1; fi; pgrep -x unlarp-agent >/dev/null && echo OK || echo FAIL`
	stdout, stderr, err := sshClient.RunCommand(activate)
	if err != nil || !strings.Contains(stdout, "OK") {
		spin.StopWithError("Falha ao ativar o agent")
		return fmt.Errorf("ativação falhou: %s %s: %v", strings.TrimSpace(stdout), strings.TrimSpace(stderr), err)
	}
	spin.StopWithSuccess("Agent instalado e rodando")

	client := agent.New(sshClient)
	if info, err := client.Info(3 * time.Second); err == nil {
		ui.Success("unlarp-agent v%s (protocol %d) respondendo em %s", info.Version, info.Protocol, displayName)
	} else {
		ui.Warn("Agent instalado mas ainda não respondeu ao handshake: %v", err)
	}
	return nil
}
