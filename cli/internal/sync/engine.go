package sync

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// Engine coordena a sincronização bidirecional
type Engine struct {
	ID               string           `json:"id"`
	LocalDir         string           `json:"local_dir"`
	RemoteDir        string           `json:"remote_dir"`
	HostName         string           `json:"host_name"`
	ConflictStrategy ConflictStrategy `json:"conflict_strategy"`
	StatePath        string           `json:"state_path"`

	sshClient  *internalssh.Client
	sftpClient *sftp.Client
	matcher    *IgnoreMatcher
	mu         sync.Mutex

	// Último estado conhecido compartilhado (A na reconciliação de 3 vias)
	lastState Snapshot
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
		lastState:        make(Snapshot),
	}

	e.loadLastState()

	return e, nil
}

// SyncExec executa um ciclo completo de sincronização de três vias
func (e *Engine) SyncExec() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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

	uploads := make([]string, 0)
	downloads := make([]string, 0)
	localDeletes := make([]string, 0)
	remoteDeletes := make([]string, 0)

	// Analisa mudanças locais e novos arquivos
	for path, lEntry := range L {
		rEntry, inR := R[path]
		aEntry, inA := A[path]

		if !inR && !inA {
			// Novo localmente, não existe remoto nem no histórico
			if !lEntry.IsDir {
				uploads = append(uploads, path)
			}
		} else if !inR && inA {
			// Excluído no remoto (estava no histórico mas sumiu do remoto)
			if lEntry.ModTime.After(aEntry.ModTime) {
				// Foi modificado localmente após a exclusão remota (conflito: recria remoto)
				uploads = append(uploads, path)
			} else {
				// Exclusão normal remota -> deleta local
				localDeletes = append(localDeletes, path)
			}
		} else if inR && !inA {
			// Existe local e remoto, mas não no histórico (novo em ambos)
			if lEntry.Size != rEntry.Size || !lEntry.ModTime.Equal(rEntry.ModTime) {
				// Conflito
				if ResolveConflict(lEntry, rEntry, e.ConflictStrategy) {
					uploads = append(uploads, path)
				} else {
					downloads = append(downloads, path)
				}
			}
		} else if inR && inA {
			// Existe em todos
			lChanged := lEntry.Size != aEntry.Size || !lEntry.ModTime.Equal(aEntry.ModTime)
			rChanged := rEntry.Size != aEntry.Size || !rEntry.ModTime.Equal(aEntry.ModTime)

			if lChanged && !rChanged {
				uploads = append(uploads, path)
			} else if rChanged && !lChanged {
				downloads = append(downloads, path)
			} else if lChanged && rChanged {
				// Conflito: modificado em ambos
				if ResolveConflict(lEntry, rEntry, e.ConflictStrategy) {
					uploads = append(uploads, path)
				} else {
					downloads = append(downloads, path)
				}
			}
		}
	}

	// Analisa mudanças remotas, exclusões locais e novos arquivos remotos
	for path, rEntry := range R {
		_, inL := L[path]
		aEntry, inA := A[path]

		if !inL && !inA {
			// Novo remoto
			if !rEntry.IsDir {
				downloads = append(downloads, path)
			}
		} else if !inL && inA {
			// Excluído localmente
			if rEntry.ModTime.After(aEntry.ModTime) {
				// Modificado remoto após exclusão local -> recria local
				downloads = append(downloads, path)
			} else {
				// Exclusão normal local -> deleta remoto
				remoteDeletes = append(remoteDeletes, path)
			}
		}
	}

	// Analisa deletados em ambos
	for path := range A {
		_, inL := L[path]
		_, inR := R[path]
		if !inL && !inR {
			// Deletado nos dois lados, nada a fazer além de limpar o histórico
		}
	}

	totalActions := len(uploads) + len(downloads) + len(localDeletes) + len(remoteDeletes)
	if totalActions == 0 {
		return 0, nil
	}

	// Executa ações

	// 1. Deleta localmente
	for _, path := range localDeletes {
		localPath := filepath.Join(e.LocalDir, path)
		_ = os.RemoveAll(localPath)
	}

	// 2. Deleta remotamente
	for _, path := range remoteDeletes {
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))
		_ = e.sftpClient.Remove(remotePath)
	}

	// 3. Executa Downloads (Remoto -> Local)
	for _, path := range downloads {
		localPath := filepath.Join(e.LocalDir, path)
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))

		// Garante diretório pai local
		_ = os.MkdirAll(filepath.Dir(localPath), 0755)

		err := e.downloadFile(remotePath, localPath)
		if err != nil {
			return 0, fmt.Errorf("falha ao baixar %s: %w", path, err)
		}
	}

	// 4. Executa Uploads (Local -> Remoto)
	for _, path := range uploads {
		localPath := filepath.Join(e.LocalDir, path)
		remotePath := filepath.ToSlash(filepath.Join(e.RemoteDir, path))

		// Garante diretório pai remoto
		remoteDir := filepath.ToSlash(filepath.Dir(remotePath))
		_ = e.sftpClient.MkdirAll(remoteDir)

		err := e.uploadFile(localPath, remotePath)
		if err != nil {
			return 0, fmt.Errorf("falha ao enviar %s: %w", path, err)
		}
	}

	// 5. Atualiza o histórico de estado salvo para o próximo ciclo
	// Gera um novo snapshot local atualizado para salvar
	newA, err := CreateLocalSnapshot(e.LocalDir, e.matcher)
	if err == nil {
		e.lastState = newA
		e.saveLastState()
	}

	return totalActions, nil
}

func (e *Engine) downloadFile(remotePath, localPath string) error {
	remoteFile, err := e.sftpClient.Open(remotePath)
	if err != nil {
		return err
	}
	defer remoteFile.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer localFile.Close()

	_, err = io.Copy(localFile, remoteFile)
	if err != nil {
		return err
	}

	// Preserva ModTime e permissões
	stat, err := remoteFile.Stat()
	if err == nil {
		_ = os.Chmod(localPath, stat.Mode())
		_ = os.Chtimes(localPath, time.Now(), stat.ModTime())
	}

	return nil
}

func (e *Engine) uploadFile(localPath, remotePath string) error {
	localFile, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer localFile.Close()

	remoteFile, err := e.sftpClient.Create(remotePath)
	if err != nil {
		return err
	}
	defer remoteFile.Close()

	_, err = io.Copy(remoteFile, localFile)
	if err != nil {
		return err
	}

	// Preserva ModTime e permissões
	stat, err := localFile.Stat()
	if err == nil {
		_ = e.sftpClient.Chmod(remotePath, stat.Mode())
		_ = e.sftpClient.Chtimes(remotePath, time.Now(), stat.ModTime())
	}

	return nil
}

func (e *Engine) loadLastState() {
	data, err := os.ReadFile(e.StatePath)
	if err != nil {
		return
	}

	var state Snapshot
	if err := json.Unmarshal(data, &state); err == nil {
		e.lastState = state
	}
}

func (e *Engine) saveLastState() {
	data, err := json.MarshalIndent(e.lastState, "", "  ")
	if err != nil {
		return
	}

	_ = os.WriteFile(e.StatePath, data, 0600)
}

// IgnoreMatcher retorna o matcher de ignores associado a esta engine
func (e *Engine) IgnoreMatcher() *IgnoreMatcher {
	return e.matcher
}
