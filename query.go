package gnarl

import "github.com/gnarl-dev/go-client/internal/oas"

// Query builders.
//
// The wire DSL is a struct with one field set per query type, which is
// faithful to the description but awkward to write by hand — and easy to get
// wrong, because setting two fields is accepted by the type system and
// rejected by the node. These constructors make exactly one valid query each.

// Match is a full-text query. The text is analyzed with the field's analyzer.
func Match(field, text string) *Query {
	// The wire accepts either a bare string or an object; the generated type
	// is therefore a union, and the bare string is the form every node
	// understands.
	var v oas.Query_Match_AdditionalProperties
	if err := v.FromQueryMatch0(text); err != nil {
		panic("gnarl: Match: encoding a string cannot fail: " + err.Error())
	}
	m := map[string]oas.Query_Match_AdditionalProperties{field: v}
	return &Query{Match: &m}
}

// Term matches an exact, un-analyzed value.
//
// On an analyzed text field this usually matches nothing, because the indexed
// terms are the analyzer's output rather than the string you wrote. That is
// the single most common surprise in any search API; use Match for text.
//
// The value is a string because that is what the API accepts — a term lookup
// is a comparison against an indexed term, and indexed terms are text. Use
// Range for numeric and date comparisons.
func Term(field, value string) *Query {
	var v oas.Query_Term_AdditionalProperties
	if err := v.FromQueryTerm0(value); err != nil {
		panic("gnarl: Term: encoding a string cannot fail: " + err.Error())
	}
	m := map[string]oas.Query_Term_AdditionalProperties{field: v}
	return &Query{Term: &m}
}

// Exists matches documents where the field has at least one value.
func Exists(field string) *Query {
	return &Query{Exists: &struct {
		Field string `json:"field"`
	}{Field: field}}
}

// MatchAll matches every document.
func MatchAll() *Query {
	empty := map[string]any{}
	return &Query{MatchAll: &empty}
}

// GeoDistance matches points within radiusMeters of (lat, lon) on a geo_point
// field.
//
// The radius is METRES — a number, not a unit-suffixed string like "5km" —
// and the centre is flat lat/lon rather than a nested object. Both are places
// an Elasticsearch habit produces a request the node rejects.
//
// lat, lon and radiusMeters are float64 throughout. float32 would displace a
// survey-grade coordinate by roughly 0.23 m.
func GeoDistance(field string, lat, lon, radiusMeters float64) *Query {
	return &Query{GeoDistance: &oas.GeoDistanceQuery{
		Field:        field,
		Lat:          lat,
		Lon:          lon,
		RadiusMeters: radiusMeters,
	}}
}

// Knn is approximate nearest-neighbour search over a dense_vector field.
func Knn(field string, vector []float64, k int) *Query {
	return &Query{Knn: &oas.KnnQuery{Field: field, Vector: vector, K: k}}
}

// BoolQuery accumulates clauses for a boolean query.
//
// Must contributes to the score and is required; Filter is required but does
// not score (and is the cheaper choice); Should is optional; MustNot excludes.
type BoolQuery struct {
	must    []Query
	filter  []Query
	should  []Query
	mustNot []Query
}

// Bool starts a boolean query.
func Bool() *BoolQuery { return &BoolQuery{} }

// Must adds required, scoring clauses.
func (b *BoolQuery) Must(qs ...*Query) *BoolQuery {
	b.must = append(b.must, deref(qs)...)
	return b
}

// Filter adds required, non-scoring clauses. Prefer this over Must whenever
// the clause is a yes/no restriction rather than part of relevance.
func (b *BoolQuery) Filter(qs ...*Query) *BoolQuery {
	b.filter = append(b.filter, deref(qs)...)
	return b
}

// Should adds optional clauses.
func (b *BoolQuery) Should(qs ...*Query) *BoolQuery {
	b.should = append(b.should, deref(qs)...)
	return b
}

// MustNot adds excluding clauses.
func (b *BoolQuery) MustNot(qs ...*Query) *BoolQuery {
	b.mustNot = append(b.mustNot, deref(qs)...)
	return b
}

// Build finishes the boolean query.
func (b *BoolQuery) Build() *Query {
	bq := &oas.BoolQuery{}
	if len(b.must) > 0 {
		bq.Must = &b.must
	}
	if len(b.filter) > 0 {
		bq.Filter = &b.filter
	}
	if len(b.should) > 0 {
		bq.Should = &b.should
	}
	if len(b.mustNot) > 0 {
		bq.MustNot = &b.mustNot
	}
	return &Query{Bool: bq}
}

func deref(qs []*Query) []Query {
	out := make([]Query, 0, len(qs))
	for _, q := range qs {
		if q != nil {
			out = append(out, *q)
		}
	}
	return out
}

// ─── Schema helpers ─────────────────────────────────────────────────────────

// NewSchema builds an index schema from field definitions.
func NewSchema(fields map[string]Field) Schema {
	return Schema{Fields: fields}
}

// TextField is an analyzed, full-text-searchable field.
func TextField() Field { return Field{Type: oas.Text} }

// KeywordField is an exact-match field: not analyzed, suitable for Term,
// filtering and sorting.
func KeywordField() Field { return Field{Type: oas.Keyword} }

// GeoPointField holds a lat/lon point, queryable with GeoDistance.
func GeoPointField() Field { return Field{Type: oas.GeoPoint} }

// DenseVectorField holds an embedding of the given dimensionality.
func DenseVectorField(dimensions int) Field {
	return Field{Type: oas.DenseVector, Dimensions: &dimensions}
}

// GeoPoint is the wire shape of a geo_point value: flat lat/lon, float64.
type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}
