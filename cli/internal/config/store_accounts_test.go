package config

import (
	"path/filepath"
	"testing"
)

func TestAccounts(t *testing.T) {
	store := NewStoreWithPath(filepath.Join(t.TempDir(), "unlarp.yaml"))
	if err := store.AddHost("box", Host{
		Host: "h", Port: 22, User: "root", Workspace: "/workspace",
		Projects: []Project{{Name: "ceoris", RemotePath: "/workspace/ceoris"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.AddAccount("box", "pessoal", "/root/.claude-accounts/pessoal"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAccount("box", "pessoal", "/x"); err == nil {
		t.Fatal("AddAccount duplicada deveria falhar")
	}

	if err := store.SetProjectAccount("box", "/workspace/ceoris", "pessoal"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectAccount("box", "/workspace/ceoris", "inexistente"); err == nil {
		t.Fatal("SetProjectAccount com conta desconhecida deveria falhar")
	}

	cfg, _ := store.Load()
	host := cfg.Hosts["box"]
	if host.Projects[0].Account != "pessoal" {
		t.Fatalf("Account do projeto = %q, esperado 'pessoal'", host.Projects[0].Account)
	}
	if dir, ok := host.AccountDir("pessoal"); !ok || dir != "/root/.claude-accounts/pessoal" {
		t.Fatalf("AccountDir = %q, %v", dir, ok)
	}
	if _, ok := host.AccountDir(""); ok {
		t.Fatal("AccountDir(\"\") deveria retornar false (fallback sem injeção)")
	}

	// remove recusa enquanto o projeto referencia
	if err := store.RemoveAccount("box", "pessoal"); err == nil {
		t.Fatal("RemoveAccount deveria recusar com projeto vinculado")
	}
	if err := store.SetProjectAccount("box", "/workspace/ceoris", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAccount("box", "pessoal"); err != nil {
		t.Fatal(err)
	}
}
