package ui

import (
	"fmt"

	"github.com/fatih/color"
)

var (
	// Cores
	success = color.New(color.FgGreen, color.Bold)
	warn    = color.New(color.FgYellow, color.Bold)
	errC    = color.New(color.FgRed, color.Bold)
	info    = color.New(color.FgCyan)
	dim     = color.New(color.FgHiBlack)
	bold    = color.New(color.Bold)

	// Ícones
	IconCheck   = "✓"
	IconCross   = "✗"
	IconDot     = "●"
	IconCircle  = "○"
	IconArrow   = "→"
	IconSync    = "↔"
	IconPush    = "→"
	IconWarning = "⚠"
	IconInfo    = "ℹ"
)

// Success imprime mensagem de sucesso
func Success(format string, a ...interface{}) {
	success.Printf(" %s %s\n", IconCheck, fmt.Sprintf(format, a...))
}

// Warn imprime aviso
func Warn(format string, a ...interface{}) {
	warn.Printf(" %s %s\n", IconWarning, fmt.Sprintf(format, a...))
}

// Error imprime erro
func Error(format string, a ...interface{}) {
	errC.Printf(" %s %s\n", IconCross, fmt.Sprintf(format, a...))
}

// Info imprime informação
func Info(format string, a ...interface{}) {
	info.Printf(" %s %s\n", IconInfo, fmt.Sprintf(format, a...))
}

// Dim imprime texto esmaecido
func Dim(format string, a ...interface{}) {
	dim.Printf("   %s\n", fmt.Sprintf(format, a...))
}

// Bold imprime texto em negrito
func Bold(format string, a ...interface{}) {
	bold.Printf("%s\n", fmt.Sprintf(format, a...))
}

// Header imprime um cabeçalho formatado
func Header(title string) {
	fmt.Println()
	bold.Printf(" %s\n", title)
	dim.Println(" " + repeatChar("─", len(title)+2))
}

// StatusLine imprime uma linha de status com label e valor
func StatusLine(label, value string, ok bool) {
	icon := IconCheck
	c := success
	if !ok {
		icon = IconCross
		c = errC
	}
	fmt.Printf("    %-10s ", label)
	c.Printf("%s %s\n", icon, value)
}

// ActiveIndicator retorna o indicador de ativo/inativo
func ActiveIndicator(active bool) string {
	if active {
		return success.Sprint(IconDot + " active")
	}
	return dim.Sprint(IconCircle + " idle")
}

// repeatChar repete um caractere n vezes
func repeatChar(char string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += char
	}
	return result
}
