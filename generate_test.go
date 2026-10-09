package gnarl

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The generated layer names the generator that wrote it, and tools.mod pins
// the generator `go generate` runs. If the two disagree, the checked-in code
// came from somewhere other than the reproducible path — and the next person
// to regenerate gets a diff they did not cause.
func TestGeneratedCodeCameFromThePinnedGenerator(t *testing.T) {
	mod, err := os.ReadFile("tools.mod")
	if err != nil {
		t.Fatalf("reading tools.mod: %v", err)
	}
	pin := regexp.MustCompile(`github\.com/oapi-codegen/oapi-codegen/v2 (v\S+)`).FindSubmatch(mod)
	if pin == nil {
		t.Fatal("tools.mod does not pin github.com/oapi-codegen/oapi-codegen/v2")
	}
	if !strings.Contains(string(mod), "\ntool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen") {
		t.Error("tools.mod has no `tool` directive for oapi-codegen, so `go tool` cannot run it")
	}

	gen, err := os.ReadFile("internal/oas/oas.gen.go")
	if err != nil {
		t.Fatalf("reading the generated layer: %v", err)
	}
	header := regexp.MustCompile(`oapi-codegen/v2 version (v\S+) DO NOT EDIT`).FindSubmatch(gen)
	if header == nil {
		t.Fatal("oas.gen.go does not name the generator version that wrote it")
	}
	if string(pin[1]) != string(header[1]) {
		t.Errorf("tools.mod pins oapi-codegen %s but oas.gen.go was written by %s",
			pin[1], header[1])
	}

	directive, err := os.ReadFile("internal/oas/generate.go")
	if err != nil {
		t.Fatalf("reading generate.go: %v", err)
	}
	if !strings.Contains(string(directive), "go tool -modfile=../../tools.mod oapi-codegen") {
		t.Error("generate.go does not run the generator pinned in tools.mod")
	}
}
