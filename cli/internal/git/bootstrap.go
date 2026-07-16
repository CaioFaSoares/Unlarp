package git

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/sftp"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// remoteBootstrapBundle é o nome do arquivo temporário usado para transferir
// o histórico git via SFTP durante o bootstrap (removido ao final).
const remoteBootstrapBundle = ".unlarp-bootstrap.bundle"

// BootstrapAction descreve o que EnsureRemoteRepo de fato fez no remoto.
type BootstrapAction string

const (
	// BootstrapNone: remoto já é um repo git saudável e em dia — no-op.
	BootstrapNone BootstrapAction = ""
	// BootstrapCreated: remoto ainda não era repo git; criado agora do zero.
	BootstrapCreated BootstrapAction = "created"
	// BootstrapHealed: remoto já tinha .git, mas sem HEAD (vazio ou quebrado
	// por um bootstrap anterior que falhou entre o init e o fetch).
	BootstrapHealed BootstrapAction = "healed"
	// BootstrapRefreshed: remoto saudável mas atrás do local; avançado pro
	// HEAD local (só quando o HEAD remoto é ancestral do local — ver
	// isAncestorLocal).
	BootstrapRefreshed BootstrapAction = "refreshed"
	// BootstrapDiverged: remoto tem commit(s) que o local não tem. Não
	// tocamos no remoto pra não perder histórico — só sinalizamos.
	// ponytail: reconciliação de divergência remota fica adiada; hoje só
	// avisamos e deixamos o remoto como está.
	BootstrapDiverged BootstrapAction = "diverged"
)

// EnsureRemoteRepo garante que remoteDir tenha um repositório git funcional e
// em dia, clonando/atualizando o histórico de localDir via `git bundle`.
// Necessário porque a engine de sync propositalmente NÃO sincroniza o
// diretório .git bruto (ver IgnoreMatcher.Matches): refs e index não são
// seguros para sync de arquivo genérico, então cada lado precisa do seu
// próprio .git independente, só sincronizamos os metadados leves de
// .git/worktrees/* com rewrite de gitdir (ver shouldRewriteGitPath).
//
// Um bundle resolve isso sem exigir que o remoto tenha rede/credenciais para
// o origin: é um arquivo único, completo, transferido pela mesma conexão
// SSH/SFTP que o unlarp já usa.
//
// O guard de "já é repo" antigo confiava em `git rev-parse
// --is-inside-work-tree`, que é true mesmo pra um repo vazio (sem HEAD) — se
// um bootstrap anterior falhasse entre o `git init` e o `fetch` (rede, bundle
// truncado, disco), o remoto ficava com um .git vazio e o guard virava no-op
// pra sempre, deixando o projeto "sem histórico" indefinidamente. Por isso o
// guard abaixo também considera CommitHash (vazio = sem HEAD = precisa
// curar) e compara o HEAD remoto com o local (atrás = atualiza; divergente =
// não mexe).
//
// No-op (sem erro, BootstrapNone) se localDir não for um repo git, ou se
// remoteDir já for um repo saudável e no mesmo commit do local.
func EnsureRemoteRepo(sshClient *internalssh.Client, sftpClient *sftp.Client, localDir, remoteDir string) (action BootstrapAction, err error) {
	local := LocalInfo(localDir)
	if !local.IsGitRepo {
		return BootstrapNone, nil
	}

	var remote RemoteGitInfo
	if timeoutErr := withTimeout(30*time.Second, func() error {
		var infoErr error
		remote, infoErr = GetRemoteGitInfo(sshClient, remoteDir)
		return infoErr
	}); timeoutErr != nil {
		return BootstrapNone, fmt.Errorf("checando git remoto: %w", timeoutErr)
	}

	switch {
	case !remote.IsGitRepo:
		action = BootstrapCreated
	case remote.CommitHash == "":
		// repo existe mas nunca ganhou HEAD — bootstrap anterior deve ter
		// falhado no meio do caminho.
		action = BootstrapHealed
	case remote.CommitHash != local.CommitHash:
		if isAncestorLocal(localDir, remote.CommitHash) {
			action = BootstrapRefreshed
		} else {
			return BootstrapDiverged, nil
		}
	default:
		return BootstrapNone, nil
	}

	tmpBundle, err := os.CreateTemp("", "unlarp-bootstrap-*.bundle")
	if err != nil {
		return BootstrapNone, fmt.Errorf("criando bundle temporário: %w", err)
	}
	tmpBundle.Close()
	defer os.Remove(tmpBundle.Name())

	if out, err := exec.Command("git", "-C", localDir, "bundle", "create", tmpBundle.Name(), "--all").CombinedOutput(); err != nil {
		return BootstrapNone, fmt.Errorf("git bundle create falhou: %s: %w", strings.TrimSpace(string(out)), err)
	}

	remoteBundlePath := strings.TrimSuffix(remoteDir, "/") + "/" + remoteBootstrapBundle
	// Timeout generoso — bundle de histórico completo pode passar de 100MB em
	// repos grandes (ex: assets versionados) e SFTP sem pipelining é lento em
	// links ruins; 10min cobre isso sem travar pra sempre numa conexão morta.
	if err := withTimeout(10*time.Minute, func() error {
		return uploadFile(sftpClient, tmpBundle.Name(), remoteBundlePath)
	}); err != nil {
		return BootstrapNone, fmt.Errorf("enviando bundle pro remoto: %w", err)
	}

	// Passos críticos numa subshell só pra isolar o exit code deles do
	// cleanup best-effort abaixo — "A && B ; C" reporta o status de C, então
	// sem isso um `rm -f` bem-sucedido mascararia falha no init/fetch/reset.
	// `git init` é idempotente, então reusar o mesmo bloco pra create/heal/
	// refresh é seguro — inclusive quando o remoto já tinha .git. `mkdir -p`
	// cobre cura proativa de um projeto registrado cujo remoteDir ainda não
	// existe (ex: antes do primeiro file-sync).
	critical := fmt.Sprintf("mkdir -p %s", shellQuote(remoteDir))
	critical += fmt.Sprintf(" && git -C %s init -q", shellQuote(remoteDir))
	// --update-head-ok: sem isso, git recusa fetch pra dentro do branch
	// atualmente checked out num repo não-bare (erro "refusing to fetch
	// into branch ... checked out") — que é sempre o caso aqui a partir do
	// segundo bootstrap bem-sucedido em diante, já que o passo de
	// symbolic-ref abaixo deixa HEAD remoto exatamente na ref que o fetch
	// seguinte precisa atualizar. Seguro porque o reset --mixed abaixo
	// reconcilia o index, e a árvore de arquivos é gerenciada pelo sync
	// SFTP separado, não por checkout git.
	critical += fmt.Sprintf(" && git -C %s fetch -q --update-head-ok %s '+refs/heads/*:refs/heads/*'", shellQuote(remoteDir), shellQuote(remoteBundlePath))
	if local.Branch != "" {
		critical += fmt.Sprintf(" && git -C %s symbolic-ref HEAD %s", shellQuote(remoteDir), shellQuote("refs/heads/"+local.Branch))
		critical += fmt.Sprintf(" && git -C %s reset --mixed -q", shellQuote(remoteDir))
	}

	cleanup := ""
	if local.RemoteURL != "" {
		remoteName := local.RemoteName
		if remoteName == "" {
			remoteName = "origin"
		}
		cleanup += fmt.Sprintf(" ; git -C %s remote add %s %s >/dev/null 2>&1", shellQuote(remoteDir), shellQuote(remoteName), shellQuote(local.RemoteURL))
	}
	cleanup += fmt.Sprintf(" ; rm -f %s", shellQuote(remoteBundlePath))

	cmd := fmt.Sprintf("(%s); st=$?%s; exit $st", critical, cleanup)

	var stderr string
	if timeoutErr := withTimeout(60*time.Second, func() error {
		var cmdErr error
		_, stderr, cmdErr = sshClient.RunCommand(cmd)
		return cmdErr
	}); timeoutErr != nil {
		return BootstrapNone, fmt.Errorf("bootstrap git remoto falhou: %s: %w", strings.TrimSpace(stderr), timeoutErr)
	}

	return action, nil
}

