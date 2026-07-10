// unlarp-agent: servidor helper que roda dentro do container workspace.
// Plano de controle/observação apenas — as transferências continuam por SFTP.
// Escuta num unix socket; o CLI chega via SSH streamlocal (sem portas novas).
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CaioFaSoares/unlarp/internal/agentapi"
)

func main() {
	stateDir := os.Getenv("UNLARP_AGENT_DIR")
	if stateDir == "" {
		stateDir = filepath.Dir(agentapi.SocketPath)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		log.Fatalf("falha ao criar diretório de estado %s: %v", stateDir, err)
	}

	srv := newServer(stateDir)
	if err := srv.loadWatches(); err != nil {
		log.Printf("aviso: falha ao restaurar watches persistidos: %v", err)
	}

	sock := filepath.Join(stateDir, filepath.Base(agentapi.SocketPath))
	_ = os.Remove(sock) // socket órfão de uma execução anterior
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Fatalf("falha ao escutar em %s: %v", sock, err)
	}
	_ = os.Chmod(sock, 0600)

	log.Printf("unlarp-agent %s (protocol %d) escutando em %s", agentapi.Version, agentapi.Protocol, sock)
	log.Fatal(http.Serve(ln, srv.mux()))
}
