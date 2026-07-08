package sync

// ConflictStrategy define o tipo de estratégia de resolução de conflitos
type ConflictStrategy string

const (
	StrategyNewestWins ConflictStrategy = "newest-wins"
	StrategyLocalWins  ConflictStrategy = "local-wins"
	StrategyRemoteWins ConflictStrategy = "remote-wins"
)

// ResolveConflict decide se o arquivo local deve sobrescrever o remoto (retorna true)
// ou se o remoto deve sobrescrever o local (retorna false) baseado na estratégia configurada.
func ResolveConflict(local, remote FileEntry, strategy ConflictStrategy) bool {
	switch strategy {
	case StrategyLocalWins:
		return true
	case StrategyRemoteWins:
		return false
	case StrategyNewestWins:
		fallthrough
	default:
		// Em caso de ModTime igual, escolhe local como fallback seguro
		if local.ModTime.Equal(remote.ModTime) {
			return true
		}
		return local.ModTime.After(remote.ModTime)
	}
}
