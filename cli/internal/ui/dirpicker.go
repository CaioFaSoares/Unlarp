package ui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/sftp"

	"github.com/CaioFaSoares/unlarp/internal/tui/styles"
)

type DirPicker struct {
	IsRemote     bool
	SFTPClient   *sftp.Client
	CurrentPath  string
	Dirs         []string
	Cursor       int
	Confirmed    bool
	Cancelled    bool
	SelectedPath string

	ManualInput  bool
	TextInput    textinput.Model

	NewDirActive bool
	NewDirInput  textinput.Model

	ExitOnConfirm bool

	err          error
}

func NewDirPicker(isRemote bool, sftpClient *sftp.Client, startPath string) *DirPicker {
	absPath := startPath
	if !isRemote {
		var err error
		absPath, err = filepath.Abs(startPath)
		if err != nil {
			absPath = startPath
		}
	} else if absPath == "" {
		absPath = "/"
	}

	ti := textinput.New()
	ti.Placeholder = "Digite o caminho absoluto..."
	ti.CharLimit = 250
	ti.Width = 60

	ndi := textinput.New()
	ndi.Placeholder = "Nome da nova pasta..."
	ndi.CharLimit = 50
	ndi.Width = 30

	dp := &DirPicker{
		IsRemote:    isRemote,
		SFTPClient:  sftpClient,
		CurrentPath: absPath,
		TextInput:   ti,
		NewDirInput: ndi,
	}
	dp.refreshDirs()
	return dp
}

func (d *DirPicker) Init() tea.Cmd {
	return nil
}

func (d *DirPicker) refreshDirs() {
	d.Dirs = []string{}
	d.Cursor = 0

	if d.IsRemote {
		if d.SFTPClient == nil {
			d.err = fmt.Errorf("SFTP client is nil")
			return
		}
		files, err := d.SFTPClient.ReadDir(d.CurrentPath)
		if err != nil {
			d.err = err
			return
		}
		for _, f := range files {
			if f.IsDir() {
				name := f.Name()
				if name != "." && name != ".." {
					d.Dirs = append(d.Dirs, name)
				}
			}
		}
	} else {
		files, err := os.ReadDir(d.CurrentPath)
		if err != nil {
			d.err = err
			return
		}
		for _, f := range files {
			if f.IsDir() {
				name := f.Name()
				if !strings.HasPrefix(name, ".") || name == ".unlarp" {
					d.Dirs = append(d.Dirs, name)
				}
			}
		}
	}

	sort.Strings(d.Dirs)
	d.err = nil
}

func (d *DirPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	if d.ManualInput {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(d.TextInput.Value())
				if val != "" {
					if d.IsRemote {
						d.CurrentPath = path.Clean(val)
					} else {
						d.CurrentPath = filepath.Clean(val)
					}
					d.ManualInput = false
					d.refreshDirs()
				}
			case "esc":
				d.ManualInput = false
			default:
				d.TextInput, cmd = d.TextInput.Update(msg)
				return d, cmd
			}
		}
		return d, nil
	}

	if d.NewDirActive {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(d.NewDirInput.Value())
				if val != "" {
					var newPath string
					if d.IsRemote {
						newPath = path.Join(d.CurrentPath, val)
						err := d.SFTPClient.Mkdir(newPath)
						if err == nil {
							d.CurrentPath = newPath
							d.refreshDirs()
						} else {
							d.err = err
						}
					} else {
						newPath = filepath.Join(d.CurrentPath, val)
						err := os.Mkdir(newPath, 0755)
						if err == nil {
							d.CurrentPath = newPath
							d.refreshDirs()
						} else {
							d.err = err
						}
					}
					d.NewDirActive = false
				}
			case "esc":
				d.NewDirActive = false
			default:
				d.NewDirInput, cmd = d.NewDirInput.Update(msg)
				return d, cmd
			}
		}
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if d.Cursor > 0 {
				d.Cursor--
			}
		case "down", "j":
			if d.Cursor < len(d.Dirs)-1 {
				d.Cursor++
			}
		case "enter":
			if len(d.Dirs) > 0 && d.Cursor >= 0 && d.Cursor < len(d.Dirs) {
				selectedDir := d.Dirs[d.Cursor]
				if d.IsRemote {
					d.CurrentPath = path.Join(d.CurrentPath, selectedDir)
				} else {
					d.CurrentPath = filepath.Join(d.CurrentPath, selectedDir)
				}
				d.refreshDirs()
			}
		case "backspace", "left", "h":
			var parent string
			if d.IsRemote {
				parent = path.Dir(d.CurrentPath)
			} else {
				parent = filepath.Dir(d.CurrentPath)
			}
			if parent != d.CurrentPath {
				d.CurrentPath = parent
				d.refreshDirs()
			}
		case "tab", "s":
			d.Confirmed = true
			d.SelectedPath = d.CurrentPath
			if d.ExitOnConfirm {
				return d, tea.Quit
			}
			return d, nil
		case "m":
			d.ManualInput = true
			d.TextInput.SetValue(d.CurrentPath)
			d.TextInput.Focus()
			return d, textinput.Blink
		case "n":
			d.NewDirActive = true
			d.NewDirInput.SetValue("")
			d.NewDirInput.Focus()
			return d, textinput.Blink
		case "esc", "ctrl+c":
			d.Cancelled = true
			if d.ExitOnConfirm || msg.String() == "ctrl+c" {
				return d, tea.Quit
			}
			return d, nil
		}
	}

	return d, nil
}

