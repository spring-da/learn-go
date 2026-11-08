package main

import (
	"errors"
	"fmt"
)

type User struct {
	Name   string
	Points int64
}

// *User 是指针接收者：通过它修改调用方的 User。
func (u *User) AddPoints(delta int64) error {
	if delta <= 0 {
		return errors.New("delta must be positive")
	}
	u.Points += delta
	return nil
}

func main() {
	user := User{Name: "Spring", Points: 10}

	if err := user.AddPoints(10); err != nil {
		fmt.Println("unexpected error:", err)
		return
	}
	fmt.Printf("after +10: name=%s points=%d\n", user.Name, user.Points)

	if err := user.AddPoints(-5); err != nil {
		fmt.Printf("rejected -5: %s; points=%d\n", err, user.Points)
	}

	// 复制结构体的值；修改副本的整数，不会修改原值的整数。
	copyUser := user
	copyUser.Points += 100
	fmt.Printf("value copy: original=%d copy=%d\n", user.Points, copyUser.Points)

	// 练习：把 AddPoints 的接收者改成 (u User)，先预测结果再运行。
}
