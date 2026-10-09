package gnarl

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestEntitlementDecodesEveryField(t *testing.T) {
	c, f := replying(t, 200, `{"active":true,"refused":null,"tier":"personal",
		"features":["private-mesh","hosted-backup"],"mesh_id":"m-1",
		"not_after":1893456000,"enforced":true}`)
	e, err := c.Entitlement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", "/v1/node/entitlement", "")
	if !e.Active || !e.Enforced || e.Tier != "personal" || e.MeshID != "m-1" || e.Refused != "" {
		t.Errorf("decoded %+v", e)
	}
	if len(e.Features) != 2 || !e.HasFeature("hosted-backup") {
		t.Errorf("Features = %v", e.Features)
	}
	// not_after is epoch SECONDS. Read as milliseconds this is January 1970.
	if want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC); !e.NotAfter.Equal(want) {
		t.Errorf("NotAfter = %v, want %v", e.NotAfter, want)
	}
}

// Refused is its own state: a token present and rejected is not "nothing
// bought", and an inactive entitlement grants nothing whatever it lists.
func TestARefusedEntitlementIsNotAbsentAndGrantsNothing(t *testing.T) {
	c, _ := replying(t, 200, `{"active":false,"refused":"expired",
		"features":["private-mesh"],"not_after":null,"enforced":true}`)
	e, err := c.Entitlement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.Refused != "expired" {
		t.Errorf("Refused = %q", e.Refused)
	}
	if !e.NotAfter.IsZero() {
		t.Errorf("a null not_after decoded to %v", e.NotAfter)
	}
	if e.HasFeature("private-mesh") {
		t.Error("a refused entitlement granted a feature")
	}
}

func TestActivateEntitlementSendsTheTrimmedKey(t *testing.T) {
	c, f := replying(t, 200, `{"active":true,"features":[],"enforced":true,"tier":"personal"}`)
	e, err := c.ActivateEntitlement(context.Background(), "  gnarl-ent1.abc\n")
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/node/entitlement/activate", "")
	if s.field(t, "key") != "gnarl-ent1.abc" {
		t.Errorf("key = %v", s.Body["key"])
	}
	if !e.Active || e.Tier != "personal" {
		t.Errorf("decoded %+v", e)
	}
}

// The node refuses a key with a plain-text 400, not the envelope. The reason
// is the whole point — "expired" and "untrusted signer" need different
// actions — so it must survive.
func TestARefusedActivationKeepsTheReason(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(400)
		_, _ = w.Write([]byte("entitlement expired at 2026-01-01"))
	})
	_, err := c.ActivateEntitlement(context.Background(), "gnarl-ent1.old")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T %v", err, err)
	}
	if e.Status != 400 || e.Reason != "entitlement expired at 2026-01-01" {
		t.Errorf("got %+v", e)
	}
}

func TestActivateEntitlementRefusesAnEmptyKeyLocally(t *testing.T) {
	c, f := replying(t, 200, `{}`)
	if _, err := c.ActivateEntitlement(context.Background(), "   "); err == nil {
		t.Error("an empty key was accepted")
	}
	if f.count() != 0 {
		t.Error("an empty key reached the node")
	}
}
