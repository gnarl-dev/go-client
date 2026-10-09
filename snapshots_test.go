package gnarl

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// The node expects the discriminator "fs"/"s3". The generated union's helper
// writes the schema name instead, which the node refuses — so the wire value
// is pinned here.
func TestRegisterRepositorySendsTheWireDiscriminator(t *testing.T) {
	c, f := replying(t, 201, `{"repository":"r","spec":{"type":"s3","bucket":"b","encrypted":true}}`)
	ctx := context.Background()

	if _, err := c.RegisterRepository(ctx, "r", FSRepository{Location: "/backups"}); err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "PUT", "/v1/repositories/r", "")
	if s.field(t, "type") != "fs" || s.field(t, "location") != "/backups" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "encryption_key")

	repo, err := c.RegisterRepository(ctx, "r", S3Repository{
		Endpoint: "https://s3", Bucket: "b", Region: "auto",
		AccessKeyID: "AK", SecretAccessKey: "SK", VirtualHost: true, EncryptionKey: "00ff",
	})
	if err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	if s.field(t, "type") != "s3" || s.field(t, "access_key_id") != "AK" ||
		s.field(t, "secret_access_key") != "SK" || s.field(t, "path_style") != false ||
		s.field(t, "encryption_key") != "00ff" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "prefix", "session_token")
	if repo.Spec == nil || repo.Spec.Bucket == nil || *repo.Spec.Bucket != "b" {
		t.Errorf("decoded %+v", repo)
	}

	// Path style is the default: say nothing and let the node decide.
	if _, err := c.RegisterRepository(ctx, "r", S3Repository{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	f.last(t).absent(t, "path_style")
}

func TestRepositoryAndSnapshotRoutes(t *testing.T) {
	c, f := replying(t, 200, `{"repositories":[{"repository":"r"}],"snapshots":["s1","s2"],
		"id":"j1","state":"running","kind":"snapshot","snapshot":"s1","signature_verified":true,
		"jobs":[{"id":"j1"}]}`)
	ctx := context.Background()

	repos, err := c.ListRepositories(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("ListRepositories = %v, %v", repos, err)
	}
	f.last(t).want(t, "GET", "/v1/repositories", "")

	if _, err := c.GetRepository(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", "/v1/repositories/r", "")

	if err := c.UnregisterRepository(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "DELETE", "/v1/repositories/r", "")

	if _, err := c.CleanupRepository(ctx, "r", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	s.want(t, "POST", "/v1/repositories/r/_cleanup", "")
	if s.field(t, "grace_seconds") != float64(7200) {
		t.Errorf("body = %s", s.Raw)
	}

	names, err := c.ListSnapshots(ctx, "r")
	if err != nil || len(names) != 2 {
		t.Fatalf("ListSnapshots = %v, %v", names, err)
	}
	f.last(t).want(t, "GET", "/v1/repositories/r/snapshots", "")

	job, err := c.SnapshotIndex(ctx, "r", "s1", "docs")
	if err != nil || job.Id == nil || *job.Id != "j1" {
		t.Fatalf("SnapshotIndex = %+v, %v", job, err)
	}
	s = f.last(t)
	s.want(t, "PUT", "/v1/repositories/r/snapshots/s1", "")
	if s.field(t, "index") != "docs" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "namespace")

	if _, err := c.SnapshotNamespace(ctx, "r", "s2", "tenant"); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	if s.field(t, "namespace") != "tenant" {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "index")

	d, err := c.GetSnapshot(ctx, "r", "s1")
	if err != nil || d.SignatureVerified == nil || !*d.SignatureVerified {
		t.Fatalf("GetSnapshot = %+v, %v", d, err)
	}
	f.last(t).want(t, "GET", "/v1/repositories/r/snapshots/s1", "")

	if err := c.DeleteSnapshot(ctx, "r", "s1"); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "DELETE", "/v1/repositories/r/snapshots/s1", "")

	yes := true
	if _, err := c.RestoreSnapshot(ctx, "r", "s1", RestoreRequest{AllowOverwriteLiveIndex: &yes}); err != nil {
		t.Fatal(err)
	}
	s = f.last(t)
	s.want(t, "POST", "/v1/repositories/r/snapshots/s1/_restore", "")
	if s.field(t, "allow_overwrite_live_index") != true {
		t.Errorf("body = %s", s.Raw)
	}
	s.absent(t, "allow_unverified_signer", "signer_public_key")

	jobs, err := c.ListSnapshotJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ListSnapshotJobs = %v, %v", jobs, err)
	}
	f.last(t).want(t, "GET", "/v1/snapshot_jobs", "")

	if _, err := c.GetSnapshotJob(ctx, "j1"); err != nil {
		t.Fatal(err)
	}
	f.last(t).want(t, "GET", "/v1/snapshot_jobs/j1", "")
}

