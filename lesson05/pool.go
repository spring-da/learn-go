package main

import (
	"errors"
	"sort"
	"sync"
)

type Job struct {
	Index int
	ID    int64
}

type LookupResult struct {
	Index int
	ID    int64
	User  User
	Found bool
	Err   error
}

func FindWithPool(repo UserRepository, ids []int64, workers int) ([]LookupResult, error) {
	if workers <= 0 {
		return nil, errors.New("workers must be greater than zero")
	}
	if len(ids) == 0 {
		return []LookupResult{}, nil
	}
	if workers > len(ids) {
		workers = len(ids)
	}

	// 等待队列容量与查询并发数是两个独立的配置；本例都设为 workers。
	jobs := make(chan Job, workers)
	results := make(chan LookupResult)
	var wg sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				user, found, err := repo.FindByID(job.ID)
				results <- LookupResult{
					Index: job.Index, ID: job.ID,
					User: user, Found: found, Err: err,
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index, id := range ids {
			jobs <- Job{Index: index, ID: id}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	all := make([]LookupResult, 0, len(ids))
	for result := range results {
		all = append(all, result)
	}
	// 先按完成情况接收，再按输入位置重排；重复 ID 仍是独立任务。
	sort.Slice(all, func(i, j int) bool {
		return all[i].Index < all[j].Index
	})
	return all, nil
}
