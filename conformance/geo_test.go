package conformance

import (
	"errors"
	"math"
	"testing"

	"github.com/lucenia/gnarl-go"
)

// Landmarks with real coordinates, so the distances asserted here mean
// something rather than being arbitrary numbers that happen to pass.
var (
	sydney    = gnarl.GeoPoint{Lat: -33.8688, Lon: 151.2093}
	melbourne = gnarl.GeoPoint{Lat: -37.8136, Lon: 144.9631}
	london    = gnarl.GeoPoint{Lat: 51.5074, Lon: -0.1278}
)

type place struct {
	Name     string         `json:"name"`
	Location gnarl.GeoPoint `json:"location"`
}

func seedPlaces(t *testing.T, c *gnarl.Client) string {
	t.Helper()
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"name":     gnarl.KeywordField(),
		"location": gnarl.GeoPointField(),
	}))
	_, err := c.BulkWithIDs(ctx(t), name, []gnarl.BulkDoc{
		{ID: "sydney", Document: place{Name: "sydney", Location: sydney}},
		{ID: "melbourne", Document: place{Name: "melbourne", Location: melbourne}},
		{ID: "london", Document: place{Name: "london", Location: london}},
	})
	if err != nil {
		t.Fatalf("seeding places: %v", err)
	}
	searchable(t, c, name, gnarl.MatchAll(), 3)
	return name
}

// GeoDistance must return the points inside the radius and only those.
//
// This is the test that matters most for the builder, because the shape it
// produces is the one an Elasticsearch habit gets wrong: the centre is flat
// lat/lon rather than a nested `location` object, and the radius is a number
// of METRES rather than a string like "1000km". The wrong shape does not
// return zero hits — it is rejected with a 422 for a missing `lat`, which is
// at least loud. Confirming the RIGHT shape works is what stops someone
// "fixing" the builder toward the familiar-but-wrong one.
func TestGeoDistanceReturnsPointsInsideRadius(t *testing.T) {
	c := need(t)
	name := seedPlaces(t, c)

	// ~713 km Sydney→Melbourne; London is ~17,000 km away.
	hits := searchable(t, c, name,
		gnarl.GeoDistance("location", sydney.Lat, sydney.Lon, 1_000_000), 2)

	ids := idsOf(hits)
	if !contains(ids, "sydney") {
		t.Errorf("the point AT the centre did not match: %v", ids)
	}
	if !contains(ids, "melbourne") {
		t.Errorf("Melbourne is ~713km away, inside a 1000km radius: %v", ids)
	}
	if contains(ids, "london") {
		t.Errorf("London is ~17000km away and must not match a 1000km "+
			"radius — the radius is not being applied: %v", ids)
	}
}

// A tight radius must exclude everything but the centre, or the query has
// degenerated into match-all and the test above would pass for the wrong
// reason.
func TestGeoDistanceRadiusIsActuallyApplied(t *testing.T) {
	c := need(t)
	name := seedPlaces(t, c)

	res, err := c.Search(ctx(t), name, gnarl.SearchRequest{
		Query: gnarl.GeoDistance("location", sydney.Lat, sydney.Lon, 10_000),
		Size:  50,
	})
	if err != nil {
		t.Fatalf("tight geo_distance: %v", err)
	}
	ids := idsOf(res.Hits)
	if len(ids) != 1 || ids[0] != "sydney" {
		t.Errorf("a 10km radius around Sydney returned %v, want only sydney", ids)
	}
}

// A coordinate must survive the client round trip at f64 precision.
//
// The generated types declared these as float32 until the description gained
// an explicit `format: double`, because OpenAPI leaves a bare `type: number`
// ambiguous and the generator resolved it as float32. float32 carries ~7
// significant digits, which displaces a survey-grade coordinate by ~0.23 m —
// silently, on every request a client makes.
//
// A 10 cm radius sits 22x below that error and 10x above the node's own
// quantisation grid (Lucene's 2^32 encoding, ~1.04 cm worst case), so it
// separates a correct client from a narrowing one. The control document,
// indexed at the f32-rounded coordinate, is what makes this a test rather
// than an assertion about arithmetic: if anything narrowed, both documents
// would collapse onto one point and both would match.
func TestCoordinatePrecisionSurvivesTheClient(t *testing.T) {
	c := need(t)

	// Deliberately full of significant digits.
	const lat, lon = 37.7896239, -122.4001745
	narrowedLat := float64(float32(lat))
	narrowedLon := float64(float32(lon))

	errM := math.Hypot(
		(lat-narrowedLat)*111132.0,
		(lon-narrowedLon)*111320.0*math.Cos(lat*math.Pi/180),
	)
	if errM <= 0.15 {
		t.Fatalf("this test separates f64 from f32 with a 10 cm radius, but "+
			"at this coordinate f32 is only %.3f m off — inside the radius, so "+
			"the control proves nothing. Pick a coordinate with more "+
			"significant digits.", errM)
	}

	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"name":     gnarl.KeywordField(),
		"location": gnarl.GeoPointField(),
	}))
	_, err := c.BulkWithIDs(ctx(t), name, []gnarl.BulkDoc{
		{ID: "exact", Document: place{
			Name: "exact", Location: gnarl.GeoPoint{Lat: lat, Lon: lon}}},
		{ID: "narrowed", Document: place{
			Name: "narrowed", Location: gnarl.GeoPoint{Lat: narrowedLat, Lon: narrowedLon}}},
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	searchable(t, c, name, gnarl.MatchAll(), 2)

	res, err := c.Search(ctx(t), name, gnarl.SearchRequest{
		Query: gnarl.GeoDistance("location", lat, lon, 0.10),
		Size:  50,
	})
	if err != nil {
		t.Fatalf("10cm geo_distance: %v", err)
	}
	ids := idsOf(res.Hits)

	if !contains(ids, "exact") {
		t.Errorf("a document indexed AT the query centre fell outside a 10 cm "+
			"radius: %v\nThe coordinate lost more than the node's ~1.04 cm "+
			"quantisation somewhere in this client. f32 would displace it by "+
			"%.3f m.", ids, errM)
	}
	if contains(ids, "narrowed") {
		t.Errorf("the control point — the same coordinate rounded to f32, "+
			"%.3f m away — matched a 10 cm radius: %v\nEither the radius is "+
			"not applied at this scale, or both coordinates were narrowed to "+
			"the same point.", errM, ids)
	}
}

// A geo_distance query against a non-geo field must be refused, not answered
// with an empty result. Zero hits is indistinguishable from "nothing matched",
// so a silent empty result sends the caller looking for missing data instead
// of a mapping mistake.
func TestGeoDistanceOnNonGeoFieldIsRefused(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"name": gnarl.KeywordField(),
	}))
	if _, err := c.IndexDocument(ctx(t), name, "x", map[string]any{"name": "x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	searchable(t, c, name, gnarl.MatchAll(), 1)

	res, err := c.Search(ctx(t), name, gnarl.SearchRequest{
		Query: gnarl.GeoDistance("name", 0, 0, 1000),
	})
	if err == nil {
		t.Fatalf("a geo_distance query on a keyword field was accepted and "+
			"returned %d hits. An empty result here reads as 'no matches' and "+
			"hides the mapping error.", len(res.Hits))
	}
	var e *gnarl.Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *gnarl.Error, got %T: %v", err, err)
	}
	if e.Reason == "" {
		t.Error("the refusal carries no reason, so the caller cannot tell " +
			"which field was wrong")
	}
	t.Logf("refused as expected: %s", e)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
