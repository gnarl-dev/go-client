package gnarl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// pagedNode serves total documents sorted by "n", honouring search_after.
func pagedNode(t *testing.T, total int) (*Client, *fakeNode) {
	t.Helper()
	return newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		var req struct {
			Size        int   `json:"size"`
			SearchAfter []any `json:"search_after"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		start := 0
		if len(req.SearchAfter) > 0 {
			start = int(req.SearchAfter[0].(float64)) + 1
		}
		var hits []string
		for i := start; i < total && len(hits) < req.Size; i++ {
			hits = append(hits, fmt.Sprintf(`{"_id":"d%d","sort":[%d,"d%d"]}`, i, i, i))
		}
		fmt.Fprintf(w, `{"hits":{"hits":[%s],"total":{"value":%d,"relation":"eq"}},
			"coverage":{"expected_claims":1,"served_claims":1,"skipped_claims":[]},"partial":false}`,
			strings.Join(hits, ","), total)
	})
}

func TestSearchAllWalksEveryPage(t *testing.T) {
	c, f := pagedNode(t, 25)
	var ids []string
	for hit, err := range c.SearchAll(context.Background(), "events", SearchRequest{
		Query: MatchAll(), Sort: []SortClause{SortAsc("n")}, Size: 10, From: 99,
	}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, hit.UnderscoreId)
	}
	if len(ids) != 25 || ids[0] != "d0" || ids[24] != "d24" {
		t.Errorf("walked %d hits: %v", len(ids), ids)
	}
	// 10 + 10 + 5, then the empty page that ends it.
	if f.count() != 4 {
		t.Errorf("node saw %d pages, want 4", f.count())
	}
	s := f.last(t)
	if after, _ := s.field(t, "search_after").([]any); len(after) != 2 || after[0] != float64(24) {
		t.Errorf("last page cursor = %v", s.Body["search_after"])
	}
	s.absent(t, "from")
	if sort, _ := s.field(t, "sort").([]any); len(sort) != 1 {
		t.Errorf("sort = %v", s.Body["sort"])
	}
}

// REGRESSION: a node can answer a page shorter than Size while rows remain.
// A walk that stopped on the first short page returned 9 of 23 rows against a
// real node, with no error.
func TestSearchAllDoesNotStopOnAShortPage(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		pages := []string{
			`{"_id":"a","sort":[1]},{"_id":"b","sort":[2]},{"_id":"c","sort":[3]}`,
			`{"_id":"d","sort":[4]}`, // short, but not the end
			`{"_id":"e","sort":[5]},{"_id":"f","sort":[6]}`,
			``,
		}
		fmt.Fprintf(w, `{"hits":{"hits":[%s],"total":{"value":6,"relation":"eq"}},
			"coverage":{"expected_claims":1,"served_claims":1,"skipped_claims":[]},"partial":false}`,
			pages[min(n-1, len(pages)-1)])
	})
	var ids []string
	for hit, err := range c.SearchAll(context.Background(), "e", SearchRequest{Sort: []SortClause{SortDesc("n")}, Size: 3}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, hit.UnderscoreId)
	}
	if strings.Join(ids, "") != "abcdef" || f.count() != 4 {
		t.Errorf("walked %v in %d pages; want abcdef in 4", ids, f.count())
	}
}

func TestSearchAllStopsWhenTheCallerDoes(t *testing.T) {
	c, f := pagedNode(t, 1000)
	n := 0
	for range c.SearchAll(context.Background(), "e", SearchRequest{Sort: []SortClause{SortAsc("n")}, Size: 10}) {
		n++
		if n == 15 {
			break
		}
	}
	if f.count() != 2 {
		t.Errorf("node saw %d pages after the caller stopped at 15 hits", f.count())
	}
}

func TestSearchAllRequiresASort(t *testing.T) {
	c, f := pagedNode(t, 5)
	for _, err := range c.SearchAll(context.Background(), "e", SearchRequest{Query: MatchAll()}) {
		if err == nil {
			t.Fatal("a walk with no sort yielded a hit")
		}
	}
	if f.count() != 0 {
		t.Error("an unsortable walk reached the node")
	}
}

func TestSearchAllSurfacesAnErrorOnce(t *testing.T) {
	c, _ := replying(t, 404, `{"error":{"type":"index_not_found","reason":"no"}}`)
	var errs []error
	for _, err := range c.SearchAll(context.Background(), "gone", SearchRequest{Sort: []SortClause{SortAsc("n")}}) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrNotFound) {
		t.Errorf("got %v", errs)
	}
}

// A hit without sort values is a page we cannot resume from: an error, not a
// silent end that reads as "that was everything".
func TestSearchAllRefusesToEndSilentlyWithoutACursor(t *testing.T) {
	c, _ := replying(t, 200, `{"hits":{"hits":[{"_id":"a"}],"total":{"value":5,"relation":"eq"}},
		"coverage":{"expected_claims":1,"served_claims":1,"skipped_claims":[]},"partial":false}`)
	var last error
	n := 0
	for _, err := range c.SearchAll(context.Background(), "e", SearchRequest{Sort: []SortClause{SortAsc("n")}, Size: 1}) {
		n++
		last = err
	}
	if n != 2 || last == nil {
		t.Errorf("got %d yields ending in %v; want a hit then an error", n, last)
	}
}

func TestBulkChunkedSplitsAndMerges(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		var req struct {
			Documents []map[string]any `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var items []string
		for _, d := range req.Documents {
			status := 201
			errBody := ""
			if d["_id"] == "d3" {
				status = 400
				errBody = `,"error":{"type":"validation_error","reason":"bad"}`
			}
			items = append(items, fmt.Sprintf(`{"_id":%q,"status":%d%s}`, d["_id"], status, errBody))
		}
		ack := "visible_for_search"
		if n == 2 {
			ack = "accepted"
		}
		fmt.Fprintf(w, `{"ack":%q,"errors":%v,"items":[%s]}`, ack,
			strings.Contains(strings.Join(items, ""), "error"), strings.Join(items, ","))
	})
	docs := make([]BulkDoc, 7)
	for i := range docs {
		docs[i] = BulkDoc{ID: fmt.Sprintf("d%d", i), Document: map[string]int{"i": i}}
	}
	res, err := c.BulkChunked(context.Background(), "i", docs, 3)
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 3 {
		t.Errorf("sent %d batches, want 3 (3+3+1)", f.count())
	}
	if len(res.Items) != 7 || res.Items[6].UnderscoreId != "d6" {
		t.Errorf("merged items = %+v", res.Items)
	}
	if !res.Errors || len(FailedItems(res)) != 1 || FailedItems(res)[0].UnderscoreId != "d3" {
		t.Errorf("a failed item in the middle batch was lost: %+v", res)
	}
	if res.Ack != AckAccepted {
		t.Errorf("Ack = %q; the weakest batch was accepted", res.Ack)
	}
}

func TestBulkChunkedStopsAtAFailedRequest(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 2 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"type":"validation_error","reason":"bad batch"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ack":"accepted","errors":false,"items":[{"_id":"x","status":201},{"_id":"y","status":201}]}`))
	})
	docs := make([]BulkDoc, 6)
	for i := range docs {
		docs[i] = BulkDoc{Document: map[string]int{"i": i}}
	}
	res, err := c.BulkChunked(context.Background(), "i", docs, 2)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("got %v", err)
	}
	if res == nil || len(res.Items) != 2 {
		t.Errorf("what the first batch accepted was not returned: %+v", res)
	}
	if f.count() != 2 {
		t.Errorf("sent %d batches after a failure, want to stop at 2", f.count())
	}
}

func TestSortBuildersEmitTheWireShape(t *testing.T) {
	for _, tc := range []struct {
		c    SortClause
		want string
	}{
		{SortAsc("ts"), `{"ts":{"order":"asc"}}`},
		{SortDesc("ts"), `{"ts":{"order":"desc"}}`},
	} {
		got, err := json.Marshal(tc.c)
		if err != nil || string(got) != tc.want {
			t.Errorf("got %s, %v; want %s", got, err, tc.want)
		}
	}
}
