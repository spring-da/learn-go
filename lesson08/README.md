# 第八课：创建用户与并发写入

目标：接收 POST JSON，区分解析错误、字段校验与唯一性冲突，并原子地完成“检查名字、分配 ID、写入”。

## 启动并调用

```powershell
Set-Location E:\WorkProject\go\lesson08
go run .
```

默认地址为 `127.0.0.1:8080`。在另一个 PowerShell 中创建用户：

```powershell
$response = Invoke-WebRequest -Uri 'http://127.0.0.1:8080/users' -Method Post -ContentType 'application/json' -Body '{"name":"  Alice  "}'
$response.StatusCode
$response.Headers.Location
$response.Content
curl.exe -i http://127.0.0.1:8080/users/3
```

新启动的仓库预置 Spring/1、Lin/2。首次创建应得到 201、Location `/users/3` 和 `{"id":3,"name":"Alice"}`。
重复创建 Alice 返回 409；Invoke-WebRequest 可能将非成功状态作为错误显示，可用下列请求文件观察原始响应：

```powershell
'{"name":"Alice"}' | Set-Content -Encoding ascii create-user.json
curl.exe -i http://127.0.0.1:8080/users -H 'Content-Type: application/json' --data-binary '@create-user.json'
```

示例文件中的名字是 ASCII；发送其他语言的名字时应使用 UTF-8 JSON。
Ctrl+C 停止服务。端口占用时，可先设置 `$env:HTTP_ADDR = '127.0.0.1:18080'` 再启动。
数据只存在于进程内存中，重新启动后恢复到两个预置用户。

## 接口约定

| 情况 | 状态 | 响应 |
|---|---|---|
| POST /users，创建成功 | 201 | 用户 JSON 和 Location |
| 同名用户已存在 | 409 | name already exists |
| JSON 语法、结构、类型或未知字段错误 | 400 | invalid JSON body |
| name 缺失、null、空白或长度非法 | 422 | name must contain 2 to 40 characters |
| 未提供 JSON Content-Type | 415 | Content-Type must be application/json |
| 请求体超过 1024 字节 | 413 | request body too large |
| GET /users/{id} | 200 / 400 / 404 | 用户、参数错误或不存在 |

名字先去除首尾空白，再校验 2～40 个 Unicode 码点，并按处理后的字符串判断重名；大小写敏感。
Go 使用 utf8.RuneCountInString，Rust 使用 chars().count()。这里的计数不是 UTF-8 字节数，也不是视觉字符簇数量。
本课仓库直接做内存操作；有效但未存储的 ID 返回 404。

## 请求 DTO 与解析

CreateUserRequest 只接收 name，User 响应还包含服务端分配的 id，可以联系 Java 的请求 DTO 与响应 DTO。
*string 用来表达“没有字符串值”，缺失或 null 会得到 nil，随后进入字段校验。
顶层 null、数组或错误字段类型作为结构错误返回 400。

Decoder.DisallowUnknownFields 拒绝多余字段；第一次 Decode 后再检查 EOF，避免忽略第二个 JSON 对象。
MaxBytesReader 限制读取的请求体大小，通过 errors.As 识别超限错误。
标准解析器仍有自身的字段匹配规则，例如 Go 的结构体字段匹配允许部分大小写变体；本例没有额外统一两种解析器对重复键等边界输入的规则。

## 为什么使用同一把写锁

如果先检查名字，再释放锁，最后重新加锁插入，两个请求都可能观察到“名字不存在”。
因此 Create 的写锁覆盖完整操作：

```text
获取写锁 → 检查名字 → 分配 ID → 更新 users 与 names → 释放写锁
```

读用户使用 RLock/RUnlock，允许多个读取者共享读锁；写入使用独占 Lock/Unlock。
nextID 和两个 map 由同一把锁保护，避免 ID 冲突和索引不一致。
检查到重名时直接返回，defer 会释放锁，且不会消耗 ID。

可以联系 Java 的 ReentrantReadWriteLock，以及“检查再执行”必须处于同一同步边界的要求。
这把锁的原子性只涵盖当前进程的共享仓库。以后改为数据库，应使用数据库唯一约束和事务承载相应约束。

本例 Create 在获取锁前后检查 context；RWMutex 的锁等待本身不支持 context 中断。
若请求取消发生在保存之后，已保存的数据仍存在，客户端超时不能单独证明写入未发生。

## 阅读和验证

先读 api.go 的 createUser，再读 repository.go 的 Create，最后看测试。

```powershell
go test ./...
```

检查包括创建后可读取、首尾空白处理、冲突、解析与字段边界，以及 32 个同名并发创建只成功一次。
另有并发创建不同名字并读取的检查，验证 ID 唯一且数据可见。

练习：为创建请求增加可选的 display_name，但仍只以 name 判断唯一性。先写清楚请求、响应和冲突规则，再修改 DTO 与仓库。

Rust 对照项目：`E:\WorkProject\rust\lesson08`，默认端口 8081。

## 官方说明

- [RWMutex](https://pkg.go.dev/sync#RWMutex)
- [DisallowUnknownFields](https://pkg.go.dev/encoding/json#Decoder.DisallowUnknownFields)
- [MaxBytesReader](https://pkg.go.dev/net/http#MaxBytesReader)
