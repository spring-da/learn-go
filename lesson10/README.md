# 第十课：创建用户与积分账户的事务

目标：把两条写入纳入同一事务，正确处理提交、提前返回、回滚，以及失败后重试。

## 启动和演示

先停止上一课的服务，再在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson10
$env:DB_PATH = 'data/registration.db'
go run .
```

默认地址为 `127.0.0.1:8080`。本课使用独立数据库，不把第九课的旧用户表当作迁移后的数据。
首次使用时数据库为空；后续重启继续使用同一文件。

在另一个 PowerShell 中发送一个负积分请求：

```powershell
'{"name":"RollbackDemo","initial_points":-1}' | Set-Content -Encoding ascii request.json
curl.exe -i http://127.0.0.1:8080/users -H 'Content-Type: application/json' --data-binary '@request.json'
```

预期 422，错误体为 `{"error":"initial points must be non-negative"}`。
保持名字不变，改用合法积分：

```powershell
'{"name":"RollbackDemo","initial_points":100}' | Set-Content -Encoding ascii request.json
curl.exe -i http://127.0.0.1:8080/users -H 'Content-Type: application/json' --data-binary '@request.json'
```

预期 201，响应包含 id、name、points，且 points 为 100。按照实际 Location 查询，可读到相同结果。
首次失败没有占用名字，否则第二次请求会变成 409。
上述请求文件内容为 ASCII 示例；其他语言的名字使用 UTF-8 JSON。

initial_points 省略或为 null 时使用 0；类型不是整数或超出 int64 时返回 400。
名字规则与上一课相同；合法名字已经存在时返回 409。

## 两张表各自负责什么

schema.sql 定义 users 和 points_accounts。
账户的 user_id 同时是主键和外键，保证每个用户最多一个账户，且账户指向存在的用户。
CHECK(points >= 0) 要求积分非负。
创建业务的事务保证用户与账户一起提交；GET 使用 JOIN 返回聚合后的用户与积分。

本课刻意让非负积分规则由数据库 CHECK 执行，使第二条 INSERT 能触发真实错误。
应用也可以提前校验以便尽早提示用户，数据库约束继续保护最终写入。
CHECK 默认拒绝当前语句，整个注册操作的回滚由下面的事务边界完成。

## Go 的事务边界

按 repository.go 的 Create 阅读：

```text
BeginTx
  INSERT users
  INSERT points_accounts
Commit
```

BeginTx 从池中占用一个连接，两条写入均通过 tx.QueryRowContext 执行。
如果换成 r.db 的方法，它会通过连接池执行，不能据此认为仍属于这个事务。

```go
defer tx.Rollback()
```

这一行在事务创建后就安排清理，错误分支提前 return 时会执行。
Commit 成功后事务已经结束，延迟 Rollback 会得到 sql.ErrTxDone，不会撤销已经提交的数据。
只有 Commit 成功，Create 才返回用户；提交报错时不把前面暂时取得的结果当作成功响应。

可以联系 Spring 的事务业务边界理解，但这里通过 BeginTx、Commit、Rollback 显式表达。
事务并不等于保存点；本课关注整笔注册一起提交或一起撤销。

## 数据库错误与 API

UNIQUE 冲突转换为 ErrNameExists，再映射 HTTP 409。
积分 CHECK 冲突转换为 ErrInvalidPoints，再映射 HTTP 422。
其他数据库错误保留原因，按仓库故障处理。
本例只有账户积分这一项 CHECK；新增其他 CHECK 时要进一步区分业务错误来源。

## 验证

```powershell
go test ./...
```

检查包括：

- 两条记录提交后同时存在，关闭再打开仍可读取积分。
- 第二条 SQL 失败后，直接检查 users 和 points_accounts 都没有新增记录。
- 失败后用同名合法请求重试可以成功。
- 两个连接池并发创建同名用户，只提交一组用户和账户。
- HTTP 默认积分、错误类型和负积分回滚行为。

测试直接统计物理表，避免仅用 JOIN 查询时看不见残留用户，从而误判回滚成功。
测试只使用临时数据库，不改动默认练习数据。

练习：增加第三张 registration_events 表，在同一事务写入注册事件。
为事件添加一种可触发的约束，并验证第三步失败时，用户与账户也一起回滚。

Rust 对照项目：`E:\WorkProject\rust\lesson10`，默认端口 8081。

## 官方说明

- [Go 事务](https://go.dev/doc/database/execute-transactions)
- [BeginTx 与 context](https://pkg.go.dev/database/sql#DB.BeginTx)
- [SQLite 外键](https://www.sqlite.org/foreignkeys.html)
