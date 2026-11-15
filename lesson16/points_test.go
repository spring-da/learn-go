package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPointsHTTPConflictRecalculationAndRegistrationReplay(t *testing.T) {
	handler := newTestHandler(t)
	created := request(handler, "POST", "/users", "application/json", `{"name":"Alice","initial_points":100}`)
	var original User
	if json.Unmarshal(created.Body.Bytes(), &original) != nil || created.Code != 201 || original.Version != 0 {
		t.Fatalf("create=%d %s", created.Code, created.Body)
	}
	first := requestWithKey(handler, "PUT", "/users/1/points", "application/json", `{"points":120,"expected_version":0}`, "")
	var account PointsAccount
	if json.Unmarshal(first.Body.Bytes(), &account) != nil || first.Code != 200 || account != (PointsAccount{UserID: 1, Points: 120, Version: 1}) {
		t.Fatalf("update=%d %s", first.Code, first.Body)
	}
	stale := requestWithKey(handler, "PUT", "/users/1/points", "application/json", `{"points":80,"expected_version":0}`, "")
	var conflict ErrorBody
	if json.Unmarshal(stale.Body.Bytes(), &conflict) != nil || stale.Code != 409 || conflict.Error != ErrVersionConflict.Error() {
		t.Fatalf("stale=%d %s", stale.Code, stale.Body)
	}
	current := request(handler, "GET", "/users/1", "", "")
	var user User
	if json.Unmarshal(current.Body.Bytes(), &user) != nil || user.Points != 120 || user.Version != 1 {
		t.Fatalf("current=%s", current.Body)
	}
	// B 的意图是扣 20，基于新积分重新计算 120-20，而不是只换版本号继续写旧的 80。
	retry := requestWithKey(handler, "PUT", "/users/1/points", "application/json", `{"points":100,"expected_version":1}`, "")
	if json.Unmarshal(retry.Body.Bytes(), &account) != nil || retry.Code != 200 || account.Points != 100 || account.Version != 2 {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body)
	}
	replay := request(handler, "POST", "/users", "application/json", `{"name":"Alice","initial_points":100}`)
	if replay.Code != 201 || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != created.Body.String() {
		t.Fatalf("registration snapshot changed: %d %s", replay.Code, replay.Body)
	}
	list := request(handler, "GET", "/outbox", "", "")
	var events []OutboxItem
	if json.Unmarshal(list.Body.Bytes(), &events) != nil || len(events) != 1 || events[0].Event.Points != 100 {
		t.Fatalf("registration event changed: %s", list.Body)
	}
}

func TestInvalidPointsUpdateRequestsLeaveAccountUnchanged(t *testing.T) {
	handler := newTestHandler(t)
	created := request(handler, "POST", "/users", "application/json", `{"name":"Alice","initial_points":100}`)
	if created.Code != 201 {
		t.Fatal(created.Body)
	}
	cases := []struct {
		path, contentType, body string
		status                  int
	}{
		{"/users/nope/points", "application/json", `{"points":120,"expected_version":0}`, 400},
		{"/users/0/points", "application/json", `{"points":120,"expected_version":0}`, 400},
		{"/users/99/points", "application/json", `{"points":120,"expected_version":0}`, 404},
		{"/users/1/points", "", `{"points":120,"expected_version":0}`, 415},
		{"/users/1/points", "text/plain", `{"points":120,"expected_version":0}`, 415},
		{"/users/1/points", "application/json", `null`, 400},
		{"/users/1/points", "application/json", `[120,0]`, 400},
		{"/users/1/points", "application/json", `{}`, 400},
		{"/users/1/points", "application/json", `{"points":120}`, 400},
		{"/users/1/points", "application/json", `{"points":null,"expected_version":0}`, 400},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":null}`, 400},
		{"/users/1/points", "application/json", `{"points":1.5,"expected_version":0}`, 400},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":"0"}`, 400},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":9223372036854775808}`, 400},
		{"/users/1/points", "application/json", `{"points":9223372036854775808,"expected_version":0}`, 400},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":0,"extra":1}`, 400},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":0} {}`, 400},
		{"/users/1/points", "application/json", `{"points":-1,"expected_version":0}`, 422},
		{"/users/1/points", "application/json", `{"points":120,"expected_version":-1}`, 422},
		{"/users/1/points", "application/json", strings.Repeat(" ", 1100) + `{}`, 413},
	}
	for _, c := range cases {
		response := requestWithKey(handler, "PUT", c.path, c.contentType, c.body, "")
		var body ErrorBody
		if response.Code != c.status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error == "" {
			t.Fatalf("body=%q path=%s status=%d want=%d result=%s", c.body, c.path, response.Code, c.status, response.Body)
		}
	}
	method := request(handler, "POST", "/users/1/points", "application/json", `{}`)
	if method.Code != 405 || method.Header().Get("Allow") != "PUT" {
		t.Fatalf("method=%d allow=%q", method.Code, method.Header().Get("Allow"))
	}
	current := request(handler, http.MethodGet, "/users/1", "", "")
	var user User
	if json.Unmarshal(current.Body.Bytes(), &user) != nil || user.Points != 100 || user.Version != 0 {
		t.Fatal("invalid requests changed account")
	}
}

