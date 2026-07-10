package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/fsutil"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// ErrSyncPaused é retornado por SyncExec quando o GitGuard pausou o sync
var ErrSyncPaused = errors.New("sync pausado pelo GitGuard — resolva a divergência na aba Projects")

// Engine coordena a sincronização bidirecional
type Engine struct {
	ID               string           `json:"id"`
	LocalDir         string           `json:"local_dir"`
	RemoteDir        string           `json:"remote_dir"`
	HostName         string           `json:"host_name"`
	ConflictStrategy ConflictStrategy `json:"conflict_strategy"`
	StatePath        string           `json:"state_path"`

	sshClient      *internalssh.Client
	sftpClient     *sftp.Client
	matcher        *IgnoreMatcher
	globalIgnores  []string
	ignoreFilePath string
	mu             sync.Mutex

	// Último estado conhecido compartilhado (A na reconciliação de 3 vias)
	lastState Snapshot

	progressMu sync.RWMutex
	progress   SyncProgress

	guardMu  sync.RWMutex
	gitGuard GitGuard

	OnFileSuccess func(path string, action string)
}

// GitGuard protege contra sobrescrita quando o remote mudou via Git
type GitGuard struct {
	Enabled         bool   `json:"enabled"`
	LastKnownCommit string `json:"last_known_commit"`
	LastKnownBranch string `json:"last_known_branch"`
	Paused          bool   `json:"paused"`
	PauseReason     string `json:"pause_reason"`
}

type FileSyncStatus string

const (
	StatusPending   FileSyncStatus = "pending"
	StatusSyncing   FileSyncStatus = "syncing"
	StatusCompleted FileSyncStatus = "completed"
	StatusFailed    FileSyncStatus = "failed"
)

type FileProgress struct {
	Path   string         `json:"path"`
	Action string         `json:"action"` // "upload" | "download" | "local_delete" | "remote_delete"
	Status FileSyncStatus `json:"status"`
	Error  error          `json:"error,omitempty"`
}

type SyncProgress struct {
	IsSyncing      bool           `json:"is_syncing"`
	TotalItems     int            `json:"total_items"`
	DoneItems      int            `json:"done_items"`
	CurrentFile    string         `json:"current_file"`
	SyncingFiles   []FileProgress `json:"syncing_files"`
	PendingFiles   []FileProgress `json:"pending_files"`
	CompletedFiles []FileProgress `json:"completed_files"`

	// Estatísticas
	Case              int     `json:"case"`
	Percent           float64 `json:"percent"`
	TotalLocal        int     `json:"total_local"`
	TotalRemote       int     `json:"total_remote"`
	InitialUploadsNew int     `json:"initial_uploads_new"`
	InitialUploadsMod int     `json:"initial_uploads_mod"`
	InitialDownloads  int     `json:"initial_downloads"`
	DoneUploadsNew    int     `json:"done_uploads_new"`
	DoneUploadsMod    int     `json:"done_uploads_mod"`
	DoneDownloads     int     `json:"done_downloads"`
}

// NewEngine cria uma nova instância da engine de sync
func NewEngine(id string, localDir, remoteDir, hostName string, globalIgnores []string, strategy ConflictStrategy, sshClient *internalssh.Client, sftpClient *sftp.Client) (*Engine, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Resolve localDir e remoteDir absolutos/normalizados
	absLocal, err := filepath.Abs(localDir)
	if err != nil {
		return nil, err
	}
	// Resolve links simbólicos no caminho local para garantir compatibilidade com watchers de filesystem
	if resolved, err := filepath.EvalSymlinks(absLocal); err == nil {
		absLocal = resolved
	}

	statePath := filepath.Join(home, ".unlarp", fmt.Sprintf("sync_state_%s_%s.json", hostName, id))

	// Cria o matcher de ignores
	ignoreFile := filepath.Join(absLocal, ".unlarpignore")
	matcher := NewIgnoreMatcher(globalIgnores, ignoreFile)

	e := &Engine{
		ID:               id,
		LocalDir:         absLocal,
		RemoteDir:        remoteDir,
		HostName:         hostName,
		ConflictStrategy: strategy,
		StatePath:        statePath,
		sshClient:        sshClient,
		sftpClient:       sftpClient,
		matcher:          matcher,
		globalIgnores:    globalIgnores,
		ignoreFilePath:   ignoreFile,
		lastState:        make(Snapshot),
	}

	if err := e.loadLastState(); err != nil {
		return nil, err
	}

	e.progress.Percent = 100
	e.progress.Case = 1

	return e, nil
}

