# 第三课：文件仓库与错误传播

沿用上一课的仓库接口思想，将存储实现换成 JSON 文件。
目标：区分不存在与故障，学习文件读取、JSON 解析、错误包装和错误识别。

## 运行与阅读顺序

用 GoLand 打开本目录。终端需以本目录作为工作目录，data 路径相对于工作目录解析。

```powershell
Set-Location E:\WorkProject\go\lesson03
go run .
```

本课把代码拆到了同一个 package 的多个 .go 文件中，`go run .` 会一起构建它们。
不要只运行 `go run main.go`，否则 repository.go 中的定义不会被包含。

先看 data/users.json，再看 repository.go，最后看 main.go。
data/broken.json 故意保留一个 JSON 不允许的尾随逗号。
data/missing.json 故意不存在，供演示文件读取失败。

程序依次演示：

```text
case=found result=hello, Spring
case=absent result=user 404 not found
case=missing-file error=missing-file
  detail: ...
case=invalid-json error=invalid-json
  detail: ...
case=found-again result=hello, Lin
```

detail 显示完整错误，文件系统消息可能随操作系统与系统语言不同而变化。
main 在每次演示失败后主动 continue，因此后续案例继续运行。
这不代表出错的 FindByID 或 Welcome 仍然继续执行；它们已经提前返回了。

## 三种结果

| 状态 | FindByID 返回 | 含义 |
|---|---|---|
| 找到 | user, true, nil | 查询完成且存在用户 |
| 没找到 | User{}, false, nil | 查询完成但不存在用户 |
| 查询失败 | User{}, false, err | 文件无法读取或数据无法解析 |

调用方先检查 err，再检查 found。查询失败时，不应使用其他返回值推断用户是否存在。
相比上一课 `(User, bool)`，新增 error 表达实际存储故障。

## 文件和 JSON

`os.ReadFile` 打开、读取并关闭文件，成功返回 `[]byte`。本例不需要自己管理文件句柄。
`[]User` 是 User 切片，用于接收 JSON 数组。
`json.Unmarshal(data, &users)` 通过指针填充切片；字段后的 `json:"id"` 等 tag 指定 JSON 键名。
tag 是结构体字段的元数据，不是 Java 注解语法。

本例只做读取和结构反序列化，不提供缓存或业务数据校验。
每次查询都会重读文件，所以修改文件后下次查询能看到新内容。
Go 的 encoding/json 默认会忽略未知 JSON 字段，缺失字段通常保持零值。
因此“JSON 能解析”不等于“业务数据合法”。

## 包装错误与识别原因

```go
return User{}, false, fmt.Errorf("read %s: %w", r.Path, err)
```

`%w` 同时完成两件事：增加文字上下文，并保留底层错误。
Welcome 再用 `%w` 包装一次，形成可遍历的错误链。
可以联系 Java 的 `new XxxException(message, cause)` 理解“保留原因”这个目的。

`errors.Is(err, fs.ErrNotExist)` 沿错误链判断文件不存在这一原因，不能用 err.Error() 字符串匹配来替代。
`errors.As(err, &syntaxError)` 沿错误链提取匹配的具体错误类型。
这里 syntaxError 本身是指针，传给 errors.As 的 &syntaxError 是用于接收结果的地址。

语法损坏触发 json.SyntaxError；合法 JSON 与字段类型不匹配等情况可能是其他错误类型。
main 的 invalid-json 分类只针对 SyntaxError，其他错误保留为 repository-error。
库和业务层返回错误，main 决定怎样展示与继续处理，避免每层重复打印。

## 动手练习

1. 把 repository.go 的某个 `%w` 改成 `%v`，运行并观察相应错误是否仍能被 Is/As 识别。注意错误文字可能看起来相同。实验后恢复。
2. 修复 data/broken.json 的尾随逗号，再运行。case 名称表示原来的演示意图；修复后该 case 应输出成功结果。实验后可恢复故障文件。
3. 增加 `fs.ErrPermission` 的识别分支。正常文件不会触发它；重点先理解如何按原因分类。

Rust 对照项目：`E:\WorkProject\rust\lesson03`。

## 官方说明

- [os.ReadFile](https://pkg.go.dev/os#ReadFile)
- [json.Unmarshal](https://pkg.go.dev/encoding/json#Unmarshal)
- [错误包装、Is 与 As](https://go.dev/blog/go1.13-errors)
