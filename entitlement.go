package gnarl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gnarl-dev/go-client/internal/oas"
)

// Entitlement is the subscription a node holds.
//
// There are three states, not two. Active is a token present and verified
// now. Refused is a token present and REJECTED — expired, or signed by a key
// this build does not trust. Neither means nothing was ever bought.
// Collapsing refused into absent tells somebody who has paid that they have
// not, and sends them to buy a second subscription when what they need is the
// newer key already on their account page.
type Entitlement struct {
	// Active is true when a token is present and verifies right now.
	Active bool

	// Refused says why a present token was rejected. Empty when there is no
	// token, or when the token is active.
	Refused string

	// Tier names the plan, when there is one.
	Tier string

	// Features are the capabilities the token grants.
	Features []string

	// MeshID is the mesh the token was issued for.
	MeshID string

	// NotAfter is when the token expires, or the zero time when it does not
	// say. The wire carries epoch SECONDS; this is already converted, so
	// there is no unit to get wrong.
	NotAfter time.Time

	// Enforced is false on a build with no signing keys compiled in, where
	// nothing is gated and there is nothing to activate. A UI should say so
	// rather than offer a box that cannot do anything.
	Enforced bool
}

// HasFeature reports whether the entitlement grants feature. An inactive
// entitlement grants nothing, whatever its token lists.
func (e *Entitlement) HasFeature(feature string) bool {
	if e == nil || !e.Active {
		return false
	}
	for _, f := range e.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// entitlementWire mirrors the description's response exactly. Every nullable
// member is a pointer so null and absent both decode to "not given".
type entitlementWire struct {
	Active   bool     `json:"active"`
	Refused  *string  `json:"refused"`
	Tier     *string  `json:"tier"`
	Features []string `json:"features"`
	MeshID   *string  `json:"mesh_id"`
	NotAfter *int64   `json:"not_after"`
	Enforced bool     `json:"enforced"`
}

// UnmarshalJSON decodes the wire shape, converting not_after from epoch
// seconds. Reading it as milliseconds puts the expiry in 1970.
func (e *Entitlement) UnmarshalJSON(b []byte) error {
	var w entitlementWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = Entitlement{
		Active:   w.Active,
		Refused:  str(w.Refused),
		Tier:     str(w.Tier),
		Features: w.Features,
		MeshID:   str(w.MeshID),
		Enforced: w.Enforced,
	}
	if w.NotAfter != nil {
		e.NotAfter = time.Unix(*w.NotAfter, 0).UTC()
	}
	return nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Entitlement returns the subscription this node holds.
func (c *Client) Entitlement(ctx context.Context) (*Entitlement, error) {
	var out Entitlement
	if err := c.do(ctx, http.MethodGet, "/v1/node/entitlement", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ActivateEntitlement verifies key and, if it verifies, stores it on the node.
// It returns the subscription now held.
//
// key is what the account page shows — the single-line `gnarl-ent1.` form —
// or the raw signed-entitlement JSON. Surrounding whitespace from a paste is
// trimmed.
//
// A refused key is an *Error with Status 400 whose Reason names which failure
// occurred — malformed, expired, or signed by an untrusted key. The node sends
// that refusal as plain text rather than the JSON envelope, so Type is empty:
// read Reason. A 503 means the node has nowhere to store a subscription.
//
// Scope is decided when a node starts, so it keeps its previous scope until
// it restarts.
func (c *Client) ActivateEntitlement(ctx context.Context, key string) (*Entitlement, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("gnarl: ActivateEntitlement: empty key")
	}
	body := oas.NodeActivateEntitlementJSONRequestBody{Key: key}
	var out Entitlement
	if err := c.do(ctx, http.MethodPost, "/v1/node/entitlement/activate", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
