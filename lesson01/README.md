# 第一课：从 Java 过渡到 Go

目标：读懂一个带校验的方法，并分清结构体的值与指针。

在 GoLand 中打开本目录，在终端运行：

```powershell
Set-Location E:\WorkProject\go\lesson01
go run .
```

预期输出：

```text
after +10: name=Spring points=20
rejected -5: delta must be positive; points=20
value copy: original=20 copy=120
```

## 按这个顺序读 main.go

1. `package main` 与 `func main()`：可执行程序的包与入口。
2. `type User struct`：定义数据结构；方法单独声明，通过接收者关联到类型。
3. `Name string`、`Points int64`：字段名在前、类型在后；`int64` 对应 Java 的 `long`。
4. `user := ...`：声明局部变量并推断类型；它仍然是静态类型。
5. `func (u *User) AddPoints(delta int64) error`：接收一个指向 User 的指针，增加积分，返回错误值。
6. `return nil`：这里表示没有错误；校验失败时返回一个非 nil 的 error。
7. `if err := ...; err != nil`：先调用并声明 err，再判断；err 的作用域包含这个 if 的分支。

方法和字段首字母大写表示可以被其他包访问。这里不是靠 Java 的 public 关键字控制导出。

## 原理：赋值、传参都传值

Java 的 `User b = a`（User 为 class）复制的是引用，两个变量指向同一对象。

本例 Go 的 `copyUser := user` 复制的是 User 结构体的值，所以两个 Points 字段可以独立变化。
这不是通用的深拷贝规则：如果结构体包含指针、slice 或 map，复制后仍可能访问同一份底层数据。

`*User` 是指针类型，`&user` 取得地址。当前的 `user` 可取地址，所以调用指针接收者方法时，
`user.AddPoints(10)` 可以理解成 `(&user).AddPoints(10)`。
传入的仍是一个值——指针的副本，但方法能通过它修改原来的结构体。

Go 有 GC。使用指针不意味着需要手动释放内存，也不能仅凭 `&` 判断变量在栈上还是堆上。

## 动手实验

先做第一个，完成后再做第二个。

1. 把 `(u *User)` 改成 `(u User)`。先预测三行输出，再运行验证。解释为何校验仍然有效，但原变量的积分没有增加。实验后恢复指针接收者。
2. 增加 `SpendPoints(delta int64) error`：delta 必须大于 0；余额不足要返回错误；任何失败都不能修改积分。依次尝试消费 5、0、100，检查余额。

第二个练习暂不提供实现，留给你动手。你可以直接修改本目录，我会根据代码继续讲解。

Rust 对照项目位于 `E:\WorkProject\rust\lesson01`。Go 的接收者实验完成后，再读它。

## 对应的官方说明

- [Go 赋值规则](https://go.dev/ref/spec#Assignment_statements)
- [指针接收者与值接收者](https://go.dev/doc/effective_go#pointers_vs_values)
- [错误是值](https://go.dev/blog/errors-are-values)
