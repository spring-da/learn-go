package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTestRepository(t *testing.T, path string) *SQLUserRepository {
	t.Helper()
	repo, err := OpenUserRepository(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func assertTableCounts(t *testing.T, repo *SQLUserRepository, wantUsers, wantAccounts int) {
	t.Helper()
	var users, accounts int
	err := repo.db.QueryRowContext(context.Background(), `
		SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM points_accounts)
	`).Scan(&users, &accounts)
	if err != nil {
		t.Fatal(err)
	}
	if users != wantUsers || accounts != wantAccounts {
		t.Fatalf("users=%d accounts=%d, want users=%d accounts=%d", users, accounts, wantUsers, wantAccounts)
	}
}

func TestCommitPersistsBothRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registration.db")
	repo := openTestRepository(t, path)
	user, err := repo.Create(ctx, "O'Reilly", 100)
	if err != nil || user.Points != 100 {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	assertTableCounts(t, repo, 1, 1)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestRepository(t, path)
	fetched, found, err := reopened.FindByID(ctx, user.ID)
	if err != nil || !found || fetched != user {
		t.Fatalf("persisted user=%+v found=%v err=%v", fetched, found, err)
	}
	if _, err := reopened.Create(ctx, user.Name, 50); !errors.Is(err, ErrNameExists) {
		t.Fatalf("want name conflict, got %v", err)
	}
	assertTableCounts(t, reopened, 1, 1)
}

func TestSecondStatementFailureRollsBackAndAllowsRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollback.db")
	repo := openTestRepository(t, path)
	if _, err := repo.Create(ctx, "Recoverable", -1); !errors.Is(err, ErrInvalidPoints) {
		t.Fatalf("want CHECK failure, got %v", err)
	}
	// 直接检查物理表，不能只依赖 JOIN 查询看不见半成品。
	assertTableCounts(t, repo, 0, 0)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestRepository(t, path)
	assertTableCounts(t, reopened, 0, 0)
	user, err := reopened.Create(ctx, "Recoverable", 50)
	if err != nil || user.Points != 50 {
		t.Fatalf("retry user=%+v err=%v", user, err)
	}
	assertTableCounts(t, reopened, 1, 1)
}

func TestConcurrentRegistrationsAcrossPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	repos := []*SQLUserRepository{openTestRepository(t, path), openTestRepository(t, path)}
	start := make(chan struct{})
	outcomes := make(chan error, 32)
	for index := 0; index < 32; index++ {
		go func(repo *SQLUserRepository) {
			<-start
			_, err := repo.Create(context.Background(), "Concurrent", 100)
			outcomes <- err
		}(repos[index%len(repos)])
	}
	close(start)
	created := 0
	for index := 0; index < 32; index++ {
		err := <-outcomes
		if err == nil {
			created++
		} else if !errors.Is(err, ErrNameExists) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d, want exactly one", created)
	}
	assertTableCounts(t, repos[0], 1, 1)
}