// UpdateSFTPClient troca o cliente SFTP usado pela engine (ex: após uma
// reconexão SSH invalidar o cliente anterior).
func (e *Engine) UpdateSFTPClient(client *sftp.Client) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sftpClient = client
}

// GetProgress retorna uma cópia segura do progresso atual
func (e *Engine) GetProgress() SyncProgress {
	e.progressMu.RLock()
	defer e.progressMu.RUnlock()
	return e.progress
}

func (e *Engine) startFileProgress(path string, action string) {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()

	// Encontra o arquivo nas pendências e remove de lá
	newPending := make([]FileProgress, 0, len(e.progress.PendingFiles))
	var found *FileProgress
	for _, fp := range e.progress.PendingFiles {
		if fp.Path == path && fp.Action == action {
			fp.Status = StatusSyncing
			found = &fp
		} else {
			newPending = append(newPending, fp)
		}
	}
	e.progress.PendingFiles = newPending

	if found == nil {
		found = &FileProgress{
			Path:   path,
			Action: action,
			Status: StatusSyncing,
		}
	}

	e.progress.CurrentFile = path
	e.progress.SyncingFiles = append(e.progress.SyncingFiles, *found)
}

func (e *Engine) finishFileProgress(path string, action string, isNew bool, err error) {
	e.progressMu.Lock()

	// Remove das ativas
	newSyncing := make([]FileProgress, 0, len(e.progress.SyncingFiles))
	var found *FileProgress
	for _, fp := range e.progress.SyncingFiles {
		if fp.Path == path && fp.Action == action {
			if err != nil {
				fp.Status = StatusFailed
				fp.Error = err
			} else {
				fp.Status = StatusCompleted
			}
			found = &fp
		} else {
			newSyncing = append(newSyncing, fp)
		}
	}
	e.progress.SyncingFiles = newSyncing

	if found == nil {
		status := StatusCompleted
		if err != nil {
			status = StatusFailed
		}
		found = &FileProgress{
			Path:   path,
			Action: action,
			Status: status,
			Error:  err,
		}
	}

	e.progress.DoneItems++

	if err == nil {
		if action == "upload" {
			if isNew {
				e.progress.DoneUploadsNew++
			} else {
				e.progress.DoneUploadsMod++
			}
		} else if action == "download" {
			e.progress.DoneDownloads++
		}
	}

	e.progress.CompletedFiles = append(e.progress.CompletedFiles, *found)
	if len(e.progress.CompletedFiles) > 10 {
		e.progress.CompletedFiles = e.progress.CompletedFiles[len(e.progress.CompletedFiles)-10:]
	}

	e.recalculatePercentLocked()
	cb := e.OnFileSuccess
	e.progressMu.Unlock()

	if err == nil && cb != nil {
		cb(path, action)
	}
}

func (e *Engine) recalculatePercentLocked() {
	switch e.progress.Case {
	case 1:
		pct := float64(e.progress.TotalRemote+e.progress.DoneUploadsNew) / float64(e.progress.TotalLocal) * 100
		if pct > 100 {
			pct = 100
		}
		e.progress.Percent = pct
	case 2:
		remUploadsMod := e.progress.InitialUploadsMod - e.progress.DoneUploadsMod
		if remUploadsMod < 0 {
			remUploadsMod = 0
		}
		pct := float64(remUploadsMod) / float64(e.progress.TotalLocal) * 100
		if pct > 100 {
			pct = 100
		}
		e.progress.Percent = pct
	case 3:
		remDownloads := e.progress.InitialDownloads - e.progress.DoneDownloads
		if remDownloads < 0 {
			remDownloads = 0
		}
		pct := float64(remDownloads) / float64(e.progress.TotalRemote) * 100
		if pct > 100 {
			pct = 100
		}
		e.progress.Percent = pct
	default:
		e.progress.Percent = 100
	}
}

// Pause pausa o sync por divergência Git
func (e *Engine) Pause(reason string) {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	e.gitGuard.Paused = true
	e.gitGuard.PauseReason = reason
}

// Resume retoma o sync após resolução da divergência
func (e *Engine) Resume() {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	e.gitGuard.Paused = false
	e.gitGuard.PauseReason = ""
}

