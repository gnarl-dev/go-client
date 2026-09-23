package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lucenia/gnarl-go"
)

// searchable polls until the query returns at least want hits.
//
// A write is acknowledged when it is DURABLE, not when it is SEARCHABLE.
// Asserting immediately after a write therefore races the commit, and the
// failure looks exactly like "search is broken" rather than "we asked too
// early". Waiting on the CONDITION is also usually faster than sleeping a
// worst-case interval, because the common case returns on the first probe.
func searchable(t *testing.T, c *gnarl.Client, index string, q *gnarl.Query, want int) []gnarl.Hit {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last []gnarl.Hit
	for {
		cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := c.Search(cx, index, gnarl.SearchRequest{Query: q, Size: 50})
		cancel()
		if err != nil {
			t.Fatalf("search on %q: %v", index, err)
		}
		last = res.Hits
		if len(last) >= want {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 30s, %q returned %d hits, wanted at least %d. "+
				"This waits on the condition, so exceeding the budget means the "+
				"documents genuinely did not become searchable.",
				index, len(last), want)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// ─── The node answers at all ────────────────────────────────────────────────

func TestNodeStatus(t *testing.T) {
	c := need(t)
	st, err := c.Status(ctx(t))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.NodeID == "" {
		t.Error("node reported an empty node_id")
	}
	if st.Mode == "" {
		t.Error("node reported an empty mode")
	}

	// Version is a DIFFERENT endpoint. This client once declared Version on
	// NodeStatus, which decoded as "" forever without ever failing — an
	// invented field is invisible until someone reads it and believes it.
	v, err := c.Version(ctx(t))
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v.Version == "" {
		t.Error("node reported an empty version")
	}
	t.Logf("node %s mode=%s running %s", st.NodeID, st.Mode, v.Version)
}

// ─── Index lifecycle ────────────────────────────────────────────────────────

func TestIndexLifecycle(t *testing.T) {
	c := need(t)
	schema := gnarl.NewSchema(map[string]gnarl.Field{
		"headline": gnarl.TextField(),
		"tag":      gnarl.KeywordField(),
	})
	name := tempIndex(t, c, schema)

	ok, err := c.IndexExists(ctx(t), name)
	if err != nil {
		t.Fatalf("IndexExists: %v", err)
	}
	if !ok {
		t.Fatal("an index we just created reports as absent")
	}

	got, err := c.GetSchema(ctx(t), name)
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}
	for _, f := range []string{"headline", "tag"} {
		if _, present := got.Fields[f]; !present {
			t.Errorf("schema is missing field %q; got %v", f, keys(got.Fields))
		}
	}

	list, err := c.ListIndexes(ctx(t))
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	if !containsIndex(list, name) {
		t.Errorf("ListIndexes did not include %q (returned %d indexes). "+
			"If this node holds more than one page of indexes, the cursor is "+
			"not being followed.", name, len(list))
	}
}

// A second create must be refused as a conflict, not silently accepted.
// Silently accepting is how a caller destroys a live index believing they
// created a fresh one.
func TestDuplicateIndexIsRefused(t *testing.T) {
	c := need(t)
	schema := gnarl.NewSchema(map[string]gnarl.Field{"headline": gnarl.TextField()})
	name := tempIndex(t, c, schema)

	err := c.CreateIndex(ctx(t), name, schema)
	if err == nil {
		t.Fatal("creating an index that already exists succeeded")
	}
	if !errors.Is(err, gnarl.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %#v (%v)", err, err)
	}
	var e *gnarl.Error
	if errors.As(err, &e) && e.Status != 409 {
		t.Errorf("want HTTP 409, got %d", e.Status)
	}
}

func TestMissingIndexIsNotFound(t *testing.T) {
	c := need(t)
	_, err := c.Search(ctx(t), "definitely-not-an-index-9f3c", gnarl.SearchRequest{
		Query: gnarl.MatchAll(),
	})
	if err == nil {
		t.Fatal("searching a nonexistent index succeeded")
	}
	if !errors.Is(err, gnarl.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// The envelope contract from the server side: both fields, always.
	var e *gnarl.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not a *gnarl.Error: %T", err)
	}
	if e.Type == "" {
		t.Error("error carries no Type — the envelope lost error.type")
	}
	if e.Reason == "" {
		t.Error("error carries no Reason — the envelope lost error.reason, " +
			"which a strict client requires")
	}
}

// ─── Documents ──────────────────────────────────────────────────────────────

type article struct {
	Headline string `json:"headline"`
	Tag      string `json:"tag"`
}

func TestDocumentRoundTrip(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"headline": gnarl.TextField(),
		"tag":      gnarl.KeywordField(),
	}))

	want := article{Headline: "the harbour bridge", Tag: "landmark"}
	id, err := c.IndexDocument(ctx(t), name, "doc-1", want)
	if err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	if id != "doc-1" {
		t.Errorf("node assigned id %q, we asked for %q — an explicit _id was "+
			"not honoured, so a caller's own keys do not survive", id, "doc-1")
	}

	var got article
	if err := c.GetDocument(ctx(t), name, "doc-1", &got); err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got != want {
		t.Errorf("round trip changed the document:\n got %+v\nwant %+v", got, want)
	}

	if err := c.DeleteDocument(ctx(t), name, "doc-1"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	// A delete is acknowledged when it is DURABLE, not when it is VISIBLE, so
	// the document stays readable for a moment afterwards — the same asymmetry
	// that makes a write readable only after commit. Measured: GET answers 200
	// immediately after DELETE returns 200, and 404 a few seconds later. So
	// poll for the condition; asserting immediately tests the commit interval
	// rather than the delete.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = c.GetDocument(ctx(t), name, "doc-1", &got)
		if errors.Is(err, gnarl.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("30s after a successful delete, GetDocument still returned "+
				"%v. The delete was acknowledged but never became visible.", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// And it must be the DOCUMENT that is missing, not the index. Both are
	// 404, and the server reported index_not_found for both until this was
	// separated — which told a caller their index had vanished when only one
	// key was absent.
	var e *gnarl.Error
	if errors.As(err, &e) && e.Type != "document_not_found" {
		t.Errorf("a missing document reported type %q, want document_not_found. "+
			"A caller cannot distinguish a deleted document from a deleted "+
			"index, which are opposite problems.", e.Type)
	}
}

func TestBulkReportsPerItemOutcomes(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"headline": gnarl.TextField(),
		"tag":      gnarl.KeywordField(),
	}))

	docs := []gnarl.BulkDoc{
		{ID: "a", Document: article{Headline: "alpha", Tag: "x"}},
		{ID: "b", Document: article{Headline: "beta", Tag: "y"}},
		{ID: "c", Document: article{Headline: "gamma", Tag: "x"}},
	}
	res, err := c.BulkWithIDs(ctx(t), name, docs)
	if err != nil {
		t.Fatalf("BulkWithIDs: %v", err)
	}
	if failed := gnarl.FailedItems(res); len(failed) > 0 {
		t.Fatalf("bulk reported %d failed items: %+v", len(failed), failed)
	}
	if len(res.Items) != len(docs) {
		t.Errorf("bulk returned %d item results for %d documents — a caller "+
			"matching results to inputs by position would misattribute them",
			len(res.Items), len(docs))
	}

	searchable(t, c, name, gnarl.MatchAll(), 3)

	n, err := c.Count(ctx(t), name)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
}

