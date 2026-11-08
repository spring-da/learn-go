package main

import "sync"

type LookupResult struct {
	ID    int64
	User  User
	Found bool
	Err   error
}

func lookup(repo UserRepository, id int64) LookupResult {
	user, found, err := repo.FindByID(id)
	return LookupResult{ID: id, User: user, Found: found, Err: err}
}

func FindSequentially(repo UserRepository, ids []int64) []LookupResult {
	all := make([]LookupResult, 0, len(ids))
	for _, id := range ids {
		all = append(all, lookup(repo, id))
	}
	return all
}

func FindConcurrently(repo UserRepository, ids []int64) []LookupResult {
	results := make(chan LookupResult)
	var wg sync.WaitGroup

	for _, id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			results <- lookup(repo, id)
		}(id)
	}

	// 等所有发送者结束后，由协调者关闭 channel。
	go func() {
		wg.Wait()
		close(results)
	}()

	// 只有当前 goroutine 追加切片；查询任务通过 channel 交回结果。
	all := make([]LookupResult, 0, len(ids))
	for result := range results {
		all = append(all, result)
	}
	return all
}