func (d *DirPicker) View() string {
	var sb strings.Builder

	titleMode := "LOCAL"
	if d.IsRemote {
		titleMode = "REMOTO (VPS)"
	}

	sb.WriteString(styles.TitleStyle.Render(fmt.Sprintf("Navegador de Diretórios — Modo %s", titleMode)))
	sb.WriteString("\n\n")

	sb.WriteString(styles.StatusLabelStyle.Render("Caminho: "))
	sb.WriteString(styles.StatusValueStyle.Render(d.CurrentPath))
	sb.WriteString("\n\n")

	if d.ManualInput {
		sb.WriteString("Digite o caminho manualmente:\n")
		sb.WriteString(d.TextInput.View())
		sb.WriteString("\n\n[Enter] Confirmar  |  [Esc] Cancelar")
		return sb.String()
	}

	if d.NewDirActive {
		sb.WriteString("Criar nova pasta:\n")
		sb.WriteString(d.NewDirInput.View())
		sb.WriteString("\n\n[Enter] Criar  |  [Esc] Cancelar")
		return sb.String()
	}

	if d.err != nil {
		sb.WriteString(lipgloss.NewStyle().Foreground(styles.ColorError).Render(fmt.Sprintf("Erro: %v", d.err)))
		sb.WriteString("\n\n")
	}

	sb.WriteString("Diretórios encontrados:\n")
	if len(d.Dirs) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(styles.ColorDim).Render("  (Nenhum subdiretório encontrado)"))
		sb.WriteString("\n")
	} else {
		for i, dir := range d.Dirs {
			line := fmt.Sprintf("  📁 %s", dir)
			if i == d.Cursor {
				sb.WriteString(styles.HostSelectedStyle.Render(line))
			} else {
				sb.WriteString(line)
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(styles.HelpStyle.Render(
		fmt.Sprintf("%s Navegar  |  %s Entrar  |  %s Voltar  |  %s Confirmar atual  |  %s Editar manual  |  %s Criar pasta  |  %s Cancelar",
			styles.KeyStyle.Render("↑/↓ / j/k"),
			styles.KeyStyle.Render("Enter"),
			styles.KeyStyle.Render("← / Backspace"),
			styles.KeyStyle.Render("Tab / s"),
			styles.KeyStyle.Render("m"),
			styles.KeyStyle.Render("n"),
			styles.KeyStyle.Render("Esc/Ctrl+C"),
		),
	))

	return sb.String()
}

func ChooseLocalDir(startDir string) (string, error) {
	dp := NewDirPicker(false, nil, startDir)
	dp.ExitOnConfirm = true
	p := tea.NewProgram(dp, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		return "", err
	}
	picker := m.(*DirPicker)
	if picker.Cancelled {
		return "", fmt.Errorf("seleção de diretório local cancelada")
	}
	return picker.SelectedPath, nil
}

func ChooseRemoteDir(sftpClient *sftp.Client, startDir string) (string, error) {
	dp := NewDirPicker(true, sftpClient, startDir)
	dp.ExitOnConfirm = true
	p := tea.NewProgram(dp, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		return "", err
	}
	picker := m.(*DirPicker)
	if picker.Cancelled {
		return "", fmt.Errorf("seleção de diretório remoto cancelada")
	}
	return picker.SelectedPath, nil
}
