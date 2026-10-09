package gnarl

import (
	"context"
	"errors"
	"testing"
)

func TestGetAndPutPolicy(t *testing.T) {
	c, f := replying(t, 200, `{"placement":"local","replication_factor":2,"embedder_profile":"local_minilm:384"}`)
	ctx := context.Background()

	p, err := c.GetPolicy(ctx, "docs")
	if err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", "/v1/indexes/docs/_policy", "")
	if p.Placement != PlacementLocal || p.ReplicationFactor == nil || *p.ReplicationFactor != 2 ||
		p.EmbedderProfile == nil || *p.EmbedderProfile != "local_minilm:384" {
		t.Errorf("decoded %+v", p)
	}

	// A partial update carries only what changes, so it cannot widen a
	// replication factor the caller never mentioned.
	local := PlacementLocal
	if _, err := c.PutPolicy(ctx, "docs", IndexPolicyUpdate{Placement: &local}); err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "PUT", "/v1/indexes/docs/_policy", "")
	if s.field(t, "placement") != "local" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "replication_factor", "embedder_profile")
}

func TestPutPolicyFromANonOriginIsForbidden(t *testing.T) {
	c, _ := replying(t, 403, `{"error":{"type":"forbidden","reason":"not the origin"}}`)
	mesh := PlacementMesh
	if _, err := c.PutPolicy(context.Background(), "docs", IndexPolicyUpdate{Placement: &mesh}); !errors.Is(err, ErrForbidden) {
		t.Errorf("got %v, want ErrForbidden", err)
	}
}

func TestForceMerge(t *testing.T) {
	c, f := replying(t, 200, `{"segments":3,"partial":true}`)
	ctx := context.Background()

	r, err := c.ForceMerge(ctx, "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "POST", "/v1/indexes/docs/_forcemerge", "max_num_segments=2")
	if r.Segments != 3 || !r.Partial {
		t.Errorf("decoded %+v", r)
	}

	if _, err := c.ForceMerge(ctx, "docs", 0); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "POST", "/v1/indexes/docs/_forcemerge", "")
}
