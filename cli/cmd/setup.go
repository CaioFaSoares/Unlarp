package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var setupKey string

var setupCmd = &cobra.Command{
	Use:   "setup [name]",
	Short: "Configurar chave SSH no host remoto",
	Long: `Detecta sua chave pública SSH local, conecta ao host e instala a chave
em ~/.ssh/authorized_keys no workspace remoto. Substitui os scripts setup-local-ssh.sh
e setup-remote-ssh.sh.

Exemplos:
  unlarp setup local                           # Setup automático
  unlarp setup coolify-prod                    # Setup para host remoto  
  unlarp setup coolify-prod --key ~/.ssh/id_ed25519.pub  # Chave específica`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostName := ""
		if len(args) > 0 {
			hostName = args[0]
		}

		hostCfg, err := getHostConfig(hostName)
		if err != nil {
			return err
		}

		displayName := hostName
		if displayName == "" {
			displayName = getActiveHost()
		}

		// Detecta chave pública
		pubKeyPath, err := findPublicKey(setupKey)
		if err != nil {
			return err
		}

		pubKey, err := os.ReadFile(pubKeyPath)
		if err != nil {
			return fmt.Errorf("erro ao ler chave pública %s: %w", pubKeyPath, err)
		}

		pubKeyStr := strings.TrimSpace(string(pubKey))
		ui.Info("Usando chave pública: %s", pubKeyPath)

		// Conecta ao host
		spin := ui.NewSpinner("Conectando a " + displayName + "...")
		spin.Start()

		client, err := internalssh.NewClient(hostCfg)
		if err != nil {
			spin.StopWithError("Falha ao configurar conexão")
			return err
		}

		if err := client.Connect(); err != nil {
			spin.StopWithError("Falha ao conectar")
			return err
		}
		defer client.Close()
		spin.StopWithSuccess("Conectado")

		// Instala chave SSH
		spin2 := ui.NewSpinner("Instalando chave SSH...")
		spin2.Start()

		installCmd := fmt.Sprintf(
			`mkdir -p ~/.ssh && chmod 700 ~/.ssh && `+
			`grep -qxF '%s' ~/.ssh/authorized_keys 2>/dev/null || echo '%s' >> ~/.ssh/authorized_keys && `+
			`chmod 600 ~/.ssh/authorized_keys && chown -R $(whoami):$(id -gn) ~/.ssh`,
			pubKeyStr, pubKeyStr,
		)

		_, stderr, err := client.RunCommand(installCmd)
		if err != nil {
			spin2.StopWithError("Falha ao instalar chave: " + stderr)
			return fmt.Errorf("erro ao instalar chave SSH: %w", err)
		}
		spin2.StopWithSuccess("Chave SSH instalada")

		// Testa reconexão
		spin3 := ui.NewSpinner("Testando reconexão sem senha...")
		spin3.Start()

		testClient, err := internalssh.NewClient(hostCfg)
		if err != nil {
			spin3.StopWithError("Falha ao configurar teste")
			return nil // Não é fatal
		}

		if err := testClient.Connect(); err != nil {
			spin3.StopWithError("Reconexão falhou — verifique a chave")
		} else {
			testClient.Close()
			spin3.StopWithSuccess("Reconexão sem senha OK")
		}

		fmt.Println()
		ui.Success("Setup completo para '%s'", displayName)
		ui.Dim("Você pode conectar com: unlarp connect %s", displayName)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(setupCmd)

	setupCmd.Flags().StringVar(&setupKey, "key", "", "caminho da chave pública SSH (default: auto-detecta)")
}

// findPublicKey encontra a chave pública SSH a ser usada
func findPublicKey(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("chave não encontrada: %s", explicit)
		}
		return explicit, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Ordem de prioridade: ed25519 > ecdsa > rsa
	candidates := []string{
		filepath.Join(home, ".ssh", "id_ed25519.pub"),
		filepath.Join(home, ".ssh", "id_ecdsa.pub"),
		filepath.Join(home, ".ssh", "id_rsa.pub"),
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("nenhuma chave pública SSH encontrada em ~/.ssh/. Gere uma com: ssh-keygen -t ed25519")
}