// IsPaused retorna se o sync está pausado e o motivo
func (e *Engine) IsPaused() (bool, string) {
	e.guardMu.RLock()
	defer e.guardMu.RUnlock()
	return e.gitGuard.Paused, e.gitGuard.PauseReason
}

// UpdateGitState atualiza o estado Git conhecido pelo guard
func (e *Engine) UpdateGitState(commit, branch string) {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	e.gitGuard.LastKnownCommit = commit
	e.gitGuard.LastKnownBranch = branch
}

// GetGitGuard retorna uma cópia do estado atual do guard
func (e *Engine) GetGitGuard() GitGuard {
	e.guardMu.RLock()
	defer e.guardMu.RUnlock()
	return e.gitGuard
}

// SetGitGuardEnabled habilita ou desabilita o guard
func (e *Engine) SetGitGuardEnabled(enabled bool) {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	e.gitGuard.Enabled = enabled
}

// rebuildMatcher reconstrói o ignore matcher de forma limpa e lê todos os arquivos .gitignore no diretório local
func (e *Engine) rebuildMatcher() {
	// 1. Cria o matcher inicial apenas com as regras estáticas (globais + .unlarpignore)
	newMatcher := NewIgnoreMatcher(e.globalIgnores, e.ignoreFilePath)

	// Caminho absoluto da raiz local
	absRoot := e.LocalDir

	// Lista para acumular os arquivos .gitignore encontrados
	type gitIgnoreFile struct {
		relDir  string
		absPath string
	}
	var gitignores []gitIgnoreFile

	// Primeiro Passo: Varre a estrutura para encontrar arquivos .gitignore,
	// respeitando APENAS os ignores estáticos compilados até agora (para evitar entrar em node_modules, .git, etc.)
	_ = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if path == absRoot {
			return nil
		}

		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Usa o matcher temporário para ignorar pastas gigantescas (ex: node_modules)
		if newMatcher.Matches(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.IsDir() && filepath.Base(path) == ".gitignore" {
			dir := filepath.Dir(relPath)
			if dir == "." {
				dir = ""
			}
			gitignores = append(gitignores, gitIgnoreFile{
				relDir:  dir,
				absPath: path,
			})
		}

		return nil
	})

	// Segundo Passo: Carrega as regras de todos os arquivos .gitignore encontrados.
	// Como todas as regras de todos os diretórios são carregadas antes de iniciar o sync,
	// evitamos problemas de ordenação léxica na descoberta das regras.
	for _, gf := range gitignores {
		newMatcher.LoadGitIgnoreFile(gf.relDir, gf.absPath)
	}

	e.matcher = newMatcher
}

// hasParentTypeConflict verifica se qualquer pasta pai do caminho informado é um arquivo regular
// no snapshot local (L) ou remoto (R). Isso detecta e previne colisões destrutivas onde uma pasta
// foi convertida em arquivo ou vice-versa em um dos lados.
func hasParentTypeConflict(path string, L, R Snapshot) bool {
	parts := strings.Split(path, "/")
	prefix := ""
	for i := 0; i < len(parts)-1; i++ {
		if i == 0 {
			prefix = parts[i]
		} else {
			prefix = prefix + "/" + parts[i]
		}

		// Se o prefixo pai existe em L como um arquivo regular
		if _, existsInL := L[prefix]; existsInL {
			return true
		}
		// Se o prefixo pai existe em R como um arquivo regular
		if _, existsInR := R[prefix]; existsInR {
			return true
		}
	}
	return false
}

// syncPlan é o resultado puro da reconciliação de 3 vias: o que transferir,
// o que deletar e quais mutações aplicar no histórico (A)
type syncPlan struct {
	uploads       []string
	downloads     []string
	localDeletes  []string
	remoteDeletes []string
	adopt         []string // idênticos em ambos mas ausentes de A: adotar no histórico
	forget        []string // deletados em ambos: limpar do histórico
}

