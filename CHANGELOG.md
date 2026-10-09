# Changelog

## v0.2.0 — 2026-10-09

The GA surface. v0.1 wrapped index CRUD, documents, bulk, search and node
status; v0.2 wraps everything a Gnarl customer is sold past search.

### Added

- **Subscription.** `Entitlement(ctx)` and `ActivateEntitlement(ctx, key)`.
  Active, refused and absent stay three states. `NotAfter` is a `time.Time`
  (the wire carries epoch seconds). A refused key is a 400 whose `Reason` names
  the failure.
- **Agent memory.** `Remember`, `Recall`, `Answer`, `Bootstrap`,
  `IngestDocument`, `IngestMessages`, `IngestVoice`, all scoped by an embedded
  `MemoryScope`. `IngestDocument` requires a space: `household` is replicated
  to mesh peers, so there is no default.
- **Namespaces.** `c.Namespace(name)` with `IndexDocument`, `Bulk`,
  `BulkChunked`, `GetDocument`, `DeleteDocument`, `Search`, `SearchAll`,
  `PutMapping`, `Promote`, `SetKey`, `KeyStatus`, `RevokeKey`, `Delete`; and
  `ListNamespaces` / `ListNamespacesPage`, which report a partial listing.
- **Placement and maintenance.** `GetPolicy`, `PutPolicy` (with
  `PlacementLocal`, `PlacementMesh`, `PlacementPublic`) and `ForceMerge`.
- **Backup and restore.** `RegisterRepository` (`FSRepository`,
  `S3Repository`), `GetRepository`, `ListRepositories`,
  `UnregisterRepository`, `CleanupRepository`, `CreateSnapshot`,
  `SnapshotIndex`, `SnapshotNamespace`, `GetSnapshot`, `ListSnapshots`,
  `DeleteSnapshot`, `RestoreSnapshot`, `ListSnapshotJobs`, `GetSnapshotJob`,
  `WaitForJob` (a failed job returns `*JobFailedError`), and
  `GetBackupSchedule` / `SetBackupSchedule` / `ClearBackupSchedule`.
- **Pagination.** `SearchAll` returns an `iter.Seq2[Hit, error]` over the
  `search_after` cursor. `SearchRequest` gains `Sort` and `SearchAfter`;
  `SortAsc` / `SortDesc` build sort clauses; `LongField` and `DateField` add
  sortable field types.
- **Bulk chunking.** `BulkChunked` splits an import into bounded requests and
  merges the per-item results in order.
- **Retries.** A 429 or 503 on an idempotent request (GET, PUT, DELETE, and
  read-only POSTs such as search and recall) is retried up to three times,
  honouring `Retry-After` in seconds or HTTP-date form. A `Retry-After` longer
  than the cap is returned rather than shortened. `WithRetry(RetryPolicy{...})`
  and `WithoutRetry()` configure it. Writes sent by POST are never retried.
- **Environment defaults.** `New("")` reads `$GNARL_URL`; a client given no
  `WithToken` sends `$GNARL_TOKEN`.
- **Errors.** `ErrConflict` (409) and `ErrUnavailable` (503) sentinels. Every
  type in the description's `ErrorType` enum now maps to a sentinel, including
  `route_not_found`, `shared_pool`, `job_in_progress`,
  `namespace_not_snapshottable` and `unverified_signer`.

### Changed

- `errors.Is` matches the HTTP status **whatever the error type**. A 404 is
  `ErrNotFound` even for a type this client does not know. It used to fall
  back to the status only when the type was empty, so `route_not_found`
  failed `errors.Is(err, ErrNotFound)`.
- `unauthorized` maps to `ErrForbidden`, not `ErrUnauthenticated`. The node
  sends it only for a wrong namespace key, on a 403.
- `namespace_not_snapshottable` maps to `ErrConflict`, not `ErrUnsupported`.
  It means the namespace is mid-promotion: wait and retry.
- The vendored description is the server's current one. Engine names in it
  are now `native` and `lucene`. A node released before the rename reports
  the native engine as `tantivy`; `ListIndexes`, `ListIndexesPage` and
  `Remember` translate it, so a caller sees one name whichever node answers.

### Fixed

- `WithTimeout` and `WithInsecureSkipVerify` no longer modify an
  `*http.Client` or transport passed to `WithHTTPClient`; the client is copied
  and the transport cloned.
- The package documentation's example used `http://`, which cannot reach a
  default node. A node serves https with a self-signed certificate.

### Build

- `go generate ./...` works: oapi-codegen v2.8.0 is pinned as a `tool` in
  `tools.mod`, which keeps the module's own Go floor at 1.24. CI regenerates
  with that pin instead of `@latest`, and the unit and conformance jobs run on
  the Go version go.mod declares.
- CI runs conformance against the latest public release from
  `gnarl-dev/releases`, nightly as well as on push, and needs no private token.
