package sync

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IgnoreMatcher determina se caminhos de arquivos devem ser ignorados no sync
type IgnoreMatcher struct {
	patterns []string
	regexes  []*regexp.Regexp
}

// NewIgnoreMatcher cria um matcher com padrões padrão e padrões de ignore do arquivo
func NewIgnoreMatcher(globalPatterns []string, ignoreFilePath string) *IgnoreMatcher {
	m := &IgnoreMatcher{
		patterns: make([]string, 0),
	}

	// Adiciona padrões globais
	for _, p := range globalPatterns {
		m.addPattern(p)
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
				m.addPattern(line)
			}
		}
	}

	return m
}

// Matches retorna true se o caminho relativo deve ser ignorado
func (m *IgnoreMatcher) Matches(relPath string, isDir bool) bool {
	// Normaliza separadores de caminho para "/"
	path := filepath.ToSlash(relPath)
	if isDir && !strings.HasSuffix(path, "/") {
		path = path + "/"
	}

	for _, re := range m.regexes {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// addPattern compila um padrão gitignore-style para regex
func (m *IgnoreMatcher) addPattern(pattern string) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return
	}

	m.patterns = append(m.patterns, pattern)

	// Converte padrão de wildcard para expressão regular
	// Referência: comportamento gitignore simplificado
	isDirOnly := strings.HasSuffix(pattern, "/")
	cleanPattern := strings.TrimSuffix(pattern, "/")

	// Converte caracteres especiais
	var reBuilder strings.Builder

	// Se não começa com "/", pode casar em qualquer nível
	if !strings.HasPrefix(cleanPattern, "/") {
		reBuilder.WriteString("(^|/)")
	} else {
		cleanPattern = strings.TrimPrefix(cleanPattern, "/")
		reBuilder.WriteString("^")
	}

	// Escapa caracteres especiais e converte wildcards
	parts := strings.Split(cleanPattern, "**")
	for i, part := range parts {
		if i > 0 {
			reBuilder.WriteString(".*")
		}

		// Escapa caracteres exceto * e ?
		escapedPart := regexp.QuoteMeta(part)
		escapedPart = strings.ReplaceAll(escapedPart, "\\*", "[^/]*")
		escapedPart = strings.ReplaceAll(escapedPart, "\\?", "[^/]")
		reBuilder.WriteString(escapedPart)
	}

	if isDirOnly {
		reBuilder.WriteString("/")
	} else {
		// Se não for exclusivo de diretório, pode casar o próprio arquivo ou um subdiretório
		reBuilder.WriteString("(/|$)")
	}

	if re, err := regexp.Compile(reBuilder.String()); err == nil {
		m.regexes = append(m.regexes, re)
	}
}