// withTimeout roda fn numa goroutine e retorna erro de timeout se ela não
// terminar em d. ponytail: RunCommand/SFTP não têm deadline nativo — numa
// conexão SSH morta, uma leitura/escrita trava pra sempre sem isso (era o
// "terminal congelou" observado no comando `unlarp git heal`). A goroutine
// perdida no caso de timeout é aceitável aqui: best-effort, raro, e o
// processo segue vivo em vez de travado. Se isso passar a doer, a correção
// de fundo é dar contexto/deadline ao *internalssh.Client em si.
func withTimeout(d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return fmt.Errorf("timeout após %s — conexão remota pode estar lenta ou morta", d)
	}
}

// HealProject é um projeto candidato a bootstrap: nome pra relatório,
// diretório local vinculado e caminho remoto correspondente.
type HealProject struct {
	Name       string
	LocalDir   string
	RemotePath string
}

// HealResult é o resultado do bootstrap de um HealProject.
type HealResult struct {
	Name   string
	Action BootstrapAction
	Err    error
}

// HealAllProjects roda EnsureRemoteRepo em sequência para cada projeto que
// tem LocalDir — cobre cura proativa de todos os projetos cadastrados num
// host (não só o que está com sync ativo). Reusa o mesmo sshClient/
// sftpClient do host para todos, já que o bootstrap não depende de estado
// entre projetos. Projetos sem LocalDir são pulados (nada pra fazer bundle
// a partir de) e não geram entrada no resultado.
//
// onStart, se não nil, é chamado com o nome do projeto antes de processá-lo —
// primeiro bootstrap de um repo grande pode levar minutos (envia o histórico
// inteiro via bundle), então sem isso o caller fica sem qualquer sinal de
// progresso até o fim.
func HealAllProjects(sshClient *internalssh.Client, sftpClient *sftp.Client, projects []HealProject, onStart func(name string)) []HealResult {
	var results []HealResult
	for _, p := range projects {
		if p.LocalDir == "" {
			continue
		}
		if onStart != nil {
			onStart(p.Name)
		}
		action, err := EnsureRemoteRepo(sshClient, sftpClient, p.LocalDir, p.RemotePath)
		results = append(results, HealResult{Name: p.Name, Action: action, Err: err})
	}
	return results
}

// isAncestorLocal diz se commit é ancestral (ou igual) do HEAD local — só
// nesse caso é seguro avançar o remoto sem risco de perder commits que só
// existam lá. Se o objeto não existir localmente (histórico divergente) o
// comando falha e tratamos como não-ancestral.
func isAncestorLocal(localDir, commit string) bool {
	if commit == "" {
		return false
	}
	return exec.Command("git", "-C", localDir, "merge-base", "--is-ancestor", commit, "HEAD").Run() == nil
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
