package git

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/sftp"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// remoteBootstrapBundle é o nome do arquivo temporário usado para transferir
// o histórico git via SFTP durante o bootstrap (removido ao final).
const remoteBootstrapBundle = ".unlarp-bootstrap.bundle"

// EnsureRemoteRepo garante que remoteDir tenha um repositório git funcional,
// clonando o histórico de localDir via `git bundle` quando o remoto ainda não
// é um repo git. Necessário porque a engine de sync propositalmente NÃO
// sincroniza o diretório .git bruto (ver IgnoreMatcher.Matches): refs e index
// não são seguros para sync de arquivo genérico, então cada lado precisa do
// seu próprio .git independente, só sincronizamos os metadados leves de
// .git/worktrees/* com rewrite de gitdir (ver shouldRewriteGitPath).
//
// Um bundle resolve isso sem exigir que o remoto tenha rede/credenciais para
// o origin: é um arquivo único, completo, transferido pela mesma conexão
// SSH/SFTP que o unlarp já usa.
//
// Retorna bootstrapped=true quando de fato criou o repo no remoto agora (para
// o caller poder avisar o usuário na primeira vez) — false em todo no-op.
// No-op (sem erro) se localDir não for um repo git, ou se remoteDir já for.
func EnsureRemoteRepo(sshClient *internalssh.Client, sftpClient *sftp.Client, localDir, remoteDir string) (bootstrapped bool, err error) {
	local := LocalInfo(localDir)
	if !local.IsGitRepo {
		return false, nil
	}

	remote, err := GetRemoteGitInfo(sshClient, remoteDir)
	if err != nil {
		return false, fmt.Errorf("checando git remoto: %w", err)
	}
	if remote.IsGitRepo {
		return false, nil
	}

	tmpBundle, err := os.CreateTemp("", "unlarp-bootstrap-*.bundle")
	if err != nil {
		return false, fmt.Errorf("criando bundle temporário: %w", err)
	}
	tmpBundle.Close()
	defer os.Remove(tmpBundle.Name())

	if out, err := exec.Command("git", "-C", localDir, "bundle", "create", tmpBundle.Name(), "--all").CombinedOutput(); err != nil {
		return false, fmt.Errorf("git bundle create falhou: %s: %w", strings.TrimSpace(string(out)), err)
	}

	remoteBundlePath := strings.TrimSuffix(remoteDir, "/") + "/" + remoteBootstrapBundle
	if err := uploadFile(sftpClient, tmpBundle.Name(), remoteBundlePath); err != nil {
		return false, fmt.Errorf("enviando bundle pro remoto: %w", err)
	}

	// Passos críticos numa subshell só pra isolar o exit code deles do
	// cleanup best-effort abaixo — "A && B ; C" reporta o status de C, então
	// sem isso um `rm -f` bem-sucedido mascararia falha no init/fetch/reset.
	critical := fmt.Sprintf("git -C %s init -q", shellQuote(remoteDir))
	critical += fmt.Sprintf(" && git -C %s fetch -q %s '+refs/heads/*:refs/heads/*'", shellQuote(remoteDir), shellQuote(remoteBundlePath))
	if local.Branch != "" {
		critical += fmt.Sprintf(" && git -C %s symbolic-ref HEAD %s", shellQuote(remoteDir), shellQuote("refs/heads/"+local.Branch))
		critical += fmt.Sprintf(" && git -C %s reset --mixed -q", shellQuote(remoteDir))
	}

	cleanup := ""
	if local.RemoteURL != "" {
		cleanup += fmt.Sprintf(" ; git -C %s remote add origin %s >/dev/null 2>&1", shellQuote(remoteDir), shellQuote(local.RemoteURL))
	}
	cleanup += fmt.Sprintf(" ; rm -f %s", shellQuote(remoteBundlePath))

	cmd := fmt.Sprintf("(%s); st=$?%s; exit $st", critical, cleanup)

	if _, stderr, err := sshClient.RunCommand(cmd); err != nil {
		return false, fmt.Errorf("bootstrap git remoto falhou: %s: %w", strings.TrimSpace(stderr), err)
	}

	return true, nil
}

// uploadFile copia um arquivo local para o remoto via SFTP (sem rewrite de
// conteúdo — usado só para o bundle binário do bootstrap).
func uploadFile(sftpClient *sftp.Client, localPath, remotePath string) error {
	local, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer local.Close()

	remote, err := sftpClient.Create(remotePath)
	if err != nil {
		return err
	}
	defer remote.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := local.Read(buf)
		if n > 0 {
			if _, writeErr := remote.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}
