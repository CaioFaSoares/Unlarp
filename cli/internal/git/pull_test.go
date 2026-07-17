package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitT roda git no dir e falha o teste em erro — repos reais em t.TempDir(),
// sem SSH/SFTP (mesmo estilo nil-client do bootstrap_test.go).
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v em %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepoT(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "config", "user.name", "t")
	gitT(t, dir, "config", "user.email", "t@t")
	return dir
}

func commitT(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "--", file)
	gitT(t, dir, "commit", "-q", "-m", "add "+file)
	return gitT(t, dir, "rev-parse", "HEAD")
}

func headsT(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join(strings.Fields(gitT(t, dir, "for-each-ref", "--format=%(refname:short):%(objectname)", "refs/heads/")), " ")
}

func TestHeadsSubset(t *testing.T) {
	cases := []struct {
		sub, super string
		want       bool
	}{
		{"", "main:aaa", true},
		{"main:aaa", "main:aaa", true},
		{"main:aaa", "main:aaa feat:bbb", true},        // branch só-local não dispara
		{"main:aaa feat:bbb", "main:aaa", false},       // branch novo no remoto dispara
		{"main:bbb", "main:aaa", false},                // branch avançou no remoto
		{"main:aaa", "", false},
	}
	for _, c := range cases {
		if got := headsSubset(c.sub, c.super); got != c.want {
			t.Errorf("headsSubset(%q, %q) = %v, esperava %v", c.sub, c.super, got, c.want)
		}
	}
}

// Caminho principal: remoto avançou na main E criou branch com commit — o
// bundle incremental traz os dois, o local faz ff na main, cria feat e segue
// o branch do HEAD remoto, sem tocar na árvore de arquivos (que é do file
// sync).
func TestApplyPullBundle(t *testing.T) {
	remote := newRepoT(t)
	commitT(t, remote, "a.txt", "v1")

	local := t.TempDir()
	gitT(t, filepath.Dir(local), "clone", "-q", remote, local)
	localMain := gitT(t, local, "rev-parse", "HEAD")

	// Remoto avança: commit na main + branch feat com commit próprio.
	remoteMain := commitT(t, remote, "b.txt", "v1")
	gitT(t, remote, "checkout", "-q", "-b", "feat")
	remoteFeat := commitT(t, remote, "c.txt", "v1")

	bundle := filepath.Join(t.TempDir(), "x.bundle")
	gitT(t, remote, "bundle", "create", bundle, "--all", "--not", localMain)

	changed, err := applyPullBundle(local, bundle, "feat", headsT(t, remote))
	if err != nil {
		t.Fatalf("applyPullBundle: %v", err)
	}
	if !changed {
		t.Fatal("esperava changed=true")
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/main"); got != remoteMain {
		t.Errorf("main local não avançou: %s, esperava %s", got, remoteMain)
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/feat"); got != remoteFeat {
		t.Errorf("feat local: %s, esperava %s", got, remoteFeat)
	}
	if got := gitT(t, local, "symbolic-ref", "--short", "HEAD"); got != "feat" {
		t.Errorf("HEAD local devia seguir o branch remoto (feat), veio %s", got)
	}
	// Árvore intocada: b.txt/c.txt existem no commit mas não na árvore —
	// quem materializa arquivo é o file sync, não o pull.
	if _, err := os.Stat(filepath.Join(local, "c.txt")); err == nil {
		t.Error("pull não devia materializar arquivos na árvore")
	}
}

// Branch novo no remoto apontando pra commit que o local já tem (worktree
// add recém-criada): não existe bundle nenhum (git recusa bundle vazio) — a
// ref tem que nascer do passo de refs faltantes.
func TestApplyPullBundleSameCommitBranch(t *testing.T) {
	remote := newRepoT(t)
	hash := commitT(t, remote, "a.txt", "v1")

	local := t.TempDir()
	gitT(t, filepath.Dir(local), "clone", "-q", remote, local)

	changed, err := applyPullBundle(local, "", "main", headsT(t, remote)+" wt-test:"+hash)
	if err != nil {
		t.Fatalf("applyPullBundle: %v", err)
	}
	if !changed {
		t.Fatal("esperava changed=true (ref criada)")
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/wt-test"); got != hash {
		t.Errorf("wt-test devia existir em %s, veio %s", hash, got)
	}
	if got := gitT(t, local, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("HEAD não devia mudar, veio %s", got)
	}
}

// HEAD local detached: refs atualizam, HEAD fica onde está.
func TestApplyPullBundleDetachedHead(t *testing.T) {
	remote := newRepoT(t)
	first := commitT(t, remote, "a.txt", "v1")

	local := t.TempDir()
	gitT(t, filepath.Dir(local), "clone", "-q", remote, local)
	gitT(t, local, "checkout", "-q", first) // detached

	remoteMain := commitT(t, remote, "b.txt", "v1")
	bundle := filepath.Join(t.TempDir(), "x.bundle")
	gitT(t, remote, "bundle", "create", bundle, "--all", "--not", first)

	changed, err := applyPullBundle(local, bundle, "main", headsT(t, remote))
	if err != nil {
		t.Fatalf("applyPullBundle: %v", err)
	}
	if !changed {
		t.Fatal("esperava changed=true")
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/main"); got != remoteMain {
		t.Errorf("main local não avançou: %s, esperava %s", got, remoteMain)
	}
	if got := gitT(t, local, "rev-parse", "HEAD"); got != first {
		t.Errorf("HEAD detached devia ficar em %s, veio %s", first, got)
	}
}

// Nada novo (sem bundle, heads remotos são subconjunto): changed=false.
func TestApplyPullBundleNoChanges(t *testing.T) {
	remote := newRepoT(t)
	commitT(t, remote, "a.txt", "v1")

	local := t.TempDir()
	gitT(t, filepath.Dir(local), "clone", "-q", remote, local)

	changed, err := applyPullBundle(local, "", "main", headsT(t, remote))
	if err != nil {
		t.Fatalf("applyPullBundle: %v", err)
	}
	if changed {
		t.Fatal("esperava changed=false")
	}
}

// Branch local que divergiu do remoto nunca é sobrescrito (refspec sem
// force) — só os demais refs avançam.
func TestApplyPullBundleNeverClobbersDivergedLocalBranch(t *testing.T) {
	remote := newRepoT(t)
	commitT(t, remote, "a.txt", "v1")

	local := t.TempDir()
	gitT(t, filepath.Dir(local), "clone", "-q", remote, local)
	gitT(t, local, "config", "user.name", "t")
	gitT(t, local, "config", "user.email", "t@t")

	// Divergência: commit local e commit remoto diferentes na main.
	localMain := commitT(t, local, "local.txt", "v1")
	commitT(t, remote, "remote.txt", "v1")
	gitT(t, remote, "checkout", "-q", "-b", "feat")
	remoteFeat := commitT(t, remote, "c.txt", "v1")

	bundle := filepath.Join(t.TempDir(), "x.bundle")
	gitT(t, remote, "bundle", "create", bundle, "--all")

	if _, err := applyPullBundle(local, bundle, "feat", headsT(t, remote)); err != nil {
		t.Fatalf("applyPullBundle: %v", err)
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/main"); got != localMain {
		t.Errorf("main local divergida foi sobrescrita: %s, esperava %s", got, localMain)
	}
	if got := gitT(t, local, "rev-parse", "refs/heads/feat"); got != remoteFeat {
		t.Errorf("feat devia chegar mesmo com main divergida: %s, esperava %s", got, remoteFeat)
	}
}
