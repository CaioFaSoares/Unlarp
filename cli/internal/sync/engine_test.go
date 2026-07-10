package sync

import (
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestTranslateSymlinkTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		fromBase string
		toBase   string
		expected string
	}{
		{"target igual ao base", "/workspace/prod", "/workspace/prod", "/local/prod", "/local/prod"},
		{"target dentro do base", "/workspace/prod/sub/f.txt", "/workspace/prod", "/local/prod", "/local/prod/sub/f.txt"},
		{"prefixo sem fronteira de path NÃO traduz", "/workspace/prod-api/f.txt", "/workspace/prod", "/local/prod", "/workspace/prod-api/f.txt"},
		{"target relativo passa intacto", "../other/f.txt", "/workspace/prod", "/local/prod", "../other/f.txt"},
		{"target absoluto fora do base passa intacto", "/etc/hosts", "/workspace/prod", "/local/prod", "/etc/hosts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := translateSymlinkTarget(tt.target, tt.fromBase, tt.toBase)
			if result != tt.expected {
				t.Errorf("translateSymlinkTarget(%q, %q, %q) = %q; esperado %q",
					tt.target, tt.fromBase, tt.toBase, result, tt.expected)
			}
		})
	}
}

func TestBuildSyncPlan(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	file := func(size int64, mt time.Time) FileEntry {
		return FileEntry{Mode: 0644, Size: size, ModTime: mt}
	}

	tests := []struct {
		name              string
		L, R, A           Snapshot
		wantUploads       []string
		wantDownloads     []string
		wantLocalDeletes  []string
		wantRemoteDeletes []string
		wantAdopt         []string
		wantForget        []string
	}{
		{
			name:        "novo local -> upload",
			L:           Snapshot{"a.txt": file(10, t0)},
			R:           Snapshot{},
			A:           Snapshot{},
			wantUploads: []string{"a.txt"},
		},
		{
			name:          "novo remoto -> download",
			L:             Snapshot{},
			R:             Snapshot{"a.txt": file(10, t0)},
			A:             Snapshot{},
			wantDownloads: []string{"a.txt"},
		},
		{
			name:             "deletado no remoto, local inalterado -> delete local",
			L:                Snapshot{"a.txt": file(10, t0)},
			R:                Snapshot{},
			A:                Snapshot{"a.txt": file(10, t0)},
			wantLocalDeletes: []string{"a.txt"},
		},
		{
			name:              "deletado local, remoto inalterado -> delete remoto",
			L:                 Snapshot{},
			R:                 Snapshot{"a.txt": file(10, t0)},
			A:                 Snapshot{"a.txt": file(10, t0)},
			wantRemoteDeletes: []string{"a.txt"},
		},
		{
			name:        "deletado no remoto mas modificado local -> recria remoto",
			L:           Snapshot{"a.txt": file(20, t1)},
			R:           Snapshot{},
			A:           Snapshot{"a.txt": file(10, t0)},
			wantUploads: []string{"a.txt"},
		},
		{
			name:        "modificado só local -> upload",
			L:           Snapshot{"a.txt": file(20, t1)},
			R:           Snapshot{"a.txt": file(10, t0)},
			A:           Snapshot{"a.txt": file(10, t0)},
			wantUploads: []string{"a.txt"},
		},
		{
			name:          "modificado só remoto -> download",
			L:             Snapshot{"a.txt": file(10, t0)},
			R:             Snapshot{"a.txt": file(20, t1)},
			A:             Snapshot{"a.txt": file(10, t0)},
			wantDownloads: []string{"a.txt"},
		},
		{
			name:        "conflito modificado em ambos, local mais novo -> upload (newest-wins)",
			L:           Snapshot{"a.txt": file(20, t1)},
			R:           Snapshot{"a.txt": file(30, t0)},
			A:           Snapshot{"a.txt": file(10, t0.Add(-time.Minute))},
			wantUploads: []string{"a.txt"},
		},
		{
			name:      "idêntico em ambos sem histórico -> adota (evita ressurreição)",
			L:         Snapshot{"a.txt": file(10, t0)},
			R:         Snapshot{"a.txt": file(10, t0)},
			A:         Snapshot{},
			wantAdopt: []string{"a.txt"},
		},
		{
			name:       "deletado em ambos -> limpa histórico",
			L:          Snapshot{},
			R:          Snapshot{},
			A:          Snapshot{"a.txt": file(10, t0)},
			wantForget: []string{"a.txt"},
		},
		{
			name:        "symlinks divergentes sem histórico -> conflito, não adoção",
			L:           Snapshot{"lnk": {Mode: os.ModeSymlink | 0777, SymlinkTarget: "[root]/a", ModTime: t1}},
			R:           Snapshot{"lnk": {Mode: os.ModeSymlink | 0777, SymlinkTarget: "[root]/b", ModTime: t0}},
			A:           Snapshot{},
			wantUploads: []string{"lnk"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := buildSyncPlan(tt.L, tt.R, tt.A, StrategyNewestWins)

			check := func(label string, got, want []string) {
				sort.Strings(got)
				sort.Strings(want)
				if len(got) == 0 && len(want) == 0 {
					return
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %v; esperado %v", label, got, want)
				}
			}
			check("uploads", p.uploads, tt.wantUploads)
			check("downloads", p.downloads, tt.wantDownloads)
			check("localDeletes", p.localDeletes, tt.wantLocalDeletes)
			check("remoteDeletes", p.remoteDeletes, tt.wantRemoteDeletes)
			check("adopt", p.adopt, tt.wantAdopt)
			check("forget", p.forget, tt.wantForget)
		})
	}
}

