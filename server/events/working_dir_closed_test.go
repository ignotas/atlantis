// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// closedPullFixture clones a pull request into a fresh data dir. Each test uses
// its own repo name because the closed-pull state is process wide.
func closedPullFixture(t *testing.T, repoFullName string) (*events.FileWorkspace, models.Repo, models.PullRequest, string) {
	t.Helper()
	repoDir := initRepo(t)
	wd := &events.FileWorkspace{
		DataDir:                     t.TempDir(),
		CheckoutMerge:               false,
		TestingOverrideHeadCloneURL: fmt.Sprintf("file://%s", repoDir),
		GpgNoSigningEnabled:         true,
	}
	repo := models.Repo{FullName: repoFullName}
	pull := models.PullRequest{Num: 7, BaseRepo: repo, HeadBranch: "branch", State: models.OpenPullState}
	t.Cleanup(func() { events.MarkPullOpen(repoFullName, pull.Num) })

	cloneDir, err := wd.Clone(logging.NewNoopLogger(t), repo, pull, "default")
	Ok(t, err)
	return wd, repo, pull, cloneDir
}

func TestDelete_RefusesCloneOfClosedPull(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	wd, repo, pull, cloneDir := closedPullFixture(t, "closed/refuses-clone")
	pullDir := filepath.Dir(cloneDir)

	Ok(t, wd.Delete(logger, repo, pull))
	_, err := os.Stat(pullDir)
	Assert(t, os.IsNotExist(err), "expected pull directory to be deleted")

	// A command that started before the close must not clone the directory again.
	_, err = wd.Clone(logger, repo, pull, "default")
	ErrEquals(t, events.ErrPullClosed.Error(), err)
	_, err = wd.Clone(logger, repo, pull, "other")
	ErrEquals(t, events.ErrPullClosed.Error(), err)
	_, err = os.Stat(pullDir)
	Assert(t, os.IsNotExist(err), "expected pull directory to stay deleted")
}

func TestDelete_ReopenedPullCanCloneAgain(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	wd, repo, pull, cloneDir := closedPullFixture(t, "closed/reopened")

	Ok(t, wd.Delete(logger, repo, pull))
	events.MarkPullOpen(repo.FullName, pull.Num)

	newCloneDir, err := wd.Clone(logger, repo, pull, "default")
	Ok(t, err)
	Equals(t, cloneDir, newCloneDir)
	_, err = os.Stat(newCloneDir)
	Ok(t, err)
}

func TestDelete_OtherPullsUnaffected(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	wd, repo, pull, _ := closedPullFixture(t, "closed/other-pulls")

	Ok(t, wd.Delete(logger, repo, pull))

	otherPull := pull
	otherPull.Num = pull.Num + 1
	_, err := wd.Clone(logger, repo, otherPull, "default")
	Ok(t, err)
}

func TestDelete_WaitsForRunningSteps(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	wd, repo, pull, cloneDir := closedPullFixture(t, "closed/waits-for-steps")

	// Simulate a plan or apply step run holding the workspace read lock.
	unlockSteps := wd.GitReadLock(repo, pull, "default")

	// Delete must not remove the directory under the running steps, and must
	// not block the caller (the close webhook) while they run.
	Ok(t, wd.Delete(logger, repo, pull))
	_, err := os.Stat(cloneDir)
	Ok(t, err)

	// Commands that are still running cannot clone into the pull any more.
	_, err = wd.Clone(logger, repo, pull, "other")
	ErrEquals(t, events.ErrPullClosed.Error(), err)

	unlockSteps()
	pullDir := filepath.Dir(cloneDir)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err = os.Stat(pullDir)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %s to be deleted after the steps finished, stat error: %v", pullDir, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDelete_NonPRPullIsNotMarkedClosed(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	wd, repo, _, _ := closedPullFixture(t, "closed/non-pr")
	// API requests without a pull request use negative, per-request numbers.
	apiPull := models.PullRequest{Num: -1, BaseRepo: repo, HeadBranch: "branch"}

	_, err := wd.Clone(logger, repo, apiPull, "default")
	Ok(t, err)
	Ok(t, wd.Delete(logger, repo, apiPull))
	_, err = wd.Clone(logger, repo, apiPull, "default")
	Ok(t, err)
}
