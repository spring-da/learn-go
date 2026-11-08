# 第五课：固定 worker、有限队列与结果顺序

目标：从“每个任务启动一个 goroutine”改成“固定数量的 goroutine 反复取任务”，同时保留输入顺序。

## 运行

```powershell
Set-Location E:\WorkProject\go\lesson05
go run .
```

先看 pool.go，再看 main.go；repository.go 沿用只读内存仓库与模拟等待。
本例每次调用创建一组 worker，处理这一批任务后结束；worker 在同一批次中被反复使用。

输入为 `[1, 2, 1, 404, 500]`。1 和 2 能找到用户，404 未找到，500 是故意模拟的查询失败。
程序分别使用 1 和 2 个 worker，两次都应返回如下 5 条结果，顺序相同：

```text
  index=0 id=1 found=Spring
  index=1 id=2 found=Lin
  index=2 id=1 found=Spring
  index=3 id=404 not-found
  index=4 id=500 error=simulated backend unavailable
```

随后输出 `empty: count=0`，以及 workers=0 的参数错误。
1 个 worker 的模拟等待总和为 840ms；2 个 worker 的调度通常可将等待重叠到约 440ms。
实际耗时随调度变化；结果、顺序和并发上限比固定耗时更重要。

## 三种角色

1. 生产者：按输入次序把 Job 放入 jobs channel，发完后 close(jobs)。
2. worker：循环从 jobs 取一个任务，查询，再把 LookupResult 发给汇总方；取完所有任务后退出。
3. 汇总方：持续接收结果；协调 goroutine 等所有 worker 退出后 close(results)，接收循环随之结束。

生产者和协调者各有一个 goroutine，但真正执行 FindByID 的只有固定数量的 worker。
因此 workers=2 表示最多有 2 个查询调用同时执行，而不是整个程序只有 2 个 goroutine。
输入少于 workers 时，会将实际 worker 数减少到输入数。

## 队列容量与背压

```go
jobs := make(chan Job, workers)
```

容量决定最多能缓存多少个尚未被取走的任务。
当队列已满，生产者发送会等待；worker 取走任务后腾出空间，生产者才能继续。
这种“下游处理不过来时让上游等待”的机制叫背压。

队列容量与 worker 数是独立参数。本例为了减少配置项，把两者设为相同值。
把容量改为 1 或 0，正在执行查询的上限仍由 worker 数控制。
本例输入切片和最终结果仍保留整批数据；有限任务队列不意味着总内存与输入规模无关。

生产者在独立 goroutine 中发送任务，调用方能同时接收结果。
若在接收结果前由当前 goroutine同步投递所有任务，可能遇到队列已满、worker 又在等待发送结果的相互等待。

## 为什么携带 Index

ID 是业务标识，Index 是这一次输入中的位置。
相同 ID 可以对应两次独立查询，所以不能用 map[ID]Result 来保留每次任务的结果。

本例先按接收顺序收集，再按 Index 排序；输出顺序稳定，执行顺序和完成顺序仍由调度决定。
排序复杂度为 O(n log n)。也可以预先分配 n 个结果槽位，再由单一汇总方按 Index 放入，改为 O(n) 汇总。

## 两层错误

FindWithPool 返回值中的 error 表示整个调用的配置问题，例如 workers<=0。
LookupResult.Err 表示某个用户查询失败。这样的任务仍产生一条结果，其余任务继续处理。

worker 数至少为 1 的前提下，空输入返回空结果；workers<=0 始终视为参数错误。
仓库在执行期间只读；调用期间调用方也应保持 ids 不变。

## 动手练习

1. 将 workers 改为 3、10，观察结果顺序是否保持，思考实际会启动多少个查询 worker。
2. 把 jobs 的容量改为 1，再解释为什么最大查询并发数没有改变。
3. 将末尾的排序改成按 Index 填充预分配切片，保持重复 ID 和错误结果完整。

Rust 对照项目：`E:\WorkProject\rust\lesson05`。

## 官方说明

- [channel 容量与阻塞](https://go.dev/ref/spec#Channel_types)
- [WaitGroup](https://pkg.go.dev/sync#WaitGroup)