func TestBuildSyncPlanRecordsConflicts(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	file := func(size int64, mt time.Time) FileEntry {
		return FileEntry{Mode: 0644, Size: size, ModTime: mt}
	}

	// Conflito com histórico: modificado em ambos, local mais novo vence
	p := buildSyncPlan(
		Snapshot{"a.txt": file(20, t1)},
		Snapshot{"a.txt": file(30, t0)},
		Snapshot{"a.txt": file(10, t0.Add(-time.Minute))},
		StrategyNewestWins,
	)
	if len(p.conflicts) != 1 || p.conflicts[0].path != "a.txt" || p.conflicts[0].winner != "local" {
		t.Errorf("esperado conflito a.txt vencedor=local, obtido %+v", p.conflicts)
	}

	// Conflito sem histórico: novo em ambos com conteúdo diferente, remoto mais novo vence
	p = buildSyncPlan(
		Snapshot{"b.txt": file(10, t0)},
		Snapshot{"b.txt": file(20, t1)},
		Snapshot{},
		StrategyNewestWins,
	)
	if len(p.conflicts) != 1 || p.conflicts[0].winner != "remote" {
		t.Errorf("esperado conflito b.txt vencedor=remote, obtido %+v", p.conflicts)
	}

	// Sem conflito: mudança só de um lado não registra nada
	p = buildSyncPlan(
		Snapshot{"c.txt": file(20, t1)},
		Snapshot{"c.txt": file(10, t0)},
		Snapshot{"c.txt": file(10, t0)},
		StrategyNewestWins,
	)
	if len(p.conflicts) != 0 {
		t.Errorf("esperado nenhum conflito, obtido %+v", p.conflicts)
	}
}

func TestLoadLastStateRebaselinesCorrupt(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/sync_state_test.json"
	if err := os.WriteFile(statePath, []byte("{corrompido"), 0600); err != nil {
		t.Fatal(err)
	}

	e := &Engine{StatePath: statePath, AuditPath: dir + "/audit.log", lastState: make(Snapshot)}
	if err := e.loadLastState(); err != nil {
		t.Fatalf("estado corrompido não deve bloquear: %v", err)
	}
	if e.StateWarning == "" {
		t.Error("StateWarning deveria estar preenchido após re-baseline")
	}
	if len(e.lastState) != 0 {
		t.Errorf("lastState deveria estar vazio após re-baseline, obtido %d entradas", len(e.lastState))
	}
	if _, err := os.Stat(statePath + ".corrupt"); err != nil {
		t.Errorf("arquivo corrompido deveria ter sido preservado em .corrupt: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("arquivo de estado original deveria ter sido movido")
	}
}
