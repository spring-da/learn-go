package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// 变量注入
//
//go:embed schema.sql
var schema string

var ErrNameExists = errors.New("name already exists")

type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
	Create(ctx context.Context, name string) (User, error)
}

type SQLUserRepository struct {
	db *sql.DB
}

// 调用方负责先创建数据库文件的父目录。
func OpenUserRepository(ctx context.Context, path string) (*SQLUserRepository, error) {
	abs, err := filepath.Abs(path) // 获取绝对路径
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)      // Windows 的 \ 转成 /
	if !strings.HasPrefix(uriPath, "/") { // 是否以 / 开头
		uriPath = "/" + uriPath
	}

	// 设置数据库连接参数
	dsn := url.URL{Scheme: "file", Path: uriPath}
	params := url.Values{}
	params.Add("_pragma", "busy_timeout(5000)") // 数据库被锁时，最多等 5000 毫秒再报 "database is locked"，避免立即失败
	params.Add("_pragma", "foreign_keys(1)")    // 开启外键约束（SQLite 默认关闭，必须手动开，且是连接级的）
	params.Add("_pragma", "synchronous(FULL)")  // 同步级别 FULL：每次事务提交都确保落盘，最安全（慢一点），断电不易丢数据
	dsn.RawQuery = params.Encode()
	// SQLite 是文件级数据库，同一时刻只允许一个写者（读可以并发，写要串行）。连接开太多没意义，反而增加锁竞争。MaxOpenConns=4 是个务实的小值，配合 busy_timeout 让写冲突时排队等待而不是直接报错。
	//对比 MySQL/Postgres，那里常见 MaxOpenConns 几十上百；SQLite 则要压低。

	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4) // 最多同时 4 条连接。
	db.SetMaxIdleConns(4) // 空闲时保留 4 条连接在池里复用。

	// WAL 是数据库文件的持久设置；连接级设置放在上面的 DSN 中。
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return &SQLUserRepository{db: db}, nil
}

func (r *SQLUserRepository) Close() error {
	return r.db.Close()
}

func (r *SQLUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	var user User
	err := r.db.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", id).Scan(&user.ID, &user.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	return user, true, nil
}

func (r *SQLUserRepository) Create(ctx context.Context, name string) (User, error) {
	var user User
	err := r.db.QueryRowContext(ctx,
		"INSERT INTO users (name) VALUES (?) RETURNING id, name", name,
	).Scan(&user.ID, &user.Name)
	if err != nil {
		var databaseError *sqlite.Error
		if errors.As(err, &databaseError) && databaseError.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return User{}, ErrNameExists
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}
