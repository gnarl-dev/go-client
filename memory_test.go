package gnarl

import (
	"context"
	"encoding/base64"
	"testing"
	"time"
)

func TestRememberSendsOnlyWhatWasSet(t *testing.T) {
	c, f := replying(t, 200, `{"id":"u-1","namespace":"ns","user":"me",
		"embedder":"local_minilm(dim=384)","engine_binding":"native"}`)
	r, err := c.Remember(context.Background(), RememberRequest{
		MemoryScope: MemoryScope{Space: "personal", Session: "s1"},
		Content:     "the oven runs hot",
		Pinned:      true,
		Tags:        map[string]string{"room": "kitchen"},
		FactType:    "semantic",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/memory/remember", "")
	if s.field(t, "content") != "the oven runs hot" || s.field(t, "space") != "personal" ||
		s.field(t, "session") != "s1" || s.field(t, "pinned") != true ||
		s.field(t, "fact_type") != "semantic" {
		t.Errorf("body = %s", s.Raw)
	}
	if tags, _ := s.field(t, "tags").(map[string]any); tags["room"] != "kitchen" {
		t.Errorf("tags = %v", s.Body["tags"])
	}
	s.absent(t, "namespace", "user", "agent", "url", "title", "source")
	if r.Id != "u-1" || r.Embedder == "" || r.Namespace != "ns" || r.User != "me" {
		t.Errorf("decoded %+v", r)
	}
}

func TestRecallDecodesMemories(t *testing.T) {
	c, f := replying(t, 200, `{"namespace":"ns","count":1,"embedder":"e",
		"memories":[{"id":"m1","score":0.9,"content":"hi","tags":"a=b",
		"created":"2026-10-09T10:00:00Z"}]}`)
	r, err := c.Recall(context.Background(), RecallRequest{
		MemoryScope: MemoryScope{Namespace: "ns", User: "me"},
		Query:       "oven", K: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/memory/recall", "")
	if s.field(t, "query") != "oven" || s.field(t, "k") != float64(7) ||
		s.field(t, "namespace") != "ns" || s.field(t, "user") != "me" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "limit", "space", "session")
	if r.Count != 1 || len(r.Memories) != 1 || r.Memories[0].Content != "hi" ||
		r.Memories[0].Tags == nil || *r.Memories[0].Tags != "a=b" {
		t.Errorf("decoded %+v", r)
	}
}

func TestAnswerAndBootstrapCarryTheScope(t *testing.T) {
	c, f := replying(t, 200, `{"answer":"42","memories":[]}`)
	out, err := c.Answer(context.Background(), AnswerRequest{
		MemoryScope: MemoryScope{Space: "household"}, Query: "meaning", K: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/memory/answer", "")
	if s.field(t, "query") != "meaning" || s.field(t, "space") != "household" || s.field(t, "k") != float64(3) {
		t.Errorf("body = %s", s.Raw)
	}
	if out["answer"] != "42" {
		t.Errorf("decoded %v", out)
	}

	if _, err := c.Bootstrap(context.Background(), BootstrapRequest{}); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	s.want(t, "POST", "/v1/memory/bootstrap", "")
	// The node reads the body as JSON and refuses an empty one.
	if s.Raw != "{}" {
		t.Errorf("an empty Bootstrap sent %q, want {}", s.Raw)
	}
}

func TestIngestDocumentEncodesTheBytes(t *testing.T) {
	c, f := replying(t, 200, `{"id":"d1","memories_written":3,"filename":"a.txt","space":"personal"}`)
	out, err := c.IngestDocument(context.Background(), IngestDocumentRequest{
		Filename: "a.txt", Content: []byte("hello\x00world"), Space: "personal",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/memory/ingest/document", "")
	got, _ := base64.StdEncoding.DecodeString(s.field(t, "content_base64").(string))
	if string(got) != "hello\x00world" || s.field(t, "filename") != "a.txt" || s.field(t, "space") != "personal" {
		t.Errorf("body = %s", s.Raw)
	}
	if out["memories_written"] != float64(3) {
		t.Errorf("decoded %v", out)
	}
}

// Space decides whether a document is shared with a household. There is no
// default, so its absence must stop the call before anything is sent.
func TestIngestDocumentRequiresASpace(t *testing.T) {
	c, f := replying(t, 200, `{}`)
	if _, err := c.IngestDocument(context.Background(), IngestDocumentRequest{Filename: "a.txt"}); err == nil {
		t.Error("a document with no space was accepted")
	}
	if f.count() != 0 {
		t.Error("it reached the node")
	}
}

func TestIngestMessagesAndVoice(t *testing.T) {
	c, f := replying(t, 200, `{"namespace":"n","user":"u","chunks":1,"ids":["i"]}`)
	at := time.UnixMilli(1_760_000_000_000)
	if _, err := c.IngestMessages(context.Background(), IngestMessagesRequest{
		MemoryScope: MemoryScope{Space: "personal"},
		Messages:    []Message{{Role: "me", Body: "hi", At: at}, {Role: "them", Body: "yo"}},
		ThreadTitle: "Sam",
	}); err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/memory/ingest/messages", "")
	msgs, _ := s.field(t, "messages").([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", s.Body["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if first["role"] != "me" || first["body"] != "hi" || first["ts_ms"] != float64(1_760_000_000_000) {
		t.Errorf("first message = %v", first)
	}
	if _, ok := second["ts_ms"]; ok {
		t.Errorf("a zero time sent ts_ms: %v", second)
	}
	if s.field(t, "thread_title") != "Sam" || s.field(t, "space") != "personal" {
		t.Errorf("body = %s", s.Raw)
	}

	if _, err := c.IngestVoice(context.Background(), IngestVoiceRequest{
		Transcript: "buy milk", Duration: 1500 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	s.want(t, "POST", "/v1/memory/ingest/voice", "")
	if s.field(t, "transcript") != "buy milk" || s.field(t, "duration_ms") != float64(1500) {
		t.Errorf("body = %s", s.Raw)
	}
}

// A node released before the rename reports the native engine as `tantivy`.
// Each response that carries the engine must name it `native`; this fails
// when one of them passes the old name through.
func TestTheEngineIsCalledNativeWhicheverNodeAnswers(t *testing.T) {
	c, _ := replying(t, 200, `{"id":"u-1","engine_binding":"tantivy"}`)
	r, err := c.Remember(context.Background(), RememberRequest{Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if r.EngineBinding == nil || *r.EngineBinding != "native" {
		t.Errorf("Remember engine = %v, want native", r.EngineBinding)
	}

	c, _ = replying(t, 200, `{"indexes":[{"name":"a","engine_binding":"tantivy"},
		{"name":"b","engine_binding":"lucene"}]}`)
	got, _, err := c.ListIndexesPage(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || *got[0].EngineBinding != "native" || *got[1].EngineBinding != "lucene" {
		t.Errorf("ListIndexesPage engines = %+v", got)
	}
}
