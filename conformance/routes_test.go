package conformance

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Every route the vendored description declares must be served by the node.
//
// This is the half of spec freshness that needs no access to the server's
// repository. The exact diff against the server's own description needs a
// token for an internal repository; this needs only a released node. It
// catches the failure that matters most to a caller — this client describing,
// and wrapping, an operation the node does not have — which is what a stale
// or over-eager vendored copy produces.
//
// A route is MISSING when the node answers 404 with no body or with type
// route_not_found (no route claimed the path), or 405 (the path exists, the
// method does not). Anything else — 400, 404 index_not_found, 415, 422, even
// 500 — means a handler ran, which is all this asserts.
//
// Requests go out with no body and with placeholder path values that name
// nothing, so a handler that does run refuses or misses rather than acting.
func TestEveryDescribedRouteIsServed(t *testing.T) {
	c := need(t)
	// A handler that runs on a placeholder may still create what it names —
	// promoting an absent namespace, say. Remove anything it left.
	t.Cleanup(func() {
		_ = c.Namespace("conf-route-probe-absent").Delete(cleanupCtx())
		_ = c.DeleteIndex(cleanupCtx(), "conf-route-probe-absent")
	})

	raw, err := os.ReadFile("../internal/oas/openapi.yaml")
	if err != nil {
		t.Fatalf("reading the vendored description: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the vendored description: %v", err)
	}

	hc := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// The harness's own node, with its self-signed certificate.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	placeholder := strings.NewReplacer(
		"{name}", "conf-route-probe-absent",
		"{ns}", "conf-route-probe-absent",
		"{doc_id}", "absent",
		"{repo}", "conf-route-probe-absent",
		"{snapshot}", "absent",
		"{id}", "absent",
	)

	var paths []string
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Operations that answer a bodyless 404 BY DESIGN on the node the harness
	// starts, so the probe cannot tell them from a missing route. Each needs a
	// reason; the list is short on purpose.
	exempt := map[string]string{
		// A --single-node node answers no discovery at all, deliberately
		// with 404 rather than 403 (http_server.rs handle_discover). The
		// description does not declare that 404.
		"GET /v1/bootstrap/discover": "single-node scope disables discovery",
	}

	checked := 0
	for _, p := range paths {
		for method := range doc.Paths[p] {
			m := strings.ToUpper(method)
			switch m {
			case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD":
			default:
				continue // `parameters` and other path-level keys
			}
			url := nodeAddr + placeholder.Replace(p)
			t.Run(m+" "+p, func(t *testing.T) {
				if why, ok := exempt[m+" "+p]; ok {
					t.Skip(why)
				}
				req, err := http.NewRequest(m, url, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := hc.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", m, url, err)
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
				if missing, why := unrouted(resp.StatusCode, body); missing {
					t.Errorf("%s %s is in the vendored description but the node %s. "+
						"Either the description is ahead of this node, or the route was "+
						"removed — this client wraps it either way.", m, p, why)
				}
			})
			checked++
		}
	}
	if checked < 40 {
		t.Errorf("checked only %d operations — the description parser has stopped "+
			"finding them, so this test is no longer checking anything", checked)
	}
}

func unrouted(status int, body []byte) (bool, string) {
	switch status {
	case http.StatusMethodNotAllowed:
		return true, "serves the path but not the method (405)"
	case http.StatusNotFound:
		if len(strings.TrimSpace(string(body))) == 0 {
			return true, "answers a bodyless 404 — no route claimed it"
		}
		var env struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &env) == nil && env.Error.Type == "route_not_found" {
			return true, "answers route_not_found"
		}
	}
	return false, fmt.Sprint(status)
}
