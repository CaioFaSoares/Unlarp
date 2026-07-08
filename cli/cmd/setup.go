package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	"github.com/CaioFaSoares/unlarp/internal/ui"
)

var (
	setupKey string
	vpsUser  string
	vpsKey   string
	vpsPort  int
)

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

		// Tenta chave específica do host ou as chaves padrões
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

		// Tenta conexão direta primeiro
		spin := ui.NewSpinner("Conectando a " + displayName + "...")
		spin.Start()

		client, err := internalssh.NewClient(hostCfg)
		if err != nil {
			spin.StopWithError("Falha ao configurar conexão")
			return err
		}

		directSuccess := false
		if err = client.Connect(); err == nil {
			defer client.Close()
			spin.StopWithSuccess("Conectado diretamente")
			directSuccess = true

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
		}

		if !directSuccess {
			spin.Stop()

			// Se a conexão direta falhou e for localhost/127.0.0.1, tenta rodar docker exec local
			if hostCfg.Host == "localhost" || hostCfg.Host == "127.0.0.1" {
				spinLocal := ui.NewSpinner("Tentando bootstrap local via Docker exec...")
				spinLocal.Start()

				cmd := exec.Command("docker", "exec", "-i", "workspace_machine", "sh", "-c",
					"mkdir -p /root/.ssh && cat >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root")
				cmd.Stdin = strings.NewReader(pubKeyStr + "\n")
				if err := cmd.Run(); err != nil {
					cmdLabel := exec.Command("sh", "-c", "docker exec -i $(docker ps -qf label=com.docker.compose.service=workspace) sh -c 'mkdir -p /root/.ssh && cat >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root'")
					cmdLabel.Stdin = strings.NewReader(pubKeyStr + "\n")
					err = cmdLabel.Run()
				}

				if err != nil {
					spinLocal.StopWithError("Falha no bootstrap local: " + err.Error())
					return err
				}
				spinLocal.StopWithSuccess("Chave injetada localmente via Docker")
			} else {
				// Tenta bootstrap via VPS
				targetVpsUser := vpsUser
				if targetVpsUser == "" {
					targetVpsUser = hostCfg.User
				}
				if targetVpsUser == "" {
					targetVpsUser = "root"
				}

				targetVpsKey := vpsKey
				if targetVpsKey == "" {
					targetVpsKey = hostCfg.Key
				}

				targetVpsPort := vpsPort

				spinVPS := ui.NewSpinner(fmt.Sprintf("Conexão direta falhou. Tentando bootstrap via host VPS (%s@%s:%d)...", targetVpsUser, hostCfg.Host, targetVpsPort))
				spinVPS.Start()

				vpsCfg := *hostCfg
				vpsCfg.Port = targetVpsPort
				vpsCfg.User = targetVpsUser
				vpsCfg.Key = targetVpsKey

				vpsClient, err := internalssh.NewClient(&vpsCfg)
				if err != nil {
					spinVPS.StopWithError("Falha ao configurar conexão com o host VPS")
					return err
				}

				// Tenta conectar por chave primeiro
				if err = vpsClient.Connect(); err != nil {
					spinVPS.Stop()

					// Se falhar, solicita a senha do host VPS interativamente
					ui.Info("Autenticação SSH por chave falhou para o host VPS (%v).", err)
					fmt.Print("Digite a senha SSH do host VPS (deixe em branco para cancelar): ")
					bytePassword, readErr := term.ReadPassword(int(syscall.Stdin))
					fmt.Println()
					if readErr != nil || len(bytePassword) == 0 {
						return fmt.Errorf("conexão direta falhou e bootstrap no VPS foi cancelado ou falhou: %w", err)
					}

					vpsCfg.Password = strings.TrimSpace(string(bytePassword))

					spinVPS2 := ui.NewSpinner("Tentando conexão com a senha fornecida...")
					spinVPS2.Start()

					vpsClient2, err2 := internalssh.NewClient(&vpsCfg)
					if err2 != nil {
						spinVPS2.StopWithError("Falha ao configurar conexão por senha")
						return err2
					}

					if err = vpsClient2.Connect(); err != nil {
						spinVPS2.StopWithError("Senha incorreta ou falha de autenticação")
						return fmt.Errorf("conexão direta falhou e autenticação na VPS falhou: %w", err)
					}
					vpsClient = vpsClient2
					spinVPS2.StopWithSuccess("Conectado ao host VPS com senha")
				} else {
					spinVPS.StopWithSuccess("Conectado ao host VPS por chave")
				}
				defer vpsClient.Close()

				spinInject := ui.NewSpinner("Injetando chave via Docker exec no VPS...")
				spinInject.Start()

				bootstrapCmd := fmt.Sprintf(
					`docker exec -i $(docker ps -qf label=com.docker.compose.service=workspace) sh -c `+
					`"mkdir -p /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root"`,
					pubKeyStr,
				)

				_, stderr, err := vpsClient.RunCommand(bootstrapCmd)
				if err != nil {
					bootstrapCmdFallback := fmt.Sprintf(
						`docker exec -i workspace_machine sh -c `+
						`"mkdir -p /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root"`,
						pubKeyStr,
					)
					_, stderr, err = vpsClient.RunCommand(bootstrapCmdFallback)
				}

				if err != nil {
					spinInject.StopWithError("Falha ao injetar chave: " + stderr)
					return err
				}
				spinInject.StopWithSuccess("Chave injetada via Docker no VPS")
			}
		}

		// Testa reconexão direta para confirmar
		spin3 := ui.NewSpinner("Testando reconexão sem senha ao container...")
		spin3.Start()

		testClient, err := internalssh.NewClient(hostCfg)
		if err != nil {
			spin3.StopWithError("Falha ao configurar teste")
			return nil
		}

		if err := testClient.Connect(); err != nil {
			spin3.StopWithError("Reconexão falhou")
		} else {
			testClient.Close()
			spin3.StopWithSuccess("Reconexão sem senha OK!")
		}

		fmt.Println()
		ui.Success("Setup completo para '%s'", displayName)
		ui.Dim("Você pode conectar com: unlarp connect %s", displayName)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(setupCmd)

	setupCmd.Flags().StringVar(&setupKey, "key", "", "caminho da chave pública SSH local a ser injetada (default: auto-detecta)")
	setupCmd.Flags().StringVar(&vpsUser, "vps-user", "", "usuário SSH para conectar ao host VPS (default: mesmo do container/root)")
	setupCmd.Flags().StringVar(&vpsKey, "vps-key", "", "caminho da chave privada SSH do host VPS (default: auto-detecta)")
	setupCmd.Flags().IntVar(&vpsPort, "vps-port", 22, "porta SSH do host VPS (default: 22)")
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
