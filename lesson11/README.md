# 第十一课：用事务实现请求幂等

目标：客户端因超时而重试注册请求时，返回第一次成功的结果，同时避免重复创建用户和积分账户。

## 先理解问题

第十课的事务解决了用户和账户一起提交的问题。但提交成功后，响应可能在网络中丢失。
客户端此时只知道超时，无法判断服务端是否已经完成注册。
如果重新提交相同用户名，只得到 UNIQUE 冲突，仍然不知道第一次请求的结果。

本课由客户端为一次业务操作生成 Idempotency-Key，重试时沿用该键。
新操作使用新键。客户端必须保存键与本次请求的对应关系。

## 启动

先停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson11
$env:DB_PATH = 'E:\WorkProject\go\lesson11\data\idempotency.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
go run .
```

本课使用独立数据库，首次启动为空；重启继续使用同一文件。请求与响应继续沿用第十课的格式。

## 动手请求

在另一个 PowerShell 中运行：

```powershell
'{"name":"Alice","initial_points":100}' | Set-Content -Encoding ascii request.json
curl.exe -i http://127.0.0.1:8080/users -H 'Content-Type: application/json' -H 'Idempotency-Key: reg-001' --data-binary '@request.json'
```

首次响应为 201，包含 Location、Idempotency-Replayed: false，以及用户的 id、name、points。
原样再执行一次 curl，仍为 201，返回同一用户和 Location，Idempotency-Replayed 变为 true。

保留 reg-001，把 initial_points 改成 200 再发：得到 409，错误为
`idempotency key was used with different parameters`。
改用 reg-002，保持用户名 Alice：得到 409，错误为 `name already exists`。

| 请求 | 结果 |
| --- | --- |
| 新键、新名字、合法积分 | 201，创建并保存结果 |
| 同键、相同规范化参数 | 201，重放第一次的结果 |
| 同键、不同规范化参数 | 409，拒绝键被重新用于另一操作 |
| 不同键、已存在的名字 | 409，用户名唯一约束仍然生效 |
| 缺少键或键格式不合法 | 400 |
| 新键、负积分 | 422，回滚全部写入，不占用键 |

Idempotency-Key 必须恰好一个值，由 1–64 个 ASCII 字母、数字、下划线或短横线组成。
该规则是本课 API 自己规定的接口契约。GET 不要求此请求头。

本例按解析后的参数比较：name 去掉两端空白；initial_points 省略或为 null 时为 0。
因此字段顺序、JSON 空白不影响判定，`{"name":" Alice "}` 与
`{"initial_points":0,"name":"Alice"}` 表示同一次请求参数。
本课只绑定成功提交的请求；失败后修正积分，可以继续使用尚未绑定的键。

## 三张表必须一起提交

users 与 points_accounts 仍保存当前业务数据。
新增 idempotency_records 保存键、规范化请求参数，以及首次成功响应的字段快照。

```text
开启 IMMEDIATE 事务
  查询 Idempotency-Key
  ├─ 已存在：比较请求参数
  │           ├─ 一致 → 取出原响应快照
  │           └─ 不同 → 回滚，返回键冲突
  └─ 不存在：INSERT users
              INSERT points_accounts
              INSERT idempotency_records
提交后返回成功
```

如果先提交用户，再单独保存幂等记录，中间失败就会出现“业务已经完成，但查不到重试结果”。
把三次写入放在同一事务，才能保持它们一起成功或一起撤销。
不要把发邮件、远程调用等耗时操作放进这个持有 SQLite 写锁的事务。

重放读取原响应快照，不从当前积分账户重新拼装结果。
例如首次注册 points=100，之后账户变成 150：GET 返回 150，原注册请求的重放仍返回 100。

## Go 中的关键实现

按 api.go → repository.go → schema.sql 阅读。

api.go 负责校验请求头、解析和规范化参数，再调用仓库：

```go
result, err := repo.Create(ctx, key, name, initialPoints)
```

RegistrationResult 把 User 和 Replayed 放在一个返回值中，API 根据 Replayed 写响应头。
ErrKeyMismatch 表达同键不同参数，与 ErrNameExists 区分，虽然二者都映射为 HTTP 409。
可以联系 Java 的结果 DTO 和领域错误理解。

repository.go 使用 BeginTx、defer tx.Rollback() 和显式 Commit；所有 SQL 都通过 tx 执行。
DSN 中 `_txlock=immediate` 让事务在查询幂等记录前先取得写入资格。
SQLite 同时只允许一个写事务，这使本例的“查键后写入”在多个连接、多个服务进程之间依次执行。
若先开启普通延迟事务、查询后才尝试写入，可能在升级写事务时遇到 SQLITE_BUSY。
busy_timeout 给锁竞争设置等待时间，并不保证任何负载下都成功。

这是本课适合 SQLite 的实现选择；换成其他数据库，需要根据其锁和隔离语义重新设计。

## 验证

```powershell
Set-Location E:\WorkProject\go\lesson11
go test ./...
```

测试覆盖请求头规则、参数规范化、HTTP 重放和冲突、失败后修正重试，以及：

- 重开数据库后仍能重放，账户变化不会改变原响应快照。
- 用触发器让第三次写入失败，直接检查三张表都为空。
- 两个连接池并发提交 32 个同键请求，仅一次创建，其余 31 次返回相同结果。
- 不同键争用同一用户名，仍然只创建一份用户和账户。

对照 Rust 版时，可把两个服务的 DB_PATH 都指向上述同一个绝对路径，再用相同键和参数分别请求。
Go 和 Rust 使用相同表结构与参数规则，可以相互读取幂等记录。

## 本课的边界与练习

幂等键只作用于本例的 POST /users，在该接口内全局唯一，不自动过期；未涉及登录用户与租户。
真实系统通常需要把调用方、接口、键共同组成作用域，并明确保留时间与删除策略。
本课只覆盖同一数据库事务中的写入，不能据此保证外部邮件、支付或消息也只执行一次。

练习：沿着“事务提交成功 → 响应丢失 → 客户端重试”的路径走读 Create，
说明为什么最终返回同一个 id；再观察第三次 INSERT 失败时，defer Rollback 如何撤销前两次写入。

参考：[SQLite 事务](https://www.sqlite.org/lang_transaction.html)、
[modernc SQLite 驱动的 DSN 参数](https://pkg.go.dev/modernc.org/sqlite)。
