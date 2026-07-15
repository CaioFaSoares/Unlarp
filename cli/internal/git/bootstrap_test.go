package git

import "testing"

// EnsureRemoteRepo deve ser no-op seguro quando localDir não é um repo git —
// não pode tentar usar sshClient/sftpClient (aqui nil) nesse caminho.
func TestEnsureRemoteRepoNoopWhenLocalNotGitRepo(t *testing.T) {
	action, err := EnsureRemoteRepo(nil, nil, t.TempDir(), "/tmp/whatever")
	if err != nil {
		t.Fatalf("esperava no-op sem erro, veio: %v", err)
	}
	if action != BootstrapNone {
		t.Fatalf("esperava BootstrapNone num no-op, veio: %q", action)
	}
}
