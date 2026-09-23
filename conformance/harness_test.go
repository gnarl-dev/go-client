// Package conformance runs this client against a REAL Gnarl node.
//
// Compiling proves the types line up with the description. It does not prove
// the description matches the server, and that gap is where client bugs live:
// a field renamed on the wire, an error shape that differs by route, a query
// the node rejects for a reason the spec never mentions. Every test in this
// package therefore drives real HTTP against a real node and asserts on what
// comes back.
//
// Running it:
//
//	go test ./conformance/...
//
// The harness finds a node in this order, and says exactly what to do if it
// cannot find one:
//
//  1. $GNARL_TEST_NODE — the address of a node you already have running.
//  2. $GNARL_BINARY — a `lucenia` binary the harness starts and stops itself.
//  3. a `lucenia` binary in the sibling lucenia checkout's target dir.
//
// A node runs without a JVM on the native engine, so the harness needs no
// Java toolchain.
package conformance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lucenia/gnarl-go"
)

// client is the shared client for the package, set up by TestMain.
var client *gnarl.Client

// skipReason is non-empty when no node could be found. Tests skip with it
// rather than failing: a developer without a node built should not see red.
var skipReason string

func TestMain(m *testing.M) {
	addr, stop, reason := startNode()
	skipReason = reason
	if reason == "" {
		c, err := gnarl.New(addr, gnarl.WithInsecureSkipVerify())
		if err != nil {
			fmt.Fprintf(os.Stderr, "conformance: building client: %v\n", err)
			os.Exit(1)
		}
		client = c
	}
	code := m.Run()
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// need returns the client, or skips the test with instructions.
func need(t *testing.T) *gnarl.Client {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
	return client
}

func startNode() (addr string, stop func(), skip string) {
	if a := os.Getenv("GNARL_TEST_NODE"); a != "" {
		return a, nil, ""
	}

	bin := os.Getenv("GNARL_BINARY")
	if bin == "" {
		for _, p := range []string{
			"../../lucenia/rust/target/release/lucenia",
			"../../lucenia/rust/target/debug/lucenia",
		} {
			if abs, err := filepath.Abs(p); err == nil {
				if st, err := os.Stat(abs); err == nil && !st.IsDir() {
					bin = abs
					break
				}
			}
		}
	}
	if bin == "" {
		return "", nil, "no node available. Either point $GNARL_TEST_NODE at a " +
			"running node, or set $GNARL_BINARY to a `lucenia` binary " +
			"(cargo build -p luceniad --bin lucenia)."
	}

	port, err := freePort()
	if err != nil {
		return "", nil, fmt.Sprintf("could not reserve a port: %v", err)
	}
	dir, err := os.MkdirTemp("", "gnarl-conformance-")
	if err != nil {
		return "", nil, fmt.Sprintf("could not make a data dir: %v", err)
	}

	// --single-node keeps this node off any real mesh: a conformance run must
	// not discover a developer's cluster, join it, and then assert on data it
	// does not own. --no-tls keeps the harness free of certificate handling.
	cmd := exec.Command(bin, "start",
		"--port", fmt.Sprint(port),
		"--data-dir", dir,
		"--single-node",
		"--no-tls",
		"--headless",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Sprintf("could not start %s: %v", bin, err)
	}

	stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		os.RemoveAll(dir)
	}

	addr = fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitReady(addr, 60*time.Second); err != nil {
		stop()
		return "", nil, fmt.Sprintf("node at %s never became ready: %v", addr, err)
	}
	return addr, stop, ""
}

// waitReady polls until the node answers, rather than sleeping a guessed
// interval. A fixed sleep is a bet on how fast the machine is: it passes on a
// laptop and fails on a loaded CI runner, and the failure looks like a product
// defect rather than a slow start.
func waitReady(addr string, within time.Duration) error {
	c, err := gnarl.New(addr, gnarl.WithTimeout(2*time.Second))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := c.Ping(ctx)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("after %v: %w", within, last)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tempIndex creates an index unique to this test and removes it afterwards.
//
// Unique per test because the package's tests share one node and Go runs them
// in one process: a shared index name turns an unrelated test's cleanup into
// this test's missing data.
func tempIndex(t *testing.T, c *gnarl.Client, schema gnarl.Schema) string {
	t.Helper()
	// Index names are capped at 64 characters by the server, and a Go test
	// name plus a nanosecond stamp overruns that easily — so hash the test
	// name rather than embedding it. Unique per test because the package
	// shares one node and Go runs its tests in one process: a shared index
	// name turns an unrelated test's cleanup into this test's missing data.
	sum := sha256.Sum256([]byte(t.Name()))
	name := fmt.Sprintf("conf-%x-%d", sum[:4], time.Now().UnixNano()%1e9)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.CreateIndex(ctx, name, schema); err != nil {
		t.Fatalf("creating index %q: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.DeleteIndex(ctx, name)
	})
	return name
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}
