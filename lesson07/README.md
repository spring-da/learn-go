# 第七课：HTTP 用户查询 API

目标：通过真实 HTTP 请求使用仓库，理解路由、参数校验、JSON 响应和业务错误到状态码的映射。

## 启动并请求

```powershell
Set-Location E:\WorkProject\go\lesson07
go run .
```

服务默认监听 `127.0.0.1:8080`，保持这个终端运行，在另一个终端请求：

```powershell
curl.exe -i http://127.0.0.1:8080/users/1
curl.exe -i http://127.0.0.1:8080/users/abc
curl.exe -i http://127.0.0.1:8080/users/404
curl.exe -i http://127.0.0.1:8080/users/500
curl.exe -i http://127.0.0.1:8080/users/99
```

成功响应为 HTTP 200 和 `{"id":1,"name":"Spring"}`。使用 Ctrl+C 停止服务。
若端口已被占用，可先在同一终端设置 `$env:HTTP_ADDR = '127.0.0.1:18080'`，再启动并修改请求端口。

## 本例接口约定

| 请求或结果 | HTTP 状态码 | JSON 内容 |
|---|---|---|
| GET /users/1 或 /users/2 | 200 | 用户对象 |
| 非数字、非正数、超出 int64 的 ID | 400 | id must be a positive 64-bit integer |
| GET /users/404 | 404 | user not found |
| GET /users/500，模拟后端失败 | 503 | repository unavailable |
| GET /users/99，模拟 600ms 慢查询 | 504 | query deadline exceeded |
| POST /users/1 | 405 | method not allowed，Allow 为 GET, HEAD |
| GET /missing | 404 | route not found |

错误响应格式为 `{"error":"..."}`。
普通查询模拟等待 20ms，每次查询有 300ms 预算；99 是本课保留的超时演示输入。
0 和负数在 API 层拒绝；其他未存储的有效正整数通常返回 404。

## 先联系 Spring MVC

| Java 中熟悉的概念 | 本例 Go 写法 |
|---|---|
| @GetMapping("/users/{id}") | mux.HandleFunc("GET /users/{id}", api.getUser) |
| @PathVariable | r.PathValue("id") 加 ParseInt 校验 |
| Controller 的仓库依赖 | API.repo 字段 |
| ResponseEntity | 设置响应状态、Content-Type，写入 JSON |

按 main.go → api.go → repository.go 阅读。main 组装仓库、路由和服务器；处理器做协议转换；仓库负责查询。

## 请求怎样经过程序

1. net/http 接收连接并解析 HTTP 请求。
2. ServeMux 按方法和路径选择处理器，提取 id。
3. getUser 校验 id，通过 r.Context() 创建带 300ms 截止时间的查询 context。
4. 仓库返回找到、未找到、后端故障或超时；处理器映射为 HTTP 状态与 JSON。

路由中 GET 也匹配 HEAD；本例为其他方法提供 JSON 405，并用 Allow 说明支持的方法。
路径未匹配时提供 JSON 404。

## 与上一课的连接

请求 context 是这次调用的父 context。查询超时沿用上一课的 Done、select 和错误检查。
服务端观察到客户端断开等取消事件时，请求 context 会取消，仓库通过它响应；本例处理器直接结束该已取消请求。
已经断开的客户端无法可靠接收一个新的错误响应。

ReadHeaderTimeout 限制读取请求头的等待；QueryTimeout 限制本例仓库查询，它们作用于不同阶段。
多个请求会并发访问同一个只读仓库；本例在启动后不修改 map。
底层故障细节记录在服务日志中，客户端得到稳定的业务错误文案。

## 验证与练习

```powershell
go test ./...
```

api_test.go 使用 httptest 对 HTTP 状态、JSON、参数边界和方法约束做检查。

练习：增加 GET /health，返回 HTTP 200 和 `{"status":"ok"}`，不访问仓库；同时为它增加一条接口检查。

Rust 对照项目：`E:\WorkProject\rust\lesson07`，默认端口为 8081。

## 官方说明

- [net/http 与 ServeMux](https://pkg.go.dev/net/http#ServeMux)
- [Request.Context](https://pkg.go.dev/net/http#Request.Context)
- [httptest](https://pkg.go.dev/net/http/httptest)
