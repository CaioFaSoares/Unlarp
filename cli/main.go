package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"syscall"

	"github.com/CaioFaSoares/unlarp/cmd"
)

func main() {
	// unlarp observa recursivamente árvores de arquivo (watchers locais por
	// sync ativo). No macOS o backend kqueue segura 1 fd por diretório
	// observado, e projetos com worktrees (cópias completas do repo) somam
	// milhares de diretórios — estoura o soft limit padrão (256 em sessão
	// tmux/GUI) e todo open/fcntl subsequente falha com EBADF. Sobe o soft
	// limit até o hard limit do processo logo no boot.
	var lim syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) == nil {
		lim.Cur = lim.Max
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim)
	}

	defer func() {
		if r := recover(); r != nil {
			errStr := fmt.Sprintf("Panic recovered: %v\nStack trace:\n%s\n", r, debug.Stack())
			_ = os.WriteFile("panic.log", []byte(errStr), 0644)
			fmt.Fprintln(os.Stderr, "Unlarp crashed! Panic log written to panic.log")
			fmt.Fprintln(os.Stderr, errStr)
			os.Exit(1)
		}
	}()
	cmd.Execute()
}
