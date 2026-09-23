package gnarl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gnarl-dev/go-client/internal/oas"
)

// Re-exported spec types, so callers never import an internal package.
type (
	// Schema is an index mapping: field name to field definition.
	Schema = oas.IndexSchema
	// Field is one field definition in a Schema.
	Field = oas.FieldDefinition
	// Query is the query DSL. Exactly one top-level key is set; use the
	// builders in query.go rather than constructing it directly.
	Query = oas.Query
	// Hit is one search result.
	Hit = oas.Hit
	// Coverage is the auditable claim-level completeness of a search.
	Coverage = oas.SearchCoverage
	// SkippedClaim names a claim omitted from a result, and why.
	SkippedClaim = oas.SkippedClaim
	// BulkResult is the outcome of a bulk request.
	BulkResult = oas.BulkIndexResponse
	// BulkItem is the outcome of one document within a bulk request.
	BulkItem = oas.BulkItemResult
)

// ─── Indexes ────────────────────────────────────────────────────────────────

// CreateIndex creates an index with the given schema.
//
// Returns an error satisfying errors.Is(err, ErrAlreadyExists) if it exists.
func (c *Client) CreateIndex(ctx context.Context, name string, schema Schema) error {
	if name == "" {
		return fmt.Errorf("gnarl: CreateIndex: empty index name")
	}
	body := oas.CreateIndexRequest{Schema: schema}
	return c.do(ctx, http.MethodPut, "/v1/indexes/"+pathEscape(name), body, nil)
}

// DeleteIndex removes an index and everything in it.
func (c *Client) DeleteIndex(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/indexes/"+pathEscape(name), nil, nil)
}

