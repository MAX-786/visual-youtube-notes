//go:build ignore

// copy_spec copies docs/design/openapi.yaml into this package so the API can
// embed the contract it validates against. The copy is committed; CI's
// generate-and-diff step fails the build if it drifts from the source.
package main

import (
	"log"
	"os"
	"path/filepath"
)

func main() {
	source := filepath.Join("..", "..", "..", "docs", "design", "openapi.yaml")
	data, err := os.ReadFile(source)
	if err != nil {
		log.Fatalf("read %s: %v", source, err)
	}
	if err := os.WriteFile("openapi.yaml", data, 0o644); err != nil {
		log.Fatalf("write openapi.yaml: %v", err)
	}
}
