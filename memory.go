package gnarl

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gnarl-dev/go-client/internal/oas"
)

// Agent memory: durable recall for assistants.
//
// Remember stores a fact, Recall retrieves by hybrid lexical and vector
// search, Answer composes over what was recalled, and the Ingest methods take
// documents, chat transcripts and voice. Every call is scoped by a space
// (`personal`, which stays on the device, or `household`, which is REPLICATED
// to mesh peers), or by an explicit namespace and user.
//
// Embedding happens on the node. A node with no on-device model and no route
// to fetch one fails fast with an error naming where to install it.

// MemoryScope selects whose memory a call reads or writes. All fields are
// optional; the node resolves an empty scope to its default space.
type MemoryScope struct {
	// Space is `personal` or `household`.
	Space string
	// Namespace and User address a memory space directly.
	Namespace string
	User      string
	// Session narrows a call to one conversation.
	Session string
}

// RememberRequest is one fact to store.
type RememberRequest struct {
	MemoryScope

	// Content is the text to embed and store. Required.
	Content string

	// Agent names the writer. The node defaults it when empty.
	Agent string

	// FactType classifies the memory, e.g. "preference" or "entity".
	FactType string

	// Pinned exempts the memory from eviction.
	Pinned bool

	// Tags are arbitrary key/value labels. Recall returns them flattened to
	// a `k=v,k=v` string.
	Tags map[string]string

	// URL, Title and Source describe where the memory came from. A URL with
	// no FactType makes it a "clip".
	URL    string
	Title  string
	Source string
}

// Remembered is the node's receipt for a stored memory.
type Remembered = oas.MemoryRemember200JSONResponseBody

// Remember embeds and stores one memory.
//
// It is never retried: a 503 does not prove the memory was not stored, and a
// retry would store it twice.
func (c *Client) Remember(ctx context.Context, req RememberRequest) (*Remembered, error) {
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("gnarl: Remember: empty content")
	}
	body := oas.MemoryRememberJSONRequestBody{
		Content:   req.Content,
		Space:     opt(req.Space),
		Namespace: opt(req.Namespace),
		User:      opt(req.User),
		Session:   opt(req.Session),
		Agent:     opt(req.Agent),
		FactType:  opt(req.FactType),
		Url:       opt(req.URL),
		Title:     opt(req.Title),
		Source:    opt(req.Source),
	}
	if req.Pinned {
		body.Pinned = &req.Pinned
	}
	if len(req.Tags) > 0 {
		body.Tags = &req.Tags
	}
	var out Remembered
	if err := c.do(ctx, http.MethodPost, "/v1/memory/remember", body, &out); err != nil {
		return nil, err
	}
	publicEngine(out.EngineBinding)
	return &out, nil
}

// RecallRequest is a query against stored memories.
type RecallRequest struct {
	MemoryScope

	// Query is what to look for. Required.
	Query string

	// K is how many memories to return. Zero means the node's default of 5;
	// values above 100 are CLAMPED to 100 rather than refused, so receiving
	// 100 does not mean there were only 100.
	K int
}

// Memory is one recalled memory.
type Memory = oas.MemoryRecall200JSONResponseBody_Memories_Item

// Recalled is the result of a recall, best match first.
type Recalled = oas.MemoryRecall200JSONResponseBody