// buildSyncPlan classifica cada caminho de L, R e A em ações. É uma função pura
// (sem I/O, sem mutação) para que a lógica de reconciliação seja testável isoladamente.
func buildSyncPlan(L, R, A Snapshot, strategy ConflictStrategy) syncPlan {
	var p syncPlan

	// Analisa mudanças locais e novos arquivos
	for path, lEntry := range L {
		if hasParentTypeConflict(path, L, R) {
			continue
		}
		rEntry, inR := R[path]
		aEntry, inA := A[path]

		if !inR && !inA {
			// Novo localmente, não existe remoto nem no histórico
			if !lEntry.IsDir {
				p.uploads = append(p.uploads, path)
			}
		} else if !inR && inA {
			// Excluído no remoto (estava no histórico mas sumiu do remoto)
			if lEntry.Changed(aEntry) {
				// Foi modificado localmente após a exclusão remota (conflito: recria remoto)
				p.uploads = append(p.uploads, path)
			} else {
				// Exclusão normal remota -> deleta local
				p.localDeletes = append(p.localDeletes, path)
			}
		} else if inR && !inA {
			// Existe local e remoto, mas não no histórico (novo em ambos)
			var hasDifference bool
			lIsSymlink := lEntry.Mode&os.ModeSymlink != 0
			rIsSymlink := rEntry.Mode&os.ModeSymlink != 0
			if lIsSymlink != rIsSymlink {
				hasDifference = true
			} else if lIsSymlink {
				hasDifference = lEntry.SymlinkTarget != rEntry.SymlinkTarget
			} else {
				hasDifference = lEntry.Size != rEntry.Size || !lEntry.ModTime.Equal(rEntry.ModTime)
			}

			if hasDifference {
				// Conflito
				if ResolveConflict(lEntry, rEntry, strategy) {
					p.uploads = append(p.uploads, path)
				} else {
					p.downloads = append(p.downloads, path)
				}
			} else {
				// Idênticos: adota no histórico. Sem isso, deletar o arquivo em um
				// lado o ressuscitaria no outro (cairia no branch "novo localmente")
				p.adopt = append(p.adopt, path)
			}
		} else if inR && inA {
			// Existe em todos
			lChanged := lEntry.Changed(aEntry)
			rChanged := rEntry.Changed(aEntry)

			if lChanged && !rChanged {
				p.uploads = append(p.uploads, path)
			} else if rChanged && !lChanged {
				p.downloads = append(p.downloads, path)
			} else if lChanged && rChanged {
				// Conflito: modificado em ambos
				if ResolveConflict(lEntry, rEntry, strategy) {
					p.uploads = append(p.uploads, path)
				} else {
					p.downloads = append(p.downloads, path)
				}
			}
		}
	}

	// Analisa mudanças remotas, exclusões locais e novos arquivos remotos
	for path, rEntry := range R {
		if hasParentTypeConflict(path, L, R) {
			continue
		}
		_, inL := L[path]
		aEntry, inA := A[path]

		if !inL && !inA {
			// Novo remoto
			if !rEntry.IsDir {
				p.downloads = append(p.downloads, path)
			}
		} else if !inL && inA {
			// Excluído localmente
			if rEntry.Changed(aEntry) {
				// Modificado remoto após exclusão local -> recria local
				p.downloads = append(p.downloads, path)
			} else {
				// Exclusão normal local -> deleta remoto
				p.remoteDeletes = append(p.remoteDeletes, path)
			}
		}
	}

	// Analisa deletados em ambos
	for path := range A {
		if hasParentTypeConflict(path, L, R) {
			continue
		}
		_, inL := L[path]
		_, inR := R[path]
		if !inL && !inR {
			// Deletado nos dois lados, nada a fazer além de limpar o histórico
			p.forget = append(p.forget, path)
		}
	}

	return p
}

