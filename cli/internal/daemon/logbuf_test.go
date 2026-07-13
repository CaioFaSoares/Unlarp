package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestLogBuf(t *testing.T) *logBuf {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	l, err := newLogBuf()
	if err != nil {
		t.Fatalf("newLogBuf: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestLogBufSinceCursor(t *testing.T) {
	l := newTestLogBuf(t)

	l.Append("INFO", "primeiro")
	l.Append("INFO", "segundo")
	l.Append("WARN", "terceiro")

	entries, latest := l.Since(1)
	if latest != 3 {
		t.Fatalf("latest = %d, want 3", latest)
	}
	if len(entries) != 2 || entries[0].Msg != "segundo" || entries[1].Msg != "terceiro" {
		t.Fatalf("Since(1) = %+v, want [segundo terceiro]", entries)
	}

	if entries, _ := l.Since(3); entries != nil {
		t.Fatalf("Since(latest) deveria devolver nada, veio %+v", entries)
	}
}

func TestLogBufRingWrap(t *testing.T) {
	l := newTestLogBuf(t)

	for i := 0; i < maxRingEntries+50; i++ {
		l.Append("INFO", "linha %d", i)
	}

	entries, latest := l.Since(0)
	if latest != uint64(maxRingEntries+50) {
		t.Fatalf("latest = %d, want %d", latest, maxRingEntries+50)
	}
	if len(entries) != maxRingEntries {
		t.Fatalf("ring deveria conter %d entradas, tem %d", maxRingEntries, len(entries))
	}
	if entries[0].Msg != "linha 50" {
		t.Fatalf("primeira entrada do ring = %q, want %q", entries[0].Msg, "linha 50")
	}
}

func TestLogBufFileRotation(t *testing.T) {
	l := newTestLogBuf(t)
	path := l.path

	big := make([]byte, maxLogFileSize+1)
	for i := range big {
		big[i] = 'a'
	}
	l.Append("INFO", "%s", string(big))
	l.Append("INFO", "depois da rotação")

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("esperava %s.1 após passar do limite: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("erro ao ler arquivo atual: %v", err)
	}
	if filepath.Base(path) == "" || len(data) == 0 {
		t.Fatalf("arquivo pós-rotação vazio")
	}
}
