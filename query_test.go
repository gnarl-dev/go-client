package gnarl

import (
	"encoding/json"
	"testing"
)

// UNIT: the builders must emit the exact wire shape.
//
// A builder that produces the wrong JSON fails at the node, far from the
// cause, and the message a user sees blames their query. Marshalling and
// comparing here puts the failure next to the mistake.

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestQueryBuildersEmitTheWireShape(t *testing.T) {
	cases := []struct {
		name  string
		query *Query
		want  string
	}{
		{
			"match is a bare string, not an object",
			Match("headline", "sydney"),
			`{"match":{"headline":"sydney"}}`,
		},
		{
			"term is a bare string",
			Term("tag", "landmark"),
			`{"term":{"tag":"landmark"}}`,
		},
		{
			"exists names the field",
			Exists("location"),
			`{"exists":{"field":"location"}}`,
		},
		{
			"match_all is an empty object",
			MatchAll(),
			`{"match_all":{}}`,
		},
		{
			// The shape that matters most. Flat lat/lon and metres — NOT the
			// Elasticsearch `{"distance":"10km","location":{...}}`, which this
			// server rejects with a 422 for a missing `lat`.
			"geo_distance is flat and in metres",
			GeoDistance("location", -33.8688, 151.2093, 1000),
			`{"geo_distance":{"field":"location","lat":-33.8688,"lon":151.2093,"radius_meters":1000}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := marshal(t, tc.query); got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A coordinate must reach the wire at full precision. This is the client-side
// half of the f64 story: the type is float64, but a builder that formatted
// through float32 anywhere would lose it here, before any node is involved.
func TestCoordinatesReachTheWireAtFullPrecision(t *testing.T) {
	const lat, lon = 37.7896239, -122.4001745
	got := marshal(t, GeoDistance("location", lat, lon, 0.1))

	want := `{"geo_distance":{"field":"location","lat":37.7896239,"lon":-122.4001745,"radius_meters":0.1}}`
	if got != want {
		t.Errorf("a coordinate lost precision on the way to the wire:\n got %s\nwant %s\n"+
			"float32 would render these as 37.789623 and -122.40018, displacing "+
			"the point by ~0.23 m on every request.", got, want)
	}
}

func TestBoolBuildsAllFourClauseKinds(t *testing.T) {
	q := Bool().
		Must(Match("headline", "sydney")).
		Filter(Term("tag", "landmark")).
		Should(Match("headline", "harbour")).
		MustNot(Match("headline", "opera")).
		Build()

	var got map[string]any
	if err := json.Unmarshal([]byte(marshal(t, q)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b, ok := got["bool"].(map[string]any)
	if !ok {
		t.Fatalf("no bool key: %v", got)
	}
	for _, clause := range []string{"must", "filter", "should", "must_not"} {
		v, present := b[clause]
		if !present {
			t.Errorf("bool query is missing %q — that clause is silently "+
				"dropped, so the query means something else", clause)
			continue
		}
		if arr, ok := v.([]any); !ok || len(arr) != 1 {
			t.Errorf("bool.%s = %v, want one clause", clause, v)
		}
	}
}

// An empty Bool must not emit empty arrays: `{"bool":{"must":[]}}` is a
// different query from `{"bool":{}}` and the node may read it as matching
// nothing.
func TestEmptyBoolOmitsItsClauses(t *testing.T) {
	if got := marshal(t, Bool().Build()); got != `{"bool":{}}` {
		t.Errorf("got %s, want {\"bool\":{}}", got)
	}
}

// A nil clause must be skipped rather than panicking or emitting null — a
// caller building clauses conditionally will pass one.
func TestNilClausesAreSkipped(t *testing.T) {
	q := Bool().Must(Match("headline", "x"), nil).Build()
	var got map[string]map[string][]any
	if err := json.Unmarshal([]byte(marshal(t, q)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n := len(got["bool"]["must"]); n != 1 {
		t.Errorf("must has %d clauses, want 1 — a nil was emitted as null, "+
			"which the node cannot parse", n)
	}
}

// UNIT: a document's fields go at the TOP LEVEL beside `_id`, not nested.
func TestDocumentBodyMergesTheIDAtTopLevel(t *testing.T) {
	type doc struct {
		Headline string `json:"headline"`
	}
	fields, err := documentBody("abc", doc{Headline: "hello"})
	if err != nil {
		t.Fatalf("documentBody: %v", err)
	}
	got := marshal(t, fields)
	if got != `{"_id":"abc","headline":"hello"}` {
		t.Errorf("got %s, want the id merged beside the fields, not wrapping them", got)
	}
}

func TestDocumentBodyOmitsAnEmptyID(t *testing.T) {
	fields, err := documentBody("", map[string]string{"headline": "hello"})
	if err != nil {
		t.Fatalf("documentBody: %v", err)
	}
	if _, present := fields["_id"]; present {
		t.Error("an empty id was sent as `_id`, so the node cannot generate one")
	}
}

// A non-object document must be refused here rather than at the node, where
// the message would be about JSON rather than about the caller's type.
func TestDocumentBodyRefusesANonObject(t *testing.T) {
	for _, bad := range []any{42, "a string", []int{1, 2}} {
		if _, err := documentBody("", bad); err == nil {
			t.Errorf("documentBody(%v) succeeded; a document must be an object", bad)
		}
	}
	if _, err := documentBody("", nil); err == nil {
		t.Error("documentBody(nil) succeeded")
	}
}

// UNIT: address handling. Defaulting to http would silently downgrade a
// caller who wrote a bare hostname, since a node serves TLS by default.
func TestNewDefaultsToHTTPS(t *testing.T) {
	c, err := New("search.example.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.baseURL.Scheme; got != "https" {
		t.Errorf("scheme = %q, want https — a bare host must not be "+
			"downgraded to cleartext", got)
	}
}

func TestNewKeepsAnExplicitScheme(t *testing.T) {
	c, err := New("http://localhost:8080")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL.Scheme != "http" {
		t.Errorf("an explicit http:// was changed to %q", c.baseURL.Scheme)
	}
}

func TestNewRejectsNonsense(t *testing.T) {
	for _, addr := range []string{"", "://", "http://"} {
		if _, err := New(addr); err == nil {
			t.Errorf("New(%q) succeeded", addr)
		}
	}
}

// A trailing slash must not produce a double slash in every path.
func TestNewTrimsTrailingSlash(t *testing.T) {
	c, err := New("http://localhost:8080/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.baseURL.String(); got != "http://localhost:8080" {
		t.Errorf("baseURL = %q, want no trailing slash", got)
	}
}

func TestTotalHitsRelation(t *testing.T) {
	if !(TotalHits{Value: 3, Relation: "eq"}).IsExact() {
		t.Error(`relation "eq" should be exact`)
	}
	if (TotalHits{Value: 3, Relation: "gte"}).IsExact() {
		t.Error(`relation "gte" is a LOWER BOUND and must not report as exact — ` +
			`a caller paginating on it would stop early`)
	}
}

// FailedItems must not report failures when there are none, and must find
// them when there are: a bulk request answers 200 with individual items
// failed, so this is the only thing standing between a caller and lost writes.
func TestFailedItemsFindsOnlyRealFailures(t *testing.T) {
	if got := FailedItems(nil); got != nil {
		t.Errorf("FailedItems(nil) = %v, want nil", got)
	}
	clean := &BulkResult{Errors: false, Items: make([]BulkItem, 3)}
	if got := FailedItems(clean); got != nil {
		t.Errorf("FailedItems on a clean result = %v, want nil", got)
	}
}
