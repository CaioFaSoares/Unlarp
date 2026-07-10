package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic escreve data em path via temp file + rename no mesmo diretório,
// garantindo que o arquivo final nunca fique truncado por um crash no meio da escrita.
// ponytail: sem fsync antes do rename — suficiente para APFS/ext4; adicionar tmp.Sync() se aparecer corrupção pós-queda de energia.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op após rename bem-sucedido

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
