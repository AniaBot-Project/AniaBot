# 数据存储

AniaBot 提供两层存储，框架注入时已按插件名（`base64(pluginName)`）做命名空间隔离 —— 不同插件用同一个 key 互不干扰。

```go
p.Storage            // 缓存层：易失，支持 TTL 与列表
p.PersistentStorage  // 持久层：重启不丢，纯 KV
```

## 选哪一层？

| 需求 | 用哪层 |
| --- | --- |
| 会话状态、验证码、频率限制、分布式锁 | 缓存层 `Storage`（带 TTL） |
| 消息队列、最近 N 条记录 | 缓存层 `Storage`（列表语义） |
| 用户积分、插件配置、定时任务、对话历史 | 持久层 `PersistentStorage` |

## 缓存层 Storage

后端：`memory`（默认，进程内）/ `redis`。

### KV 操作

```go
// 字符串
p.Storage.SetString(ctx, "key", "value")
val, ok := p.Storage.GetString(ctx, "key")

// 任意类型（内部 JSON 序列化）
type Config struct{ Level int }
p.Storage.Set(ctx, "cfg", Config{Level: 3})
var cfg Config
ok := p.Storage.Get(ctx, "cfg", &cfg)

// 删除与清空
p.Storage.Del(ctx, "key")
p.Storage.Clear(ctx) // 清空本插件命名空间
```

### TTL

```go
// 写入时带过期时间
p.Storage.SetString(ctx, "code", "123456", storage.WithTTL(5*time.Minute))

// 已有 key 续期
p.Storage.Expire(ctx, "key", time.Hour)
```

### 列表操作（Redis List 语义）

```go
p.Storage.LPush(ctx, "queue", "a", "b")   // 左侧插入
p.Storage.RPush(ctx, "queue", "c")        // 右侧插入
val, ok := p.Storage.LPop(ctx, "queue")   // 左侧弹出
items, ok := p.Storage.LRange(ctx, "queue", 0, 99)
n := p.Storage.LLen(ctx, "queue")
p.Storage.LTrim(ctx, "queue", 0, 99)      // 只保留前 100 条
```

还有 `LRem` / `LSet` / `LIndex`，完整签名见 [API · 存储接口](/api/storage)。

### 子命名空间

```go
userStore := p.Storage.Clone("user:10001")  // 按用户再隔离
userStore.SetString(ctx, "nickname", "小明")
```

### 扫描键

```go
keys, err := p.Storage.ScanKeys(ctx, "user:*", 100)
```

## 持久层 PersistentStorage

后端：`sqlite`（默认，纯 Go）/ `mysql`。

```go
// KV（无 TTL、无列表 —— 小规模有序数据可整体存 JSON 数组，
// 数据量大时建议用可排序的键逐条存储，如 e:<序号>）
p.PersistentStorage.SetString(ctx, "token", "abc")
val, ok := p.PersistentStorage.GetString(ctx, "token")

// 任意类型（JSON 序列化，out 必须传指针）
p.PersistentStorage.Set(ctx, "users", []string{"a", "b"})
var users []string
ok := p.PersistentStorage.Get(ctx, "users", &users)

// 存在性 / 删除 / 列举 / 清空
p.PersistentStorage.Has(ctx, "token")
p.PersistentStorage.Del(ctx, "token")       // key 不存在也返回 true
keys, _ := p.PersistentStorage.Keys(ctx, "task:") // 按前缀列出相对键
p.PersistentStorage.Clear(ctx)              // 清空本插件全部数据，谨慎！

// 子命名空间
taskStore := p.PersistentStorage.Clone("clock")
```

## 实战模式

### 模式一：用户计数器

```go
key := "count:" + msg.Sender.UserId.String()
val, _ := p.Storage.GetString(ctx, key)
n, _ := strconv.Atoi(val)
p.Storage.SetString(ctx, key, strconv.Itoa(n+1), storage.WithTTL(24*time.Hour))
```

### 模式二：保存最近 N 条记录

```go
p.Storage.LPush(ctx, "recent", record)
p.Storage.LTrim(ctx, "recent", 0, 99) // 只留 100 条
```

### 模式三：持久化用户档案

```go
type Profile struct {
    Nickname string `json:"nickname"`
    Score    int    `json:"score"`
}

profiles := p.PersistentStorage.Clone("profile")
var pf Profile
if !profiles.Get(ctx, msg.Sender.UserId.String(), &pf) {
    pf = Profile{Nickname: msg.Sender.Nickname}
}
pf.Score += 10
profiles.Set(ctx, msg.Sender.UserId.String(), pf)
```

### 模式四：关系表行级存储（SQL 后端，带回退）

数据量大、需要按条件过滤或增量追加时（如日志、逐条消息），可探测可选的 SQL 能力，自建关系表；探测失败回退纯 KV，功能不缺失：

```go
db, dialect, ok := storage.SQLBackend(p.PersistentStorage)
if ok {
    err := storage.EnsureTables(ctx, db, dialect, storage.TableDDL{
        Name:   "ania_myplugin_log",
        SQLite: []string{`CREATE TABLE IF NOT EXISTS ania_myplugin_log (seq INTEGER NOT NULL PRIMARY KEY, content TEXT NOT NULL)`},
        MySQL:  []string{`CREATE TABLE IF NOT EXISTS ania_myplugin_log (seq BIGINT NOT NULL PRIMARY KEY, content MEDIUMTEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},
    })
    if err != nil {
        ok = false // 建表失败同样回退，只记日志不阻断启动
    }
}
```

约定：表名统一 `ania_` 前缀；MySQL 字符串键用 `VARCHAR(255) COLLATE utf8mb4_bin`、大载荷用 `MEDIUMTEXT`。框架内的对话历史（`ania_chat_session` + `ania_chat_message`）、长期记忆（`ania_memory`）、知识库（`ania_kb_doc`）、Query/任务日志（`ania_query_log` / `ania_task_log`）都是这一模式的参考实现。详见 [API · 存储接口 · 关系表能力](/api/storage#关系表能力-可选)。

## 错误处理约定

所有方法返回 `(value, bool)` 或 `bool` —— 内部错误已记录日志，**不返回 error**。调用方只需检查布尔值：

```go
if val, ok := p.Storage.GetString(ctx, "key"); ok {
    // 使用 val
}
```
