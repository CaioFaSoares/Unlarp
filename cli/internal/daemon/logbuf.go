package daemon

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/CaioFaSoares/unlarp/internal/daemonapi"
)

// maxLogFileSize é o limite antes da rotação (rename para .1, mantendo 1
// geração) — sem lib nova, como descrito em DAEMON.md.
const maxLogFileSize = 5 * 1024 * 1024

// maxRingEntries limita o buffer em memória consultado por GET /v1/logs?since=.
const maxRingEntries = 2000

// logBuf é a fonte de verdade dos logs do daemon: buffer circular em memória
// com número de sequência (para o polling `since=` da TUI) + arquivo
// ~/.unlarp/daemon.log em paralelo.
type logBuf struct {
	mu      sync.Mutex
	entries []daemonapi.LogEntry
	seq     uint64
	path    string
	file    *os.File
}

func newLogBuf() (*logBuf, error) {
	path, err := daemonapi.LogPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &logBuf{path: path, file: f}, nil
}

func (l *logBuf) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// Append registra uma linha de log — vira o onWarn/OnFileSuccess das engines.
func (l *logBuf) Append(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	entry := daemonapi.LogEntry{Seq: l.seq, Time: now.Format(time.RFC3339), Level: level, Msg: msg}
	l.entries = append(l.entries, entry)
	if len(l.entries) > maxRingEntries {
		l.entries = l.entries[len(l.entries)-maxRingEntries:]
	}

	line := fmt.Sprintf("%s %s %s\n", entry.Time, level, msg)
	l.rotateIfNeededLocked()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
}

// rotateIfNeededLocked renomeia o arquivo atual para .1 quando passa de
// maxLogFileSize. Chamador deve segurar l.mu. Mantém só 1 geração — falha na
// rotação não deve travar o daemon, só faz o arquivo crescer além do alvo.
func (l *logBuf) rotateIfNeededLocked() {
	if l.file == nil {
		return
	}
	info, err := l.file.Stat()
	if err != nil || info.Size() < maxLogFileSize {
		return
	}
	if err := l.file.Close(); err != nil {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		// Sem arquivo para escrever é melhor que um daemon travado; o ring
		// buffer em memória segue funcionando para GET /v1/logs.
		l.file = nil
		return
	}
	l.file = f
}

// Since devolve as entradas com Seq > since, mais o cursor atual — é o
// incremento que a TUI consome no tickCmd existente (polling de 1s).
func (l *logBuf) Since(since uint64) ([]daemonapi.LogEntry, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) == 0 || since >= l.seq {
		return nil, l.seq
	}

	// Busca linear: maxRingEntries é pequeno (2000), sem necessidade de
	// índice — ponytail: trocar por busca binária se o ring crescer muito.
	start := 0
	for i, e := range l.entries {
		if e.Seq > since {
			start = i
			break
		}
	}
	out := make([]daemonapi.LogEntry, len(l.entries)-start)
	copy(out, l.entries[start:])
	return out, l.seq
}
