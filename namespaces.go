package gnarl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gnarl-dev/go-client/internal/oas"
)

// Namespaces are many lightweight tenants over bounded physical pools: name
// one and write to it, with no index to create first. A namespace is a
// filtered alias over a shared pool until it declares a dense_vector mapping
// or is promoted, when it moves to its own dedicated index.

// Namespace is a handle on one namespace. Making one sends nothing; the
// namespace is created by its first write.
type Namespace struct {
	c    *Client
	name string
}

// Namespace returns a handle on the named namespace.
func (c *Client) Namespace(name string) *Namespace {
	return &Namespace{c: c, name: name}
}

// Name is the namespace's name.
func (n *Namespace) Name() string { return n.name }

func (n *Namespace) path(suffix string) string {
	return "/v1/namespaces/" + pathEscape(n.name) + suffix
}

func (n *Namespace) check(op string) error {
	if n.name == "" {
		return fmt.Errorf("gnarl: Namespace.%s: empty namespace name", op)
	}
	return nil
}

// IndexDocument writes one document, creating the namespace on first write.
// Pass an empty id to have one generated; the id returned is the tenant's
// own, never the internal namespace-scoped form.
func (n *Namespace) IndexDocument(ctx context.Context, id string, doc any) (string, error) {
	if err := n.check("IndexDocument"); err != nil {
		return "", err
	}
	body, err := documentBody(id, doc)
	if err != nil {
		return "", fmt.Errorf("gnarl: Namespace.IndexDocument: %w", err)
	}
	var out oas.IndexDocumentResponse
	if err := n.c.do(ctx, http.MethodPost, n.path("/_doc"), body, &out); err != nil {
		return "", err
	}
	return out.UnderscoreId, nil
}

// Bulk writes many documents in one request — the path for a tenant import.
//
// As with Client.Bulk, the request can succeed while items fail: check
// FailedItems. A partial accept is reported as a failure for every item, so
// retrying the batch (idempotent by id) is safe.
func (n *Namespace) Bulk(ctx context.Context, docs []BulkDoc) (*BulkResult, error) {
	if err := n.check("Bulk"); err != nil {
		return nil, err
	}
	return n.c.bulk(ctx, n.path("/_bulk"), "Namespace.Bulk", docs)
}

// GetDocument fetches a document's _source and unmarshals it into dst.
func (n *Namespace) GetDocument(ctx context.Context, id string, dst any) error {
	if err := n.check("GetDocument"); err != nil {
		return err
	}
	var out struct {
		Source json.RawMessage `json:"_source"`
	}
	if err := n.c.do(ctx, http.MethodGet, n.path("/_doc/"+pathEscape(id)), nil, &out); err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(out.Source, dst)
}

// DeleteDocument removes one document. It is acknowledged when durable, not
// when invisible: a read straight afterwards may still see it.
func (n *Namespace) DeleteDocument(ctx context.Context, id string) error {
	if err := n.check("DeleteDocument"); err != nil {
		return err
	}
	return n.c.do(ctx, http.MethodDelete, n.path("/_doc/"+pathEscape(id)), nil, nil)
}

// Search runs a query restricted to this namespace by a filter the caller
// cannot override. A kNN query needs a dedicated namespace (see PutMapping
// and Promote); on a pooled one it is refused.
func (n *Namespace) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if err := n.check("Search"); err != nil {
		return nil, err
	}
	return n.c.search(ctx, n.path("/_search"), req)
}

// PutMapping declares an explicit mapping — a dense_vector field — which
// moves the namespace to its own dedicated index so vector search is
// single-tenant. The namespace must be fresh: one with pooled data answers
// 409 (errors.Is(err, ErrConflict)). Scalar fields are inferred on write and
// need no mapping.
func (n *Namespace) PutMapping(ctx context.Context, schema Schema) error {
	if err := n.check("PutMapping"); err != nil {
		return err
	}
	return n.c.do(ctx, http.MethodPut, n.path("/_mapping"), schema, nil)
}

// Promote moves a pooled namespace to a dedicated index online — dual write,
// backfill, cutover, purge — with no lost writes. It is idempotent, and so is
// retried like any idempotent request.
func (n *Namespace) Promote(ctx context.Context) error {
	if err := n.check("Promote"); err != nil {
		return err
	}
	return n.c.call(ctx, http.MethodPost, n.path("/_promote"), nil, nil, true)
}

