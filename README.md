# gnarl-go

The Go client for [Gnarl](https://gnarl.dev) — a decentralized search fabric.

A node is a peer, not a coordinator, so there is no cluster endpoint to point
at. You talk to a node and it answers for the mesh. Any node will do.

```bash
go get github.com/lucenia/gnarl-go
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/lucenia/gnarl-go"
)

func main() {
    c, err := gnarl.New("http://localhost:8080")
    if err != nil {
        log.Fatal(err)
    }
    ctx := context.Background()

    err = c.CreateIndex(ctx, "places", gnarl.NewSchema(map[string]gnarl.Field{
        "name":     gnarl.KeywordField(),
        "location": gnarl.GeoPointField(),
    }))
    if err != nil {
        log.Fatal(err)
    }

    _, err = c.IndexDocument(ctx, "places", "sydney", map[string]any{
        "name":     "sydney",
        "location": gnarl.GeoPoint{Lat: -33.8688, Lon: 151.2093},
    })
    if err != nil {
        log.Fatal(err)
    }

    res, err := c.Search(ctx, "places", gnarl.SearchRequest{
        Query: gnarl.GeoDistance("location", -33.8688, 151.2093, 1_000_000),
    })
    if err != nil {
        log.Fatal(err)
    }
    for _, hit := range res.Hits {
        fmt.Println(hit.UnderscoreId)
    }
}
```

## Errors

Every failure is one type. Match on the sentinels rather than on strings or
status codes:

```go
err := c.CreateIndex(ctx, "places", schema)
switch {
case errors.Is(err, gnarl.ErrAlreadyExists):
    // fine, it was already there
case errors.Is(err, gnarl.ErrValidation):
    var e *gnarl.Error
    errors.As(err, &e)
    log.Printf("bad schema: %s", e.Reason)
case err != nil:
    return err
}
```

A `*gnarl.Error` carries `Type`, `Reason`, `Status`, an optional `Detail`
block, and `RetryAfter` on a 429:

```go
_, err := c.Search(ctx, "places", gnarl.SearchRequest{Query: gnarl.MatchAll()})

var e *gnarl.Error
if errors.As(err, &e) && errors.Is(err, gnarl.ErrRateLimited) {
    time.Sleep(e.RetryAfterOrDefault(2 * time.Second))
}
```

## Completeness

A search spans claims held by many peers, and a node answers with whatever it
could reach. For interactive search that is the right default. For anything
auditable — a compliance export, a reconciliation job — a quietly partial
answer is a wrong answer:

```go
res, err := c.Search(ctx, "places", gnarl.SearchRequest{
    Query:           gnarl.MatchAll(),
    RequireComplete: true,
})
if err == nil {
    log.Printf("complete: %d hits from %d claims",
        len(res.Hits), res.Coverage.ServedClaims)
}

var incomplete *gnarl.IncompleteError
if errors.As(err, &incomplete) {
    // The partial result is still here if you want to degrade to it
    // deliberately rather than lose the work.
    log.Printf("%d of %d claims answered",
        incomplete.Response.Coverage.ServedClaims,
        incomplete.Response.Coverage.ExpectedClaims)
}
```

Every response carries `Coverage` whether you ask for completeness or not.

## Two things that surprise people

**`geo_distance` is flat, and the radius is metres.** Not the Elasticsearch
shape — there is no nested `location` object and no `"10km"` string. The
builder makes the right one:

```go
lat, lon := -33.8688, 151.2093
q := gnarl.GeoDistance("location", lat, lon, 10_000) // ten kilometres
_ = q
```

Coordinates are `float64` throughout. `float32` would displace a survey-grade
coordinate by about 23 cm on every request.

**Four field names are reserved**: `id`, `version`, `title` and
`canonical_url`. They belong to the document envelope, so declaring one is
rejected at index creation. `title` is the one people hit — use `name`,
`headline` or `subject`.

## Authentication

A node with RBAC enabled exempts loopback callers, so a local node usually
needs no token. A remote one always does:

```go
authed, err := gnarl.New("https://node.example.com",
    gnarl.WithToken(os.Getenv("GNARL_TOKEN")))
if err != nil {
    return err
}
_ = authed
```

A node generates a self-signed certificate on first run. Against a node you
started yourself, `gnarl.WithInsecureSkipVerify()` skips verification — never
against one you did not.

## How this package is built

`internal/oas` is **generated** from the node's OpenAPI description and is
never edited by hand, so the payload types cannot drift from the server. The
root package is written by hand, so it can be idiomatic. Regenerate with:

```bash
go generate ./internal/oas
```

## Tests

Four layers:

| layer | where | what it proves |
|---|---|---|
| unit | `*_test.go` | builders emit the exact wire shape; every error body parses |
| regression | `*_test.go` | the deprecated `message` alias still works against an older node |
| integration | `conformance/` | real HTTP against a real node |
| smoke | `conformance/` | a node boots and a document survives a round trip |

Compiling proves the types match the description. It does not prove the
description matches the server — and that gap is where client bugs live. So
the conformance suite starts a real node and drives it:

```bash
go test ./...                              # unit only; conformance skips
GNARL_BINARY=/path/to/lucenia go test ./...  # starts a node, runs everything
GNARL_TEST_NODE=http://localhost:8080 go test ./...  # uses a node you have
```

Writing this client found four defects in the API description on its first
afternoon, none of which broke a build: an index-name length that contradicted
the server, a pattern that rejected names the server accepts, four reserved
field names documented nowhere, and a missing document reporting
`index_not_found`. That is what the conformance layer is for.

## License

Apache-2.0. The node itself is AGPL-3.0-or-later; the client is permissive so
it can be embedded freely.
