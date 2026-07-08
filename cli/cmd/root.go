package cmd

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/CaioFaSoares/unlarp/internal/config"
)

var (
	cfgFile string
	verbose bool
	hostOverride string
)

var rootCmd = &cobra.Command{
	Use:   "unlarp",
	Short: "Gerenciamento de workspaces remotos via SSH",
	Long: `Unlarp é uma ferramenta CLI para configurar, conectar e interagir
com workspaces de desenvolvimento remotos. Suporta sincronização bidirecional
de arquivos, túneis SSH, gestão de múltiplas sessões e uma TUI interativa.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute é chamado pelo main.go
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		color.Red("Erro: %v", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "arquivo de configuração (default: ~/.unlarp.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "output detalhado")
	rootCmd.PersistentFlags().StringVar(&hostOverride, "host", "", "override do host ativo para este comando")

	// Bind flags ao Viper
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".unlarp")
	}

	// Variáveis de ambiente com prefixo UNLARP_
	viper.SetEnvPrefix("UNLARP")
	viper.AutomaticEnv()

	// Tenta ler config, mas não falha se não existir (será criada com `config add`)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			if verbose {
				fmt.Fprintln(os.Stderr, "Aviso: erro ao ler config:", err)
			}
		}
	}
}

// getActiveHost retorna o nome do host ativo, considerando override por flag
func getActiveHost() string {
	if hostOverride != "" {
		return hostOverride
	}

	store := config.NewStore()
	cfg, err := store.Load()
	if err != nil {
		return ""
	}
	return cfg.DefaultHost
}

// getHostConfig retorna a configuração do host ativo ou do host especificado
func getHostConfig(name string) (*config.Host, error) {
	if name == "" {
		name = getActiveHost()
	}
	if name == "" {
		return nil, fmt.Errorf("nenhum host configurado. Use 'unlarp config add' para adicionar um host")
	}

	store := config.NewStore()
	cfg, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("erro ao carregar config: %w", err)
	}

	host, ok := cfg.Hosts[name]
	if !ok {
		return nil, fmt.Errorf("host '%s' não encontrado. Use 'unlarp config list' para ver hosts disponíveis", name)
	}

	return &host, nil
}
