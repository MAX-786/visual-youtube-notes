//go:build ignore

// Copies the frozen generation assets from docs/design/generation into this
// package so they can be embedded; a test asserts the copies are identical.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	pairs := [][2]string{
		{"../../../docs/design/generation/system-prompt.md", "assets/system-prompt.md"},
		{"../../../docs/design/generation/note-request.schema.json", "assets/note-request.schema.json"},
	}
	for _, p := range pairs {
		data, err := os.ReadFile(filepath.FromSlash(p[0]))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.FromSlash(p[1]), data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
