# 第十六课：泛型、TTL 与有界回执缓存

目标：用一份带类型约束的缓存支持不同键和值，正确处理过期、容量、并发和返回值所有权，再接入回执查询。

## 启动与演示

停止占用 8080 的上一课服务，在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson16
$env:DB_PATH = 'E:\WorkProject\go\lesson16\data\cache.db'
$env:NOTIFY_DB_PATH = 'E:\WorkProject\go\lesson16\data\notifications.db'
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:WORKER_ENABLED = '0'
$env:CACHE_CAPACITY = '2'
$env:CACHE_TTL_MS = '2000'
go run .
```

本课使用独立数据库，重启保留业务数据；缓存只保存在当前进程内，重启为空。
为直观看到淘汰，演示设置最多 2 条、TTL 2 秒。服务默认容量为 64，默认 TTL 为 2 秒。
CACHE_CAPACITY 支持 1–1024，CACHE_TTL_MS 支持 1–60000。

另开 PowerShell：

```powershell
& 'E:\WorkProject\go\lesson16\demo.ps1'
```

脚本每次创建新账户和转账记录，并自动验证：

| 步骤 | X-Receipt-Cache |
| --- | --- |
| 第一次 GET 回执 | MISS，查询数据库后写缓存 |
| 再次 GET 同一回执 | HIT |
| 再做一次转账，然后读旧回执 | HIT，仍为原快照 |
| DELETE 缓存项后再 GET | MISS |
| 等待 TTL 后再 GET | MISS |
| 容量 2，依次装入第三份回执，再查最早的回执 | MISS，最早项已淘汰 |

脚本要求容量为 2，TTL 在 500–5000 毫秒，以便稳定观察。
旧回执的来源积分仍为 70；三次转账后，当前账户来源积分为 59。

## 为什么缓存回执

回执是成功转账时保存的不可变快照，后续转账不会更新这行记录。
因此缓存旧回执的 70 是正确结果；当前余额必须由 GET /users/{id} 读取。
POST /transfers 的余额校验、扣减和入账继续直接使用数据库事务。
本课不把余额缓存作为转账判断依据。

GET /transfers/{id} 使用缓存，POST 的首次响应或幂等重放不预先写入查询缓存。
不存在的回执和数据库错误不会写入缓存；原本 404 的 ID 后来创建成功，下一次查询能找到它。

## Go 泛型的约束在限制什么

核心类型在 cache.go：

```go
type TTLCache[K comparable, V any] struct {
    // map[K]cacheEntry[V]、FIFO 顺序、互斥锁等。
}
```

K comparable 让 K 可以作为 map 的键，例子包括 int64、string 和字段可比较的结构体。
V any 允许实例化时选择具体值类型，不是把缓存里的值都存成 any。

```go
receipts, err := NewTTLCache[int64, TransferReceipt](2, 2*time.Second)
labels, err := NewTTLCache[string, string](2, 2*time.Second)
```

这是两个独立的类型实例；取值不需要类型断言或反射。
实际服务使用 int64 回执 ID 和 TransferReceipt。
测试还使用 string 键与 User 值，验证同一份代码可以复用。

GetAt 返回 (V, bool)。不存在时用 var zero V 得到零值，同时 bool=false；
调用者不能只根据值是否为 0、空字符串或空结构体判断是否命中。

Go 返回的是 V 的值拷贝，不自动深拷贝切片、map 或指针指向的数据。
本课回执由 int64 字段构成，返回值修改不会改变缓存中的那一份。
泛型工具用于其他可变值时，需要调用方明确共享或复制的规则。

## TTL 与时间

PutAt 保存 expiresAt=now+ttl；GetAt 仅在 now.Before(expiresAt) 时命中。
恰好到期也算过期。读取不会续期，重新写入同键才会重置 TTL。
方法显式接收时间，便于测试直接推进时间而不等待真实定时器。
HTTP 入口传 time.Now()，Time.Add 保留单调时钟信息，比较不依赖系统墙上时间的校准。

过期采用惰性清理：读取目标项、写入或查看统计时清理，不另外启动清理 goroutine。
空闲时过期项可能仍占位置，但条目和 FIFO 元数据始终有容量上限。
容量按条数计，不是按值占用的字节数计。

## 容量规则与锁

本例使用 FIFO：最早写入的项先淘汰，命中读取不改变顺序。
写入前先清掉过期项，避免把仍然有效的项提前挤掉。
重复写同一个键会移除旧的顺序记录再追加，防止队列随着更新无限增长。
实现使用有界 map 加切片，清理和调整顺序为 O(capacity)，适合本课的小容量配置。

sync.Mutex 只保护这些内存操作：

```text
短锁：检查缓存 → 释放锁
数据库查询：不持有缓存锁
短锁：成功结果写缓存 → 释放锁
```

不要把整个数据库查询包在缓存互斥锁里，否则一个慢查询会阻塞其他键的访问和统计。
并发未命中允许多次回源，本课没有实现相同键请求合并。

## 接口与统计

| 接口 | 作用 |
| --- | --- |
| GET /transfers/{id} | 成功时附 X-Receipt-Cache: HIT 或 MISS |
| GET /cache/stats | 条目数、容量、TTL、命中、未命中、淘汰、过期、数据库加载尝试次数 |
| DELETE /cache/receipts/{id} | 返回 204，移除本进程缓存项，不删除数据库回执 |
| GET /users/{id} | 读取数据库当前用户状态 |

database_loads 只统计回执查询的数据库尝试，包括查不到或失败的情况。
这些统计属于当前进程；两个服务即使共用数据库，也各有各的缓存与统计。
手动删除缓存项也只影响收到该请求的进程。

## 验证与练习

```powershell
Set-Location E:\WorkProject\go\lesson16
go test ./...
```

保留前 34 项测试，新加 9 项：不同类型实例与返回值、TTL 边界与覆盖、FIFO 与过期优先清理、
反复更新后的元数据上限、非法参数、并发访问、HTTP 命中/删除/过期与快照、404 不缓存，
以及连接池被占满时缓存统计仍能完成。

阅读顺序：cache.go → transfers_api.go 的 getTransfer → api.go 的实例化 → cache_test.go → demo.ps1。
练习：解释为什么 K 需要 comparable，而 V 不需要；再观察 GetAt 返回 (V, bool) 如何表示缓存了一个合法零值。

参考：[Go 泛型教程](https://go.dev/doc/tutorial/generics)、
[Go 单调时钟](https://pkg.go.dev/time#hdr-Monotonic_Clocks)。
