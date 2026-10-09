package gnarl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gnarl-dev/go-client/internal/oas"
)

// Backup and restore.
//
// Register a repository — a directory, or any S3-compatible bucket — then
// take, list, restore and delete snapshots in it. Every route here needs the
// admin role on a private mesh: restoring writes over live data, deleting
// destroys a backup, and registering a repository decides where this node's
// data is copied to.
//
// Snapshot, restore and cleanup are long-running. They return a SnapshotJob
// straight away and run one at a time per node; WaitForJob follows one to the
// end. A second job while one runs is refused with 409
// (errors.Is(err, ErrConflict)).

// RepositorySpec says where a repository's bytes live. It is an FSRepository
// or an S3Repository.
//
// The variants are sent as the generated FsRepositorySpec and S3RepositorySpec
// directly, NOT through the generated union's From helpers. The description's
// discriminator has no `mapping`, so those helpers stamp the SCHEMA name into
// the discriminator — `"type":"FsRepositorySpec"` — where the node expects
// `"fs"`, and every registration would be refused.
type RepositorySpec interface {
	repositorySpec() any
}

// FSRepository is a repository in a directory on the node.
type FSRepository struct {
	// Location is the directory. Required.
	Location string

	// EncryptionKey, if set, is a 32-byte key in hex that seals every object
	// written, so the store holds ciphertext only. Losing it loses the
	// backup. It is never returned by any endpoint.
	EncryptionKey string
}

func (r FSRepository) repositorySpec() any {
	return oas.FsRepositorySpec{
		Type:          oas.FsRepositorySpecTypeFs,
		Location:      r.Location,
		EncryptionKey: opt(r.EncryptionKey),
	}
}

// S3Repository is a repository in an S3-compatible bucket — AWS S3, MinIO,
// Ceph, Cloudflare R2, or Google Cloud Storage through its interoperability
// endpoint.
type S3Repository struct {
	// Endpoint is the scheme and host, e.g. https://s3.us-east-1.amazonaws.com.
	Endpoint string
	Bucket   string
	// Region is the signing region. GCS and R2 accept "auto" or "us-east-1".
	Region string
	// Prefix lets one bucket hold several repositories.
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is for temporary STS credentials.
	SessionToken string
	// VirtualHost puts the bucket in the host name rather than the path.
	// The default, path style, is what MinIO and most self-hosted stores
	// require.
	VirtualHost bool
	// EncryptionKey is as for FSRepository.
	EncryptionKey string
}

func (r S3Repository) repositorySpec() any {
	spec := oas.S3RepositorySpec{
		Type:            oas.S3RepositorySpecTypeS3,
		Endpoint:        r.Endpoint,
		Bucket:          r.Bucket,
		Region:          r.Region,
		Prefix:          opt(r.Prefix),
		AccessKeyId:     r.AccessKeyID,
		SecretAccessKey: r.SecretAccessKey,
		SessionToken:    opt(r.SessionToken),
		EncryptionKey:   opt(r.EncryptionKey),
	}
	if r.VirtualHost {
		pathStyle := false
		spec.PathStyle = &pathStyle
	}
	return spec
}

// Repository is a registered repository's non-secret configuration. Secrets
// are omitted, not masked.
type Repository = oas.RegisteredRepository

func repoPath(repo string) string { return "/v1/repositories/" + pathEscape(repo) }

