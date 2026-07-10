package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Cria arquivo novo
	if err := WriteFileAtomic(path, []byte("v1"), 0600); err != nil {
		t.Fatalf("escrita inicial falhou: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "v1" {
		t.Fatalf("conteúdo = %q; esperado v1", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("perm = %v; esperado 0600", info.Mode().Perm())
	}

	// Sobrescreve arquivo existente
	if err := WriteFileAtomic(path, []byte("v2"), 0600); err != nil {
		t.Fatalf("sobrescrita falhou: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "v2" {
		t.Fatalf("conteúdo = %q; esperado v2", data)
	}

	// Não deixa temp files para trás
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("diretório tem %d entradas; esperado só o arquivo final", len(entries))
	}

	// Alvo intacto quando a escrita falha (diretório inexistente para o temp)
	badPath := filepath.Join(dir, "nao-existe", "state.json")
	if err := WriteFileAtomic(badPath, []byte("x"), 0600); err == nil {
		t.Fatal("esperado erro para diretório inexistente")
	}
	if data, _ := os.ReadFile(path); string(data) != "v2" {
		t.Fatalf("arquivo original alterado após falha: %q", data)
	}
}
