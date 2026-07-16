package git

import (
	"errors"
	"testing"
	"time"
)

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

// HealAllProjects deve pular projetos sem LocalDir (nada pra fazer bundle a
// partir de) sem tentar usar sshClient/sftpClient (aqui nil) — e não deve
// gerar entrada de resultado pra eles.
func TestHealAllProjectsSkipsProjectsWithoutLocalDir(t *testing.T) {
	projects := []HealProject{
		{Name: "sem-local", LocalDir: "", RemotePath: "/tmp/whatever"},
		{Name: "nao-e-git", LocalDir: t.TempDir(), RemotePath: "/tmp/whatever2"},
	}

	results := HealAllProjects(nil, nil, projects, nil)

	if len(results) != 1 {
		t.Fatalf("esperava 1 resultado (só o projeto com LocalDir), veio: %d", len(results))
	}
	if results[0].Name != "nao-e-git" {
		t.Fatalf("esperava resultado do projeto com LocalDir, veio: %q", results[0].Name)
	}
	if results[0].Action != BootstrapNone || results[0].Err != nil {
		t.Fatalf("esperava no-op sem erro (LocalDir não é repo git), veio action=%q err=%v", results[0].Action, results[0].Err)
	}
}

// withTimeout deve retornar erro (não travar o teste) quando fn nunca
// termina — é a proteção contra o "terminal congelou" de uma conexão SSH
// morta, onde RunCommand/SFTP bloqueiam pra sempre sem isso.
func TestWithTimeoutReturnsErrorInsteadOfBlockingForever(t *testing.T) {
	err := withTimeout(20*time.Millisecond, func() error {
		select {} // nunca retorna — simula leitura travada numa conexão morta
	})
	if err == nil {
		t.Fatal("esperava erro de timeout, veio nil")
	}
}

// withTimeout deve propagar o erro real de fn quando ela termina a tempo,
// sem mascará-lo como timeout.
func TestWithTimeoutPropagatesFnError(t *testing.T) {
	wantErr := errors.New("falha real")
	err := withTimeout(time.Second, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("esperava erro real propagado, veio: %v", err)
	}
}
