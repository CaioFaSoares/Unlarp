// Package compose interpreta a saída de `docker compose` para orquestrar
// serviços de projetos cadastrados (remoto ou local) sem depender do SDK do Docker.
package compose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Service é uma linha de `docker compose ps --format json`
type Service struct {
	Name       string      `json:"Name"`
	Service    string      `json:"Service"`
	State      string      `json:"State"`
	Publishers []Publisher `json:"Publishers"`
}

// Publisher é uma porta publicada por um serviço
type Publisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

// ParsePS aceita tanto o formato array quanto o NDJSON (um objeto por linha)
// que versões diferentes do docker compose v2 emitem para `ps --format json`.
func ParsePS(out string) ([]Service, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}

	if strings.HasPrefix(out, "[") {
		var svcs []Service
		if err := json.Unmarshal([]byte(out), &svcs); err != nil {
			return nil, fmt.Errorf("saída inesperada de docker compose ps: %w", err)
		}
		return svcs, nil
	}

	var svcs []Service
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var s Service
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, fmt.Errorf("saída inesperada de docker compose ps: %w", err)
		}
		svcs = append(svcs, s)
	}
	return svcs, nil
}

// PublishedPorts retorna as portas TCP publicadas distintas, ordenadas —
// as candidatas a auto-tunnel quando o compose roda remoto.
func PublishedPorts(svcs []Service) []int {
	seen := make(map[int]bool)
	var ports []int
	for _, s := range svcs {
		for _, p := range s.Publishers {
			if p.PublishedPort > 0 && p.Protocol != "udp" && !seen[p.PublishedPort] {
				seen[p.PublishedPort] = true
				ports = append(ports, p.PublishedPort)
			}
		}
	}
	sort.Ints(ports)
	return ports
}

// CommandFor monta o comando `docker compose` para um projeto: cd no diretório
// e -f explícito quando o projeto define um arquivo compose fora do padrão.
func CommandFor(projectDir, composeFile, action string) string {
	cmd := "docker compose"
	if composeFile != "" {
		cmd += " -f " + shellQuote(composeFile)
	}
	return fmt.Sprintf("cd %s && %s %s", shellQuote(projectDir), cmd, action)
}

// EnsureRestart monta o comando que fixa restart policy nos containers do
// projeto — garante que os serviços internos voltem sozinhos após reboot do servidor.
func EnsureRestart(projectDir, composeFile string) string {
	return CommandFor(projectDir, composeFile, "ps -q") + " | xargs -r docker update --restart unless-stopped"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
