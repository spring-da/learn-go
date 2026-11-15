package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrVersionConflict      = errors.New("version conflict; read the latest user before retrying")
	ErrVersionExhausted     = errors.New("account version cannot be advanced")
	ErrInvalidVersion       = errors.New("expected_version must be non-negative")
	ErrInvalidUpdatedPoints = errors.New("points must be non-negative")
)

type PointsAccount struct {
	UserID  int64 `json:"user_id"`
	Points  int64 `json:"points"`
	Version int64 `json:"version"`
}

func (r *SQLUserRepository) ReplacePoints(ctx context.Context, id, points, expectedVersion int64) (PointsAccount, error) {
	if points < 0 {
		return PointsAccount{}, ErrInvalidUpdatedPoints
	}
	if expectedVersion < 0 {
		return PointsAccount{}, ErrInvalidVersion
	}
	var account PointsAccount
	// 比较版本、写入积分、递增版本由同一条语句完成；不能拆成先查版本再无条件更新。
	err := r.db.QueryRowContext(ctx, `UPDATE points_accounts
		SET points = ?, version = version + 1
		WHERE user_id = ? AND version = ? AND version < 9223372036854775807
		RETURNING user_id, points, version`, points, id, expectedVersion,
	).Scan(&account.UserID, &account.Points, &account.Version)
	if err == nil {
		return account, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PointsAccount{}, fmt.Errorf("replace points: %w", err)
	}
	// 本课没有删除用户的接口；补查只区分不存在、版本冲突和版本耗尽，不决定是否写入。
	current, found, err := r.FindByID(ctx, id)
	if err != nil {
		return PointsAccount{}, err
	}
	if !found {
		return PointsAccount{}, ErrUserNotFound
	}
	if current.Version == math.MaxInt64 && expectedVersion == current.Version {
		return PointsAccount{}, ErrVersionExhausted
	}
	return PointsAccount{}, ErrVersionConflict
}
