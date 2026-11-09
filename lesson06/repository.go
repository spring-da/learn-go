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
	defer timer.Stop() // 相当于 java thread.sleep(delay); 区别在 defer 不是"立即执行 等当前函数 return 的时候再执行"，而不是在写这行的时候立刻调用。
	// 多个 case 同时就绪时，select 会伪随机挑一个执行，不会"按顺序"或"优先某个"。 谁先就绪就走谁，这正是"可被取消的 sleep"的本意。
	select {
	case <-ctx.Done(): // 被取消/超时了，别等了  ctx 被取消，channel 被 close，立即可读
		return User{}, false, ctx.Err()
	case <-timer.C: // 我睡够 delay 了  delay 时间到，收到一个时间值
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
