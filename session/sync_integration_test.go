package session

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncTaskInitializesLocalRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	ctx := context.Background()
	f := s.Features()
	if _, err := f.CreatePlaylist(ctx, PlaylistEdit{Title: "shared"}); err != nil {
		t.Fatal(err)
	}
	job, err := f.SyncAction(ctx, "init", SyncInput{Remote: remote, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var result Work
	waitFor(t, 5*time.Second, func() bool {
		works, e := f.Work(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, work := range works {
			if work.ID == job.ID {
				result = work
				return work.State != "running"
			}
		}
		return false
	})
	if result.State != "completed" {
		_, e := f.syncer.Client().Init(ctx, remote, "main", false, "")
		t.Fatalf("sync task: %+v; direct init diagnostic: %v", result, e)
	}
}
