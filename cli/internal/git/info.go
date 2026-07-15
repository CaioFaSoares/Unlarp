package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	internalssh "github.com/CaioFaSoares/unlarp/internal/ssh"
)

// RemoteGitInfo contém o estado Git de um projeto remoto
type RemoteGitInfo struct {
	IsGitRepo     bool
	Branch        string
	CommitHash    string
	CommitMessage string
	CommitTime    time.Time
	IsDirty       bool
	RemoteName    string
	RemoteURL     string
	AheadBehind   AheadBehind
}

// AheadBehind representa a comparação entre HEAD local e origin
type AheadBehind struct {
	Ahead  int // commits no remote não pushados
	Behind int // commits no origin que o remote não pullou
}

// GetRemoteGitInfo consulta o estado Git de um projeto remoto via SSH.
// Executa um único comando SSH que extrai todas as informações de uma vez.
func GetRemoteGitInfo(client *internalssh.Client, projectPath string) (RemoteGitInfo, error) {
	var info RemoteGitInfo

	cmd := fmt.Sprintf(
		`cd %s 2>/dev/null && git rev-parse --is-inside-work-tree >/dev/null 2>&1 && `+
			`echo "REPO|true" && `+
			`echo "BRANCH|$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" && `+
			`echo "HASH|$(git rev-parse --short HEAD 2>/dev/null)" && `+
			`echo "MSG|$(git log -1 --format=%%s 2>/dev/null)" && `+
			`echo "TIME|$(git log -1 --format=%%aI 2>/dev/null)" && `+
			`echo "DIRTY|$(git status --porcelain 2>/dev/null | head -1)" && `+
			`REMOTE=$(git remote | grep -x "origin" || git remote | head -1) && `+
			`echo "REMOTE|$REMOTE" && `+
			`echo "URL|$(git remote get-url $REMOTE 2>/dev/null)" && `+
			`echo "AB|$(git rev-list --left-right --count HEAD...$REMOTE/$(git rev-parse --abbrev-ref HEAD) 2>/dev/null)" || `+
			`echo "REPO|false"`,
		shellQuote(projectPath),
	)

	stdout, _, err := client.RunCommand(cmd)
	if err != nil {
		return info, fmt.Errorf("erro ao consultar git remoto: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		val := strings.TrimSpace(parts[1])

		switch key {
		case "REPO":
			info.IsGitRepo = val == "true"
			if !info.IsGitRepo {
				return info, nil
			}
		case "BRANCH":
			info.Branch = val
		case "HASH":
			info.CommitHash = val
		case "MSG":
			info.CommitMessage = val
		case "TIME":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				info.CommitTime = t
			}
		case "DIRTY":
			info.IsDirty = val != ""
		case "REMOTE":
			info.RemoteName = val
		case "URL":
			info.RemoteURL = val
		case "AB":
			abParts := strings.Fields(val)
			if len(abParts) == 2 {
				info.AheadBehind.Ahead, _ = strconv.Atoi(abParts[0])
				info.AheadBehind.Behind, _ = strconv.Atoi(abParts[1])
			}
		}
	}

	return info, nil
}

// LocalInfo consulta o estado Git de um diretório do filesystem local via
// os/exec — usado pelo unlarp-agent, onde o "remoto" é local ao container.
// Mesmos campos de GetRemoteGitInfo, sem parsing de shell.
func LocalInfo(dir string) RemoteGitInfo {
	var info RemoteGitInfo
	run := func(args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}

	if run("rev-parse", "--is-inside-work-tree") != "true" {
		return info
	}
	info.IsGitRepo = true
	info.Branch = run("rev-parse", "--abbrev-ref", "HEAD")
	info.CommitHash = run("rev-parse", "--short", "HEAD")
	info.CommitMessage = run("log", "-1", "--format=%s")
	if t, err := time.Parse(time.RFC3339, run("log", "-1", "--format=%aI")); err == nil {
		info.CommitTime = t
	}
	info.IsDirty = run("status", "--porcelain") != ""
	
	remote := "origin"
	if remotes := strings.Split(run("remote"), "\n"); len(remotes) > 0 && remotes[0] != "" {
		hasOrigin := false
		for _, r := range remotes {
			if r == "origin" {
				hasOrigin = true
				break
			}
		}
		if !hasOrigin {
			remote = remotes[0]
		}
	}
	info.RemoteName = remote
	info.RemoteURL = run("remote", "get-url", remote)
	if ab := strings.Fields(run("rev-list", "--left-right", "--count", "HEAD..."+remote+"/"+info.Branch)); len(ab) == 2 {
		info.AheadBehind.Ahead, _ = strconv.Atoi(ab[0])
		info.AheadBehind.Behind, _ = strconv.Atoi(ab[1])
	}
	return info
}

// PullLocal executa `git pull` no repositório local do usuário.
func PullLocal(localDir string, remote string, branch string) error {
	if remote == "" {
		remote = "origin"
	}

	args := []string{"-C", localDir, "pull", remote}
	if branch != "" {
		args = append(args, branch)
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull falhou: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// shellQuote protege um argumento para uso seguro em shell remoto
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
