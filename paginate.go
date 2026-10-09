package gnarl

import (
	"context"
	"fmt"
	"iter"
)

// DefaultPageSize is the page SearchAll fetches when the request sets no Size.
const DefaultPageSize = 100

// SearchAll walks every hit of a sorted search, a page at a time, using the
// search_after cursor rather than an offset:
//
//	for hit, err := range c.SearchAll(ctx, "events", gnarl.SearchRequest{
//	    Query: gnarl.MatchAll(),
//	    Sort:  []gnarl.SortClause{gnarl.SortAsc("ts")},
//	}) {
//	    if err != nil {
//	        return err
//	    }
//	    use(hit)
//	}
//
// A page costs the same whether it is the first or the ten-thousandth, and the
// walk is stable under concurrent writes in a way From is not. req.Size is the
// page size; req.From is ignored.
//
// req.Sort is REQUIRED: a cursor is the sort values of the last hit, so there
// is nothing to resume from without an explicit order. Sort on something
// reasonably distinct — documents sharing the cursor's first sort value are
// fetched and discarded, so long runs of one value slow a walk down.
//
// The walk ends on the first empty page, so it always costs one request more
// than the pages that held hits. An error ends it after being yielded once. RequireComplete applies to
// every page, so a walk that must be complete fails on the first partial page
// instead of quietly skipping a claim's rows.
func (c *Client) SearchAll(ctx context.Context, index string, req SearchRequest) iter.Seq2[Hit, error] {
	if index == "" {
		return failed(fmt.Errorf("gnarl: SearchAll: empty index name"))
	}
	return c.searchAll(ctx, func(ctx context.Context, r SearchRequest) (*SearchResponse, error) {
		return c.Search(ctx, index, r)
	}, req)
}

// SearchAll is Client.SearchAll within one namespace.
func (n *Namespace) SearchAll(ctx context.Context, req SearchRequest) iter.Seq2[Hit, error] {
	if err := n.check("SearchAll"); err != nil {
		return failed(err)
	}
	return n.c.searchAll(ctx, n.Search, req)
}

func failed(err error) iter.Seq2[Hit, error] {
	return func(yield func(Hit, error) bool) { yield(Hit{}, err) }
}

func (c *Client) searchAll(
	ctx context.Context,
	search func(context.Context, SearchRequest) (*SearchResponse, error),
	req SearchRequest,
) iter.Seq2[Hit, error] {
	return func(yield func(Hit, error) bool) {
		if len(req.Sort) == 0 {
			yield(Hit{}, fmt.Errorf("gnarl: SearchAll: an explicit Sort is required — "+
				"a search_after cursor is the sort values of the last hit"))
			return
		}
		if req.Size <= 0 {
			req.Size = DefaultPageSize
		}
		req.From = 0
		for {
			res, err := search(ctx, req)
			if err != nil {
				yield(Hit{}, err)
				return
			}
			for _, h := range res.Hits {
				if !yield(h, nil) {
					return
				}
			}
			// Only an EMPTY page ends the walk. A short page does not: a
			// node can answer fewer than Size rows while more remain. On a
			// four-claim index, size 5, the page after the first came back
			// with 4 rows and 14 still to go — so stopping on a short page
			// silently truncated a 23-row walk to 9. One extra request at
			// the end is the price of never doing that.
			if len(res.Hits) == 0 {
				return
			}
			last := res.Hits[len(res.Hits)-1]
			if last.Sort == nil || len(*last.Sort) == 0 {
				yield(Hit{}, fmt.Errorf("gnarl: SearchAll: hit %q carries no sort "+
					"values, so there is no cursor to resume from", last.UnderscoreId))
				return
			}
			req.SearchAfter = *last.Sort
		}
	}
}

// BulkChunked indexes docs in batches of at most size documents, and merges
// the results into one, in input order.
//
// One request per batch keeps each body bounded — a single million-document
// bulk is a request no proxy will forward. size zero or less means 500.
//
// It stops at the first batch whose REQUEST fails, returning what was
// accepted so far with the error; batches that succeeded are not undone.
// Items that failed inside a successful batch do not stop it — as with Bulk,
// check FailedItems.
func (c *Client) BulkChunked(ctx context.Context, index string, docs []BulkDoc, size int) (*BulkResult, error) {
	return bulkChunked(docs, size, func(batch []BulkDoc) (*BulkResult, error) {
		return c.BulkWithIDs(ctx, index, batch)
	})
}

// BulkChunked is Client.BulkChunked into one namespace.
func (n *Namespace) BulkChunked(ctx context.Context, docs []BulkDoc, size int) (*BulkResult, error) {
	return bulkChunked(docs, size, func(batch []BulkDoc) (*BulkResult, error) {
		return n.Bulk(ctx, batch)
	})
}

func bulkChunked(docs []BulkDoc, size int, send func([]BulkDoc) (*BulkResult, error)) (*BulkResult, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("gnarl: BulkChunked: no documents")
	}
	if size <= 0 {
		size = 500
	}
	merged := &BulkResult{}
	for start := 0; start < len(docs); start += size {
		end := min(start+size, len(docs))
		res, err := send(docs[start:end])
		if err != nil {
			return merged, fmt.Errorf("gnarl: BulkChunked: documents %d-%d: %w", start, end-1, err)
		}
		merged.Items = append(merged.Items, res.Items...)
		merged.Errors = merged.Errors || res.Errors
		// The weakest acknowledgement any batch reached is the one the whole
		// call can claim.
		if merged.Ack == "" || ackRank(res.Ack) < ackRank(merged.Ack) {
			merged.Ack = res.Ack
		}
		if res.TimedOut != nil && *res.TimedOut {
			timedOut := true
			merged.TimedOut = &timedOut
		}
	}
	return merged, nil
}

func ackRank(a BulkAck) int {
	switch a {
	case AckAccepted:
		return 0
	case AckAcceptedDurably:
		return 1
	case AckVisibleForSearch:
		return 2
	}
	return -1
}