// Recall retrieves memories by hybrid lexical and vector search.
func (c *Client) Recall(ctx context.Context, req RecallRequest) (*Recalled, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("gnarl: Recall: empty query")
	}
	body := oas.MemoryRecallJSONRequestBody{
		Query:     req.Query,
		Space:     opt(req.Space),
		Namespace: opt(req.Namespace),
		User:      opt(req.User),
		Session:   opt(req.Session),
	}
	if req.K > 0 {
		body.K = &req.K
	}
	var out Recalled
	if err := c.doRead(ctx, "/v1/memory/recall", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerRequest is a question to answer over recalled memories.
type AnswerRequest struct {
	MemoryScope

	// Query is the question. Required.
	Query string

	// K is how many memories to recall before composing. Zero means the
	// node's default of 5.
	K int
}

// answerBody is the wire body. The node accepts the whole memory scope here,
// as it does for recall; the description declares only namespace, so the
// generated type cannot carry space, user or session.
type answerBody struct {
	Query     string `json:"query"`
	Space     string `json:"space,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	User      string `json:"user,omitempty"`
	Session   string `json:"session,omitempty"`
	K         int    `json:"k,omitempty"`
}

// Answer recalls memories and composes an answer over them on the node.
//
// The description does not type the response, so it is returned as decoded
// JSON rather than as a struct whose fields would be guesses.
func (c *Client) Answer(ctx context.Context, req AnswerRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("gnarl: Answer: empty query")
	}
	body := answerBody{
		Query: req.Query, Space: req.Space, Namespace: req.Namespace,
		User: req.User, Session: req.Session, K: req.K,
	}
	var out map[string]any
	if err := c.doRead(ctx, "/v1/memory/answer", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BootstrapRequest prepares a memory space and, given a Query, primes it with
// what is already relevant — what an agent calls at the start of a session.
type BootstrapRequest struct {
	MemoryScope

	// Query, when set, selects the memories returned for priming.
	Query string

	// K bounds how many are returned.
	K int
}

type bootstrapBody struct {
	Query     string `json:"query,omitempty"`
	Space     string `json:"space,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	User      string `json:"user,omitempty"`
	Session   string `json:"session,omitempty"`
	K         int    `json:"k,omitempty"`
}

// Bootstrap prepares a memory space. The response is returned as decoded JSON
// for the same reason as Answer's.
//
// A body is always sent, even an empty one: the node reads the request as
// JSON and refuses one with no body at all.
func (c *Client) Bootstrap(ctx context.Context, req BootstrapRequest) (map[string]any, error) {
	body := bootstrapBody{
		Query: req.Query, Space: req.Space, Namespace: req.Namespace,
		User: req.User, Session: req.Session, K: req.K,
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodPost, "/v1/memory/bootstrap", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Ingest ─────────────────────────────────────────────────────────────────

// IngestDocumentRequest is one file to extract, chunk and remember.
type IngestDocumentRequest struct {
	// Filename is load-bearing twice: its extension selects the extractor,
	// and it is the identity chunks are keyed by, so sending the same
	// document again replaces rather than duplicates.
	Filename string

	// Content is the file's bytes. They are base64-encoded on the wire.
	Content []byte

	// Space is REQUIRED: `personal` stays on the device, `household` is
	// replicated to mesh peers. There is no default, so a document is never
	// shared with a whole household without the caller choosing it.
	Space string
}

type ingestDocumentBody struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
	Space         string `json:"space"`
}

// IngestDocument extracts text from a file and stores it as memories.
func (c *Client) IngestDocument(ctx context.Context, req IngestDocumentRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Filename) == "" {
		return nil, fmt.Errorf("gnarl: IngestDocument: empty filename")
	}
	if strings.TrimSpace(req.Space) == "" {
		return nil, fmt.Errorf("gnarl: IngestDocument: Space is required " +
			"(personal stays on this device; household is shared with mesh peers)")
	}
	body := ingestDocumentBody{
		Filename:      req.Filename,
		ContentBase64: base64.StdEncoding.EncodeToString(req.Content),
		Space:         req.Space,
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodPost, "/v1/memory/ingest/document", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Message is one turn of a chat transcript.
type Message struct {
	// Role is `me`, `them`, or a contact label.
	Role string
	Body string
	// At is when it was sent. The zero time omits it.
	At time.Time
}

type messageWire struct {
	Role string `json:"role"`
	Body string `json:"body"`
	TsMs *int64 `json:"ts_ms,omitempty"`
}

// IngestMessagesRequest is a chat transcript to remember.
type IngestMessagesRequest struct {
	MemoryScope
	Messages    []Message
	ThreadTitle string
	ThreadID    string
	Source      string
}

type ingestMessagesBody struct {
	Messages    []messageWire `json:"messages"`
	Space       string        `json:"space,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	User        string        `json:"user,omitempty"`
	ThreadTitle string        `json:"thread_title,omitempty"`
	ThreadID    string        `json:"thread_id,omitempty"`
	Source      string        `json:"source,omitempty"`
}

// IngestMessages stores a chat transcript as memories.
func (c *Client) IngestMessages(ctx context.Context, req IngestMessagesRequest) (map[string]any, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("gnarl: IngestMessages: no messages")
	}
	body := ingestMessagesBody{
		Messages: make([]messageWire, 0, len(req.Messages)),
		Space:    req.Space, Namespace: req.Namespace, User: req.User,
		ThreadTitle: req.ThreadTitle, ThreadID: req.ThreadID, Source: req.Source,
	}
	for _, m := range req.Messages {
		w := messageWire{Role: m.Role, Body: m.Body}
		if !m.At.IsZero() {
			ms := m.At.UnixMilli()
			w.TsMs = &ms
		}
		body.Messages = append(body.Messages, w)
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodPost, "/v1/memory/ingest/messages", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IngestVoiceRequest is a transcribed voice memo to remember.
type IngestVoiceRequest struct {
	MemoryScope
	Transcript string
	Duration   time.Duration
	AudioPath  string
	Source     string
}

type ingestVoiceBody struct {
	Transcript string `json:"transcript"`
	Space      string `json:"space,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	User       string `json:"user,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	AudioPath  string `json:"audio_path,omitempty"`
	Source     string `json:"source,omitempty"`
}

// IngestVoice stores a voice transcript as memories.
func (c *Client) IngestVoice(ctx context.Context, req IngestVoiceRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Transcript) == "" {
		return nil, fmt.Errorf("gnarl: IngestVoice: empty transcript")
	}
	body := ingestVoiceBody{
		Transcript: req.Transcript,
		Space:      req.Space, Namespace: req.Namespace, User: req.User,
		DurationMs: req.Duration.Milliseconds(),
		AudioPath:  req.AudioPath, Source: req.Source,
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodPost, "/v1/memory/ingest/voice", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// opt turns "" into an absent optional field, so the node's own default
// applies rather than an empty string the caller never chose.
func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
