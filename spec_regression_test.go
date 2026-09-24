package gnarl

// Regression tests against the VENDORED description itself.
//
// This client had no layer like this, and it cost something concrete: the
// description declared `SearchProfile` with `graph` required and no `fan_out`
// at all, while the server sends `fan_out` for every profiled query and
// `graph` only for a graph traversal. Go's generated layer decodes a missing
// required field to the zero value without complaining, so nothing here ever
// noticed — the Python client caught it on its first afternoon because
// pydantic refuses.
//
// A lenient decoder is a good property for compatibility and a bad one for
// detection. These tests supply the detection.

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("internal/oas/openapi.yaml")
	if err != nil {
		t.Fatalf("reading the vendored description: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the vendored description: %v", err)
	}
	return doc
}

func schema(t *testing.T, name string) map[string]any {
	t.Helper()
	doc := loadSpec(t)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	s, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("the description declares no schema %q", name)
	}
	return s
}

// Neither half of the profile is guaranteed.
//
// `graph` is emitted only for a graph traversal, so the ORDINARY response —
// any other query with profile:true — carries `fan_out` alone. Requiring
// `graph` made the common case a contract violation.
func TestSearchProfileRequiresNeitherHalf(t *testing.T) {
	s := schema(t, "SearchProfile")
	if _, present := s["required"]; present {
		t.Errorf("SearchProfile declares a required member; the server sends "+
			"both `graph` and `fan_out` conditionally: %v", s["required"])
	}
	props, _ := s["properties"].(map[string]any)
	for _, want := range []string{"graph", "fan_out"} {
		if _, ok := props[want]; !ok {
			t.Errorf("SearchProfile does not declare %q, which the server sends", want)
		}
	}
}

// The fan-out is what `--explain` exists to return. A client that cannot see
// which peer served which claim cannot audit a distributed answer.
func TestFanOutProfileCarriesTheAuditTrail(t *testing.T) {
	props, _ := schema(t, "FanOutProfile")["properties"].(map[string]any)
	for _, want := range []string{
		"nodes_contacted", "nodes_responded", "deadline_ms", "claims",
		"verified_claims", "unverified_claims", "failed_verification_claims",
		"local_claims",
	} {
		if _, ok := props[want]; !ok {
			t.Errorf("FanOutProfile is missing %q", want)
		}
	}
}

// Four verification states, and `unverified` is its own.
//
// "Cannot check" is not "check failed". In the node, collapsing those two
// destroyed data more than once, because the failed state WITHHOLDS
// documents. A client that flattens them re-creates the bug on the reading
// side, where it looks like loss with no server-side evidence.
func TestClaimRouteKeepsUnverifiedDistinctFromFailed(t *testing.T) {
	props, _ := schema(t, "ClaimRoute")["properties"].(map[string]any)
	verification, ok := props["verification"].(map[string]any)
	if !ok {
		t.Fatal("ClaimRoute declares no `verification`")
	}
	rawEnum, ok := verification["enum"].([]any)
	if !ok {
		t.Fatal("`verification` is not an enum, so a caller cannot switch on it")
	}
	got := map[string]bool{}
	for _, v := range rawEnum {
		got[v.(string)] = true
	}
	for _, want := range []string{"verified", "unverified", "failed", "local"} {
		if !got[want] {
			t.Errorf("the verification enum is missing %q", want)
		}
	}
	if len(got) != 4 {
		t.Errorf("expected exactly four verification states, got %v", got)
	}
}

// Coordinates are doubles, all the way down.
//
// Generating this client from a description with a bare `type: number`
// produced `Lat float32`, which displaces a survey-grade coordinate by about
// 0.23 m — enough to move a point across a boundary while every count-based
// test still passed.
func TestGeoCoordinatesAreDoubles(t *testing.T) {
	props, _ := schema(t, "GeoDistanceQuery")["properties"].(map[string]any)
	for _, field := range []string{"lat", "lon", "radius_meters"} {
		f, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("GeoDistanceQuery declares no %q", field)
		}
		if f["format"] != "double" {
			t.Errorf("%s lost `format: double` (got %v) — a generated client is "+
				"then free to pick float32 again", field, f["format"])
		}
	}
}

// The index-name rules must match the server's.
//
// Two defects this client found on its first afternoon: the description
// capped names at 128 where the server caps at 64, and required `^[a-z]`
// where the server accepts a leading digit. Accepting a name the server
// refuses is the worse direction — the error then arrives from the node with
// no client-side explanation.
func TestIndexNameRulesMatchTheServer(t *testing.T) {
	doc := loadSpec(t)
	components, _ := doc["components"].(map[string]any)
	params, _ := components["parameters"].(map[string]any)
	indexName, _ := params["IndexName"].(map[string]any)
	s, _ := indexName["schema"].(map[string]any)

	if s["maxLength"] != 64 {
		t.Errorf("IndexName maxLength is %v, the server enforces 64", s["maxLength"])
	}
	if s["pattern"] != "^[a-z0-9][a-z0-9_-]*$" {
		t.Errorf("IndexName pattern is %v; the server accepts a leading digit", s["pattern"])
	}
}

// The four reserved field names must be documented where a schema author will
// meet them. `title` is the one people reach for first.
func TestReservedFieldNamesAreDocumented(t *testing.T) {
	props, _ := schema(t, "IndexSchema")["properties"].(map[string]any)
	fields, _ := props["fields"].(map[string]any)
	described, _ := fields["description"].(string)
	for _, reserved := range []string{"`id`", "`version`", "`title`", "`canonical_url`"} {
		if !strings.Contains(described, reserved) {
			t.Errorf("the reserved field name %s is not documented", reserved)
		}
	}
}