// RegisterRepository registers (or replaces) a repository under name.
//
// The node opens and lists the repository before accepting it, so a wrong
// bucket or a bad credential fails here rather than inside a scheduled backup
// nobody is watching.
func (c *Client) RegisterRepository(ctx context.Context, name string, spec RepositorySpec) (*Repository, error) {
	if name == "" {
		return nil, fmt.Errorf("gnarl: RegisterRepository: empty repository name")
	}
	if spec == nil {
		return nil, fmt.Errorf("gnarl: RegisterRepository: nil spec")
	}
	var out Repository
	if err := c.do(ctx, http.MethodPut, repoPath(name), spec.repositorySpec(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRepository returns one repository.
func (c *Client) GetRepository(ctx context.Context, name string) (*Repository, error) {
	var out Repository
	if err := c.do(ctx, http.MethodGet, repoPath(name), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRepositories returns every repository registered on this node.
func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	var out struct {
		Repositories []Repository `json:"repositories"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/repositories", nil, &out); err != nil {
		return nil, err
	}
	return out.Repositories, nil
}

// UnregisterRepository forgets a repository. Its data is left alone:
// deleting someone's backups as a side effect of tidying a config would not
// be recoverable.
func (c *Client) UnregisterRepository(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, repoPath(name), nil, nil)
}

// CleanupRepository reclaims segments no surviving snapshot references.
// Objects younger than grace are kept, because a snapshot writes its segments
// before its descriptor; zero means the node's default of a day.
func (c *Client) CleanupRepository(ctx context.Context, repo string, grace time.Duration) (*SnapshotJob, error) {
	body := oas.CleanupRepositoryJSONRequestBody{}
	if grace > 0 {
		secs := int(grace / time.Second)
		body.GraceSeconds = &secs
	}
	var out SnapshotJob
	if err := c.do(ctx, http.MethodPost, repoPath(repo)+"/_cleanup", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ─── Snapshots ──────────────────────────────────────────────────────────────

// SnapshotJob is a snapshot, restore or cleanup in flight or finished.
type SnapshotJob = oas.SnapshotJob

// SnapshotDescriptor is what a snapshot captured, signed by the node that
// took it. Read SignatureVerified before trusting it: a descriptor that merely
// parses proves nothing.
type SnapshotDescriptor = oas.SnapshotDescriptor

// CreateSnapshotRequest names what to capture: exactly one of Index or
// Namespace.
type CreateSnapshotRequest = oas.CreateSnapshotRequest

// RestoreRequest controls a restore. The zero value refuses a snapshot whose
// signer this node cannot verify, and refuses to overwrite a live index.
type RestoreRequest = oas.RestoreRequest

// Job states.
const (
	JobRunning   = oas.SnapshotJobStateRunning
	JobSucceeded = oas.SnapshotJobStateSucceeded
	JobFailed    = oas.SnapshotJobStateFailed
)

func snapshotPath(repo, snapshot string) string {
	return repoPath(repo) + "/snapshots/" + pathEscape(snapshot)
}

// CreateSnapshot starts a snapshot named snapshot in repo.
//
// A pooled namespace has no segments of its own, so its documents are
// exported instead (the job's result reports kind "logical"): exact in
// content, but not a point-in-time cut. Promote the namespace when that
// matters.
func (c *Client) CreateSnapshot(ctx context.Context, repo, snapshot string, req CreateSnapshotRequest) (*SnapshotJob, error) {
	hasIndex := req.Index != nil && *req.Index != ""
	hasNamespace := req.Namespace != nil && *req.Namespace != ""
	if hasIndex == hasNamespace {
		return nil, fmt.Errorf("gnarl: CreateSnapshot: set exactly one of Index or Namespace")
	}
	var out SnapshotJob
	if err := c.do(ctx, http.MethodPut, snapshotPath(repo, snapshot), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SnapshotIndex is CreateSnapshot for one index.
func (c *Client) SnapshotIndex(ctx context.Context, repo, snapshot, index string) (*SnapshotJob, error) {
	return c.CreateSnapshot(ctx, repo, snapshot, CreateSnapshotRequest{Index: &index})
}

// SnapshotNamespace is CreateSnapshot for one namespace.
func (c *Client) SnapshotNamespace(ctx context.Context, repo, snapshot, namespace string) (*SnapshotJob, error) {
	return c.CreateSnapshot(ctx, repo, snapshot, CreateSnapshotRequest{Namespace: &namespace})
}

// GetSnapshot returns a snapshot's descriptor.
func (c *Client) GetSnapshot(ctx context.Context, repo, snapshot string) (*SnapshotDescriptor, error) {
	var out SnapshotDescriptor
	if err := c.do(ctx, http.MethodGet, snapshotPath(repo, snapshot), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSnapshots returns the names of the snapshots in repo.
func (c *Client) ListSnapshots(ctx context.Context, repo string) ([]string, error) {
	var out struct {
		Snapshots []string `json:"snapshots"`
	}
	if err := c.do(ctx, http.MethodGet, repoPath(repo)+"/snapshots", nil, &out); err != nil {
		return nil, err
	}
	return out.Snapshots, nil
}

// DeleteSnapshot deletes a snapshot's descriptor. Segments are shared between
// snapshots, so reclaiming them is CleanupRepository's job.
func (c *Client) DeleteSnapshot(ctx context.Context, repo, snapshot string) error {
	return c.do(ctx, http.MethodDelete, snapshotPath(repo, snapshot), nil, nil)
}

// RestoreSnapshot starts a restore. A restore is not retried on 429 or 503:
// it writes over live data, and the decision to do that twice belongs to the
// caller.
func (c *Client) RestoreSnapshot(ctx context.Context, repo, snapshot string, req RestoreRequest) (*SnapshotJob, error) {
	var out SnapshotJob
	if err := c.do(ctx, http.MethodPost, snapshotPath(repo, snapshot)+"/_restore", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ─── Jobs ───────────────────────────────────────────────────────────────────

// ListSnapshotJobs returns this node's snapshot jobs, newest first. History
// survives a restart; a job that was running when the node stopped comes back
// as failed, because whether it finished is unknown.
func (c *Client) ListSnapshotJobs(ctx context.Context) ([]SnapshotJob, error) {
	var out struct {
		Jobs []SnapshotJob `json:"jobs"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/snapshot_jobs", nil, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

// GetSnapshotJob returns one job.
func (c *Client) GetSnapshotJob(ctx context.Context, id string) (*SnapshotJob, error) {
	if id == "" {
		return nil, fmt.Errorf("gnarl: GetSnapshotJob: empty job id")
	}
	var out SnapshotJob
	if err := c.do(ctx, http.MethodGet, "/v1/snapshot_jobs/"+pathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JobFailedError is returned by WaitForJob when the job finished and failed.
type JobFailedError struct {
	Job *SnapshotJob
}

func (e *JobFailedError) Error() string {
	reason := "no reason given"
	if e.Job.Error != nil && *e.Job.Error != "" {
		reason = *e.Job.Error
	}
	id := ""
	if e.Job.Id != nil {
		id = *e.Job.Id
	}
	return fmt.Sprintf("gnarl: snapshot job %s failed: %s", id, reason)
}

// WaitForJob polls a job every interval until it is no longer running, and
// returns it. A failed job returns the job AND a *JobFailedError, so a caller
// that only checks err cannot mistake a failure for success. interval zero
// means one second.
//
// The context bounds the wait. A job keeps running on the node when the wait
// is abandoned; this stops watching it, nothing more.
func (c *Client) WaitForJob(ctx context.Context, id string, interval time.Duration) (*SnapshotJob, error) {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		job, err := c.GetSnapshotJob(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case job.State == nil:
			return job, fmt.Errorf("gnarl: snapshot job %s has no state", id)
		case *job.State == JobSucceeded:
			return job, nil
		case *job.State == JobFailed:
			return job, &JobFailedError{Job: job}
		case *job.State != JobRunning:
			// A state this client does not know. Waiting on it could wait
			// forever, so surface it.
			return job, fmt.Errorf("gnarl: snapshot job %s is in unknown state %q", id, *job.State)
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-t.C:
		}
	}
}

// ─── Schedule ───────────────────────────────────────────────────────────────

// BackupSchedule is an automatic backup and what has happened to it.
// NextDueInSeconds and NextSnapshotName are derived on each read.
type BackupSchedule = oas.BackupSchedule

// ScheduleRequest sets a repository's automatic backup.
type ScheduleRequest struct {
	// Target is the index to back up. Required.
	Target string

	// Every is the cadence: one hour to one week, in whole hours. The node
	// divides time into fixed UTC windows of this length and takes one
	// backup per window, so a machine asleep at the moment still backs up
	// when it wakes.
	Every time.Duration

	// Disabled keeps the settings but stops the backups.
	Disabled bool

	// Prefix leads each generated snapshot name. Lower-case letters, digits
	// and '-'. Empty means the node's default, "auto".
	Prefix string
}

// GetBackupSchedule returns repo's schedule, or nil when the repository has
// none. A missing repository is an error (errors.Is(err, ErrNotFound)) — a
// different answer from "nothing scheduled".
func (c *Client) GetBackupSchedule(ctx context.Context, repo string) (*BackupSchedule, error) {
	var out struct {
		Schedule json.RawMessage `json:"schedule"`
	}
	if err := c.do(ctx, http.MethodGet, repoPath(repo)+"/schedule", nil, &out); err != nil {
		return nil, err
	}
	if len(out.Schedule) == 0 || string(out.Schedule) == "null" {
		return nil, nil
	}
	var s BackupSchedule
	if err := json.Unmarshal(out.Schedule, &s); err != nil {
		return nil, fmt.Errorf("gnarl: decoding backup schedule: %w", err)
	}
	return &s, nil
}

// SetBackupSchedule creates or edits repo's schedule and returns it as stored.
// Editing keeps the progress already made, so changing the cadence never
// costs a duplicate backup.
func (c *Client) SetBackupSchedule(ctx context.Context, repo string, req ScheduleRequest) (*BackupSchedule, error) {
	if req.Target == "" {
		return nil, fmt.Errorf("gnarl: SetBackupSchedule: empty target")
	}
	if req.Every%time.Hour != 0 || req.Every < time.Hour || req.Every > 168*time.Hour {
		return nil, fmt.Errorf("gnarl: SetBackupSchedule: Every must be whole hours from 1h to 168h, got %v", req.Every)
	}
	enabled := !req.Disabled
	body := oas.SetBackupScheduleJSONBody{
		Target:     req.Target,
		EveryHours: int(req.Every / time.Hour),
		Enabled:    &enabled,
		Prefix:     opt(req.Prefix),
	}
	var out struct {
		Schedule BackupSchedule `json:"schedule"`
	}
	if err := c.do(ctx, http.MethodPut, repoPath(repo)+"/schedule", body, &out); err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}

// ClearBackupSchedule stops automatic backups for repo. Backups already taken
// are untouched. removed is false when there was no schedule, which is not an
// error.
func (c *Client) ClearBackupSchedule(ctx context.Context, repo string) (removed bool, err error) {
	var out struct {
		Removed bool `json:"removed"`
	}
	if err := c.do(ctx, http.MethodDelete, repoPath(repo)+"/schedule", nil, &out); err != nil {
		return false, err
	}
	return out.Removed, nil
}
