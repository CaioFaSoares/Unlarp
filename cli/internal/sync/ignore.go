package sync

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// IgnoreMatcher determina se caminhos de arquivos devem ser ignorados no sync
type IgnoreMatcher struct {
	mu             sync.RWMutex
	globalPatterns []string
	ignoreFilePath string
	patterns       []string
	regexes        []*regexp.Regexp
	negatedRegexes []*regexp.Regexp
}

// NewIgnoreMatcher cria um matcher com padrões padrão e padrões de ignore do arquivo
func NewIgnoreMatcher(globalPatterns []string, ignoreFilePath string) *IgnoreMatcher {
	m := &IgnoreMatcher{
		globalPatterns: globalPatterns,
		ignoreFilePath: ignoreFilePath,
		patterns:       make([]string, 0),
		regexes:        make([]*regexp.Regexp, 0),
		negatedRegexes: make([]*regexp.Regexp, 0),
	}

	// Adiciona padrões globais (filtrando padrões que ignorariam .claude/ por completo)
	for _, p := range globalPatterns {
		if isIgnoringClaude(p) {
			continue
		}
		m.addPattern("", p)
	}

	// Tenta carregar padrões do arquivo .unlarpignore
	if ignoreFilePath != "" {
		if file, err := os.Open(ignoreFilePath); err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				// Ignora comentários e linhas vazias
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if isIgnoringClaude(line) {
					continue
				}
				m.addPattern("", line)
			}
		}
	}

	return m
}

// isIgnoringClaude verifica se a regra ignora a pasta .claude
func isIgnoringClaude(pattern string) bool {
	clean := strings.TrimPrefix(strings.TrimSpace(pattern), "!")
	clean = strings.TrimPrefix(clean, "/")
	clean = strings.TrimSuffix(clean, "/")
	return clean == ".claude" || clean == ".claude/*" || clean == ".claude/**"
}

// LoadGitIgnoreFile lê um arquivo .gitignore e adiciona as suas regras relativas ao diretório informado
func (m *IgnoreMatcher) LoadGitIgnoreFile(gitIgnoreDir, filePath string) {
	if file, err := os.Open(filePath); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Se estiver no nível da raiz, não deixa ignorar .claude
			if gitIgnoreDir == "" && isIgnoringClaude(line) {
				continue
			}
			m.addPattern(gitIgnoreDir, line)
		}
	}
}

// Matches retorna true se o caminho relativo deve ser ignorado
func (m *IgnoreMatcher) Matches(relPath string, isDir bool) bool {
	// Normaliza separadores de caminho para "/"
	path := filepath.ToSlash(relPath)
	if isDir && !strings.HasSuffix(path, "/") {
		path = path + "/"
	}

	// REGRA DE EXCEÇÃO CRÍTICA PARA WORKTREES E METADADOS DO GIT:
	// 1. O arquivo regular chamado ".git" em uma worktree NÃO deve ser ignorado.
	// 2. O diretório ".git/" do repositório principal ou qualquer outro subdiretório ".git/" DEVE ser ignorado.
	baseName := filepath.Base(path)
	if baseName == ".git" {
		return isDir
	}

	// 3. A pasta ".git/worktrees/" e seus conteúdos NÃO devem ser ignorados,
	// pois mantêm os metadados das worktrees que precisamos sincronizar.
	if strings.HasPrefix(path, ".git/worktrees/") || path == ".git/worktrees" {
		return false
	}

	// Proteção para a pasta .claude onde residem as worktrees dos agentes:
	// A pasta .claude em si e o nível do nome da worktree não devem ser ignorados.
	// Ex: ".claude", ".claude/", ".claude/worktree-1", ".claude/worktree-1/"
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(parts) > 0 && parts[0] == ".claude" {
		if len(parts) <= 2 {
			return false // Nunca ignora ".claude" ou ".claude/nome-da-worktree"
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// 1. Verifica se coincide com alguma regra de ignore positiva
	ignored := false
	for _, re := range m.regexes {
		if re.MatchString(path) {
			ignored = true
			break
		}
	}

	// 2. Se for ignorado, verifica se existe alguma regra de negação (exclusão) que o resgate
	if ignored {
		for _, re := range m.negatedRegexes {
			if re.MatchString(path) {
				return false // Resgatado! Não deve ser ignorado.
			}
		}
	}

	return ignored
}

// addPattern compila um padrão para regex e adiciona às listas apropriadas (positiva ou negação)
func (m *IgnoreMatcher) addPattern(dir string, pattern string) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return
	}

	isNegated := strings.HasPrefix(pattern, "!")
	cleanPattern := pattern
	if isNegated {
		cleanPattern = pattern[1:]
	}

	isDirOnly := strings.HasSuffix(cleanPattern, "/")
	cleanPattern = strings.TrimSuffix(cleanPattern, "/")

	hasLeadingSlash := strings.HasPrefix(cleanPattern, "/")
	cleanPattern = strings.TrimPrefix(cleanPattern, "/")

	// Determina se o padrão tem alguma barra interna (o que o torna relativo ao diretório do gitignore)
	hasInternalSlash := strings.Contains(cleanPattern, "/")

	var reBuilder strings.Builder
	dir = filepath.ToSlash(dir)
	dir = strings.TrimPrefix(dir, "/")
	dir = strings.TrimSuffix(dir, "/")

	if dir != "" {
		// O caminho testado deve começar com o diretório correspondente
		reBuilder.WriteString("^" + regexp.QuoteMeta(dir) + "/")

		if hasLeadingSlash || hasInternalSlash {
			// Relativo ao diretório do gitignore
			reBuilder.WriteString(compileWildcard(cleanPattern))
		} else {
			// Casamento em qualquer nível sob o diretório do gitignore
			reBuilder.WriteString("(?:.*/)?")
			reBuilder.WriteString(compileWildcard(cleanPattern))
		}
	} else {
		// Nível raiz ou padrão global
		if hasLeadingSlash || hasInternalSlash {
			// Relativo à raiz do workspace
			reBuilder.WriteString("^")
			reBuilder.WriteString(compileWildcard(cleanPattern))
		} else {
			// Casamento em qualquer nível
			reBuilder.WriteString("(^|/)")
			reBuilder.WriteString(compileWildcard(cleanPattern))
		}
	}

	if isDirOnly {
		reBuilder.WriteString("/")
	} else {
		reBuilder.WriteString("(/|$)")
	}

	re, err := regexp.Compile(reBuilder.String())
	if err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.patterns = append(m.patterns, pattern)
	if isNegated {
		m.negatedRegexes = append(m.negatedRegexes, re)
	} else {
		m.regexes = append(m.regexes, re)
	}
}

// helper para compilar wildcards de gitignore (** e *)
func compileWildcard(pattern string) string {
	var reBuilder strings.Builder
	parts := strings.Split(pattern, "**")
	for i, part := range parts {
		if i > 0 {
			reBuilder.WriteString(".*")
		}
		escapedPart := regexp.QuoteMeta(part)
		escapedPart = strings.ReplaceAll(escapedPart, "\\*", "[^/]*")
		escapedPart = strings.ReplaceAll(escapedPart, "\\?", "[^/]")
		reBuilder.WriteString(escapedPart)
	}
	return reBuilder.String()
}
