package conformance

import (
	"fmt"
	"testing"

	gnarl "github.com/gnarl-dev/go-client"
)

// ─── Search after ───────────────────────────────────────────────────────────

func TestSearchAllWalksTheWholeIndexOnce(t *testing.T) {
	c := need(t)
	name := tempIndex(t, c, gnarl.NewSchema(map[string]gnarl.Field{"n": gnarl.LongField()}))

	const total = 23
	docs := make([]gnarl.BulkDoc, total)
	for i := range docs {
		docs[i] = gnarl.BulkDoc{ID: fmt.Sprintf("d%02d", i), Document: map[string]int{"n": i}}
	}
	res, err := c.BulkChunked(ctx(t), name, docs, 10)
	if err != nil {
		t.Fatalf("BulkChunked: %v", err)
	}
	if failed := gnarl.FailedItems(res); len(failed) > 0 || len(res.Items) != total {
		t.Fatalf("BulkChunked: %d items, %d failed: %+v", len(res.Items), len(failed), failed)
	}
	searchable(t, c, name, gnarl.MatchAll(), total)

	seen := map[string]bool{}
	var order []float64
	for hit, err := range c.SearchAll(ctx(t), name, gnarl.SearchRequest{
		Query: gnarl.MatchAll(),
		Sort:  []gnarl.SortClause{gnarl.SortAsc("n")},
		Size:  5,
	}) {
		if err != nil {
			t.Fatalf("SearchAll: %v", err)
		}
		if seen[hit.UnderscoreId] {
			t.Fatalf("%s returned twice — the cursor did not advance", hit.UnderscoreId)
		}
		seen[hit.UnderscoreId] = true
		if hit.Sort != nil && len(*hit.Sort) > 0 {
			if v, ok := (*hit.Sort)[0].(float64); ok {
				order = append(order, v)
			}
		}
	}
	if len(seen) != total {
		t.Errorf("walked %d distinct hits, want %d", len(seen), total)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("walk is out of order at %d: %v", i, order)
		}
	}
}
