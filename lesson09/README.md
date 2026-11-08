# 第九课：SQLite 持久化与数据库唯一约束

目标：把第八课的内存仓库替换为数据库，理解参数绑定、结果扫描、连接池与约束错误映射。

## 启动

```powershell
Set-Location E:\WorkProject\go\lesson09
go run .
```

首次构建会下载 modernc.org/sqlite 及相关依赖；此驱动无需 CGO。
默认监听 `127.0.0.1:8080`，数据库位于当前项目的 `data/users.db`。
这课的新数据库初始为空，用户 ID 由数据库生成，不预置前几课的用户。

在另一个 PowerShell 里创建用户：

```powershell
$response = Invoke-WebRequest -Uri 'http://127.0.0.1:8080/users' -Method Post -ContentType 'application/json' -Body '{"name":"Alice"}'
$location = $response.Headers.Location
$response.StatusCode
$response.Content
Invoke-RestMethod -Uri ('http://127.0.0.1:8080' + $location)
```

首次创建得到 201，Location 指向实际用户资源。之后停止服务，再在同一项目目录运行 `go run .`，
重新请求刚才的 Location，仍应读到用户；再次创建 Alice 则返回 409。
同一 PowerShell 会话里的 $location 可继续使用。也可以记录响应中的 id 后请求 `/users/该ID`。

可以在启动前设置两个环境变量：

```powershell
$env:HTTP_ADDR = '127.0.0.1:18080'
$env:DB_PATH = 'data/another.db'
go run .
```

DB_PATH 决定使用哪个数据库文件。相对路径以启动时的工作目录为基准；需要重复使用数据时保持路径一致。
data/ 被 gitignore 排除，源码、schema.sql、go.mod、go.sum 保留在版本管理中。

## 阅读顺序

1. schema.sql：表结构，尤其是 INTEGER PRIMARY KEY AUTOINCREMENT、NOT NULL 和 UNIQUE。
2. repository.go：OpenUserRepository、FindByID、Create。
3. main.go：创建目录、打开长期使用的仓库并装配 HTTP 服务。
4. api.go：沿用第八课的请求解析、字段校验和状态码映射。

`//go:embed schema.sql` 在编译时把建表 SQL 放入字符串，二进制运行时不需要再从当前目录查找 schema 文件。
CREATE TABLE IF NOT EXISTS 是本课的初始建表方式，后续表结构升级会学习版本化迁移。

## 参数绑定与结果扫描

```go
err := db.QueryRowContext(ctx,
    "INSERT INTO users (name) VALUES (?) RETURNING id, name", name,
).Scan(&user.ID, &user.Name)
```

问号是参数占位符，name 作为数据交给驱动。包含单引号的名字也按普通名字处理。
RETURNING 让一次插入返回新记录的 id 和 name。
QueryRowContext 返回单行结果对象，Scan 把列写入 Go 变量，并报告查询或扫描错误。
查询未找到时，Scan 返回 sql.ErrNoRows；仓库把它转成 found=false，让 HTTP 层继续返回 404。

本例以单条 INSERT 完成创建，SQLite 为这条语句提供原子性；多条业务语句的整体事务在下一课学习。

## 为什么由 UNIQUE 保证重名规则

表中的 `name TEXT NOT NULL UNIQUE` 是所有连接都必须遵守的约束。
Create 直接尝试 INSERT。若发生唯一约束冲突，就检查驱动的类型化错误码，返回 ErrNameExists，HTTP 层映射为 409。
业务层提前查询可以用来提示用户，但不能代替数据库约束，因为检查与插入之间可能发生其他写入。

仓库没有再用内存 map、名字索引或手动 ID 计数器；数据库保存记录并分配 ID。
ID 不承诺连续无空洞，业务不要依赖“上一个 ID 加一”。

## 连接池、WAL 与等待

*sql.DB 是可共享的连接池，本例每个仓库最多 4 个连接，长期复用它，而不是每个请求重新打开数据库。
每个新连接通过 DSN 设置 busy_timeout、foreign_keys 和同步选项，避免只给某个连接设置 PRAGMA。
启动时启用 WAL。SQLite 仍然只允许一个写事务同时写入；连接池大小不等于并行写入数量。
busy_timeout 允许遇到暂时的数据库锁时等待释放，超过等待范围的存储错误仍可能返回 503。

Close 关闭连接池，测试会在重新打开前调用它。正常提交的数据保存在数据库文件中，进程重启可继续使用。

## 验证

```powershell
go test ./...
```

检查覆盖 HTTP 请求契约、关闭再打开后数据保留、单引号名字的参数绑定，
以及两个独立连接池对同一个数据库同时发起 32 次同名创建，要求只有 1 次成功。
测试使用临时数据库，不改动默认 data/users.db。

练习：把两个服务实例的 DB_PATH 设为同一个绝对路径、HTTP_ADDR 设为不同端口。
通过一个实例创建，再从另一个实例读取；解释为什么这里的唯一性不依赖某个进程里的 Mutex。

Rust 对照项目：`E:\WorkProject\rust\lesson09`，默认端口 8081。

## 官方说明

- [Go 连接池](https://go.dev/doc/database/manage-connections)
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)
- [SQLite UNIQUE](https://www.sqlite.org/lang_createtable.html#unique_constraints)
- [SQLite RETURNING](https://www.sqlite.org/lang_returning.html)