func TestConcurrentSameVersionAcrossPoolsHasOneWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "race.db")
	first, second := openTestRepository(t, path), openTestRepository(t, path)
	registerForDelivery(t, first)
	start := make(chan struct{})
	type outcome struct {
		account PointsAccount
		err     error
	}
	results := make(chan outcome, 32)
	for index := 0; index < 32; index++ {
		repo := first
		if index%2 != 0 {
			repo = second
		}
		go func(points int64) {
			<-start
			account, err := repo.ReplacePoints(ctx, 1, points, 0)
			results <- outcome{account, err}
		}(1000 + int64(index))
	}
	close(start)
	winners, conflicts := 0, 0
	var winner PointsAccount
	for index := 0; index < 32; index++ {
		result := <-results
		if result.err == nil {
			winners++
			winner = result.account
		} else if errors.Is(result.err, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(result.err)
		}
	}
	if winners != 1 || conflicts != 31 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	current, found, err := second.FindByID(ctx, 1)
	if err != nil || !found || current.Points != winner.Points || current.Version != 1 || winner.Version != 1 {
		t.Fatalf("current=%+v winner=%+v err=%v", current, winner, err)
	}
	assertCounts(t, first, 1, 1, 1)
}

func TestConcurrentReadRecalculateRetryKeepsEveryIncrement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry.db")
	first, second := openTestRepository(t, path), openTestRepository(t, path)
	registerForDelivery(t, first)
	start := make(chan struct{})
	results := make(chan error, 16)
	for index := 0; index < 16; index++ {
		repo := first
		if index%2 != 0 {
			repo = second
		}
		go func() {
			<-start
			for attempt := 0; attempt < 128; attempt++ {
				user, found, err := repo.FindByID(ctx, 1)
				if err != nil || !found {
					results <- errors.New("could not read current user")
					return
				}
				_, err = repo.ReplacePoints(ctx, 1, user.Points+1, user.Version)
				if err == nil {
					results <- nil
					return
				}
				if !errors.Is(err, ErrVersionConflict) {
					results <- err
					return
				}
				runtime.Gosched()
			}
			results <- errors.New("retry budget exhausted")
		}()
	}
	close(start)
	for index := 0; index < 16; index++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	current, found, err := first.FindByID(ctx, 1)
	if err != nil || !found || current.Points != 116 || current.Version != 16 {
		t.Fatalf("lost increment: %+v err=%v", current, err)
	}
}

func TestPointsValidationAndVersionLimit(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "limits.db"))
	registerForDelivery(t, repo)
	if _, err := repo.ReplacePoints(ctx, 99, 100, 0); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if _, err := repo.ReplacePoints(ctx, 1, -1, 0); !errors.Is(err, ErrInvalidUpdatedPoints) {
		t.Fatalf("negative points: %v", err)
	}
	if _, err := repo.ReplacePoints(ctx, 1, 120, -1); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("negative version: %v", err)
	}
	// 仅为边界测试直接设置版本；业务写入全部通过 ReplacePoints。
	if _, err := repo.db.Exec("UPDATE points_accounts SET version = ? WHERE user_id = 1", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplacePoints(ctx, 1, 120, math.MaxInt64); !errors.Is(err, ErrVersionExhausted) {
		t.Fatalf("version limit: %v", err)
	}
	current, _, err := repo.FindByID(ctx, 1)
	if err != nil || current.Points != 100 || current.Version != math.MaxInt64 {
		t.Fatalf("limit changed account: %+v %v", current, err)
	}
}

func TestFailedPointsStatementKeepsVersionAndSuccessfulUpdatePersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persist.db")
	repo := openTestRepository(t, path)
	registerForDelivery(t, repo)
	if _, err := repo.db.Exec(`CREATE TRIGGER reject_points BEFORE UPDATE ON points_accounts BEGIN SELECT RAISE(ABORT, 'simulated update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplacePoints(ctx, 1, 120, 0); err == nil {
		t.Fatal("expected statement failure")
	}
	unchanged, _, err := repo.FindByID(ctx, 1)
	if err != nil || unchanged.Points != 100 || unchanged.Version != 0 {
		t.Fatalf("failed statement partially applied: %+v %v", unchanged, err)
	}
	if _, err := repo.db.Exec("DROP TRIGGER reject_points"); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.ReplacePoints(ctx, 1, 120, 0)
	if err != nil || updated.Version != 1 {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	repo.Close()
	reopened := openTestRepository(t, path)
	current, _, err := reopened.FindByID(ctx, 1)
	if err != nil || current.Points != 120 || current.Version != 1 {
		t.Fatalf("reopened=%+v err=%v", current, err)
	}
	replay, err := reopened.Create(ctx, "outbox-key", "Alice", 100)
	if err != nil || !replay.Replayed || replay.User.Points != 100 || replay.User.Version != 0 {
		t.Fatalf("snapshot=%+v err=%v", replay, err)
	}
	assertCounts(t, reopened, 1, 1, 1)
}
