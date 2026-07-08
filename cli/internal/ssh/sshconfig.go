package ssh

import (
	"fmt"
	"os"
	"path/filepath"

	sshconfig "github.com/kevinburke/ssh_config"
)

// SSHConfigEntry representa uma entrada lida de ~/.ssh/config (read-only)
type SSHConfigEntry struct {
	Host     string
	HostName string
	Port     string
	User     string
	KeyFile  string
}

// ReadSSHConfig lê uma entrada de ~/.ssh/config pelo nome do Host
// Nunca modifica o arquivo — apenas leitura.
func ReadSSHConfig(hostAlias string) (*SSHConfigEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("não foi possível determinar home dir: %w", err)
	}

	configPath := filepath.Join(home, ".ssh", "config")
	f, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("~/.ssh/config não encontrado")
		}
		return nil, fmt.Errorf("erro ao abrir ~/.ssh/config: %w", err)
	}
	defer f.Close()

	cfg, err := sshconfig.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("erro ao parsear ~/.ssh/config: %w", err)
	}

	// Extrai valores do host
	entry := &SSHConfigEntry{
		Host: hostAlias,
	}

	entry.HostName, _ = cfg.Get(hostAlias, "HostName")
	entry.Port, _ = cfg.Get(hostAlias, "Port")
	entry.User, _ = cfg.Get(hostAlias, "User")
	entry.KeyFile, _ = cfg.Get(hostAlias, "IdentityFile")

	// Se HostName está vazio, o alias provavelmente não existe
	if entry.HostName == "" {
		return nil, fmt.Errorf("host '%s' não encontrado em ~/.ssh/config", hostAlias)
	}

	return entry, nil
}

// ListSSHConfigHosts lista todos os hosts definidos em ~/.ssh/config
func ListSSHConfigHosts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(home, ".ssh", "config")
	f, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg, err := sshconfig.Decode(f)
	if err != nil {
		return nil, err
	}

	var hosts []string
	for _, host := range cfg.Hosts {
		for _, pattern := range host.Patterns {
			name := pattern.String()
			if name != "*" && name != "" {
				hosts = append(hosts, name)
			}
		}
	}

	return hosts, nil
}
