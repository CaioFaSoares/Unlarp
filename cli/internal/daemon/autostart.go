package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
)

// Connect devolve um Client conectado ao daemon, subindo-o (fork/exec
// desanexado) se ainda não estiver de pé. Multiplataforma (sem
// launchd/systemd) — é o auto-start descrito em DAEMON.md §4.
func Connect() (*Client, error) {
	c, err := NewClient()
	if err != nil {
		return nil, err
	}
	if _, err := c.Ping(500 * time.Millisecond); err == nil {
		return c, nil
	}
	if err := spawn(); err != nil {
		return nil, fmt.Errorf("erro ao subir o daemon: %w", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Ping(300 * time.Millisecond); err == nil {
			return c, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon não respondeu a tempo após auto-start")
}

// spawn adquire um flock em ~/.unlarp/daemon.lock (serializa clientes
// concorrentes tentando subir o daemon ao mesmo tempo) e faz fork/exec de
// `unlarp daemon` desanexado do terminal atual.
func spawn() error {
	lockPath, err := daemonapi.LockPath()
	if err != nil {
		return err
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Outro processo pode ter subido o daemon enquanto esperávamos o lock.
	if c, err := NewClient(); err == nil {
		if _, err := c.Ping(300 * time.Millisecond); err == nil {
			return nil
		}
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	}
	return cmd.Start()
}
