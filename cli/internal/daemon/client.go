package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
)

// Client fala com o `unlarp daemon` local sobre o unix socket — mesmo padrão
// de internal/agent/client.go, sem o hop SSH (o socket já é local).
type Client struct {
	http *http.Client
}

// NewClient cria um cliente apontando para o socket padrão do daemon. Não
// disca ainda — só falha quando um método é chamado (mesma semântica do
// http.Client comum).
func NewClient() (*Client, error) {
	sockPath, err := daemonapi.SocketPath()
	if err != nil {
		return nil, err
	}
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sockPath)
				},
				MaxIdleConns:    2,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}, nil
}

// Ping tenta o handshake em `timeout` — usado pelo autostart para decidir se
// o daemon já está de pé antes de tentar subir um novo.
func (c *Client) Ping(timeout time.Duration) (daemonapi.InfoResponse, error) {
	return c.Info(timeout)
}

func (c *Client) Info(timeout time.Duration) (daemonapi.InfoResponse, error) {
	var info daemonapi.InfoResponse
	err := c.call(timeout, http.MethodGet, "/v1/info", nil, &info)
	return info, err
}

func (c *Client) ListSyncs() (daemonapi.SyncsResponse, error) {
	var resp daemonapi.SyncsResponse
	err := c.call(10*time.Second, http.MethodGet, "/v1/syncs", nil, &resp)
	return resp, err
}

func (c *Client) CreateSync(req daemonapi.CreateSyncRequest) (daemonapi.SyncInfo, error) {
	var info daemonapi.SyncInfo
	err := c.call(2*time.Minute, http.MethodPost, "/v1/syncs", req, &info)
	return info, err
}

func (c *Client) DeleteSync(id string) error {
	return c.call(10*time.Second, http.MethodDelete, "/v1/syncs/"+id, nil, nil)
}

func (c *Client) PauseSync(id, reason string) error {
	return c.call(10*time.Second, http.MethodPost, "/v1/syncs/"+id+"/pause",
		struct {
			Reason string `json:"reason"`
		}{Reason: reason}, nil)
}

func (c *Client) ResumeSync(id string) error {
	return c.call(10*time.Second, http.MethodPost, "/v1/syncs/"+id+"/resume", nil, nil)
}

func (c *Client) ListTunnels() (daemonapi.TunnelsResponse, error) {
	var resp daemonapi.TunnelsResponse
	err := c.call(10*time.Second, http.MethodGet, "/v1/tunnels", nil, &resp)
	return resp, err
}

func (c *Client) CreateTunnel(req daemonapi.CreateTunnelRequest) (daemonapi.TunnelInfo, error) {
	var info daemonapi.TunnelInfo
	err := c.call(15*time.Second, http.MethodPost, "/v1/tunnels", req, &info)
	return info, err
}

func (c *Client) DeleteTunnel(id string) error {
	return c.call(10*time.Second, http.MethodDelete, "/v1/tunnels/"+id, nil, nil)
}

func (c *Client) Logs(since uint64) (daemonapi.LogsResponse, error) {
	var resp daemonapi.LogsResponse
	err := c.call(10*time.Second, http.MethodGet, fmt.Sprintf("/v1/logs?since=%d", since), nil, &resp)
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

	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reqBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return fmt.Errorf("daemon respondeu %d: %s", resp.StatusCode, msg.String())
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
