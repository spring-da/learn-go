# 第二课：用接口替换用户仓库

业务：根据 ID 查用户；找到则生成欢迎语，没找到则显示提示。
目标：理解 Go 的隐式接口实现、map 查询和多返回值。

## 运行

用 GoLand 打开本目录，或者在 PowerShell 中运行：

```powershell
Set-Location E:\WorkProject\go\lesson02
go run .
```

预期输出：

```text
id=1 -> hello, Spring
id=2 -> hello, Lin
id=404 -> user 404 not found
```

## 按这个顺序阅读

1. `UserRepository`：声明“根据 ID 查用户”的能力，返回用户和是否找到。
2. `MemoryUserRepository`：用 `map[int64]User` 保存数据，可以类比 Java 的 `Map<Long, User>`。
3. `FindByID`：从 map 取值，返回查询结果。
4. `Welcome`：依赖接口来完成业务，不依赖具体仓库类型。
5. `main`：构造仓库，再显式传给业务函数。这就是手动组织依赖，不需要容器。

`fmt.Sprintf` 生成并返回字符串；`fmt.Printf` 把内容打印出来。

## 和 Java 接口的差异

Java 中通常会写 `class MemoryUserRepository implements UserRepository`。
Go 中不用写 implements：只要方法集合满足接口要求，就实现了这个接口。
这里 FindByID 使用指针接收者，因此实现接口的是 `*MemoryUserRepository`。

你可以做一个编译实验：把 main 中 `&MemoryUserRepository{...}` 的 `&` 去掉。
再运行 `go run .`，观察为何 MemoryUserRepository 的值不能传给 Welcome；实验后恢复。
虽然某个可取地址的值可以直接调用指针接收者方法，这种调用便利并不会改变它的方法集合。

本例的 UserRepository 是普通的行为接口，可以作为变量和参数类型。
它描述调用方需要的能力，不要求实现类型拥有共同的基类。

## 查询失败为何返回 bool

```go
user, ok := r.users[id]
```

这同时取得值和键是否存在。若键不存在，user 得到 User 的零值，ok 为 false。
本例的 User 零值是 `User{Name: ""}`，所以不能通过空名字可靠地判断用户是否存在。

这是 Go 的多返回值，不是返回一个需要解包的 Java 对象。
这里的“没找到”是正常查询结果。内存仓库当前没有模拟连接失败等故障，因而没有 error 返回值。
以后接数据库时，可以考虑 `(User, bool, error)` 来区分“找到、没找到、查询失败”。

接着联系第一课：FindByID 返回的 User 是值的副本。
修改返回用户的 Name 不会更新 map 中存储的 User。

## 动手练习

1. 新增 `EmptyUserRepository`，为它实现相同的 FindByID，始终返回 `User{}, false`。
2. 在 main 中把它传给 Welcome，保留原来的内存仓库调用，观察同一业务函数能否使用两种实现。
3. 在内存仓库中加入 `3: {Name: ""}`，查询 3 和 404，解释空名字用户与不存在用户为何能被区分。

完成标准：业务函数 Welcome 的代码保持不变，两种仓库都能使用；3 被判定为存在，404 被判定为不存在。

Rust 对照项目：`E:\WorkProject\rust\lesson02`。

## 官方说明

- [接口与实现关系](https://go.dev/ref/spec#Interface_types)
- [方法集合](https://go.dev/ref/spec#Method_sets)
- [map 查询及存在标记](https://go.dev/ref/spec#Index_expressions)