// SyncExec executa um ciclo completo de sincronização de três vias
func (e *Engine) SyncExec() (int, error) {
	// Verifica se o sync está pausado pelo GitGuard antes de tudo
	e.guardMu.RLock()
	paused := e.gitGuard.Paused
	e.guardMu.RUnlock()
	if paused {
		return 0, ErrSyncPaused
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Reconstrói o ignore matcher com todos os arquivos .gitignore locais dinamicamente
	e.rebuildMatcher()

	// 1. Gera Snapshot Local (L)
	L, err := CreateLocalSnapshot(e.LocalDir, e.matcher)
	if err != nil {
		return 0, fmt.Errorf("falha no snapshot local: %w", err)
	}

	// 2. Gera Snapshot Remoto (R)
	R, err := CreateRemoteSnapshot(e.sftpClient, e.RemoteDir, e.matcher)
	if err != nil {
		return 0, fmt.Errorf("falha no snapshot remoto: %w", err)
	}

	// A é o lastState (último estado salvo)
	A := e.lastState

	plan := buildSyncPlan(L, R, A, e.ConflictStrategy)
	uploads := plan.uploads
	downloads := plan.downloads
	localDeletes := plan.localDeletes
	remoteDeletes := plan.remoteDeletes

	// Aplica mutações de histórico decididas pelo planejamento
	stateChanged := false
	for _, path := range plan.adopt {
		e.lastState[path] = L[path]
		stateChanged = true
	}
	for _, path := range plan.forget {
		delete(e.lastState, path)
		stateChanged = true
	}

	totalActions := len(uploads) + len(downloads) + len(localDeletes) + len(remoteDeletes)
	if totalActions == 0 {
		if stateChanged {
			if err := e.saveLastState(); err != nil {
				return 0, fmt.Errorf("falha ao salvar estado do sync: %w", err)
			}
		}
		return 0, nil
	}

	// Inicializa progresso no início de um ciclo ativo
	e.progressMu.Lock()
	totalLocal := len(L)
	totalRemote := len(R)
	if totalLocal == 0 {
		totalLocal = 1
	}
	if totalRemote == 0 {
		totalRemote = 1
	}

	e.progress.IsSyncing = true
	e.progress.TotalItems = totalActions
	e.progress.DoneItems = 0
	e.progress.TotalLocal = totalLocal
	e.progress.TotalRemote = totalRemote
	e.progress.CurrentFile = ""
	e.progress.SyncingFiles = nil
	e.progress.PendingFiles = nil
	e.progress.CompletedFiles = nil

	// Constrói lista de PendingFiles
	for _, path := range localDeletes {
		e.progress.PendingFiles = append(e.progress.PendingFiles, FileProgress{
			Path:   path,
			Action: "local_delete",
			Status: StatusPending,
		})
	}
	for _, path := range remoteDeletes {
		e.progress.PendingFiles = append(e.progress.PendingFiles, FileProgress{
			Path:   path,
			Action: "remote_delete",
			Status: StatusPending,
		})
	}
	for _, path := range downloads {
		e.progress.PendingFiles = append(e.progress.PendingFiles, FileProgress{
			Path:   path,
			Action: "download",
			Status: StatusPending,
		})
	}
	for _, path := range uploads {
		e.progress.PendingFiles = append(e.progress.PendingFiles, FileProgress{
			Path:   path,
			Action: "upload",
			Status: StatusPending,
		})
	}

	e.progress.InitialUploadsNew = 0
	e.progress.InitialUploadsMod = 0
	e.progress.InitialDownloads = len(downloads)
	e.progress.DoneUploadsNew = 0
	e.progress.DoneUploadsMod = 0
	e.progress.DoneDownloads = 0

	for _, path := range uploads {
		_, inR := R[path]
		if inR {
			e.progress.InitialUploadsMod++
		} else {
			e.progress.InitialUploadsNew++
		}
	}

	// Classificação do Caso
	if len(downloads) > 0 {
		e.progress.Case = 3
	} else if e.progress.InitialUploadsMod > 0 {
		e.progress.Case = 2
	} else if e.progress.InitialUploadsNew > 0 || totalLocal > totalRemote {
		e.progress.Case = 1
	} else {
		e.progress.Case = 1
	}

	e.recalculatePercentLocked()
	e.progressMu.Unlock()

	defer func() {
		e.progressMu.Lock()
		e.progress.IsSyncing = false
		e.progress.Percent = 100
		e.progress.Case = 1
		e.progress.CurrentFile = ""
		e.progress.SyncingFiles = nil
		e.progress.PendingFiles = nil
		e.progressMu.Unlock()
	}()

	// Executa ações com acumulação de erros para resiliência
	var errs []error

	// 1. Deleta localmente (apenas se for arquivo/link regular, nunca diretório)
	for _, path := range localDeletes {
		e.startFileProgress(path, "local_delete")
		localPath := filepath.Join(e.LocalDir, path)

		stat, err := os.Lstat(localPath)
		if err == nil {
			if stat.IsDir() {
				err = fmt.Errorf("tentativa de deletar diretório local %s ignorada por segurança", path)
			} else {
				err = os.Remove(localPath)
			}
		} else if os.IsNotExist(err) {
			err = nil // Já não existe
		}

		e.finishFileProgress(path, "local_delete", false, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("falha ao deletar local %s: %w", path, err))
		} else {
			// Sucesso: remove da base de histórico
			delete(e.lastState, path)
		}
	}

	// 2. Deleta remotamente (apenas se for arquivo/link regular, nunca diretório)
	for _, path := range remoteDeletes {
		e.startFileProgress(path, "remote_delete")
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))

		stat, err := e.sftpClient.Lstat(remotePath)
		if err == nil {
			if stat.IsDir() {
				err = fmt.Errorf("tentativa de deletar diretório remoto %s ignorada por segurança", path)
			} else {
				err = e.sftpClient.Remove(remotePath)
			}
		} else if os.IsNotExist(err) {
			err = nil // Já não existe
		}

		e.finishFileProgress(path, "remote_delete", false, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("falha ao deletar remoto %s: %w", path, err))
		} else {
			// Sucesso: remove da base de histórico
			delete(e.lastState, path)
		}
	}

	// Persiste os deletes antes das transferências: um crash daqui em diante
	// não pode ressuscitar arquivos já deletados
	_ = e.saveLastState()

	// ponytail: save periódico a cada 25 transferências, não por operação
	opsSinceSave := 0

	// 3. Executa Downloads (Remoto -> Local)
	for _, path := range downloads {
		localPath := filepath.Join(e.LocalDir, path)
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))

		// Garante diretório pai local
		_ = os.MkdirAll(filepath.Dir(localPath), 0755)

		e.startFileProgress(path, "download")
		err := e.downloadFile(remotePath, localPath)
		e.finishFileProgress(path, "download", false, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("falha ao baixar %s: %w", path, err))
		} else {
			// Sucesso: atualiza o histórico com os metadados corretos locais
			if statLocal, errStat := os.Lstat(localPath); errStat == nil {
				var symlinkTarget string
				if statLocal.Mode()&os.ModeSymlink != 0 {
					if t, errTarget := os.Readlink(localPath); errTarget == nil {
						symlinkTarget = normalizeSymlinkTarget(t, e.LocalDir)
					}
				}
				e.lastState[path] = FileEntry{
					RelPath:       path,
					Size:          statLocal.Size(),
					ModTime:       statLocal.ModTime().UTC().Truncate(time.Second),
					Mode:          statLocal.Mode(),
					IsDir:         statLocal.IsDir(),
					SymlinkTarget: symlinkTarget,
				}
			}
			opsSinceSave++
			if opsSinceSave%25 == 0 {
				_ = e.saveLastState()
			}
		}
	}

	// 4. Executa Uploads (Local -> Remoto)
	for _, path := range uploads {
		localPath := filepath.Join(e.LocalDir, path)
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))

		// Garante diretório pai remoto
		remoteDir := filepath.ToSlash(filepath.Dir(remotePath))
		_ = e.sftpClient.MkdirAll(remoteDir)

		e.startFileProgress(path, "upload")
		err := e.uploadFile(localPath, remotePath)
		_, inR := R[path]
		isNew := !inR
		e.finishFileProgress(path, "upload", isNew, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("falha ao enviar %s: %w", path, err))
		} else {
			// Sucesso: atualiza o histórico com os metadados corretos locais
			if statLocal, errStat := os.Lstat(localPath); errStat == nil {
				var symlinkTarget string
				if statLocal.Mode()&os.ModeSymlink != 0 {
					if t, errTarget := os.Readlink(localPath); errTarget == nil {
						symlinkTarget = normalizeSymlinkTarget(t, e.LocalDir)
					}
				}
				e.lastState[path] = FileEntry{
					RelPath:       path,
					Size:          statLocal.Size(),
					ModTime:       statLocal.ModTime().UTC().Truncate(time.Second),
					Mode:          statLocal.Mode(),
					IsDir:         statLocal.IsDir(),
					SymlinkTarget: symlinkTarget,
				}
			}
			opsSinceSave++
			if opsSinceSave%25 == 0 {
				_ = e.saveLastState()
			}
		}
	}

	// Salva o histórico das alterações aplicadas com sucesso
	if err := e.saveLastState(); err != nil {
		errs = append(errs, fmt.Errorf("falha ao salvar estado do sync: %w", err))
	}

	if len(errs) > 0 {
		return totalActions - len(errs), fmt.Errorf("sync concluído com %d falha(s). Exemplo: %v", len(errs), errs[0])
	}

	return totalActions, nil
}

