package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mitchellh/go-homedir"
	"gopkg.in/yaml.v3"
)

// Config é a estrutura raiz do arquivo ~/.unlarp.yaml
type Config struct {
	DefaultHost string          `yaml:"default_host"`
	Hosts       map[string]Host `yaml:"hosts"`
	Sync        SyncConfig      `yaml:"sync"`
	Tunnel      TunnelConfig    `yaml:"tunnel"`
	Session     SessionConfig   `yaml:"session"`
	SSH         SSHGlobalConfig `yaml:"ssh"`
}

// Host representa um workspace remoto configurado
type Host struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	User      string `yaml:"user"`
	Key       string `yaml:"key"`
	Workspace string `yaml:"workspace"`
	Container string `yaml:"container,omitempty"` // Para operações Docker diretas (local only)
}

// SyncConfig contém as configurações globais de sincronização
type SyncConfig struct {
	PollInterval     string   `yaml:"poll_interval"`
	ConflictStrategy string   `yaml:"conflict_strategy"`
	InitialSync      string   `yaml:"initial_sync"`
	IgnorePatterns   []string `yaml:"ignore_patterns"`
}

// TunnelConfig contém as configurações de túneis SSH
type TunnelConfig struct {
	AutoReconnect  bool   `yaml:"auto_reconnect"`
	ReconnectDelay string `yaml:"reconnect_delay"`
}

// SessionConfig contém as configurações de sessões
type SessionConfig struct {
	LazyDisconnect string `yaml:"lazy_disconnect"`
	PersistState   bool   `yaml:"persist_state"`
}

// SSHGlobalConfig contém configurações globais de SSH
type SSHGlobalConfig struct {
	ReadSSHConfig      bool `yaml:"read_ssh_config"`
	StrictHostChecking bool `yaml:"strict_host_checking"`
}

// PollIntervalDuration retorna o poll interval como time.Duration
func (s *SyncConfig) PollIntervalDuration() time.Duration {
	d, err := time.ParseDuration(s.PollInterval)
	if err != nil {
		return 1 * time.Second
	}
	return d
}

// ReconnectDelayDuration retorna o reconnect delay como time.Duration
func (t *TunnelConfig) ReconnectDelayDuration() time.Duration {
	d, err := time.ParseDuration(t.ReconnectDelay)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

// Validate verifica se o Host tem os campos obrigatórios
func (h *Host) Validate() error {
	if h.Host == "" {
		return fmt.Errorf("host é obrigatório")
	}
	if h.Port == 0 {
		return fmt.Errorf("port é obrigatório")
	}
	if h.User == "" {
		return fmt.Errorf("user é obrigatório")
	}
	if h.Workspace == "" {
		return fmt.Errorf("workspace é obrigatório")
	}
	return nil
}

// ExpandedKey retorna o caminho da chave SSH com ~ expandido
func (h *Host) ExpandedKey() (string, error) {
	if h.Key == "" {
		return "", nil
	}
	return homedir.Expand(h.Key)
}

// Address retorna host:port formatado
func (h *Host) Address() string {
	return fmt.Sprintf("%s:%d", h.Host, h.Port)
}

// Store gerencia leitura/escrita do arquivo de configuração
type Store struct {
	path string
}

// NewStore cria um novo Store usando o caminho padrão (~/.unlarp.yaml)
func NewStore() *Store {
	home, _ := os.UserHomeDir()
	return &Store{
		path: filepath.Join(home, ".unlarp.yaml"),
	}
}

// NewStoreWithPath cria um Store com caminho customizado (para testes)
func NewStoreWithPath(path string) *Store {
	return &Store{path: path}
}

// Path retorna o caminho do arquivo de configuração
func (s *Store) Path() string {
	return s.path
}

// Load carrega a configuração do arquivo YAML
func (s *Store) Load() (*Config, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.defaultConfig(), nil
		}
		return nil, fmt.Errorf("erro ao ler %s: %w", s.path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("erro ao parsear %s: %w", s.path, err)
	}

	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]Host)
	}

	return &cfg, nil
}

// Save salva a configuração no arquivo YAML
func (s *Store) Save(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("erro ao serializar config: %w", err)
	}

	// Garante que o diretório pai existe
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("erro ao criar diretório %s: %w", dir, err)
	}

	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("erro ao salvar %s: %w", s.path, err)
	}

	return nil
}

// AddHost adiciona um host à configuração e salva
func (s *Store) AddHost(name string, host Host) error {
	cfg, err := s.Load()
	if err != nil {
		return err
	}

	if _, exists := cfg.Hosts[name]; exists {
		return fmt.Errorf("host '%s' já existe. Use 'unlarp config edit %s' para editar", name, name)
	}

	cfg.Hosts[name] = host

	// Se é o primeiro host, define como default
	if len(cfg.Hosts) == 1 {
		cfg.DefaultHost = name
	}

	return s.Save(cfg)
}

// RemoveHost remove um host da configuração e salva
func (s *Store) RemoveHost(name string) error {
	cfg, err := s.Load()
	if err != nil {
		return err
	}

	if _, exists := cfg.Hosts[name]; !exists {
		return fmt.Errorf("host '%s' não encontrado", name)
	}

	delete(cfg.Hosts, name)

	// Se removeu o default, limpa ou define outro
	if cfg.DefaultHost == name {
		cfg.DefaultHost = ""
		for n := range cfg.Hosts {
			cfg.DefaultHost = n
			break
		}
	}

	return s.Save(cfg)
}

// UpdateHost atualiza um host existente na configuração e salva
func (s *Store) UpdateHost(name string, host Host) error {
	cfg, err := s.Load()
	if err != nil {
		return err
	}

	if _, exists := cfg.Hosts[name]; !exists {
		return fmt.Errorf("host '%s' não encontrado", name)
	}

	cfg.Hosts[name] = host
	return s.Save(cfg)
}

// SetDefault define o host default
func (s *Store) SetDefault(name string) error {
	cfg, err := s.Load()
	if err != nil {
		return err
	}

	if _, exists := cfg.Hosts[name]; !exists {
		return fmt.Errorf("host '%s' não encontrado", name)
	}

	cfg.DefaultHost = name
	return s.Save(cfg)
}

// defaultConfig retorna uma configuração padrão vazia
func (s *Store) defaultConfig() *Config {
	return &Config{
		Hosts: make(map[string]Host),
		Sync: SyncConfig{
			PollInterval:     "1s",
			ConflictStrategy: "newest-wins",
			InitialSync:      "full",
			IgnorePatterns: []string{
				".git/",
				"node_modules/",
				".nix-*",
				"*.swp",
				".DS_Store",
				"__pycache__/",
				".venv/",
				"target/",
				"dist/",
			},
		},
		Tunnel: TunnelConfig{
			AutoReconnect:  true,
			ReconnectDelay: "5s",
		},
		Session: SessionConfig{
			LazyDisconnect: "30m",
			PersistState:   true,
		},
		SSH: SSHGlobalConfig{
			ReadSSHConfig:      true,
			StrictHostChecking: false,
		},
	}
}
