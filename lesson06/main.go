package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func errorName(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline-exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return err.Error()
	}
}

func runCase(label string, ctx context.Context, repo UserRepository, ids []int64) {
	start := time.Now()
	results, err := FindWithPool(ctx, repo, ids, 2)
	if err != nil {
		fmt.Println("pool error:", err)
		return
	}
	fmt.Printf("case=%s count=%d elapsed=%dms stop=%s\n", label, len(results), time.Since(start).Milliseconds(), errorName(ctx.Err()))
	for _, result := range results {
		switch {
		case result.Err != nil:
			fmt.Printf("  index=%d id=%d error=%s\n", result.Index, result.ID, errorName(result.Err))
		case !result.Found:
			fmt.Printf("  index=%d id=%d not-found\n", result.Index, result.ID)
		default:
			fmt.Printf("  index=%d id=%d found=%s\n", result.Index, result.ID, result.User.Name)
		}
	}
}

func timeoutCase(repo UserRepository, ids []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel() // 200 毫秒以后 cancel 执行 然后 runCase 内部继续 但是内部的 ctx.Done() 会理解打断
	runCase("timeout", ctx, repo, ids)
}

func manualCase(repo UserRepository, ids []int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel) // time.AfterFunc(d, f) 的语义就是："d 之后，异步执行 f"。这里传进去的 cancel 类型是 context.CancelFunc，它就是 func()：
	defer timer.Stop()
	runCase("manual-cancel", ctx, repo, ids) // 如果只靠 defer cancel()，取消会发生在 runCase 跑完之后，那取消就毫无意义了。AfterFunc 里的 cancel 是为了在 runCase 运行期间就把 ctx 取消掉
}

func main() {
	repo := NewMemoryUserRepository()
	ids := []int64{1, 2, 1, 404, 500}
	runCase("normal", context.Background(), repo, ids)
	timeoutCase(repo, ids)
	manualCase(repo, ids)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runCase("already-cancelled", ctx, repo, ids)
}