// ─── Search ─────────────────────────────────────────────────────────────────

func TestSearchMatchTermAndBool(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"headline": gnarl.TextField(),
		"tag":      gnarl.KeywordField(),
	}))
	_, err := c.BulkWithIDs(ctx(t), name, []gnarl.BulkDoc{
		{ID: "a", Document: article{Headline: "sydney harbour bridge", Tag: "landmark"}},
		{ID: "b", Document: article{Headline: "melbourne laneways", Tag: "street"}},
		{ID: "c", Document: article{Headline: "sydney opera house", Tag: "landmark"}},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	searchable(t, c, name, gnarl.MatchAll(), 3)

	t.Run("match is analyzed", func(t *testing.T) {
		hits := searchable(t, c, name, gnarl.Match("headline", "sydney"), 2)
		if ids := idsOf(hits); len(ids) != 2 {
			t.Errorf("match(title, sydney) = %v, want the two sydney docs", ids)
		}
	})

	t.Run("term is exact", func(t *testing.T) {
		hits := searchable(t, c, name, gnarl.Term("tag", "landmark"), 2)
		if ids := idsOf(hits); len(ids) != 2 {
			t.Errorf("term(tag, landmark) = %v, want two", ids)
		}
	})

	t.Run("bool combines", func(t *testing.T) {
		q := gnarl.Bool().
			Must(gnarl.Match("headline", "sydney")).
			Filter(gnarl.Term("tag", "landmark")).
			MustNot(gnarl.Match("headline", "opera")).
			Build()
		hits := searchable(t, c, name, q, 1)
		ids := idsOf(hits)
		if len(ids) != 1 || ids[0] != "a" {
			t.Errorf("bool query = %v, want exactly [a] — must/filter/must_not "+
				"are not all being applied", ids)
		}
	})
}

// Coverage is the product's distinguishing claim, so a client must surface it
// rather than quietly return whatever arrived.
func TestSearchReportsCoverage(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{
		"headline": gnarl.TextField(),
	}))
	if _, err := c.IndexDocument(ctx(t), name, "only", article{Headline: "one"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	searchable(t, c, name, gnarl.MatchAll(), 1)

	res, err := c.Search(ctx(t), name, gnarl.SearchRequest{
		Query:           gnarl.MatchAll(),
		RequireComplete: true,
	})
	if err != nil {
		t.Fatalf("a complete search on a single-node index was reported "+
			"incomplete: %v", err)
	}
	if res.Coverage.ExpectedClaims == 0 {
		t.Error("coverage.expected_claims is 0, so RequireComplete can never " +
			"detect a gap: every partial result would compare 0 >= 0 and pass")
	}
	if res.Coverage.ServedClaims != res.Coverage.ExpectedClaims {
		t.Errorf("served %d of %d claims on a healthy single node",
			res.Coverage.ServedClaims, res.Coverage.ExpectedClaims)
	}
	if res.Partial {
		t.Error("a single-node search reported partial=true")
	}
}

func idsOf(hits []gnarl.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.UnderscoreId)
	}
	return out
}

func keys(m map[string]gnarl.Field) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsIndex(list []gnarl.IndexInfo, name string) bool {
	for _, i := range list {
		if i.Name == name {
			return true
		}
	}
	return false
}
