package main

import (
	"errors"
	"time"
)

type User struct {
	Name string
}

type UserRepository interface {
	FindByID(id int64) (User, bool, error)
}

// 构造后只读取这个 map，不在查询过程中修改它。
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

func (r *MemoryUserRepository) FindByID(id int64) (User, bool, error) {
	// 模拟远程查询的等待时间，便于观察任务重叠。
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
	time.Sleep(delay)

	if id == 500 {
		return User{}, false, errors.New("simulated backend unavailable")
	}
	user, found := r.users[id]
	return user, found, nil
}
