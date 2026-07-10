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
	RelPath       string      `json:"rel_path"`
	Size          int64       `json:"size"`
	ModTime       time.Time   `json:"mod_time"`
	Mode          os.FileMode `json:"mode"`
	IsDir         bool        `json:"is_dir"`
	Hash          string      `json:"hash,omitempty"`
	SymlinkTarget string      `json:"symlink_target,omitempty"`
}

// Changed verifica se os metadados diferem significativamente dos de outra FileEntry
func (fe FileEntry) Changed(other FileEntry) bool {
	selfIsSym := fe.Mode&os.ModeSymlink != 0
	otherIsSym := other.Mode&os.ModeSymlink != 0

	if selfIsSym != otherIsSym {
		return true
	}
	if selfIsSym {
		return fe.SymlinkTarget != other.SymlinkTarget
	}
	return fe.Size != other.Size || !fe.ModTime.Equal(other.ModTime)
}

// Snapshot representa o estado completo de um diretório em um momento
type Snapshot map[string]FileEntry

// normalizeSymlinkTarget normaliza o destino do link simbólico para que seja comparável entre
// ambientes locais e remotos, substituindo referências à raiz do sync por uma tag genérica "[root]".
func normalizeSymlinkTarget(target string, rootDir string) string {
	targetSlash := filepath.ToSlash(target)
	rootSlash := filepath.ToSlash(rootDir)

	if targetSlash == rootSlash {
		return "[root]"
	}

	prefix := rootSlash
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if strings.HasPrefix(targetSlash, prefix) {
		rel := strings.TrimPrefix(targetSlash, prefix)
		return "[root]/" + rel
	}

	return targetSlash
}

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

		var symlinkTarget string
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			symlinkTarget = normalizeSymlinkTarget(target, absRoot)
		}

		entry := FileEntry{
			RelPath:       relPath,
			Size:          info.Size(),
			ModTime:       info.ModTime().UTC().Truncate(time.Second),
			Mode:          info.Mode(),
			IsDir:         info.IsDir(),
			SymlinkTarget: symlinkTarget,
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
	if client == nil {
		return nil, fmt.Errorf("sftp client is nil")
	}

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

		var symlinkTarget string
		if stat.Mode()&os.ModeSymlink != 0 {
			target, err := client.ReadLink(path)
			if err != nil {
				return nil, fmt.Errorf("erro ao ler link remoto %s: %w", path, err)
			}
			symlinkTarget = normalizeSymlinkTarget(target, cleanRoot)
		}

		entry := FileEntry{
			RelPath:       relPath,
			Size:          stat.Size(),
			ModTime:       stat.ModTime().UTC().Truncate(time.Second),
			Mode:          stat.Mode(),
			IsDir:         isDir,
			SymlinkTarget: symlinkTarget,
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
