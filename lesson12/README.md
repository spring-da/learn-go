# 第十二课：Outbox 与后台通知任务

目标：注册成功时可靠记录通知任务，后台投递失败后重试，重启后继续处理，并让接收端安全处理重复事件。

## 先运行案例

停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson12
$env:DB_PATH = 'E:\WorkProject\go\lesson12\data\outbox.db'
$env:NOTIFY_DB_PATH = 'E:\WorkProject\go\lesson12\data\notifications.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:WORKER_ENABLED = '1'
$env:DEMO_FAIL_FIRST = '1'
$env:DEMO_LOSE_ACK_ONCE = '0'
go run .
```

首次使用这两个数据库时为空，重启会保留数据。本课不会迁移第十一课的旧数据库。
默认 worker 每秒轮询一次，每轮最多投递一个到期事件，HTTP 超时为 2 秒。
如果曾设置 NOTIFY_URL，请清除该变量，以使用当前进程内置的接收端。

另开一个 PowerShell，注册用户：

```powershell
'{"name":"Alice","initial_points":100}' | Set-Content -Encoding ascii request.json
curl.exe -i http://127.0.0.1:8080/users -H 'Content-Type: application/json' -H 'Idempotency-Key: reg-001' --data-binary '@request.json'
```

注册返回 201，说明用户、账户、请求幂等记录、待投递事件已提交；通知由后台任务随后投递。
DEMO_FAIL_FIRST=1 会让接收端在本次进程的第一条合法通知处理前返回 503。
观察服务日志中的 delivery failed、retry scheduled，随后出现 delivery confirmed。
再次发送相同注册请求会重放用户结果，不增加事件。

查看发送端和接收端：

```powershell
curl.exe -s http://127.0.0.1:8080/outbox
curl.exe -s http://127.0.0.1:8080/notifications
```

在全新的数据库中，最后应看到一个 status=sent、attempts=2 的事件，以及一条通知。
两次查询返回数组，各最多显示最早的 100 条记录。POST /users 的规则与上一课一致。

## 两个数据库与一个 HTTP 边界

```text
注册 API
  └─ 同一事务：users + points_accounts + idempotency_records + outbox_events
                                    ↓ 提交
后台 worker：读到期事件 → HTTP POST /notifications → 更新事件状态
                                    ↓
接收端独立数据库：notifications（event_id 唯一）
```

outbox.db 保存注册数据与待办事件；notifications.db 保存接收端已经处理的通知。
为方便运行，两端由同一个程序提供路由，但后台任务通过真实 HTTP 调用接收端。
NOTIFY_URL 可以改为另一份案例进程的 /notifications 地址。
此时该远端接收端自己的 NOTIFY_DB_PATH 决定通知保存在哪里。

本例把 notifications 表中的一条记录作为“通知已处理”的效果，不发送真实邮件。
这使 event_id 去重与处理效果能在接收端同一个事务内提交。
如果将来接入真实邮件提供方，还要处理邮件调用与本地记录之间的故障边界。

## 为什么事件必须放进注册事务

按 repository.go 的 Create 阅读：

```text
BEGIN IMMEDIATE
  查询请求幂等键；已有成功结果则重放并结束
  INSERT users
  INSERT points_accounts
  INSERT idempotency_records
  INSERT outbox_events
COMMIT
```

第四次写入失败也会撤销前三次写入。反过来，提交成功就一定留下一个待处理事件。
事件保存注册时的 user_id、name、points 快照，而不是每次投递时重新查询当前账户。
event_id 由 SQLite 随机生成并持久保存，重试沿用它；每个注册用户对应一个事件。

可以联系 Java 的 @Transactional：事务内保存业务记录和 Outbox 记录；
后台任务做另一段独立工作。事务提交之后才可能读到事件。

## worker 怎样重试

主要阅读 outbox.go 的 DispatchOnce 与 RunWorker：

1. 查询 status=pending 且 next_attempt_at 已到期的事件。
2. 结束查询、释放数据库连接后发送 HTTP，不持有注册事务。
3. 收到 2xx：标记 sent，清除错误。
4. 超时或非 2xx：保持 pending，记录错误，把下次投递时间设为本次轮询时间加 2 秒。

next_attempt_at 是 Unix 毫秒，写进数据库，因此重启后仍按保存的时间继续。
attempts 表示已完成并成功记录状态的投递尝试数；如果进程在发送后、更新状态前退出，
该次网络调用可能没有计入 attempts。它不是精确的 HTTP 调用总数。

RunWorker 用 ticker 等待下一轮，用 select 监听 context.Done()。
HTTP 请求携带 worker 的 context，因此退出时也能取消正在等待的调用。
main.go 在退出时先取消并等待 worker，再关闭 HTTP 服务和数据库，避免后台任务继续使用已经关闭的连接池。

## 为什么重发仍然安全

最容易遗漏的路径是：

```text
接收端成功提交通知
  → 发送端没拿到成功确认，或没能标记 sent
  → 事件仍为 pending
  → 下次发送相同 event_id
```

notifications.go 的 Accept 在事务中执行 INSERT ... ON CONFLICT(event_id) DO NOTHING。
首次插入表示处理成功；同 ID、同内容表示已处理，仍返回 200。
同 ID、不同内容返回 409，避免把另一个事件当成重复消息。
去重记录是持久化的，接收端重启后也不会重复插入。

进阶演示：停止服务，把 DEMO_FAIL_FIRST 设为 0、DEMO_LOSE_ACK_ONCE 设为 1，再启动并注册一个新名字、新请求键。
接收端会先保存通知再返回一次 503，模拟“效果已完成，但发送端没有成功确认”。
最终通知仍只有一条，事件由重发后得到的 200 标记完成。
这两个故障开关均只在每次进程启动后触发一次。

## 重启恢复演示

在启动终端设 WORKER_ENABLED=0，启动后注册一个新名字、新请求键。
查询 /outbox：新事件为 pending；/notifications 没有对应事件。
按 Ctrl+C，保留 DB_PATH 与 NOTIFY_DB_PATH，把 WORKER_ENABLED 改为 1 后重新启动。
新事件会继续投递，event_id 不变。恢复的是数据库里的任务，而不是上次进程内存中的 goroutine。

## 测试与阅读顺序

```powershell
Set-Location E:\WorkProject\go\lesson12
go test ./...
```

保留上一课的 9 项测试，扩展为检查四张发送端表；另有 6 项 Outbox 测试：
事件写入失败的整体回滚、到期重试、双方数据库重开后的重复确认、标记 sent 失败后的安全重发、
16 个重复事件并发投递，以及等待 HTTP 时取消 worker。

推荐阅读顺序：schema.sql → repository.go 的 Create → outbox.go → notifications.go → main.go。
练习：在 TestMarkSentFailureAllowsSafeRedelivery 中断点观察，
解释为什么“发送成功”仍不能直接等同于“发送端已可靠记录完成”。

## 本例的范围

每个发送端数据库运行一个 worker；两种语言默认各用自己的数据库。
本课未实现多 worker 的任务领取与租约。接收端去重可以处理重复投递，但这不等于实现了多 worker 调度。
失败事件持续重试，没有死信队列；重试事件也可能被后面的已到期事件越过，不承诺全局顺序。
生产系统还需要根据业务设计重试退避、永久错误处理、事件保留策略与身份认证。

参考：[事务发件箱模式](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html)、
[Go HTTP Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)。
