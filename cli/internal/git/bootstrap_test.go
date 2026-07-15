package git

import "testing"

// EnsureRemoteRepo deve ser no-op seguro quando localDir não é um repo git —
// não pode tentar usar sshClient/sftpClient (aqui nil) nesse caminho.
func TestEnsureRemoteRepoNoopWhenLocalNotGitRepo(t *testing.T) {
	bootstrapped, err := EnsureRemoteRepo(nil, nil, t.TempDir(), "/tmp/whatever")
	if err != nil {
		t.Fatalf("esperava no-op sem erro, veio: %v", err)
	}
	if bootstrapped {
		t.Fatal("esperava bootstrapped=false num no-op")
	}
}
