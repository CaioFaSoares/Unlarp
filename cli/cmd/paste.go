package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var pasteSession string

var pasteCmd = &cobra.Command{
	Use:   "paste [host]",
	Short: "Enviar a imagem do clipboard (macOS) para um pane tmux remoto",
	Long: `Lê a imagem do clipboard do macOS, envia via SFTP para
~/.unlarp/pastes/ no servidor e cola o CAMINHO do arquivo no pane tmux alvo.

O Claude Code aceita caminho de arquivo de imagem no prompt: depois do paste,
complemente o texto e aperte Enter na sessão para o agente ler a imagem.
Limitação: não é um Ctrl+V nativo — o pane recebe o caminho, não os bytes.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPaste,
}

func init() {
	rootCmd.AddCommand(pasteCmd)
	pasteCmd.Flags().StringVar(&pasteSession, "session", "", "sessão tmux alvo (obrigatório)")
	_ = pasteCmd.MarkFlagRequired("session")
}

// clipboardPNG salva a imagem do clipboard num arquivo temporário local.
// pngpaste se existir (brew install pngpaste), senão osascript.
func clipboardPNG() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("paste de imagem só é suportado no macOS por enquanto")
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("unlarp-paste-%d.png", time.Now().Unix()))

	if _, err := exec.LookPath("pngpaste"); err == nil {
		if out, err := exec.Command("pngpaste", tmp).CombinedOutput(); err != nil {
			return "", fmt.Errorf("clipboard não contém imagem? pngpaste: %s", strings.TrimSpace(string(out)))
		}
		return tmp, nil
	}

	script := fmt.Sprintf(
		`set f to open for access POSIX file "%s" with write permission
write (the clipboard as «class PNGf») to f
close access f`, tmp)
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return "", fmt.Errorf("clipboard não contém imagem? osascript: %s (dica: brew install pngpaste)", strings.TrimSpace(string(out)))
	}
	return tmp, nil
}

func runPaste(cmd *cobra.Command, args []string) error {
	localPNG, err := clipboardPNG()
	if err != nil {
		return err
	}
	defer os.Remove(localPNG)

	sshClient, displayName, err := connectForAgent(args)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	// Home remota: root é o caso padrão do workspace; senão pergunta ao shell
	remoteHome := "/root"
	if hostCfg, err := getHostConfig(strings.Join(args, "")); err == nil && hostCfg.User != "root" {
		if out, _, err := sshClient.RunCommand("echo $HOME"); err == nil && strings.TrimSpace(out) != "" {
			remoteHome = strings.TrimSpace(out)
		}
	}
	remoteDir := remoteHome + "/.unlarp/pastes"
	remotePath := fmt.Sprintf("%s/%d.png", remoteDir, time.Now().Unix())

	sftpClient, err := internalssh.NewSFTPClient(sshClient)
	if err != nil {
		return fmt.Errorf("erro SFTP: %w", err)
	}
	defer sftpClient.Close()

	if err := sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("erro criando %s: %w", remoteDir, err)
	}
	if err := sftpClient.Upload(localPNG, remotePath); err != nil {
		return fmt.Errorf("erro enviando imagem: %w", err)
	}

	// paste-buffer em vez de send-keys: o pane recebe o texto literal, sem
	// interpretação de teclas. Sem Enter — o usuário complementa e confirma.
	tmuxCmd := fmt.Sprintf("tmux set-buffer -b unlarp-paste %s && tmux paste-buffer -b unlarp-paste -t %s",
		shellQuoteArg(remotePath), shellQuoteArg(pasteSession))
	if _, stderr, err := sshClient.RunCommand(tmuxCmd); err != nil {
		return fmt.Errorf("erro colando na sessão '%s': %s: %w", pasteSession, strings.TrimSpace(stderr), err)
	}

	ui.Success("Imagem enviada para %s em %s", remotePath, displayName)
	ui.Info("O caminho foi colado na sessão '%s' — complete o prompt e aperte Enter para o claude ler a imagem.", pasteSession)
	return nil
}