// translateSymlinkTarget traduz um target absoluto de symlink de uma base para outra,
// respeitando fronteiras de path ("/workspace/prod" não casa com "/workspace/prod-api").
// Targets relativos ou fora da base passam intactos. Espera bases em formato slash.
func translateSymlinkTarget(target, fromBase, toBase string) string {
	t := filepath.ToSlash(target)
	if t == fromBase {
		return toBase
	}
	if strings.HasPrefix(t, fromBase+"/") {
		return toBase + strings.TrimPrefix(t, fromBase)
	}
	return target
}

func (e *Engine) downloadFile(remotePath, localPath string) error {
	// 1. Verifica se o remote é um link simbólico
	stat, err := e.sftpClient.Lstat(remotePath)
	if err == nil && stat.Mode()&os.ModeSymlink != 0 {
		target, err := e.sftpClient.ReadLink(remotePath)
		if err != nil {
			return err
		}

		// Traduz target se for caminho absoluto dentro do base remoto
		target = filepath.FromSlash(translateSymlinkTarget(target, filepath.ToSlash(e.RemoteDir), filepath.ToSlash(e.LocalDir)))

		// Limpa colisão local (nunca diretório, por segurança)
		if statLocal, err := os.Lstat(localPath); err == nil {
			if statLocal.IsDir() {
				return fmt.Errorf("conflito de tipo: o caminho local %s é um diretório, não é possível criar symlink nele", localPath)
			}
			_ = os.Remove(localPath)
		}
		return os.Symlink(target, localPath)
	}

	// 2. Arquivo regular: valida conflito de tipo local
	if statLocal, err := os.Lstat(localPath); err == nil && statLocal.IsDir() {
		return fmt.Errorf("conflito de tipo: o caminho local %s é um diretório, não é possível baixar arquivo para ele", localPath)
	}

	remoteFile, err := e.sftpClient.Open(remotePath)
	if err != nil {
		return err
	}
	defer remoteFile.Close()
	remoteStat, errStat := remoteFile.Stat()

	// Escreve num temp no mesmo diretório e faz rename atômico:
	// um crash/queda de rede no meio da cópia nunca deixa arquivo truncado no destino
	tmpFile, err := os.CreateTemp(filepath.Dir(localPath), ".unlarp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // no-op após rename bem-sucedido

	relPath, errRel := filepath.Rel(e.LocalDir, localPath)
	if errRel == nil && e.shouldRewriteGitPath(relPath) {
		content, err := io.ReadAll(remoteFile)
		if err != nil {
			tmpFile.Close()
			return err
		}
		if _, err := tmpFile.Write(e.rewriteGitContent(content, "download")); err != nil {
			tmpFile.Close()
			return err
		}
	} else {
		n, err := io.Copy(tmpFile, remoteFile)
		if err != nil {
			tmpFile.Close()
			return err
		}
		if errStat == nil && n != remoteStat.Size() {
			tmpFile.Close()
			return fmt.Errorf("transferência incompleta de %s: %d de %d bytes", remotePath, n, remoteStat.Size())
		}
	}

	// Preserva permissões e ModTime no temp antes do rename
	if errStat == nil {
		_ = tmpFile.Chmod(remoteStat.Mode())
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if errStat == nil {
		_ = os.Chtimes(tmpPath, time.Now(), remoteStat.ModTime())
	}

	return os.Rename(tmpPath, localPath)
}

func (e *Engine) uploadFile(localPath, remotePath string) error {
	// 1. Verifica se o local é um link simbólico
	stat, err := os.Lstat(localPath)
	if err == nil && stat.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(localPath)
		if err != nil {
			return err
		}

		// Traduz target se for caminho absoluto dentro do base local
		target = translateSymlinkTarget(target, filepath.ToSlash(e.LocalDir), filepath.ToSlash(e.RemoteDir))

		// Limpa colisão remota via SFTP (nunca diretório, por segurança).
		// SFTP em vez de shell remoto: sobrevive a reconexões (UpdateSFTPClient)
		if statRemote, err := e.sftpClient.Lstat(remotePath); err == nil {
			if statRemote.IsDir() {
				return fmt.Errorf("conflito de tipo: o caminho remoto %s é um diretório, não é possível criar symlink nele", remotePath)
			}
			_ = e.sftpClient.Remove(remotePath)
		}
		return e.sftpClient.Symlink(target, remotePath)
	}

	// 2. Arquivo regular: valida conflito de tipo remoto
	if statRemote, err := e.sftpClient.Lstat(remotePath); err == nil && statRemote.IsDir() {
		return fmt.Errorf("conflito de tipo: o caminho remoto %s é um diretório, não é possível enviar arquivo para ele", remotePath)
	}

	localFile, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer localFile.Close()
	localStat, errStat := localFile.Stat()

	// Escreve num temp remoto e faz rename atômico:
	// uma queda de rede no meio da cópia nunca deixa arquivo truncado no destino
	tmpRemote := remotePath + ".unlarp-tmp"
	remoteFile, err := e.sftpClient.Create(tmpRemote)
	if err != nil {
		return err
	}

	fail := func(err error) error {
		remoteFile.Close()
		_ = e.sftpClient.Remove(tmpRemote)
		return err
	}

	relPath, errRel := filepath.Rel(e.LocalDir, localPath)
	if errRel == nil && e.shouldRewriteGitPath(relPath) {
		content, err := io.ReadAll(localFile)
		if err != nil {
			return fail(err)
		}
		if _, err := remoteFile.Write(e.rewriteGitContent(content, "upload")); err != nil {
			return fail(err)
		}
	} else {
		n, err := io.Copy(remoteFile, localFile)
		if err != nil {
			return fail(err)
		}
		if errStat == nil && n != localStat.Size() {
			return fail(fmt.Errorf("transferência incompleta de %s: %d de %d bytes", localPath, n, localStat.Size()))
		}
	}

	if err := remoteFile.Close(); err != nil {
		_ = e.sftpClient.Remove(tmpRemote)
		return err
	}

	// Preserva permissões e ModTime no temp antes do rename
	if errStat == nil {
		_ = e.sftpClient.Chmod(tmpRemote, localStat.Mode())
		_ = e.sftpClient.Chtimes(tmpRemote, time.Now(), localStat.ModTime())
	}

	if err := e.sftpClient.PosixRename(tmpRemote, remotePath); err != nil {
		// Fallback para servidores SFTP sem posix-rename: remove + rename
		_ = e.sftpClient.Remove(remotePath)
		if err2 := e.sftpClient.Rename(tmpRemote, remotePath); err2 != nil {
			_ = e.sftpClient.Remove(tmpRemote)
			return err2
		}
	}

	return nil
}

// shouldRewriteGitPath retorna se o arquivo relativo necessita de tradução de caminho absoluto do Git
func (e *Engine) shouldRewriteGitPath(relPath string) bool {
	path := filepath.ToSlash(relPath)

	// Caso 1: arquivo de texto ".git" de uma worktree (geralmente tem prefixo gitdir: )
	if filepath.Base(path) == ".git" {
		return true
	}

	// Caso 2: arquivo "gitdir" em metadados de worktrees (.git/worktrees/<name>/gitdir)
	parts := strings.Split(path, "/")
	n := len(parts)
	if n >= 4 && parts[n-1] == "gitdir" && parts[n-3] == "worktrees" && parts[n-4] == ".git" {
		return true
	}
	return false
}

// rewriteGitContent traduz as referências de caminhos absolutos do conteúdo do arquivo Git
func (e *Engine) rewriteGitContent(content []byte, direction string) []byte {
	str := string(content)
	localBase := filepath.ToSlash(e.LocalDir)
	remoteBase := filepath.ToSlash(e.RemoteDir)

	if direction == "upload" {
		str = strings.ReplaceAll(str, localBase, remoteBase)
	} else {
		str = strings.ReplaceAll(str, remoteBase, localBase)
	}
	return []byte(str)
}

func (e *Engine) loadLastState() error {
	data, err := os.ReadFile(e.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Primeiro sync: começa com histórico vazio
		}
		// Nunca prossegue silenciosamente com histórico vazio se o arquivo existe:
		// isso causaria uma tempestade de conflitos na reconciliação de 3 vias
		return fmt.Errorf("falha ao ler estado do sync em %s (delete o arquivo para re-baseline): %w", e.StatePath, err)
	}

	var state Snapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("estado do sync corrompido em %s (delete o arquivo para re-baseline): %w", e.StatePath, err)
	}

	// Trunca os timestamps carregados do estado anterior para garantir
	// consistência em segundos, evitando falsos positivos com dados legados
	for k, entry := range state {
		entry.ModTime = entry.ModTime.UTC().Truncate(time.Second)
		state[k] = entry
	}
	e.lastState = state
	return nil
}

func (e *Engine) saveLastState() error {
	data, err := json.MarshalIndent(e.lastState, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(e.StatePath, data, 0600)
}

// IgnoreMatcher retorna o matcher de ignores associado a esta engine
func (e *Engine) IgnoreMatcher() *IgnoreMatcher {
	return e.matcher
}
