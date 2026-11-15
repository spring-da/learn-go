# 第十四课：乐观锁与并发修改积分

目标：两个客户端基于同一份旧数据修改积分时，检测版本冲突，避免后一次写入覆盖前一次修改。

## 运行与演示

先停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson14
$env:DB_PATH = 'E:\WorkProject\go\lesson14\data\optimistic.db'
$env:NOTIFY_DB_PATH = 'E:\WorkProject\go\lesson14\data\notifications.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:WORKER_ENABLED = '0'
$env:SHUTDOWN_GRACE_MS = '5000'
go run .
```

本课新增 version 与注册快照字段，使用自己的新数据库；不要将第十三课文件直接当成已迁移的数据库。
暂时暂停 Outbox worker，便于专注积分与版本变化。注册事件仍然持久保存。

另开 PowerShell 执行：

```powershell
& 'E:\WorkProject\go\lesson14\demo.ps1'
```

脚本每次生成新用户名和请求键，依次模拟两个客户端读旧数据、修改、发生冲突、重新计算：

```text
A、B 先读取：points=100，version=0
A 想加 20：写入 120，带 expected_version=0 → 200，version=1
B 想减 20：写入 80，仍带 expected_version=0 → 409，数据不变
B 重新读取 120、version=1，重算 120-20=100 → 200，version=2
```

最终输出 FirstUpdate=200、StaleUpdate=409、RecalculatedRetry=200、FinalPoints=100、FinalVersion=2。
RegistrationReplayVersion=0 表示注册幂等重放仍返回最初快照。
脚本完成后服务继续运行，Ctrl+C 仍按上一课的流程退出。

## 为什么数据库事务与写锁还不够

即使两次 UPDATE 按顺序执行，只要都无条件写入客户端算好的目标值，最终仍可能变成 80。
A 的加 20 会被 B 根据旧积分计算出的结果覆盖。
问题在于“客户端读取 → 思考或计算 → 提交修改”跨越了多个请求，单次写入的原子性不能判断客户端是否用过期数据计算。

本课为 points_accounts 增加非负整数 version，新账户为 0。
所有业务积分修改同时检查并推进版本：

```sql
UPDATE points_accounts
SET points = ?, version = version + 1
WHERE user_id = ? AND version = ?
  AND version < 9223372036854775807
RETURNING user_id, points, version;
```

版本比较与更新是同一个数据库操作。匹配则写入并返回新状态，不匹配则一行也不修改。
不能在应用里先 SELECT 比较版本，随后执行不带版本条件的 UPDATE，中间仍会有竞争窗口。
进程内 Mutex 也不能协调另一份 Go 服务或 Rust 服务；版本条件在共享数据库中判断。

可以和 Java/JPA 的 @Version 对照：它同样用于检测读取后版本已变化的乐观锁失败。
本课手写 SQL 与错误映射，不依赖 ORM 自动完成检查。

## 新接口

```http
PUT /users/1/points
Content-Type: application/json

{"points":120,"expected_version":0}
```

响应示例：

```json
{"user_id":1,"points":120,"version":1}
```

GET /users/1 与注册响应中的用户对象都新增 version 字段。
PUT 接受目标积分；“加 20、减 20”是演示客户端的计算意图。
Idempotency-Key 继续用于 POST /users；PUT 不使用注册接口的重放记录。

| 情况 | 状态 |
| --- | --- |
| 版本匹配且字段合法 | 200，积分替换，版本加 1 |
| 当前版本与 expected_version 不同 | 409，积分和版本均不修改 |
| 用户不存在 | 404 |
| 积分或 expected_version 为负数 | 422 |
| 缺字段、null、非整数、超出 int64、未知字段 | 400 |
| 请求体超过 1024 字节 | 413 |
| Content-Type 不合要求 | 415 |

版本达到 int64 上限时拒绝继续推进并返回 409，避免 SQLite 的整数溢出转成其他数值类型。
成功写入相同积分也会推进版本；版本表示成功更新的次序，不等于积分差值。

## Go 中怎样表达结果

先看 points.go 的 ReplacePoints，再看 points_api.go。
QueryRowContext(...).Scan(...) 成功则得到当次更新后的 PointsAccount。
sql.ErrNoRows 在这里可能表示版本冲突，不能一概当作仓库故障。
条件更新没有返回行后，补查只区分不存在、版本冲突和版本耗尽；它不负责决定能否写入。
本例没有删除用户的接口，用户存在性不会被其他业务请求删除改变。

ErrVersionConflict 与 ErrUserNotFound 由 errors.Is 区分，API 分别映射 409 与 404。
其他 SQL 错误保留原因并返回仓库不可用。
points 和 expected_version 使用 *int64 解析，以区分 JSON 中缺失或 null 与合法的 0。

单条 UPDATE 自身是原子的，因而这次更新不需要把之前客户端的 GET 包进一个长事务。
PUT 返回当次 SQL 的结果；另一个请求随后再次更新时，GET 看到更大的版本也正常。

## 冲突后要重新计算

409 后需要重新读取当前值，并根据业务意图决定是否重试。
只把 expected_version 从 0 改成 1，却继续提交旧的 points=80，会绕过检测并覆盖已经完成的修改。
演示中的扣 20 要基于新状态计算，得到 points=100。
自动重试是否合适取决于业务；并不是所有人工编辑都适合直接重算并覆盖。

测试中的并发递增使用有上限的“读 → 计算 → 条件更新 → 冲突则重试”循环。
生产场景还需要根据竞争情况确定退避与重试上限。

## 与注册幂等、Outbox 的关系

乐观锁解决“是不是基于旧版本修改”，请求幂等解决“是不是在重试同一次操作”。
用户积分更新到 version=2 后，重放原注册请求仍返回注册时 points=100、version=0。
要修改当前账户，先 GET 当前用户，不要把注册快照当作最新状态。
原 UserRegistered 事件继续保留注册时的积分；本课没有增加其他事件类型。

## 验证与阅读顺序

```powershell
Set-Location E:\WorkProject\go\lesson14
go test ./...
```

保留前 20 项测试，新增 6 项验证：HTTP 冲突与重新计算、非法请求不改数据、
两个连接池中 32 个同版本写入只有一个成功、16 个并发递增经重新读取重试后全部保留、
版本边界，以及 SQL 失败不推进版本且成功结果重启可读。
原快照测试也改为通过带版本的更新接口修改积分。

阅读顺序：schema.sql → points.go → points_api.go → points_test.go → demo.ps1。
练习：将 demo.ps1 中 B 重试的目标积分故意写成 80，说明为什么这次版本检查会通过，
但最终业务结果仍然不符合“先加 20 再减 20”的意图。

参考：[SQLite UPDATE](https://www.sqlite.org/lang_update.html)、
[SQLite RETURNING](https://www.sqlite.org/lang_returning.html)、
[Jakarta Persistence @Version](https://jakarta.ee/specifications/persistence/3.2/apidocs/jakarta.persistence/jakarta/persistence/Version.html)。
