package tui

import "testing"

func TestClassifyClaudePane(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{
			name: "trabalhando",
			tail: "✳ Reticulating splines… (esc to interrupt · ctrl+t to hide todos)\n  ⎿ Running tests",
			want: "trabalhando",
		},
		{
			name: "esperando permissão",
			tail: "Do you want to make this edit to app.go?\n ❯ 1. Yes\n   2. No",
			want: "esperando",
		},
		{
			name: "idle no prompt",
			tail: "╭──────────╮\n│ > │\n╰──────────╯\n  ? for shortcuts",
			want: "idle",
		},
		{
			name: "tela irreconhecível degrada para vazio",
			tail: "make: *** [all] Error 2\n$ ",
			want: "",
		},
	}
	for _, c := range cases {
		if got := classifyClaudePane(c.tail); got != c.want {
			t.Errorf("%s: classifyClaudePane => %q, esperado %q", c.name, got, c.want)
		}
	}
}
