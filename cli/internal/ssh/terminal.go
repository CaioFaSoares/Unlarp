package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// getTerminalSize retorna largura e altura do terminal
func getTerminalSize() (int, int, error) {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}

	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("output inesperado do stty")
	}

	height, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}

	width, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}

	return width, height, nil
}