// Delete removes every document in the namespace and its key material. On a
// private mesh this needs the admin role.
func (n *Namespace) Delete(ctx context.Context) error {
	if err := n.check("Delete"); err != nil {
		return err
	}
	return n.c.do(ctx, http.MethodDelete, n.path(""), nil, nil)
}

// NamespaceKeyStatus reports whether a namespace is encrypted and whether
// this node can currently read it. It never carries key material.
type NamespaceKeyStatus = oas.NamespaceKeyStatus

// SetKey registers a tenant key-encryption key (KEK) for the namespace, or
// unlocks an already-keyed one on this node. kek must be 32 bytes.
//
// A wrong key is refused with 403 (errors.Is(err, ErrForbidden)). Searchable
// terms stay plaintext by design; _source is sealed.
func (n *Namespace) SetKey(ctx context.Context, kek []byte) (*NamespaceKeyStatus, error) {
	if err := n.check("SetKey"); err != nil {
		return nil, err
	}
	if len(kek) != 32 {
		return nil, fmt.Errorf("gnarl: Namespace.SetKey: a KEK is 32 bytes, got %d", len(kek))
	}
	body := oas.PutNamespaceKeyJSONRequestBody{Key: base64.StdEncoding.EncodeToString(kek)}
	var out NamespaceKeyStatus
	if err := n.c.do(ctx, http.MethodPut, n.path("/_key"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KeyStatus reports the namespace's encryption state.
func (n *Namespace) KeyStatus(ctx context.Context) (*NamespaceKeyStatus, error) {
	if err := n.check("KeyStatus"); err != nil {
		return nil, err
	}
	var out NamespaceKeyStatus
	if err := n.c.do(ctx, http.MethodGet, n.path("/_key"), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey crypto-erases the namespace: the wrapped DEK is revoked and a
// fail-closed tombstone left behind. This cannot be undone.
func (n *Namespace) RevokeKey(ctx context.Context) (*NamespaceKeyStatus, error) {
	if err := n.check("RevokeKey"); err != nil {
		return nil, err
	}
	var out NamespaceKeyStatus
	if err := n.c.do(ctx, http.MethodDelete, n.path("/_key"), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NamespaceInfo is one entry from ListNamespaces.
type NamespaceInfo struct {
	Name string `json:"name"`

	// Promotion is "pooled", "migrating" or "dedicated".
	Promotion string `json:"promotion"`

	// Keyed is true when a BYOK key is registered.
	Keyed bool `json:"keyed"`

	// Unlocked is true when the tenant key has been supplied to this node
	// this session.
	Unlocked bool `json:"unlocked,omitempty"`
}

// NamespacePage is one page of ListNamespacesPage.
type NamespacePage struct {
	Namespaces []NamespaceInfo `json:"namespaces"`

	// NextAfter is the cursor for the next page; empty once complete.
	NextAfter string `json:"next_after,omitempty"`

	// Partial is true when a peer could not be reached, so this page is a
	// floor rather than a complete answer. The catalog is written by
	// whichever node served a namespace's first write and is not
	// replicated, so a missing peer means missing namespaces.
	Partial bool `json:"partial,omitempty"`
}

// ListNamespaces returns every namespace the mesh reports, following the
// cursor to the end. partial is true if ANY page was partial — a listing
// that silently dropped an unreachable peer's namespaces would read as
// complete.
func (c *Client) ListNamespaces(ctx context.Context) (all []NamespaceInfo, partial bool, err error) {
	cursor := ""
	for {
		page, err := c.ListNamespacesPage(ctx, cursor, 0)
		if err != nil {
			return nil, false, err
		}
		all = append(all, page.Namespaces...)
		partial = partial || page.Partial
		if page.NextAfter == "" {
			return all, partial, nil
		}
		cursor = page.NextAfter
	}
}

// ListNamespacesPage returns one page. limit zero means the node's default.
func (c *Client) ListNamespacesPage(ctx context.Context, after string, limit int) (*NamespacePage, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/namespaces"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out NamespacePage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
