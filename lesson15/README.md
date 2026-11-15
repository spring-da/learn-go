# 第十五课：原子积分转账与幂等回执

目标：一次转账中扣减与入账一起提交，失败时全部回滚；重试同一次成功转账时返回原回执，不重复扣款。

## 启动与演示

停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson15
$env:DB_PATH = 'E:\WorkProject\go\lesson15\data\transfers.db'
$env:NOTIFY_DB_PATH = 'E:\WorkProject\go\lesson15\data\notifications.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:WORKER_ENABLED = '0'
$env:SHUTDOWN_GRACE_MS = '5000'
go run .
```

使用本课独立数据库，首次为空，重启保留数据；不要复用上一课文件作为已迁移的数据库。
先暂停注册通知 worker，便于观察积分变化；注册 Outbox 仍会保存。

另开 PowerShell：

```powershell
& 'E:\WorkProject\go\lesson15\demo.ps1'
```

每次运行创建一对新账户，依次验证：

| 操作 | A / B 积分 | 两边版本 |
| --- | --- | --- |
| 创建账户 | 100 / 20 | 0 / 0 |
| A 向 B 转 30，首次提交 | 70 / 50 | 1 / 1 |
| 同键、同参数重试 | 70 / 50 | 1 / 1 |
| 同键改成转 31 | 409，数据不变 | 1 / 1 |
| 新键请求转 1000 | 409，余额不足 | 1 / 1 |
| 用尚未绑定的失败键，修正为转 10 | 60 / 60 | 2 / 2 |

最终 TotalPoints=120，原回执仍保留第一次转账后的 70 / 50。
演示后服务继续运行，Ctrl+C 使用前一课的优雅关闭流程。

## 新接口

```http
POST /transfers
Content-Type: application/json
Idempotency-Key: transfer-001

{"from_user_id":1,"to_user_id":2,"amount":30}
```

首次成功返回 201、Location: /transfers/{id}、Idempotency-Replayed: false。
同键同参数重试仍返回 201、相同回执与 Location，重放标记为 true。
GET /transfers/{id} 查询持久化回执；其余额与版本是转账当时的快照。
当前状态由 GET /users/{id} 查询。

| 情况 | 状态 |
| --- | --- |
| 首次成功或成功结果重放 | 201 |
| 成功键用于另一组合法参数 | 409 |
| 余额不足、入账会溢出 int64、版本无法推进 | 409 |
| 来源或目标账户不存在 | 404 |
| 自己转自己、非正数 ID 或 amount | 422 |
| 缺少或非法幂等键、JSON 字段缺失/null/类型错误/未知字段 | 400 |
| 请求体超过 1024 字节 / Content-Type 不合要求 | 413 / 415 |

ID 与 amount 均为 int64 整数。键格式沿用 1–64 个 ASCII 字母、数字、下划线或短横线。
POST /transfers 与 POST /users 的键分属各自的表，可以使用相同文本而互不冲突。
本课只绑定成功提交的转账；失败请求不占用键，修正后可以继续使用。

## 为什么一个事务还要保存回执

```text
BEGIN IMMEDIATE
  按转账幂等键查回执
    ├─ 已存在：比较参数，一致则重放；不一致则冲突
    └─ 不存在：读取两个当前账户
                校验余额、入账上限、版本上限
                扣减来源账户，版本 +1
                增加目标账户，版本 +1
                保存转账回执与幂等键
COMMIT 后返回成功
```

如果扣减先提交，入账失败就会丢失积分。
如果两边余额提交后才单独保存幂等记录，中间失败会让重试再扣一次。
因此两个余额与包含幂等键的回执必须一起提交。

transfers 的一行同时包含请求参数与首次成功响应，不再重复建一份相同用途的转账幂等表。
重放直接读取该行；后来余额变化，不会改变第一次回执。
失败时没有任何部分结果被当作成功返回。

## 与上一课的乐观锁有什么关系

上一课 PUT 提交的是“我根据版本 0 算出目标积分 80”，需要检查客户端持有的旧版本。
本课 POST 提交的是“从 A 向 B 转 30”，服务端在事务中读取当前状态并执行这项意图。
所以客户端不必提供 expected_version，但两个账户仍要推进 version。
这会让其他客户端手里的旧 PUT 版本失效，防止覆盖转账后的账户状态。

本例通过 SQLite BEGIN IMMEDIATE 在读余额之前取得写入资格，其他写事务排队。
扣减语句仍包含 points >= amount 和已读取的版本条件；入账语句包含安全的上限与版本条件。
换数据库时，要重新考虑隔离、行锁、死锁与重试，不能假定 SQLite 的单写事务规则普遍适用。

## Go 的错误与回滚

阅读 transfers.go 的 Transfer：BeginTx 后立刻 defer tx.Rollback()。
所有读取、扣减、入账和回执写入都通过 tx 执行，不使用池中的另一个连接。
任何业务错误或 SQL 错误提前返回，都会执行回滚；只有 Commit 成功才返回回执。

ErrInsufficientPoints 表达余额不足，ErrTransferKeyMismatch 表达成功键参数不一致。
API 使用 errors.Is 做状态映射，数据库故障保留错误原因。
TransferResult 把回执与 Replayed 分开，API 根据 Replayed 写响应头。

入账上限用 amount > math.MaxInt64-toPoints 检查，不能先计算 toPoints+amount 再判断，
因为加法本身就可能溢出。版本递增前也检查 int64 上限。
Rust 对照版用 checked_add 表达同样的边界。

## 验证什么才足够

```powershell
Set-Location E:\WorkProject\go\lesson15
go test ./...
```

保留前 26 项测试，新增 8 项，覆盖：

- HTTP 回执、键作用域、版本推进与非法请求。
- 重开数据库后仍重放最初回执。
- 用触发器让入账或回执写入失败，检查两边余额、版本和回执表全部回滚。
- 两个连接池并发提交 32 个同键请求：1 次执行、31 次重放。
- 32 个不同键各转 10，来源只有 100：10 次成功、22 次余额不足，没有透支。
- 双向并发转账，积分总和保持不变。
- 失败键可重用、账户缺失、版本边界、入账溢出与最大 int64 金额。

这里的总量守恒针对转账操作；已有的 PUT 接口仍能显式替换积分。
注册 Outbox 继续处理 UserRegistered，本课的转账只在同一数据库内完成，不调用外部服务。

阅读顺序：schema.sql 的 transfers → transfers.go → transfers_api.go → transfers_test.go → demo.ps1。
练习：在“回执 INSERT 失败”的测试中观察，解释为什么两个 UPDATE 的 RETURNING 已经给出新值，
仍不能认为这笔转账已经提交成功。

参考：[SQLite 事务](https://www.sqlite.org/lang_transaction.html)、
[SQLite UPDATE](https://www.sqlite.org/lang_update.html)、
[SQLite RETURNING](https://www.sqlite.org/lang_returning.html)。
