package ssh

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
)

// SFTPClient encapsula o cliente SFTP para transferência de arquivos
type SFTPClient struct {
	client *sftp.Client
}

// NewSFTPClient cria um novo cliente SFTP a partir de uma conexão SSH existente
func NewSFTPClient(sshClient *Client) (*SFTPClient, error) {
	if sshClient.conn == nil {
		return nil, fmt.Errorf("conexão SSH não estabelecida")
	}

	client, err := sftp.NewClient(sshClient.conn)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar cliente SFTP: %w", err)
	}

	return &SFTPClient{client: client}, nil
}

// Close fecha o cliente SFTP
func (s *SFTPClient) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// Inner retorna o cliente SFTP subjacente
func (s *SFTPClient) Inner() *sftp.Client {
	return s.client
}

// Upload envia um arquivo local para o host remoto
func (s *SFTPClient) Upload(localPath, remotePath string) error {
	// Abre arquivo local
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("erro ao abrir arquivo local %s: %w", localPath, err)
	}
	defer localFile.Close()

	// Garante que o diretório remoto existe
	remoteDir := filepath.Dir(remotePath)
	if err := s.client.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("erro ao criar diretório remoto %s: %w", remoteDir, err)
	}

	// Cria arquivo remoto
	remoteFile, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo remoto %s: %w", remotePath, err)
	}
	defer remoteFile.Close()

	// Copia conteúdo
	if _, err := io.Copy(remoteFile, localFile); err != nil {
		return fmt.Errorf("erro ao copiar para %s: %w", remotePath, err)
	}

	// Preserva permissões
	localInfo, err := os.Stat(localPath)
	if err == nil {
		s.client.Chmod(remotePath, localInfo.Mode())
	}

	return nil
}

// Download baixa um arquivo do host remoto para o local
func (s *SFTPClient) Download(remotePath, localPath string) error {
	// Abre arquivo remoto
	remoteFile, err := s.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("erro ao abrir arquivo remoto %s: %w", remotePath, err)
	}
	defer remoteFile.Close()

	// Garante que o diretório local existe
	localDir := filepath.Dir(localPath)
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return fmt.Errorf("erro ao criar diretório local %s: %w", localDir, err)
	}

	// Cria arquivo local
	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo local %s: %w", localPath, err)
	}
	defer localFile.Close()

	// Copia conteúdo
	if _, err := io.Copy(localFile, remoteFile); err != nil {
		return fmt.Errorf("erro ao copiar de %s: %w", remotePath, err)
	}

	// Preserva permissões
	remoteInfo, err := s.client.Stat(remotePath)
	if err == nil {
		os.Chmod(localPath, remoteInfo.Mode())
	}

	return nil
}

// Delete remove um arquivo no host remoto
func (s *SFTPClient) Delete(remotePath string) error {
	return s.client.Remove(remotePath)
}

// MkdirAll cria diretórios recursivamente no host remoto
func (s *SFTPClient) MkdirAll(remotePath string) error {
	return s.client.MkdirAll(remotePath)
}

// RemoveDir remove um diretório no host remoto (deve estar vazio)
func (s *SFTPClient) RemoveDir(remotePath string) error {
	return s.client.RemoveDirectory(remotePath)
}

// Stat retorna informações sobre um arquivo/diretório remoto
func (s *SFTPClient) Stat(remotePath string) (os.FileInfo, error) {
	return s.client.Stat(remotePath)
}

// ReadDir lista o conteúdo de um diretório remoto
func (s *SFTPClient) ReadDir(remotePath string) ([]os.FileInfo, error) {
	return s.client.ReadDir(remotePath)
}

// Exists verifica se um arquivo/diretório remoto existe
func (s *SFTPClient) Exists(remotePath string) bool {
	_, err := s.client.Stat(remotePath)
	return err == nil
}

// ReadFile lê o conteúdo de um arquivo remoto inteiro
func (s *SFTPClient) ReadFile(remotePath string) ([]byte, error) {
	file, err := s.client.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}

// WriteFile escreve conteúdo em um arquivo remoto
func (s *SFTPClient) WriteFile(remotePath string, data []byte, perm os.FileMode) error {
	// Garante diretório pai
	dir := filepath.Dir(remotePath)
	if err := s.client.MkdirAll(dir); err != nil {
		return fmt.Errorf("erro ao criar diretório %s: %w", dir, err)
	}

	file, err := s.client.Create(remotePath)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return err
	}

	return s.client.Chmod(remotePath, perm)
}
