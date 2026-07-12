package git

import "strings"

// SanitizeWorktreeName achata o nome da branch para uso em caminho de worktree
// e em nome de sessão tmux (tmux não aceita '.' e ':'; '/' viraria subpasta).
func SanitizeWorktreeName(branch string) string {
	r := strings.NewReplacer("/", "-", ".", "-", ":", "-", " ", "-")
	return r.Replace(branch)
}

// WorktreeRelPath é o caminho relativo padrão da worktree DENTRO do repo.
// Ficar dentro do repo é deliberado: o sync do projeto já espelha a worktree
// localmente (os ignores e o rewrite de gitdir da engine tratam .claude/worktree-*).
func WorktreeRelPath(branch string) string {
	return ".claude/worktree-" + SanitizeWorktreeName(branch)
}
