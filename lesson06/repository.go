package main

import (
	"context"
	"errors"
	"time"
)

type User struct {
	Name string
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
}

type MemoryUserRepository struct {
	users map[int64]User
}

func NewMemoryUserRepository() *MemoryUserRepository {
	return &MemoryUserRepository{
		users: map[int64]User{
			1: {Name: "Spring"},
			2: {Name: "Lin"},
		},
	}
}

func (r *MemoryUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	// 已取消的排队任务直接返回，不进入模拟查询等待。
	if err := ctx.Err(); err != nil {
		return User{}, false, err
	}

	delay := 50 * time.Millisecond
	switch id {
	case 1:
		delay = 240 * time.Millisecond
	case 2:
		delay = 80 * time.Millisecond
	case 404:
		delay = 160 * time.Millisecond
	case 500:
		delay = 120 * time.Millisecond
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return User{}, false, ctx.Err()
	case <-timer.C:
	}
	// 查询等待完成与取消可能接近同时发生，再检查一次。
	if err := ctx.Err(); err != nil {
		return User{}, false, err
	}
	if id == 500 {
		return User{}, false, errors.New("backend-unavailable")
	}
	user, found := r.users[id]
	return user, found, nil
}
