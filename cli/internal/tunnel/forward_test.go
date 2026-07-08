package tunnel

import (
	"net"
	"testing"
)

func TestDirectionString(t *testing.T) {
	if got := DirectionRemote.String(); got != "remote" {
		t.Errorf("DirectionRemote.String() = %q, want %q", got, "remote")
	}
	if got := DirectionLocal.String(); got != "local" {
		t.Errorf("DirectionLocal.String() = %q, want %q", got, "local")
	}
}

// TestDialPeerRemoteDirection garante que, em DirectionRemote, o forwarder
// disca para a porta local (não depende de um sshClient para essa ponta).
func TestDialPeerRemoteDirection(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
			close(accepted)
		}
	}()

	f := &Forwarder{
		LocalPort: ln.Addr().(*net.TCPAddr).Port,
		Direction: DirectionRemote,
	}

	conn, addr, err := f.dialPeer()
	if err != nil {
		t.Fatalf("dialPeer: %v", err)
	}
	defer conn.Close()

	if addr == "" {
		t.Fatalf("dialPeer retornou endereço vazio")
	}
	<-accepted
}
