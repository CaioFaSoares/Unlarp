// Package agent é o cliente HTTP do unlarp-agent que roda no container.
// O transporte é o unix socket do agent alcançado por SSH streamlocal — a
// conexão SSH existente já autentica e criptografa; nenhuma porta nova.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/agentapi"
	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
	internalsync "github.com/CaioFaSoares/unlarp/internal/sync"
)

type Client struct {
	http *http.Client
}

// New cria um cliente do agent sobre uma conexão SSH já estabelecida.
func New(sshClient *internalssh.Client) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return sshClient.Conn().Dial("unix", agentapi.SocketPath)
				},
				// Conexões unix-sobre-SSH são baratas; sem pool agressivo
				MaxIdleConns:    2,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// Detect retorna um cliente pronto se o agent está instalado, respondendo e
// com protocolo compatível — nil caso contrário (o caller usa o fallback
// SFTP atual). Nunca retorna erro: ausência de agent não é falha.
func Detect(sshClient *internalssh.Client) *Client {
	if sshClient == nil || sshClient.Conn() == nil {
		return nil
	}
	c := New(sshClient)
	info, err := c.Info(2 * time.Second)
	if err != nil || info.Protocol != agentapi.Protocol {
		return nil
	}
	return c
}

func (c *Client) Info(timeout time.Duration) (agentapi.InfoResponse, error) {
	var info agentapi.InfoResponse
	err := c.call(timeout, http.MethodGet, "/v1/info", nil, &info)
	return info, err
}

// Watch registra (idempotente) a observação de um diretório remoto e retorna
// o cursor atual de eventos.
func (c *Client) Watch(dir string, globalIgnores []string) (uint64, error) {
	var resp agentapi.WatchResponse
	err := c.call(10*time.Second, http.MethodPost, "/v1/watch",
		agentapi.WatchRequest{Dir: dir, GlobalIgnores: globalIgnores}, &resp)
	return resp.Seq, err
}

// Events faz long-poll por mudanças após o cursor `since`. Bloqueia até uma
// mudança ou até o timeout do servidor (resposta com Changed=false).
func (c *Client) Events(dir string, since uint64, timeout time.Duration) (agentapi.EventsResponse, error) {
	var resp agentapi.EventsResponse
	path := fmt.Sprintf("/v1/events?dir=%s&since=%d&timeout=%s", dir, since, timeout)
	// Margem sobre o timeout do servidor para a resposta viajar
	err := c.call(timeout+10*time.Second, http.MethodGet, path, nil, &resp)
	return resp, err
}

// Snapshot pede ao agent o snapshot de um diretório calculado no container —
// uma chamada HTTP no lugar de uma varredura SFTP inteira.
func (c *Client) Snapshot(dir string, globalIgnores []string) (internalsync.Snapshot, error) {
	var snap internalsync.Snapshot
	err := c.call(2*time.Minute, http.MethodPost, "/v1/snapshot",
		agentapi.SnapshotRequest{Dir: dir, GlobalIgnores: globalIgnores}, &snap)
	return snap, err
}

// Projects devolve o estado do workspace (git por path, tmux, containers)
// numa chamada só — o que a TUI fazia com múltiplos execs SSH.
func (c *Client) Projects(root string, paths []string) (agentapi.ProjectsResponse, error) {
	var resp agentapi.ProjectsResponse
	err := c.call(30*time.Second, http.MethodPost, "/v1/projects",
		agentapi.ProjectsRequest{Root: root, Paths: paths}, &resp)
	return resp, err
}

// GitOp executa uma operação git da allowlist do agent (checkout,
// worktree_add, worktree_remove) e devolve branch/commit resultantes.
func (c *Client) GitOp(dir, op string, args []string) (agentapi.GitOpResponse, error) {
	var resp agentapi.GitOpResponse
	err := c.call(60*time.Second, http.MethodPost, "/v1/git/op",
		agentapi.GitOpRequest{Dir: dir, Op: op, Args: args}, &resp)
	return resp, err
}

func (c *Client) call(timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var reqBody *bytes.Buffer = &bytes.Buffer{}
	if body != nil {
		if err := json.NewEncoder(reqBody).Encode(body); err != nil {
			return err
		}
	}

	// O host "unix" é ignorado: o DialContext sempre alcança o socket do agent
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reqBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return fmt.Errorf("agent respondeu %d: %s", resp.StatusCode, msg.String())
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
