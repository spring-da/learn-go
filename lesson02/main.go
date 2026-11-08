package main

import "fmt"

type User struct {
	Name string
}

// 接口只声明业务需要的能力。
type UserRepository interface {
	FindByID(id int64) (User, bool)
}

type MemoryUserRepository struct {
	users map[int64]User
}

// 没有 implements：*MemoryUserRepository 的方法满足接口，就实现了接口。
func (r *MemoryUserRepository) FindByID(id int64) (User, bool) {
	user, ok := r.users[id]
	return user, ok
}

// 业务依赖接口；换一个仓库实现时，不需要改这个函数。
func Welcome(repo UserRepository, id int64) string {
	user, ok := repo.FindByID(id)
	if !ok {
		return fmt.Sprintf("user %d not found", id)
	}
	return fmt.Sprintf("hello, %s", user.Name)
}

func main() {
	repo := &MemoryUserRepository{
		users: map[int64]User{
			1: {Name: "Spring"},
			2: {Name: "Lin"},
		},
	}

	// range 返回下标和值；这里用 _ 忽略下标。
	for _, id := range []int64{1, 2, 404} {
		fmt.Printf("id=%d -> %s\n", id, Welcome(repo, id))
	}

	// 练习：增加 EmptyUserRepository，再把它传给 Welcome。
}
