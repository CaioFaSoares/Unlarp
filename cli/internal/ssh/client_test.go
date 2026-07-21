package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestWatchConnectionClosesDeadLink garante que watchConnection derruba a
// conexão quando ela para de responder, em vez de deixar reads/writes de
// outras goroutines travados pra sempre numa conexão morta.
func TestWatchConnectionClosesDeadLink(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerando chave: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	serverCfg := &ssh.ServerConfig{NoClientAuth: true}
	serverCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverRawConn := make(chan net.Conn, 1)
	go func() {
		netConn, err := ln.Accept()
		if err != nil {
			return
		}
		serverRawConn <- netConn
		sConn, chans, reqs, err := ssh.NewServerConn(netConn, serverCfg)
		if err != nil {
			return
		}
		defer sConn.Close()
		go ssh.DiscardRequests(reqs)
		for newCh := range chans {
			newCh.Reject(ssh.Prohibited, "sem canais neste teste")
		}
	}()

	clientCfg := &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	sshClient, err := ssh.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	origInterval, origTimeout := keepaliveInterval, keepaliveTimeout
	keepaliveInterval = 30 * time.Millisecond
	keepaliveTimeout = 200 * time.Millisecond
	defer func() { keepaliveInterval, keepaliveTimeout = origInterval, origTimeout }()

	c := &Client{conn: sshClient, stopKeepalive: make(chan struct{})}
	go c.watchConnection()

	// Simula link morto: fecha o socket cru do lado servidor sem handshake de
	// disconnect, então o próximo keepalive do cliente fica sem resposta.
	(<-serverRawConn).Close()

	done := make(chan error, 1)
	go func() { done <- sshClient.Wait() }()

	select {
	case <-done:
		// watchConnection detectou a falha e fechou a conexão — sucesso.
	case <-time.After(2 * time.Second):
		t.Fatal("watchConnection não fechou a conexão morta a tempo")
	}
}
