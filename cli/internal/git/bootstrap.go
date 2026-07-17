package git

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// remoteBootstrapBundle é o nome do arquivo temporário usado para transferir
// o histórico git via SFTP durante o bootstrap (removido ao final).
const remoteBootstrapBundle = ".unlarp-bootstrap.bundle"

// remotePullBundle é o equivalente do caminho inverso: bundle criado no
// remoto e baixado pro local quando o remoto está na frente.
const remotePullBundle = ".unlarp-pull.bundle"

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
	// BootstrapDiverged: local E remoto têm commits que o outro lado não
	// tem. Não tocamos em nenhum dos dois pra não perder histórico — só
	// sinalizamos. O caso "remoto na frente" virou BootstrapPulled; aqui
	// sobrou só divergência de verdade.
	BootstrapDiverged BootstrapAction = "diverged"
	// BootstrapPulled: remoto na frente do local (agente commitou ou criou
	// branch/worktree lá) — histórico remoto puxado pro local via bundle.
	// Inverso do Refreshed.
	BootstrapPulled BootstrapAction = "pulled"
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
		} else if isAncestorRemote(sshClient, remoteDir, local.CommitHash) {
			// Remoto na frente (agente commitou lá): puxa o histórico de
			// volta via bundle em vez de só avisar — é o que faz commits,
			// branches e worktrees remotos aparecerem no local.
			if _, err := pullRemoteHistory(sshClient, sftpClient, localDir, remoteDir, local, remote); err != nil {
				return BootstrapNone, err
			}
			return BootstrapPulled, nil
		} else {
			return BootstrapDiverged, nil
		}
	default:
		// HEADs iguais, mas um branch de worktree pode ter nascido ou
		// avançado no remoto sem mover o HEAD principal — o digest de heads
		// pega isso. Direcional: branch só-local não dispara pull.
		if !headsSubset(remote.Heads, local.Heads) {
			changed, err := pullRemoteHistory(sshClient, sftpClient, localDir, remoteDir, local, remote)
			if err != nil {
				return BootstrapNone, err
			}
			if changed {
				return BootstrapPulled, nil
			}
		}
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

// downloadFile copia um arquivo do remoto para o local via SFTP (sem rewrite
// de conteúdo — usado só para o bundle binário do pull).
func downloadFile(sftpClient *sftp.Client, remotePath, localPath string) error {
	remote, err := sftpClient.Open(remotePath)
	if err != nil {
		return err
	}
	defer remote.Close()

	local, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer local.Close()

	_, err = io.Copy(local, remote)
	return err
}

// isAncestorRemote diz se commit é ancestral (ou igual) do HEAD remoto — o
// caso em que é seguro puxar o histórico remoto sem perder commits locais.
// Qualquer falha (objeto inexistente lá, SSH caído) conta como não-ancestral.
func isAncestorRemote(sshClient *internalssh.Client, remoteDir, commit string) bool {
	if commit == "" {
		return false
	}
	ok := false
	if err := withTimeout(30*time.Second, func() error {
		_, _, cmdErr := sshClient.RunCommand(fmt.Sprintf(
			"git -C %s merge-base --is-ancestor %s HEAD",
			shellQuote(remoteDir), shellQuote(commit)))
		ok = cmdErr == nil
		return nil
	}); err != nil {
		return false
	}
	return ok
}

// headsSubset diz se todo par "name:hash" de sub está presente em super.
// Direcional de propósito: um branch só-local não dispara pull (o remoto
// nunca vai ter essa ref, e re-puxar todo ciclo seria loop infinito).
func headsSubset(sub, super string) bool {
	have := make(map[string]bool)
	for _, p := range strings.Fields(super) {
		have[p] = true
	}
	for _, p := range strings.Fields(sub) {
		if !have[p] {
			return false
		}
	}
	return true
}

