package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

var (
	ErrInvalidTransfer     = errors.New("accounts must be distinct positive IDs and amount must be positive")
	ErrTransferKeyMismatch = errors.New("transfer idempotency key was used with different parameters")
	ErrInsufficientPoints  = errors.New("insufficient points")
	ErrPointsOverflow      = errors.New("destination points would exceed int64")
)

type TransferReceipt struct {
	ID          int64 `json:"id"`
	FromUserID  int64 `json:"from_user_id"`
	ToUserID    int64 `json:"to_user_id"`
	Amount      int64 `json:"amount"`
	FromPoints  int64 `json:"from_points"`
	ToPoints    int64 `json:"to_points"`
	FromVersion int64 `json:"from_version"`
	ToVersion   int64 `json:"to_version"`
}

type TransferResult struct {
	Transfer TransferReceipt
	Replayed bool
}

const transferColumns = "id, from_user_id, to_user_id, amount, from_points, to_points, from_version, to_version"

func scanTransfer(row interface{ Scan(...any) error }) (TransferReceipt, error) {
	var receipt TransferReceipt
	err := row.Scan(&receipt.ID, &receipt.FromUserID, &receipt.ToUserID, &receipt.Amount,
		&receipt.FromPoints, &receipt.ToPoints, &receipt.FromVersion, &receipt.ToVersion)
	return receipt, err
}

func (r *SQLUserRepository) FindTransfer(ctx context.Context, id int64) (TransferReceipt, bool, error) {
	receipt, err := scanTransfer(r.db.QueryRowContext(ctx, "SELECT "+transferColumns+" FROM transfers WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return TransferReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (r *SQLUserRepository) Transfer(ctx context.Context, key string, fromID, toID, amount int64) (TransferResult, error) {
	if fromID <= 0 || toID <= 0 || fromID == toID || amount <= 0 {
		return TransferResult{}, ErrInvalidTransfer
	}
	// _txlock=immediate 在查幂等键与余额前取得 SQLite 写入资格。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TransferResult{}, fmt.Errorf("begin transfer: %w", err)
	}
	defer tx.Rollback()
	saved, err := scanTransfer(tx.QueryRowContext(ctx, "SELECT "+transferColumns+" FROM transfers WHERE idempotency_key = ?", key))
	if err == nil {
		if saved.FromUserID != fromID || saved.ToUserID != toID || saved.Amount != amount {
			return TransferResult{}, ErrTransferKeyMismatch
		}
		if err := tx.Commit(); err != nil {
			return TransferResult{}, fmt.Errorf("finish transfer replay: %w", err)
		}
		return TransferResult{Transfer: saved, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TransferResult{}, fmt.Errorf("read transfer key: %w", err)
	}
	var fromPoints, fromVersion, toPoints, toVersion int64
	for _, account := range []struct {
		id              int64
		points, version *int64
	}{
		{fromID, &fromPoints, &fromVersion}, {toID, &toPoints, &toVersion},
	} {
		err := tx.QueryRowContext(ctx, "SELECT points, version FROM points_accounts WHERE user_id = ?", account.id).Scan(account.points, account.version)
		if errors.Is(err, sql.ErrNoRows) {
			return TransferResult{}, ErrUserNotFound
		}
		if err != nil {
			return TransferResult{}, fmt.Errorf("read transfer account: %w", err)
		}
	}
	if fromPoints < amount {
		return TransferResult{}, ErrInsufficientPoints
	}
	// 先做减法比较，避免先执行 toPoints+amount 就发生 int64 溢出。
	if amount > math.MaxInt64-toPoints {
		return TransferResult{}, ErrPointsOverflow
	}
	if fromVersion == math.MaxInt64 || toVersion == math.MaxInt64 {
		return TransferResult{}, ErrVersionExhausted
	}
	receipt := TransferReceipt{FromUserID: fromID, ToUserID: toID, Amount: amount}
	err = tx.QueryRowContext(ctx, `UPDATE points_accounts SET points = points - ?, version = version + 1
		WHERE user_id = ? AND points >= ? AND version = ? RETURNING points, version`, amount, fromID, amount, fromVersion,
	).Scan(&receipt.FromPoints, &receipt.FromVersion)
	if err != nil {
		return TransferResult{}, fmt.Errorf("debit transfer: %w", err)
	}
	err = tx.QueryRowContext(ctx, `UPDATE points_accounts SET points = points + ?, version = version + 1
		WHERE user_id = ? AND points <= ? AND version = ? RETURNING points, version`, amount, toID, math.MaxInt64-amount, toVersion,
	).Scan(&receipt.ToPoints, &receipt.ToVersion)
	if err != nil {
		return TransferResult{}, fmt.Errorf("credit transfer: %w", err)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO transfers
		(idempotency_key, from_user_id, to_user_id, amount, from_points, to_points, from_version, to_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, key, fromID, toID, amount,
		receipt.FromPoints, receipt.ToPoints, receipt.FromVersion, receipt.ToVersion,
	).Scan(&receipt.ID)
	if err != nil {
		return TransferResult{}, fmt.Errorf("save transfer receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TransferResult{}, fmt.Errorf("commit transfer: %w", err)
	}
	return TransferResult{Transfer: receipt}, nil
}
