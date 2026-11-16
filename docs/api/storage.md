# 存储接口参考

两层存储均通过插件 `Meta` 注入，按插件名自动命名空间隔离。

```go
p.Storage            // storage.Storage —— 缓存层
p.PersistentStorage  // storage.PersistentStorage —— 持久层
```

::: info 通用约定
- 所有方法返回 `(value, bool)` 或 `bool`，内部错误记录日志后以 `false` 返回
- `Get(ctx, key, out)` 的 `out` 必须是**指针**，内部 JSON 反序列化
- `Clone(prefix)` 创建子命名空间，可多级嵌套
:::

## Storage —— 缓存层

后端：进程内内存（默认）/ Redis。支持 TTL 与列表语义。

### KV

```go
GetString(ctx context.Context, key string) (string, bool)
SetString(ctx context.Context, key, val string, option ...Option) bool

Get(ctx context.Context, key string, out any) bool
Set(ctx context.Context, key string, val any, option ...Option) bool

Del(ctx context.Context, key string) bool
Clear(ctx context.Context) bool                       // 清空本插件命名空间

ScanKeys(ctx context.Context, pattern string, count int64) ([]string, error)

Clone(prefix string) Storage

Expire(ctx context.Context, key string, ttl time.Duration) bool
```

### 选项（Option）

```go
storage.WithTTL(ttl)        // 写入时设置过期时间
storage.WithCheckExist()    // 写入时检查键是否已存在
```

### 列表（Redis List 语义）

```go
LPush(ctx context.Context, key string, values ...any) int64      // 左侧插入，返回长度
RPush(ctx context.Context, key string, values ...any) int64      // 右侧插入，返回长度
LPop(ctx context.Context, key string) (any, bool)                // 左侧弹出
RPop(ctx context.Context, key string) (any, bool)                // 右侧弹出
LRange(ctx context.Context, key string, start, stop int64) ([]any, bool)
LLen(ctx context.Context, key string) int64
LRem(ctx context.Context, key string, count int64, value any) int64  // 移除指定值
LSet(ctx context.Context, key string, index int64, value any) bool   // 按索引设置
LIndex(ctx context.Context, key string, index int64) (any, bool)
LTrim(ctx context.Context, key string, start, stop int64) bool       // 保留范围内元素
```

## PersistentStorage —— 持久层

后端：SQLite（默认，纯 Go）/ MySQL。无 TTL、无列表语义。

```go
GetString(ctx context.Context, key string) (string, bool)
SetString(ctx context.Context, key, val string) bool      // 覆盖写

Get(ctx context.Context, key string, out any) bool        // JSON 反序列化到 out（指针）
Set(ctx context.Context, key string, val any) bool        // JSON 序列化

Has(ctx context.Context, key string) bool
Del(ctx context.Context, key string) bool                 // 键不存在也返回 true
Keys(ctx context.Context, prefix string) ([]string, error) // 按前缀列出相对键
Clear(ctx context.Context) bool                           // 清空本插件全部数据，谨慎！

Clone(prefix string) PersistentStorage
```

::: tip 有序数据怎么存？
持久层没有列表语义。需要保存数组时，小规模数据可把切片作为 JSON 值整体读写：

```go
var list []string
p.PersistentStorage.Get(ctx, "list", &list)
list = append(list, "new item")
p.PersistentStorage.Set(ctx, "list", list)
```

数据量大（如持续增长的日志）时，整体读写会产生放大且单条记录体积失控。两种解法：

- SQL 后端下探测[关系表能力](#关系表能力-可选)，每行一条记录（querylog / tasklog / AI 对话历史即采用这种方式）
- 纯 KV 方案用可排序的键逐条存储（如 `e:<序号>`），通过 `Keys("e:")` 列举、按序号排序
:::

## 关系表能力（可选）

持久层的 SQL 后端（sqlite / mysql）还实现了可选接口 `storage.SQLPersistentStorage`，插件可探测后自建关系表，获得行级存储与 SQL 查询能力：

```go
db, dialect, ok := storage.SQLBackend(p.PersistentStorage)
if !ok {
    // 非 SQL 后端：回退纯 KV 方案（功能不应缺失）
}
err := storage.EnsureTables(ctx, db, dialect, storage.TableDDL{
    Name:   "ania_myplugin_item",
    SQLite: []string{`CREATE TABLE IF NOT EXISTS ania_myplugin_item (...)`},
    MySQL:  []string{`CREATE TABLE IF NOT EXISTS ania_myplugin_item (...) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},
})
```

- `SQLBackend(store)` 内部是类型断言（与 `bot.QQ` 同款惯例）；`Clone` 出的子存储共享同一 `*sql.DB`，探测同样成功
- `TableDDL` 按方言提供建表语句，`EnsureTables` 幂等执行（建议 `CREATE TABLE IF NOT EXISTS`）
- 探测或建表失败时应只记日志并回退 KV，**不要阻断插件启动**
- 插件自建表统一 `ania_` 前缀；框架内置表：`ania_chat_session` / `ania_chat_message`（对话历史）、`ania_memory`（长期记忆）、`ania_kb_doc`（知识库）、`ania_intention`（主动互动意图）、`ania_query_log` / `ania_task_log` / `ania_op_log`（Query / 任务 / 操作日志）
- 约定：MySQL 字符串键用 `VARCHAR(255) COLLATE utf8mb4_bin`、大载荷用 `MEDIUMTEXT`；SQL 冗余过滤列只做 WHERE 收窄，Go 侧匹配仍是最终判定，保证 SQL / KV 两条路径语义一致

## 后端配置

缓存层在面板「配置编辑」页修改（保存后重启生效）：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.store.cache.driver` | `memory` | `memory` / `redis` |
| `bot.store.cache.redis.address` | `localhost:6379` | Redis 地址 |
| `bot.store.cache.redis.password` | 空 | Redis 密码 |
| `bot.store.cache.redis.db` | `0` | Redis 数据库编号 |

持久层的位置属于引导配置，通过环境变量设置（详见 [配置详解 · 引导配置](/guide/configuration#引导配置-环境变量)）：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ANIABOT_STORE_DRIVER` | `sqlite` | `sqlite` / `mysql` |
| `ANIABOT_SQLITE_PATH` | `./data/aniabot.db` | SQLite 数据库文件路径 |
| `ANIABOT_MYSQL_DSN` | 无 | MySQL go-sql-driver DSN（驱动为 mysql 时必填） |

使用示例与实战模式见 [插件开发 · 数据存储](/plugin/storage)。
