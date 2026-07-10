// Package agentapi define o contrato compartilhado entre o CLI (Mac) e o
// unlarp-agent (container). Só DTOs e constantes — sem lógica — para que os
// dois binários compilem do mesmo módulo sem dependência cruzada.
package agentapi

import "github.com/CaioFaSoares/unlarp/internal/git"

const (
	// Protocol é incrementado a cada mudança incompatível na API. O CLI trata
	// mismatch como "agent ausente" e cai no comportamento SFTP atual.
	Protocol = 1

	// Version do binário do agent (informativa, exibida em `unlarp agent status`)
	Version = "0.1.0"

	// SocketPath é o unix socket dentro do container. Vive em /root/.unlarp
	// (volume persistente workspace-home), nunca dentro de um sync root.
	SocketPath = "/root/.unlarp/agent.sock"
)

type InfoResponse struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

type WatchRequest struct {
	Dir           string   `json:"dir"`
	GlobalIgnores []string `json:"global_ignores"`
}

// WatchResponse devolve o cursor atual do diretório — o cliente começa o
// long-poll de eventos a partir dele.
type WatchResponse struct {
	Seq uint64 `json:"seq"`
}

type EventsResponse struct {
	Seq     uint64 `json:"seq"`
	Changed bool   `json:"changed"`
}

// SnapshotRequest pede o snapshot de um diretório calculado dentro do
// container (walk+stat locais, sem SFTP). A resposta é um sync.Snapshot JSON.
type SnapshotRequest struct {
	Dir           string   `json:"dir"`
	GlobalIgnores []string `json:"global_ignores"`
}

// ProjectsRequest pede o estado do workspace numa chamada só: git de cada
// path informado (ou dos dirs de 1º nível de Root quando Paths vazio),
// sessões tmux e containers docker.
type ProjectsRequest struct {
	Root  string   `json:"root,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type ProjectInfo struct {
	Path string            `json:"path"`
	Git  git.RemoteGitInfo `json:"git"`
}

type TmuxSessionInfo struct {
	Name         string `json:"name"`
	Windows      int    `json:"windows"`
	Attached     bool   `json:"attached"`
	Command      string `json:"command"`
	Path         string `json:"path"`
	PaneDead     bool   `json:"pane_dead"`
	ActivityUnix int64  `json:"activity_unix"`
}

type ContainerInfo struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

type ProjectsResponse struct {
	Projects   []ProjectInfo     `json:"projects"`
	Tmux       []TmuxSessionInfo `json:"tmux"`
	Containers []ContainerInfo   `json:"containers"`
}

// Operações git permitidas pelo agent — fronteira de confiança: allowlist
// fechada, sem passthrough de comandos arbitrários.
const (
	GitOpCheckout       = "checkout"
	GitOpWorktreeAdd    = "worktree_add"
	GitOpWorktreeRemove = "worktree_remove"
)

type GitOpRequest struct {
	Dir  string   `json:"dir"`
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

// GitOpResponse devolve o estado git resultante — o caller usa Branch/Commit
// para atualizar o GitGuard sem uma consulta extra.
type GitOpResponse struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	Output string `json:"output"`
}
