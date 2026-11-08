# 第六课：context、超时与协作式取消

目标：把取消信号从调用方传到 worker，再传到查询的等待点；响应信号后完成任务退出与结果汇总。

## 运行与阅读顺序

```powershell
Set-Location E:\WorkProject\go\lesson06
go run .
```

先看 main.go 中 timeoutCase 和 manualCase，再看 repository.go 的 select，最后看 pool.go 如何等待 worker。
本例仍使用 2 个查询 worker，输入 `[1, 2, 1, 404, 500]`，数据与等待时长沿用上一课。

## 四种情况

| case | 调用方行为 | 通常观察到的结果 |
|---|---|---|
| normal | 不设置取消和超时 | 约 440ms；找到、未找到与模拟后端错误均保留 |
| timeout | 整批共享 200ms 预算 | 已完成结果保留，其余查询返回 deadline-exceeded |
| manual-cancel | 约 100ms 后调用 cancel | 已完成结果保留，其余查询返回 cancelled |
| already-cancelled | 调用前已 cancel | 各查询入口立即返回 cancelled |

四种 case 都为每个输入产生一条结果，count=5，重复 ID 与输入顺序继续保留。
任务完成与取消接近同时发生时，具体哪些结果已完成会受调度影响。
程序输出 elapsed 是观测值，不是严格时限保证。

## context 怎样向下传递

```go
FindWithPool(ctx, repo, ids, workers)
repo.FindByID(ctx, id)
```

context 作为请求相关操作的第一个参数显式向下传递。
同一批次共享一个 ctx，所以超时预算从创建 context 时开始计算，而不是每个 worker 各获得 200ms。

`WithTimeout` 创建带截止时间的 context；`WithCancel` 创建可主动取消的 context。
它们返回的 cancel 应在操作结束时调用，以释放关联资源，即使操作已经提前完成。
本例用 defer cancel() 安排清理。

## 查询怎样响应取消

```go
select {
case <-ctx.Done():
    return User{}, false, ctx.Err()
case <-timer.C:
}
```

Done 是一个在 context 结束时关闭的 channel，关闭会使等待该 channel 的接收操作就绪。
timer.C 表示模拟查询等待完成。select 等待任一分支就绪，再执行对应分支。
如果多个分支同时就绪，select 不保证优先处理取消，所以模拟等待结束后又检查一次 ctx.Err()。

查询入口也检查 ctx.Err()，因此取消后排队任务能直接产生错误结果，不再进入模拟等待。
ctx.Err() 用于区分 context.Canceled 和 context.DeadlineExceeded；本例用 errors.Is 判断。

这个 timer 是可取消等待的演示。实际 HTTP、数据库调用应使用底层库提供的 context 接口，
使取消能作用到真正的 I/O 等待；仅在外层传一个 ctx 不会让任意阻塞函数自动响应。

## 取消与等待结束

cancel() 发出信号，调用本身不等待 worker 结束。
本例仍由 worker 交回结果，由 WaitGroup 确认 worker 全部退出，再关闭结果通道。
FindWithPool 会持续接收直到结果流结束才返回。

这个案例选择“每个输入都有结果”的契约：取消后仍会投递、取出并整理剩余任务的元数据，
查询入口将它们快速标记为取消。取消错误保存在 LookupResult.Err 中，函数外层 error 只表达配置错误。
因此待处理输入很多时，整理结果仍有开销；期限到达是停止信号，函数返回还要等待协作退出。

仓库如果忽略 ctx 或执行不能中断的长阻塞操作，整批返回时间就仍受该操作约束。
这可以联系 Java 中的中断标志与任务配合检查来理解。

## 动手练习

1. 把超时预算改成 50ms、500ms，观察保留的结果和错误类型。
2. 在一次实验中，把可取消 select 替换成 `time.Sleep(delay)`，保留前后的 ctx.Err() 检查，观察超时后为何仍要等待已开始的慢查询。实验后恢复。
3. 思考另一种 API：取消后只返回已开始的任务结果。如果采用它，怎样让生产者和发送结果的 worker 都能结束？先写出行为约定，再改代码。

Rust 对照项目：`E:\WorkProject\rust\lesson06`。

## 官方说明

- [context 与取消规则](https://pkg.go.dev/context)
- [WithTimeout](https://pkg.go.dev/context#WithTimeout)
- [CancelFunc 发信号但不等待](https://pkg.go.dev/context#CancelFunc)