func TestCreateSnapshotNeedsExactlyOneTarget(t *testing.T) {
	c, f := replying(t, 202, `{}`)
	idx, ns := "i", "n"
	for _, req := range []CreateSnapshotRequest{{}, {Index: &idx, Namespace: &ns}} {
		if _, err := c.CreateSnapshot(context.Background(), "r", "s", req); err == nil {
			t.Errorf("%+v was accepted", req)
		}
	}
	if f.count() != 0 {
		t.Error("an invalid snapshot request reached the node")
	}
}

func TestWaitForJobPollsUntilItFinishes(t *testing.T) {
	c, f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n < 3 {
			_, _ = w.Write([]byte(`{"id":"j","state":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"j","state":"succeeded","result":{"kind":"physical"}}`))
	})
	job, err := c.WaitForJob(context.Background(), "j", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if *job.State != JobSucceeded || f.count() != 3 {
		t.Errorf("state %v after %d polls", *job.State, f.count())
	}
}

// A failed job must be an error: a caller checking only err must not read a
// failed backup as a successful one.
func TestWaitForJobReportsFailureAsAnError(t *testing.T) {
	c, _ := replying(t, 200, `{"id":"j","state":"failed","error":"bucket unreachable"}`)
	job, err := c.WaitForJob(context.Background(), "j", time.Millisecond)
	var jf *JobFailedError
	if !errors.As(err, &jf) {
		t.Fatalf("got %v, want *JobFailedError", err)
	}
	if job == nil || jf.Job != job {
		t.Error("the failed job is not returned for inspection")
	}
	if want := "gnarl: snapshot job j failed: bucket unreachable"; err.Error() != want {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestWaitForJobStopsWithTheContext(t *testing.T) {
	c, _ := replying(t, 200, `{"id":"j","state":"running"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.WaitForJob(ctx, "j", 5*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the context's deadline", err)
	}
}

func TestWaitForJobDoesNotWaitOnAnUnknownState(t *testing.T) {
	c, _ := replying(t, 200, `{"id":"j","state":"paused"}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.WaitForJob(ctx, "j", time.Millisecond); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want an immediate unknown-state error", err)
	}
}

func TestBackupScheduleRoutes(t *testing.T) {
	ctx := context.Background()

	// null schedule is "nothing scheduled", which is not an error.
	c, f := replying(t, 200, `{"repository":"r","schedule":null}`)
	s, err := c.GetBackupSchedule(ctx, "r")
	if err != nil || s != nil {
		t.Fatalf("GetBackupSchedule = %+v, %v; want nil, nil", s, err)
	}
	f.last(t).want(t, "GET", "/v1/repositories/r/schedule", "")

	c, f = replying(t, 200, `{"repository":"r","schedule":{"target":"docs","everyHours":24,
		"enabled":true,"prefix":"auto","lastWindow":1760000000,"nextDueInSeconds":60,
		"nextSnapshotName":"auto-x"}}`)
	s, err = c.GetBackupSchedule(ctx, "r")
	if err != nil || s == nil || *s.Target != "docs" || *s.EveryHours != 24 || *s.LastWindow != 1760000000 {
		t.Fatalf("GetBackupSchedule = %+v, %v", s, err)
	}

	s, err = c.SetBackupSchedule(ctx, "r", ScheduleRequest{Target: "docs", Every: 24 * time.Hour})
	if err != nil || s == nil || *s.NextSnapshotName != "auto-x" {
		t.Fatalf("SetBackupSchedule = %+v, %v", s, err)
	}
	req := f.last(t)
	req.want(t, "PUT", "/v1/repositories/r/schedule", "")
	if req.field(t, "target") != "docs" || req.field(t, "everyHours") != float64(24) || req.field(t, "enabled") != true {
		t.Errorf("body = %s", req.Raw)
	}
	req.absent(t, "prefix")

	c, f = replying(t, 200, `{"repository":"r","removed":true}`)
	removed, err := c.ClearBackupSchedule(ctx, "r")
	if err != nil || !removed {
		t.Fatalf("ClearBackupSchedule = %v, %v", removed, err)
	}
	f.last(t).want(t, "DELETE", "/v1/repositories/r/schedule", "")
}

// A missing repository is not "nothing scheduled".
func TestBackupScheduleOfAMissingRepositoryIsNotFound(t *testing.T) {
	c, _ := replying(t, 404, `{"error":{"type":"repository_not_found","reason":"no repository 'r'"}}`)
	s, err := c.GetBackupSchedule(context.Background(), "r")
	if !errors.Is(err, ErrNotFound) || s != nil {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestSetBackupScheduleRefusesACadenceTheNodeCannotRun(t *testing.T) {
	c, f := replying(t, 200, `{}`)
	for _, every := range []time.Duration{0, 30 * time.Minute, 90 * time.Minute, 169 * time.Hour} {
		if _, err := c.SetBackupSchedule(context.Background(), "r", ScheduleRequest{Target: "t", Every: every}); err == nil {
			t.Errorf("Every = %v was accepted", every)
		}
	}
	if f.count() != 0 {
		t.Error("an invalid cadence reached the node")
	}
}
