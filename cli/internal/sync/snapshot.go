package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// FileEntry armazena os metadados de um único arquivo ou diretório
type FileEntry struct {
	RelPath string      `json:"rel_path"`
	Size    int64       `json:"size"`
	ModTime time.Time   `json:"mod_time"`
	Mode    os.FileMode `json:"mode"`
	IsDir   bool        `json:"is_dir"`
	Hash    string      `json:"hash,omitempty"`
}

// Snapshot representa o estado completo de um diretório em um momento
type Snapshot map[string]FileEntry

// CreateLocalSnapshot gera um snapshot de um diretório local
func CreateLocalSnapshot(rootDir string, matcher *IgnoreMatcher) (Snapshot, error) {
	snapshot := make(Snapshot)
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Não processa a própria raiz
		if path == absRoot {
			return nil
		}

		// Calcula caminho relativo usando separador "/" padrão do Unix
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		// Verifica ignore rules
		if matcher != nil && matcher.Matches(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir // Evita descer em diretórios ignorados
			}
			return nil
		}

		if d.IsDir() {
			return nil // Não inclui diretórios no mapa de snapshot, apenas arquivos/symlinks
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		entry := FileEntry{
			RelPath: relPath,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Truncate(time.Second),
			Mode:    info.Mode(),
			IsDir:   info.IsDir(),
		}

		snapshot[relPath] = entry
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("erro ao gerar snapshot local: %w", err)
	}

	return snapshot, nil
}

// CreateRemoteSnapshot gera um snapshot de um diretório remoto via SFTP
func CreateRemoteSnapshot(client *sftp.Client, rootDir string, matcher *IgnoreMatcher) (Snapshot, error) {
	snapshot := make(Snapshot)

	// Garante que o caminho remoto é absoluto ou normalizado
	cleanRoot := filepath.ToSlash(filepath.Clean(rootDir))

	w := client.Walk(cleanRoot)
	for w.Step() {
		if w.Err() != nil {
			return nil, fmt.Errorf("erro ao varrer diretório remoto %s: %w", w.Path(), w.Err())
		}

		path := filepath.ToSlash(w.Path())
		if path == cleanRoot {
			continue
		}

		// Determina o caminho relativo
		relPath := strings.TrimPrefix(path, cleanRoot)
		relPath = strings.TrimPrefix(relPath, "/")

		if relPath == "" {
			continue
		}

		stat := w.Stat()
		isDir := stat.IsDir()

		// Verifica ignore rules
		if matcher != nil && matcher.Matches(relPath, isDir) {
			if isDir {
				w.SkipDir() // Evita descer em diretórios ignorados
			}
			continue
		}

		if isDir {
			continue // Não inclui diretórios no mapa de snapshot, apenas arquivos/symlinks
		}

		entry := FileEntry{
			RelPath: relPath,
			Size:    stat.Size(),
			ModTime: stat.ModTime().UTC().Truncate(time.Second),
			Mode:    stat.Mode(),
			IsDir:   isDir,
		}

		snapshot[relPath] = entry
	}

	return snapshot, nil
}

// CalculateLocalHash calcula o hash SHA256 de um arquivo local
func CalculateLocalHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// CalculateRemoteHash calcula o hash SHA256 de um arquivo remoto
func CalculateRemoteHash(client *sftp.Client, filePath string) (string, error) {
	f, err := client.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
