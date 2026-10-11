package gnarl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// isolate gives a test a clean view of everything New("") reads: no
// $GNARL_URL, no $LUCENIA_DATA_DIR, and a HOME with nothing in it. Without
// this the test would read the developer's own ~/.lucenia.
func isolate(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvURL, "")
	t.Setenv(EnvDataDir, "")
	return home
}

func writeEndpoint(t *testing.T, dataDir, body string) {
	t.Helper()
	dir := filepath.Join(dataDir, "runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func baseOf(t *testing.T, addr string) string {
	t.Helper()
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New(%q): %v", addr, err)
	}
	return c.baseURL.String()
}

func TestNewWithNothingConfiguredUsesTheLocalApp(t *testing.T) {
	isolate(t)
	if got := baseOf(t, ""); got != "http://127.0.0.1:43300" {
		t.Errorf("base = %q, want the Gnarly app's loopback address", got)
	}
}

func TestNewReadsTheEndpointInLuceniaDataDir(t *testing.T) {
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"127.0.0.1","port":51234,"pid":7}`)
	if got := baseOf(t, ""); got != "http://127.0.0.1:51234" {
		t.Errorf("base = %q", got)
	}
}

func TestNewFallsBackToTheEndpointUnderHome(t *testing.T) {
	home := isolate(t)
	writeEndpoint(t, filepath.Join(home, ".lucenia"), `{"scheme":"http","host":"localhost","port":40001}`)
	if got := baseOf(t, ""); got != "http://localhost:40001" {
		t.Errorf("base = %q", got)
	}
}

func TestLuceniaDataDirWinsOverHome(t *testing.T) {
	home := isolate(t)
	writeEndpoint(t, filepath.Join(home, ".lucenia"), `{"scheme":"http","host":"127.0.0.1","port":40001}`)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"127.0.0.1","port":40002}`)
	if got := baseOf(t, ""); got != "http://127.0.0.1:40002" {
		t.Errorf("base = %q, want $LUCENIA_DATA_DIR's endpoint", got)
	}
}

func TestGnarlURLWinsOverTheRecordedEndpoint(t *testing.T) {
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"127.0.0.1","port":40002}`)
	t.Setenv(EnvURL, "https://node.example.com")
	if got := baseOf(t, ""); got != "https://node.example.com" {
		t.Errorf("base = %q", got)
	}
	// And an explicit address wins over both.
	if got := baseOf(t, "https://other.example.com"); got != "https://other.example.com" {
		t.Errorf("base = %q", got)
	}
}

// A recorded endpoint is a local hint. Plain HTTP to another machine would
// be a downgrade nobody chose, so it is skipped for the next candidate.
func TestARecordedPlainHTTPEndpointOffLoopbackIsSkipped(t *testing.T) {
	home := isolate(t)
	writeEndpoint(t, filepath.Join(home, ".lucenia"), `{"scheme":"http","host":"127.0.0.1","port":40001}`)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"192.168.1.20","port":43300}`)
	if got := baseOf(t, ""); got != "http://127.0.0.1:40001" {
		t.Errorf("base = %q, want the non-loopback http hint skipped", got)
	}
}

func TestARecordedHTTPSEndpointMayBeAnyHost(t *testing.T) {
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"https","host":"192.168.1.20","port":43300}`)
	if got := baseOf(t, ""); got != "https://192.168.1.20:43300" {
		t.Errorf("base = %q", got)
	}
}

func TestARecordedIPv6EndpointIsBracketed(t *testing.T) {
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"::1","port":43300}`)
	if got := baseOf(t, ""); got != "http://[::1]:43300" {
		t.Errorf("base = %q", got)
	}
}

func TestAnUnusableEndpointFileIsSkipped(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `{"scheme":`,
		"bad scheme": `{"scheme":"ftp","host":"127.0.0.1","port":1}`,
		"no host":    `{"scheme":"http","host":"","port":1}`,
		"port 0":     `{"scheme":"http","host":"127.0.0.1","port":0}`,
		"port big":   `{"scheme":"http","host":"127.0.0.1","port":70000}`,
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			dd := t.TempDir()
			t.Setenv(EnvDataDir, dd)
			writeEndpoint(t, dd, body)
			if got := baseOf(t, ""); got != DefaultLocalURL {
				t.Errorf("base = %q, want the default", got)
			}
		})
	}
}

func TestAConfiguredHTTPSAddressIsNeverDowngraded(t *testing.T) {
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	writeEndpoint(t, dd, `{"scheme":"http","host":"127.0.0.1","port":43300}`)
	t.Setenv(EnvURL, "localhost:43300") // scheme-less: https, as always
	if got := baseOf(t, ""); got != "https://localhost:43300" {
		t.Errorf("base = %q", got)
	}
}

// End to end: New("") reaches the server the recorded endpoint names.
func TestNewWithNoAddressReachesTheRecordedNode(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path == "/v1/node/status"
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	isolate(t)
	dd := t.TempDir()
	t.Setenv(EnvDataDir, dd)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeEndpoint(t, dd, `{"scheme":"http","host":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)
	c, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("the recorded node was not reached")
	}
}
