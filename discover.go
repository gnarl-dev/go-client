package gnarl

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultLocalURL is the address New uses when nothing else names a node: the
// Gnarly app's local Console, which listens on loopback over plain HTTP.
const DefaultLocalURL = "http://127.0.0.1:43300"

// EnvDataDir names the node's data directory. New reads the endpoint the node
// recorded there before falling back to ~/.lucenia, the node's own default.
const EnvDataDir = "LUCENIA_DATA_DIR"

// localEndpoint is <data_dir>/runtime/endpoint.json, which a node writes when
// it starts to say where a client on this machine should reach it.
type localEndpoint struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	PID    int    `json:"pid"`
}

// endpointFiles lists the endpoint.json files to try, in order:
// $LUCENIA_DATA_DIR first, then ~/.lucenia — the same order the gnarl CLI uses.
func endpointFiles() []string {
	var out []string
	if d := os.Getenv(EnvDataDir); d != "" {
		out = append(out, filepath.Join(d, "runtime", "endpoint.json"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".lucenia", "runtime", "endpoint.json"))
	}
	return out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// readEndpoint returns the base URL a recorded endpoint names, or "" if the
// file is missing, unreadable, malformed or unsafe to use.
//
// Plain HTTP is accepted only on loopback: the file is a local hint, and a
// hint that sent a client to a cleartext address off this machine would be a
// downgrade nobody chose.
func readEndpoint(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var ep localEndpoint
	if json.Unmarshal(b, &ep) != nil {
		return ""
	}
	host := strings.Trim(ep.Host, "[]")
	if host == "" || ep.Port < 1 || ep.Port > 65535 {
		return ""
	}
	switch ep.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(host) {
			return ""
		}
	default:
		return ""
	}
	return ep.Scheme + "://" + net.JoinHostPort(host, strconv.Itoa(ep.Port))
}

// discoverLocal finds the node on this machine: the first usable recorded
// endpoint, else DefaultLocalURL. It reads files only; it never dials.
func discoverLocal() string {
	for _, p := range endpointFiles() {
		if u := readEndpoint(p); u != "" {
			return u
		}
	}
	return DefaultLocalURL
}

// resolveAddr applies New's order: the explicit address, then $GNARL_URL,
// then the node on this machine.
func resolveAddr(addr string) string {
	if addr != "" {
		return addr
	}
	if env := os.Getenv(EnvURL); env != "" {
		return env
	}
	return discoverLocal()
}