// pullRemoteHistory traz o histórico git do remoto pro local: o remoto cria
// um bundle incremental (só o que falta aqui), o bundle desce via SFTP e
// applyPullBundle aplica refs e index. changed=false quando não havia nada
// novo de fato (nem objetos nem refs).
func pullRemoteHistory(sshClient *internalssh.Client, sftpClient *sftp.Client, localDir, remoteDir string, local, remote RemoteGitInfo) (bool, error) {
	remoteBundlePath := strings.TrimSuffix(remoteDir, "/") + "/" + remotePullBundle

	var basis []string
	for _, pair := range strings.Fields(local.Heads) {
		if _, hash, ok := strings.Cut(pair, ":"); ok && hash != "" {
			basis = append(basis, hash)
		}
	}
	hashList := strings.Join(basis, " ")
	if hashList == "" {
		hashList = `""` // `for h in ;` é erro de sintaxe em sh
	}
	// O for/cat-file filtra hashes que o remoto não conhece (branch
	// só-local): um hash desconhecido direto no --not derrubaria o bundle
	// create com "bad revision".
	bundleCmd := fmt.Sprintf(
		`cd %s && basis=""; for h in %s; do git cat-file -e "$h^{commit}" 2>/dev/null && basis="$basis ^$h"; done; git bundle create %s --all $basis`,
		shellQuote(remoteDir), hashList, shellQuote(remotePullBundle))

	var stderr string
	emptyBundle := false
	if err := withTimeout(10*time.Minute, func() error {
		var cmdErr error
		_, stderr, cmdErr = sshClient.RunCommand(bundleCmd)
		return cmdErr
	}); err != nil {
		if strings.Contains(stderr, "empty bundle") {
			// Nenhum objeto novo — ainda pode haver ref nova apontando pra
			// objeto que o local já tem (worktree add recém-criada).
			emptyBundle = true
		} else {
			return false, fmt.Errorf("criando bundle no remoto: %s: %w", strings.TrimSpace(stderr), err)
		}
	}

	bundlePath := ""
	if !emptyBundle {
		defer func() {
			// Cleanup remoto best-effort — não dá pra usar o truque de
			// subshell do push porque o download acontece entre create e rm.
			_ = withTimeout(30*time.Second, func() error {
				_, _, _ = sshClient.RunCommand("rm -f " + shellQuote(remoteBundlePath))
				return nil
			})
		}()

		tmp, err := os.CreateTemp("", "unlarp-pull-*.bundle")
		if err != nil {
			return false, fmt.Errorf("criando bundle temporário: %w", err)
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		if err := withTimeout(10*time.Minute, func() error {
			return downloadFile(sftpClient, remoteBundlePath, tmp.Name())
		}); err != nil {
			return false, fmt.Errorf("baixando bundle do remoto: %w", err)
		}
		bundlePath = tmp.Name()
	}

	return applyPullBundle(localDir, bundlePath, remote.Branch, remote.Heads)
}

// applyPullBundle aplica localmente o resultado de um pull: fetch do bundle
// (quando houver), criação das refs que não vieram em bundle nenhum e o
// mesmo truque symbolic-ref+reset do bootstrap remoto pra seguir o branch
// do remoto. 100% local — testável sem SSH. bundlePath=="" significa "sem
// objetos novos, só refs".
func applyPullBundle(localDir, bundlePath, remoteBranch, remoteHeads string) (bool, error) {
	gitOut := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", localDir}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	headBefore, _ := gitOut("rev-parse", "HEAD")

	changed := false
	if bundlePath != "" {
		if out, err := gitOut("bundle", "verify", bundlePath); err != nil {
			return false, fmt.Errorf("bundle inválido: %s", out)
		}
		// Refspec sem force = ff-only por ref: branch local que divergiu é
		// recusado (nunca sobrescrito), e rejeição parcial não é erro fatal
		// — os objetos entram mesmo assim. --update-head-ok pela mesma
		// razão do push remoto: o branch checked-out é justamente o alvo
		// mais comum.
		_, _ = gitOut("fetch", "-q", "--update-head-ok", bundlePath, "refs/heads/*:refs/heads/*")
		changed = true
	}

	// Refs novas cujo tip o local já tem não entram em bundle (limitação do
	// git bundle) — cria direto a partir da lista de heads do remoto.
	haveOut, _ := gitOut("for-each-ref", "--format=%(refname:short)", "refs/heads/")
	have := make(map[string]bool)
	for _, name := range strings.Fields(haveOut) {
		have[name] = true
	}
	for _, pair := range strings.Fields(remoteHeads) {
		name, hash, ok := strings.Cut(pair, ":")
		if !ok || name == "" || have[name] {
			continue
		}
		if _, err := gitOut("branch", "--", name, hash); err == nil {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	// Daqui pra baixo é seguir o HEAD remoto — só quando é seguro mexer.
	branch, headErr := gitOut("symbolic-ref", "-q", "--short", "HEAD")
	if headErr != nil {
		return changed, nil // detached — só refs neste ciclo
	}
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply"} {
		gp, _ := gitOut("rev-parse", "--git-path", marker)
		if gp == "" {
			continue
		}
		if !filepath.IsAbs(gp) {
			gp = filepath.Join(localDir, gp)
		}
		if _, err := os.Stat(gp); err == nil {
			return changed, nil // merge/rebase em curso — não mexe no HEAD
		}
	}

	if remoteBranch != "" && remoteBranch != branch {
		if _, err := gitOut("rev-parse", "--verify", "refs/heads/"+remoteBranch); err == nil {
			_, _ = gitOut("symbolic-ref", "HEAD", "refs/heads/"+remoteBranch)
		}
	}

	// reset --mixed só quando o commit sob HEAD de fato mudou: reconcilia o
	// index preservando a árvore (que é do file sync, dos dois lados — a
	// mesma justificativa do bootstrap remoto). Sem isso o status local
	// mostraria "deleted" pra tudo que o commit novo trouxe.
	if headAfter, _ := gitOut("rev-parse", "HEAD"); headAfter != headBefore {
		_, _ = gitOut("reset", "--mixed", "-q")
	}

	// Worktrees locais vinculadas: o index delas também fica pra trás se o
	// branch avançou via fetch. Best-effort, erros ignorados.
	// ponytail: se o git local recusar fetch em ref checked-out de worktree
	// (varia por versão), a ref atrasa um ciclo; upgrade é fetch+reset por
	// worktree.
	if wtOut, err := gitOut("worktree", "list", "--porcelain"); err == nil {
		for _, line := range strings.Split(wtOut, "\n") {
			if !strings.HasPrefix(line, "worktree ") {
				continue
			}
			wt := strings.TrimPrefix(line, "worktree ")
			if wt == localDir {
				continue
			}
			_ = exec.Command("git", "-C", wt, "reset", "--mixed", "-q").Run()
		}
	}

	return changed, nil
}
