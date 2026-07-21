package ssh

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"github.com/mitchellh/go-homedir"

	"github.com/CaioFaSoares/unlarp/internal/config"
)

// keepaliveInterval/keepaliveTimeout regem o watchdog de conexão morta (ver
// watchConnection). var, não const, para o teste poder encolhê-los.
var (
	keepaliveInterval = 15 * time.Second
	keepaliveTimeout  = 10 * time.Second
)

// Client encapsula uma conexão SSH reutilizável
type Client struct {
	host          *config.Host
	conn          *ssh.Client
	config        *ssh.ClientConfig
	stopKeepalive chan struct{}
}

// NewClient cria um novo client SSH a partir de um Host configurado
func NewClient(host *config.Host) (*Client, error) {
	authMethods, err := buildAuthMethods(host)
	if err != nil {
		return nil, fmt.Errorf("erro ao configurar autenticação: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            host.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: implementar known_hosts
		Timeout:         10 * time.Second,
	}

	return &Client{
		host:   host,
		config: sshConfig,
	}, nil
}

// Connect estabelece a conexão SSH
func (c *Client) Connect() error {
	addr := c.host.Address()
	conn, err := ssh.Dial("tcp", addr, c.config)
	if err != nil {
		return fmt.Errorf("falha ao conectar em %s: %w", addr, err)
	}
	c.conn = conn
	c.stopKeepalive = make(chan struct{})
	go c.watchConnection()
	return nil
}

// watchConnection fecha a conexão se ela parar de responder a keepalives.
// ponytail: RunCommand/SFTP não têm deadline nativo de socket — numa conexão
// morta, um read/write trava pra sempre. Fechar aqui destrava qualquer
// transfer em andamento em vez de vazar a goroutine bloqueada (era o "Mac
// trava e precisa reiniciar" observado durante o git heal de repos grandes).
func (c *Client) watchConnection() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopKeepalive:
			return
		case <-ticker.C:
			done := make(chan error, 1)
			go func() {
				_, _, err := c.conn.SendRequest("keepalive@unlarp", true, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					c.conn.Close()
					return
				}
			case <-time.After(keepaliveTimeout):
				c.conn.Close()
				return
			}
		}
	}
}

// Close fecha a conexão SSH
func (c *Client) Close() error {
	if c.stopKeepalive != nil {
		close(c.stopKeepalive)
		c.stopKeepalive = nil
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// IsConnected verifica se a conexão está ativa
func (c *Client) IsConnected() bool {
	if c.conn == nil {
		return false
	}
	// Tenta enviar um request para verificar se está viva
	_, _, err := c.conn.SendRequest("keepalive@unlarp", true, nil)
	return err == nil
}

// Session cria uma nova sessão SSH (para execução de comandos)
func (c *Client) Session() (*ssh.Session, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("não conectado. Chame Connect() primeiro")
	}
	return c.conn.NewSession()
}

// Conn retorna a conexão SSH subjacente (para SFTP, tunnels, etc.)
func (c *Client) Conn() *ssh.Client {
	return c.conn
}

// RunCommand executa um comando remoto e retorna stdout, stderr e erro
func (c *Client) RunCommand(command string) (string, string, error) {
	session, err := c.Session()
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	var stdout, stderr []byte

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("erro ao obter stdout: %w", err)
	}

	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("erro ao obter stderr: %w", err)
	}

	if err := session.Start(command); err != nil {
		return "", "", fmt.Errorf("erro ao executar comando: %w", err)
	}

	stdout, _ = readAll(stdoutPipe)
	stderr, _ = readAll(stderrPipe)

	err = session.Wait()
	return string(stdout), string(stderr), err
}

// InteractiveShell abre uma sessão SSH interativa com PTY
func (c *Client) InteractiveShell(shell string) error {
	// Desabilita focus reporting temporariamente para que o terminal local não envie sequências ^[[I / ^[[O
	// que confundem ferramentas de stdin como o Claude Code.
	_, _ = os.Stdout.Write([]byte("\x1b[?1004l"))

	session, err := c.Session()
	if err != nil {
		return err
	}
	defer session.Close()

	// Conecta stdin/stdout/stderr do terminal local
	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// Coloca o terminal local em modo raw
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("erro ao configurar terminal em modo raw: %w", err)
	}
	defer term.Restore(fd, state)

	// Solicita pseudo-terminal
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	// Detecta tamanho do terminal
	width, height := 80, 24
	if w, h, err := getTerminalSize(); err == nil {
		width, height = w, h
	}

	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("erro ao solicitar PTY: %w", err)
	}

	// Propaga redimensionamento dinâmico de janela (SIGWINCH)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		for range sigChan {
			if w, h, err := getTerminalSize(); err == nil {
				_ = session.WindowChange(h, w)
			}
		}
	}()
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()

	if shell == "" {
		shell = "/bin/bash"
	}

	if err := session.Start(shell); err != nil {
		return fmt.Errorf("erro ao iniciar shell: %w", err)
	}

	return session.Wait()
}

// Ping testa a latência da conexão
func (c *Client) Ping() (time.Duration, error) {
	start := time.Now()
	_, _, err := c.conn.SendRequest("keepalive@unlarp", true, nil)
	return time.Since(start), err
}

// TestConnection tenta conectar e desconectar para verificar se o host é acessível
func TestConnection(host *config.Host) (time.Duration, error) {
	start := time.Now()

	addr := host.Address()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("host %s não acessível: %w", addr, err)
	}
	conn.Close()

	return time.Since(start), nil
}

// buildAuthMethods constrói os métodos de autenticação SSH
func buildAuthMethods(host *config.Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Tenta senha se fornecida
	if host.Password != "" {
		methods = append(methods, ssh.Password(host.Password))
	}

	// Tenta chave específica do host
	if host.Key != "" {
		keyPath, err := homedir.Expand(host.Key)
		if err != nil {
			return nil, fmt.Errorf("erro ao expandir caminho da chave: %w", err)
		}
		signer, err := loadKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("erro ao carregar chave %s: %w", keyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
		return methods, nil
	}

	// Auto-detecta chaves
	signers := detectKeys()
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("nenhuma chave SSH encontrada. Especifique com --key ou gere uma com: ssh-keygen -t ed25519")
	}

	return methods, nil
}

// loadKey carrega uma chave SSH privada de um arquivo
func loadKey(path string) (ssh.Signer, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}

	return signer, nil
}

// detectKeys procura chaves SSH no diretório ~/.ssh/
func detectKeys() []ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	sshDir := filepath.Join(home, ".ssh")
	keyNames := []string{"id_ed25519", "id_ecdsa", "id_rsa"}

	var signers []ssh.Signer
	for _, name := range keyNames {
		path := filepath.Join(sshDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}

		signer, err := loadKey(path)
		if err != nil {
			continue
		}

		signers = append(signers, signer)
	}

	return signers
}

// readAll lê todo o conteúdo de um io.Reader
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var result []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return result, nil
}
