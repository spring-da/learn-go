# 第四课：并发批量查询

目标：理解 goroutine、无缓冲 channel、WaitGroup 和任务完成后的结果汇总。
沿用仓库接口与“找到、未找到、失败”三种结果；本课使用只读内存数据和 Sleep 模拟远程查询。

## 运行与阅读顺序

```powershell
Set-Location E:\WorkProject\go\lesson04
go run .
```

先看 repository.go，再对照 batch.go 的两个批量函数，最后看 main.go。
同包代码分在多个文件中，使用 `go run .` 一起构建。

本例对 1、2、404、500 分别模拟 240、80、160、120 毫秒的等待。
ID 500 是特意安排的故障输入，404 则正常查询但没有用户。
在正常调度下串行约需 600 毫秒，并发约需最长一次等待的 240 毫秒，加上各自的运行开销。
时间受系统调度等影响，不把固定时长或固定加速倍数当作正确性标准。

主要输出示例：

```text
sequential: count=4 elapsed=...ms
  id=1 found=Spring
  id=2 found=Lin
  id=404 not-found
  id=500 error=simulated backend unavailable
concurrent: count=4 elapsed=...ms
  id=2 found=Lin
  id=500 error=simulated backend unavailable
  id=404 not-found
  id=1 found=Spring
```

并发结果按接收顺序收集，具体顺序不作保证；上面只是常见的一次输出顺序。
打印发生在各次批量函数返回之后，输出行的出现时间不表示任务完成的精确时间。

## 先联系 Java 经验

| 目的 | Java 中熟悉的概念 | 本例 Go 写法 |
|---|---|---|
| 并发执行任务 | 启动线程或提交任务 | `go func(...) { ... }(id)` |
| 在线程间传递结果 | BlockingQueue 等队列 | channel |
| 等待一批任务完成 | CountDownLatch 等协调工具 | WaitGroup |

这些只是用途上的类比。goroutine 是由 Go runtime 调度的执行单元，并非每个 goroutine 固定占用一个 OS 线程。
Sleep 模拟等待；本例观察等待的重叠，不是 CPU 密集计算性能测试。

## 并发函数分为三步

1. 每个 ID 启动一个 goroutine，完成查询后发送一个 LookupResult。
2. 一个协调 goroutine 等待所有发送任务完成，再 close(results)。
3. 当前 goroutine 持续接收结果，统一追加到切片；channel 关闭且没有剩余值时，range 结束。

`wg.Add(1)` 在启动任务之前登记，`defer wg.Done()` 在任务结束时执行，`wg.Wait()` 等计数归零。
defer 用于在函数退出时安排清理动作，这里保证正常返回时完成计数扣减。
channel 传值与 WaitGroup 计数承担不同职责：前者传结果，后者用来确定何时可以关闭结果流。

为什么协调者也放进 goroutine？因为当前结果 channel 没有缓冲：

```go
results := make(chan LookupResult)
```

发送需要与接收配合。如果当前 goroutine 先 wg.Wait()，尚未开始接收，任务就可能停在发送处，
无法运行到 defer wg.Done()，等待也无法结束。

为什么不能每个任务都 close(results)？一个任务完成时，其他任务仍可能发送；向已关闭 channel 发送会 panic。
本例统一由确认所有发送者结束的协调者关闭它。

## 数据为什么能共享

仓库 map 在启动任务前构建完毕，所有任务只读它；纯并发读取是允许的。
查询方法返回 User 值，每个结果携带自己的值。
结果切片由当前 goroutine 统一追加，因此没有多个任务同时修改切片。

接口本身不保证实现满足并发调用要求。这里的安全性取决于具体实现及只读的使用方式。
如果以后加入共享缓存写入，就要重新设计同步方式。

## 动手练习

1. 把 ids 改为空切片，再运行。两个批量函数都应返回 0 条结果，而不是一直等待。
2. 在输入中重复一次 ID 1。并发结果也应保留两次查询结果，不能按 ID 静默去重。
3. 将 make(chan LookupResult) 改成 make(chan LookupResult, len(ids))，理解有缓冲后发送何时会等待。
4. 如果要求结果按输入顺序返回，先描述如何在任务中携带输入下标，再由汇总方放回对应位置。

当前每个 ID 启动一个任务，输入规模固定且很小。批量规模扩大时，下一步可用 worker pool 限制并发数。

Rust 对照项目：`E:\WorkProject\rust\lesson04`。

## 官方说明

- [goroutine 与 channel](https://go.dev/doc/effective_go#concurrency)
- [WaitGroup](https://pkg.go.dev/sync#WaitGroup)