// IndexExists reports whether the index exists.
//
// It distinguishes "absent" from "could not tell": a transport failure or a
// 500 returns an error rather than false, because treating those as absent is
// how a caller ends up deleting or recreating live data.
func (c *Client) IndexExists(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/v1/indexes/"+pathEscape(name), nil, nil)
	if err == nil {
		return true, nil
	}
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// IndexInfo is one entry from ListIndexes.
type IndexInfo = oas.IndexMetadata

// ListIndexes returns every index this node can see.
//
// The endpoint is paginated by a cursor, and this follows it to the end. A
// caller who stops at the first page silently sees a prefix of their own data,
// which is exactly the bug the cursor exists to prevent — so the convenience
// method is the complete one, and ListIndexesPage is there when you want the
// pages yourself.
func (c *Client) ListIndexes(ctx context.Context) ([]IndexInfo, error) {
	var all []IndexInfo
	cursor := ""
	for {
		page, next, err := c.ListIndexesPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}

// ListIndexesPage returns one page of indexes and the cursor for the next.
// An empty next cursor means the listing is complete.
func (c *Client) ListIndexesPage(ctx context.Context, after string) ([]IndexInfo, string, error) {
	path := "/v1/indexes"
	if after != "" {
		path += "?after=" + url.QueryEscape(after)
	}
	var raw oas.IndexListResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, "", err
	}
	next := ""
	if raw.NextAfter != nil {
		next = *raw.NextAfter
	}
	return raw.Indexes, next, nil
}

// GetSchema returns the index's current mapping.
func (c *Client) GetSchema(ctx context.Context, name string) (*Schema, error) {
	var out Schema
	err := c.do(ctx, http.MethodGet, "/v1/indexes/"+pathEscape(name)+"/_schema", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Count returns the number of documents in an index.
func (c *Client) Count(ctx context.Context, name string) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/indexes/"+pathEscape(name)+"/_count", nil, &out)
	return out.Count, err
}

// ─── Documents ──────────────────────────────────────────────────────────────

// IndexDocument indexes one document, returning the id the node assigned or
// accepted. Pass an empty id to have one generated.
//
// doc is marshalled as JSON, so a struct with json tags or a map both work.
func (c *Client) IndexDocument(ctx context.Context, index, id string, doc any) (string, error) {
	body, err := documentBody(id, doc)
	if err != nil {
		return "", fmt.Errorf("gnarl: IndexDocument: %w", err)
	}
	var out oas.IndexDocumentResponse
	path := "/v1/indexes/" + pathEscape(index) + "/_doc"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	return out.UnderscoreId, nil
}

// documentBody merges the caller's document with an optional `_id`.
//
// The wire shape puts document fields at the TOP LEVEL beside `_id` rather
// than under a wrapper, so the id cannot be attached by nesting. Marshalling
// to a map and merging is the only way to do it without requiring every caller
// to add an `_id` field to their own struct.
func documentBody(id string, doc any) (map[string]json.RawMessage, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil document")
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(buf, &fields); err != nil {
		return nil, fmt.Errorf("a document must encode to a JSON object: %w", err)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	if id != "" {
		encoded, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		fields["_id"] = encoded
	}
	return fields, nil
}

// GetDocument fetches a document's _source and unmarshals it into dst.
func (c *Client) GetDocument(ctx context.Context, index, id string, dst any) error {
	var out struct {
		Source json.RawMessage `json:"_source"`
	}
	path := "/v1/indexes/" + pathEscape(index) + "/_doc/" + pathEscape(id)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(out.Source, dst)
}

// DeleteDocument removes one document by id.
func (c *Client) DeleteDocument(ctx context.Context, index, id string) error {
	path := "/v1/indexes/" + pathEscape(index) + "/_doc/" + pathEscape(id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Bulk indexes many documents in one request.
//
// The returned BulkResult reports per-item outcomes. Check Errors before
// assuming the batch succeeded: a bulk request can return 200 with individual
// items failed, which is the single most common way to lose writes silently.
func (c *Client) Bulk(ctx context.Context, index string, docs []any) (*BulkResult, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("gnarl: Bulk: no documents")
	}
	encoded := make([]map[string]json.RawMessage, 0, len(docs))
	for i, d := range docs {
		f, err := documentBody("", d)
		if err != nil {
			return nil, fmt.Errorf("gnarl: Bulk: document %d: %w", i, err)
		}
		encoded = append(encoded, f)
	}
	var out BulkResult
	path := "/v1/indexes/" + pathEscape(index) + "/_bulk"
	body := map[string]any{"documents": encoded}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BulkDoc pairs an explicit document id with its content, for a bulk request
// where the ids matter.
type BulkDoc struct {
	ID       string
	Document any
}

// BulkWithIDs is Bulk for documents whose ids you choose.
func (c *Client) BulkWithIDs(ctx context.Context, index string, docs []BulkDoc) (*BulkResult, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("gnarl: BulkWithIDs: no documents")
	}
	encoded := make([]map[string]json.RawMessage, 0, len(docs))
	for i, d := range docs {
		f, err := documentBody(d.ID, d.Document)
		if err != nil {
			return nil, fmt.Errorf("gnarl: BulkWithIDs: document %d: %w", i, err)
		}
		encoded = append(encoded, f)
	}
	var out BulkResult
	path := "/v1/indexes/" + pathEscape(index) + "/_bulk"
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"documents": encoded}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FailedItems returns the items in a bulk result that did not succeed.
//
// Bulk answers 200 with individual failures, so a caller who checks only the
// HTTP status loses writes without seeing an error. This makes the check a
// one-liner so there is no excuse to skip it.
func FailedItems(r *BulkResult) []BulkItem {
	if r == nil || !r.Errors {
		return nil
	}
	var out []BulkItem
	for _, it := range r.Items {
		if it.Error != nil {
			out = append(out, it)
		}
	}
	return out
}

// ─── Search ─────────────────────────────────────────────────────────────────

// SearchRequest is a search over one index.
type SearchRequest struct {
	// Query selects documents. Build it with Match, Term, GeoDistance, Knn,
	// Bool and friends.
	Query *Query

	// Size is the number of hits to return. Zero means the server default.
	Size int

	// From is the pagination offset. Prefer SearchAfter past a few pages:
	// every claim must collect From+Size rows, so deep offsets cost more
	// everywhere, not just here.
	From int

	// RequireComplete turns a partial result into an error.
	//
	// A search spans claims held by many peers, and a node answers with
	// whatever it could reach. That is the right default for interactive
	// search and the wrong one for anything auditable — a compliance export
	// or a reconciliation job must not quietly read a subset. When this is
	// set, a response with partial=true, or one where fewer claims answered
	// than were expected, returns *IncompleteError.
	RequireComplete bool

	// Profile asks the node for execution timing in Response.Profile.
	Profile bool

	// Verify requires tamper evidence for this search.
	//
	// Every served claim must be PROVEN — a proof binding to an anchor the
	// node holds independently of whoever served it. A claim nobody could
	// check counts against completeness exactly as an unanswered one does, so
	// Verify with Partial false means every row you received was proven, and
	// Partial true tells you which were not.
	//
	// Distinct from RequireComplete: that asks whether every claim ANSWERED,
	// this asks whether every answer was PROVEN. Set both to demand both.
	Verify bool

	// DeadlineMS bounds how long a shard may take, in milliseconds.
	//
	// Zero means the node's own budget. A shard that misses it is skipped and
	// reported in Coverage, so a tight deadline degrades into an honest
	// partial answer rather than an error — which is the point of setting one.
	//
	// The node clamps this to its own configured timeout: you may ask for
	// less time, never more.
	DeadlineMS int64
}

// SearchResponse is the result of a search.
type SearchResponse struct {
	// Hits are the matching documents, best first.
	Hits []Hit

	// Total is the hit-count summary. Read Relation before Value: the
	// default is a LOWER BOUND, not an exact count.
	Total TotalHits

	// Partial is true when the result may be incomplete because some claims
	// did not answer.
	Partial bool

	// Coverage is the auditable claim-level completeness of this search.
	Coverage Coverage

	// Profile is execution timing, present only if the request asked for it.
	Profile *oas.SearchProfile
}

// TotalHits is a hit count and whether it is exact.
type TotalHits struct {
	Value int64 `json:"value"`
	// Relation is "eq" for an exact count or "gte" for a lower bound.
	Relation string `json:"relation"`
}

// IsExact reports whether Value is an exact count rather than a lower bound.
func (t TotalHits) IsExact() bool { return t.Relation == "eq" }

// IncompleteError is returned when RequireComplete was set and the node could
// not answer from every claim.
type IncompleteError struct {
	// Response is the partial result, so a caller can inspect or degrade to
	// it deliberately rather than losing the work.
	Response *SearchResponse
}

func (e *IncompleteError) Error() string {
	c := e.Response.Coverage
	return fmt.Sprintf(
		"gnarl: incomplete search: %d of %d claims answered, %d skipped "+
			"(RequireComplete was set)",
		c.ServedClaims, c.ExpectedClaims, len(c.SkippedClaims))
}

// Search runs a query against one index.
func (c *Client) Search(ctx context.Context, index string, req SearchRequest) (*SearchResponse, error) {
	if index == "" {
		return nil, fmt.Errorf("gnarl: Search: empty index name")
	}
	body := oas.SearchRequest{Query: req.Query}
	if req.Size > 0 {
		body.Size = &req.Size
	}
	if req.From > 0 {
		body.From = &req.From
	}
	if req.Profile {
		body.Profile = &req.Profile
	}
	if req.Verify {
		body.Verify = &req.Verify
	}
	if req.DeadlineMS > 0 {
		ms := req.DeadlineMS
		body.Scope = &oas.QueryScope{DeadlineMs: &ms}
	}

	var raw oas.SearchResponse
	path := "/v1/indexes/" + pathEscape(index) + "/_search"
	if err := c.do(ctx, http.MethodPost, path, body, &raw); err != nil {
		return nil, err
	}

	out := &SearchResponse{
		Hits:     raw.Hits.Hits,
		Total:    TotalHits{Value: int64(raw.Hits.Total.Value), Relation: string(raw.Hits.Total.Relation)},
		Partial:  raw.Partial,
		Coverage: raw.Coverage,
		Profile:  raw.Profile,
	}

	if req.RequireComplete {
		// Both conditions matter. `partial` is the node's own verdict, and
		// the claim arithmetic catches the case where it was not set but a
		// claim went unserved anyway — the point of asking for completeness
		// is not to trust a single flag.
		if out.Partial || out.Coverage.ServedClaims < out.Coverage.ExpectedClaims {
			return out, &IncompleteError{Response: out}
		}
	}
	return out, nil
}

// ─── Node ───────────────────────────────────────────────────────────────────

// NodeStatus is a node's self-report.
//
// The fields mirror the description exactly. There is deliberately no
// Version here: the node's version is a separate endpoint (Version), and an
// invented field would decode as the zero value forever without ever failing.
type NodeStatus struct {
	// NodeID is the node's 64-char hex identity.
	NodeID string `json:"node_id"`

	// Mode is the mesh scope this node is running in: private, public, lan,
	// dev-mesh or single-node.
	Mode string `json:"mode"`

	// Peers is how many peers this node knows; ReachablePeers how many it can
	// currently reach. They differ during a partition, which is the point.
	Peers          int `json:"peers"`
	ReachablePeers int `json:"reachable_peers"`

	// Claims is how many claim units this node holds, ServingReady how many
	// can answer a query right now.
	Claims       int `json:"claims"`
	ServingReady int `json:"serving_ready"`

	// ProofVerified is the number of claims whose storage proof has been
	// verified.
	ProofVerified int `json:"proof_verified"`

	// DataDir is absent only for an ephemeral in-memory node.
	DataDir string `json:"data_dir,omitempty"`
}

// NodeVersion is the node's build identity, from a different endpoint than
// Status.
type NodeVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// Version returns the node's build version.
func (c *Client) Version(ctx context.Context) (*NodeVersion, error) {
	var out NodeVersion
	if err := c.do(ctx, http.MethodGet, "/v1/node/version", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status returns the node's status. It needs no token even on an RBAC node:
// observability is deliberately ungated, so this keeps working during the
// incident you need it for.
func (c *Client) Status(ctx context.Context) (*NodeStatus, error) {
	var out NodeStatus
	if err := c.do(ctx, http.MethodGet, "/v1/node/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ping reports whether the node answers. It is Status without the body.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/node/status", nil, nil)
}
