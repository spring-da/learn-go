# 第十三课：HTTP 服务的优雅关闭与请求取消

目标：退出时停止接收新工作，给已有请求留出完成时间，超过宽限期再取消处理，等待请求与 worker 结束后关闭数据库。

## 先做正常关闭实验

停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson13
$env:DB_PATH = 'E:\WorkProject\go\lesson13\data\shutdown.db'
$env:NOTIFY_DB_PATH = 'E:\WorkProject\go\lesson13\data\notifications.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:SHUTDOWN_GRACE_MS = '5000'
$env:WORKER_ENABLED = '0'
go run .
```

为清楚观察 HTTP 请求，先暂停 Outbox worker。注册仍会把事件保存在数据库中。
这两个数据库与上一课独立，首次使用为空，后续启动保留数据。

另开 PowerShell，运行演示脚本：

```powershell
Set-Location E:\WorkProject\go\lesson13
.\demo.ps1
```

脚本会生成新的用户名与请求键，异步发起一个延迟 2 秒的注册请求，
确认服务正在处理请求后，向 /demo/shutdown 发送关闭指令。
它与 Ctrl+C 进入相同的关闭流程，方便在 Windows 终端稳定复现实验。

预期输出 SHUTDOWN_STATUS=202、REGISTRATION_STATUS=201。
服务端随后输出 shutdown complete: forced=false active=0，并结束进程。
201 表示注册事务正常完成；关闭指令没有立即取消它。

## 再做超时取消实验

在启动终端把宽限期改为 500 毫秒，重新运行：

```powershell
$env:SHUTDOWN_GRACE_MS = '500'
go run .
```

另一个终端再次运行同一个 demo.ps1。它会使用新名字和新请求键。
注册请求仍需等待 2 秒，因而会超过 500 毫秒的宽限期。
预期服务端输出 grace ended，随后 forced=true active=0。
客户端可能看到 503，也可能看到连接被关闭的错误；强制关闭时不能假定总能收到完整 HTTP 响应。

本例延迟发生在业务 handler 和数据库操作之前，所以被取消的这次演示不会写入四张业务表。
重启后，正常完成的第一次注册仍存在，取消的第二次注册不存在。
如果取消发生在事务已提交之后，取消不会撤销已提交的数据；仍需使用上一课的幂等键处理结果不确定的重试。

## 生命周期分成两种信号

```text
running
  → 收到 Ctrl+C 或 POST /demo/shutdown
  → draining：拒绝新业务请求，停止 listener，通知 worker 退出
  → 等待已有 HTTP 请求完成
      ├─ 宽限期内完成 → 等待清理
      └─ 宽限期耗尽 → 取消请求 context，关闭连接，再等待 handler 返回
  → 等待 worker 结束
  → 关闭两个数据库
```

SHUTDOWN_GRACE_MS 可设为 1–10000，默认 5000。它控制 HTTP 请求的宽限期，不是整个操作系统进程的硬退出时限。
数据库清理、连接归还和后台任务结束也需要完成。
Go 的 context 取消是协作式的，不能杀死忽略取消信号的 goroutine。
本课的延迟、HTTP 调用和数据库操作都使用相应的 context。

## Go 实现里重点看什么

先读 lifecycle.go 的 ServeUntilShutdown。

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
defer cancel()
err := server.Shutdown(shutdownCtx)
```

这里使用新的 context.Background() 建立等待预算，不能直接复用已经因退出信号而取消的 context。
Shutdown 会关闭 listener 与空闲连接，等待活动请求完成；它不会因为开始关闭就立刻打断已有 handler。
Serve 会先返回 ErrServerClosed，主函数仍要继续等待 Shutdown 和业务处理结束。

请求的 BaseContext 与“开始退出”的信号也分开创建。
宽限期结束才调用 cancelRequests()，并用 server.Close() 关闭连接。
Close 本身不等于所有 handler 都已返回，因此还要等 lifecycle.drained。

接着读 Lifecycle 的 enter、leave 与 RequestShutdown：

- 是否允许新请求和活动请求计数在同一把互斥锁下更新，避免开始排空时又放进新工作。
- 每个已接收的业务请求 defer leave()，所有返回路径都会减少计数。
- 进入 draining 且计数归零时关闭 drained 通道，用关闭通道广播完成状态。
- state 与 shutdown 两个演示接口不进入业务计数，避免查看状态时给自己增加一个活动业务请求。

最后读 main.go：收到开始退出信号就取消 worker，但请求的取消要等宽限期。
handler 和 worker 都结束后才关闭数据库。可以联系 Java 中“停止接收任务 → awaitTermination → 释放连接池”的顺序理解。

## 演示接口与参数

| 接口或参数 | 作用 |
| --- | --- |
| GET /readyz | 正常时 200；进入 draining 后状态为不就绪 |
| GET /demo/state | 查看 draining 与 active_requests |
| POST /demo/shutdown | 本地课程的关闭触发入口，返回 202 |
| X-Demo-Delay-Ms | 在业务 handler 前等待 0–10000 毫秒，最多一个值 |
| POST /users、GET /users/{id}、GET /outbox、/notifications | 沿用上一课业务 |

listener 关闭后，新连接可能直接失败，而不是得到 503；不能依赖此时还能通过同一端口查询状态。
演示使用默认的本机回环地址。demo 接口用于课程实验。

## 与 Outbox 连接起来

在正常关闭实验后，保留数据库路径，将 WORKER_ENABLED 改为 1、SHUTDOWN_GRACE_MS 改回 5000 后启动。
GET /outbox 可以看到前一次注册保存的事件，worker 会继续发送。
退出不会要求把所有 pending 事件都发完；它们已经持久化，可以留给下一次启动处理。
请求中途取消时，如果接收端已处理某条通知，上一课的 event_id 去重仍然负责防止重复效果。

## 验证与练习

```powershell
Set-Location E:\WorkProject\go\lesson13
go test ./...
```

保留上一课 15 项测试，新加 5 项：排空后拒绝新工作与 readiness、宽限期内注册成功并持久化、
超时取消延迟请求、超时取消未发送完整的请求体，以及非法演示延迟不进入业务 handler。
这些测试使用本机 HTTP 服务，直接检查四张物理表验证数据库结果。

阅读顺序：lifecycle.go 的 ServeUntilShutdown → protect → Lifecycle → main.go → lifecycle_test.go。
练习：解释为什么不能把 signal.NotifyContext 得到的取消信号直接当作所有 HTTP 请求的 BaseContext；
再比较 Go 的 defer leave() 与 Rust 版的 ActiveRequest::drop。

参考：[Go Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)、
[Go context](https://pkg.go.dev/context)。
