package gnarl_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// DOC TEST: every Go example in README.md must compile against THIS package.
//
// WHY
//
// A README is the first thing a user runs and the last thing anyone updates.
// Rename a method, change a signature, drop a field, and the prose keeps
// claiming the old shape — confidently, in the place a newcomer trusts most.
// Nothing catches it, because documentation is not compiled.
//
// So compile it. Each fenced ```go block is extracted, wrapped if it is a
// bare snippet, and built against the local module. A failure names the
// README line the block starts on.
//
// This is the same class of defect as a CLI reference describing commands
// that do not exist: docs drift toward what USED to be true, and only a
// machine reading them notices.

// A fence, optionally preceded by a skip directive that must state a reason.
// Same idiom as the Python client's doc tests. An exemption without a reason is
// how a suite quietly stops covering anything, so the reason is required and
// its absence is a test failure rather than a silent skip.
var goFence = regexp.MustCompile(
	"(?s)(?:<!--\\s*doctest:\\s*skip([^>]*?)-->\\s*\\n)?```go\\n(.*?)```")

// readmeBlocks returns each Go fence with the README line it starts on.
func readmeBlocks(t *testing.T) []struct {
	Line int
	Code string
} {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	text := string(raw)

	var out []struct {
		Line int
		Code string
	}
	for _, m := range goFence.FindAllStringSubmatchIndex(text, -1) {
		if m[2] >= 0 {
			reason := strings.TrimSpace(text[m[2]:m[3]])
			if !strings.HasPrefix(reason, "because ") {
				t.Errorf("README.md: a doctest skip must say why: "+
					"`<!-- doctest: skip because ... -->`, got %q", reason)
			}
			continue
		}
		code := text[m[4]:m[5]]
		line := strings.Count(text[:m[0]], "\n") + 1
		out = append(out, struct {
			Line int
			Code string
		}{Line: line, Code: code})
	}
	return out
}

func TestReadmeGoExamplesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("compiling README examples invokes the toolchain")
	}

	blocks := readmeBlocks(t)
	if len(blocks) == 0 {
		t.Fatal("no ```go blocks found in README.md — either the README lost " +
			"its examples or this extractor stopped matching them, and in both " +
			"cases the examples are no longer being checked")
	}

	modDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving module dir: %v", err)
	}

	for i, b := range blocks {
		t.Run(fmt.Sprintf("README.md:%d", b.Line), func(t *testing.T) {
			src := asCompilableProgram(b.Code)

			dir := t.TempDir()
			write(t, filepath.Join(dir, "main.go"), src)
			write(t, filepath.Join(dir, "go.mod"), fmt.Sprintf(`module readmeexample%d

go 1.23

require github.com/gnarl-dev/go-client v0.0.0

replace github.com/gnarl-dev/go-client => %s
`, i, modDir))

			// Reuse the parent module's resolved dependencies rather than
			// reaching the network: a doc test must not be the thing that
			// fails when a proxy is down.
			copyFile(t, filepath.Join(modDir, "go.sum"), filepath.Join(dir, "go.sum"))

			cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "out"), ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("the example starting at README.md:%d does not compile:\n%s\n"+
					"--- source ---\n%s\n"+
					"A README example that does not build is worse than no example: "+
					"a reader assumes it is current and copies it.",
					b.Line, out, numbered(src))
			}
		})
	}
}

// asCompilableProgram turns a fence into something `go build` accepts.
//
// A README shows two kinds of block: a whole program, and a bare snippet that
// assumes a client and a context already exist. Wrapping the second kind is
// what lets the README stay readable while still being checked — the
// alternative is padding every example with boilerplate nobody wants to read.
func asCompilableProgram(code string) string {
	if strings.Contains(code, "package main") {
		return code
	}
	return `package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	gnarl "github.com/gnarl-dev/go-client"
)

// Referenced by snippets so the wrapper does not have to guess which of
// these each one uses.
var (
	_ = context.Background
	_ = errors.Is
	_ = log.Printf
	_ = os.Getenv
	_ = time.Sleep
	_ = gnarl.New
)

func main() {
	ctx := context.Background()
	_ = ctx
	c, err := gnarl.New("http://localhost:8080")
	if err != nil {
		log.Fatal(err)
	}
	_ = c
	schema := gnarl.NewSchema(map[string]gnarl.Field{"name": gnarl.KeywordField()})
	_ = schema

	if err := snippet(ctx, c, schema); err != nil {
		log.Fatal(err)
	}
}

// Returns an error so a snippet may end in ` + "`return err`" + `, which is how
// these fragments are actually written — inside a function that propagates.
func snippet(ctx context.Context, c *gnarl.Client, schema gnarl.Schema) error {
	_, _, _ = ctx, c, schema
` + code + `
	return nil
}
`
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		return // no go.sum yet is not fatal
	}
	write(t, to, string(b))
}

func numbered(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		fmt.Fprintf(&b, "%4d | %s\n", i+1, line)
	}
	return b.String()
}
