package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/CaioFaSoares/unlarp/cmd"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			errStr := fmt.Sprintf("Panic recovered: %v\nStack trace:\n%s\n", r, debug.Stack())
			_ = os.WriteFile("panic.log", []byte(errStr), 0644)
			fmt.Fprintln(os.Stderr, "Unlarp crashed! Panic log written to panic.log")
			fmt.Fprintln(os.Stderr, errStr)
			os.Exit(1)
		}
	}()
	cmd.Execute()
}
