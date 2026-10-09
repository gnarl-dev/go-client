package gnarl

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
)

func TestNamespaceRoutes(t *testing.T) {
	c, f := replying(t, 200, `{"_id":"d/1","_source":{"n":1},"namespace":"t a",
		"encrypted":true,"unlocked":true,"registered":true,
		"hits":{"hits":[],"total":{"value":0,"relation":"eq"}},
		"coverage":{"expected_claims":1,"served_claims":1,"skipped_claims":[]},"partial":false,
		"ack":"accepted","errors":false,"items":[]}`)
	ctx := context.Background()
	ns := c.Namespace("t a")
	const base = "/v1/namespaces/t%20a"

	id, err := ns.IndexDocument(ctx, "d/1", map[string]int{"n": 1})
	if err != nil || id != "d/1" {
		t.Fatalf("IndexDocument = %q, %v", id, err)
	}
	s := f.last(t)
	s.want(t, "POST", base+"/_doc", "")
	if s.field(t, "_id") != "d/1" || s.field(t, "n") != float64(1) {
		t.Errorf("body = %s", s.Raw)
	}

	var doc struct{ N int }
	if err := ns.GetDocument(ctx, "d/1", &doc); err != nil || doc.N != 1 {
		t.Fatalf("GetDocument = %+v, %v", doc, err)
	}
	// The id is caller data and may contain a slash: it must be one segment.
	f.last(t).want(t, "GET", base+"/_doc/d%2F1", "")

	if err := ns.DeleteDocument(ctx, "d/1"); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "DELETE", base+"/_doc/d%2F1", "")

	if _, err := ns.Search(ctx, SearchRequest{Query: MatchAll(), Size: 5}); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	s.want(t, "POST", base+"/_search", "")
	if s.field(t, "size") != float64(5) {
		t.Errorf("body = %s", s.Raw)
	}

	if _, err := ns.Bulk(ctx, []BulkDoc{{ID: "a", Document: map[string]int{"x": 1}}}); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "POST", base+"/_bulk", "")

	if err := ns.PutMapping(ctx, NewSchema(map[string]Field{"v": DenseVectorField(3)})); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	s.want(t, "PUT", base+"/_mapping", "")
	if _, ok := s.field(t, "fields").(map[string]any)["v"]; !ok {
		t.Errorf("body = %s", s.Raw)
	}

	if err := ns.Promote(ctx); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "POST", base+"/_promote", "")

	kek := make([]byte, 32)
	kek[0] = 7
	st, err := ns.SetKey(ctx, kek)
	if err != nil || !st.Encrypted || !st.Unlocked || st.Registered == nil || !*st.Registered {
		t.Fatalf("SetKey = %+v, %v", st, err)
	}
	s = f.last(t)
	s.want(t, "PUT", base+"/_key", "")
	if got, _ := base64.StdEncoding.DecodeString(s.field(t, "key").(string)); len(got) != 32 || got[0] != 7 {
		t.Errorf("key = %v", s.Body["key"])
	}

	if _, err := ns.KeyStatus(ctx); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", base+"/_key", "")
	if _, err := ns.RevokeKey(ctx); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "DELETE", base+"/_key", "")

	if err := ns.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "DELETE", base, "")
}

func TestSetKeyRefusesAKeyOfTheWrongLength(t *testing.T) {
	c, f := replying(t, 200, `{}`)
	if _, err := c.Namespace("n").SetKey(context.Background(), []byte("short")); err == nil {
		t.Error("a 5-byte KEK was accepted")
	}
	if f.count() != 0 {
		t.Error("it reached the node")
	}
}

func TestAnEmptyNamespaceNameIsRefusedLocally(t *testing.T) {
	c, f := replying(t, 200, `{}`)
	if err := c.Namespace("").Delete(context.Background()); err == nil {
		t.Error("deleting the empty namespace was sent")
	}
	if f.count() != 0 {
		t.Error("it reached the node — DELETE /v1/namespaces/ is not a namespace")
	}
}

// The listing follows the cursor, and a partial page anywhere makes the whole
// listing partial: an unreachable peer's namespaces are missing, not absent.
func TestListNamespacesFollowsTheCursorAndKeepsPartial(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"namespaces":[{"name":"a","promotion":"pooled","keyed":false}],
				"next_after":"a","partial":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"namespaces":[{"name":"b","promotion":"dedicated","keyed":true,"unlocked":true}]}`))
	})
	all, partial, err := c.ListNamespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].Name != "b" || all[1].Promotion != "dedicated" || !all[1].Keyed || !all[1].Unlocked {
		t.Errorf("got %+v", all)
	}
	if !partial {
		t.Error("the first page was partial, but the listing reports complete")
	}
	f.last(t).want(t, "GET", "/v1/namespaces", "after=a")

	if _, err := c.ListNamespacesPage(context.Background(), "", 10); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", "/v1/namespaces", "limit=10")
}
