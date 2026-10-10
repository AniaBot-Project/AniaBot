# Agents.md

This file provides guidance to Agents when working with code in this repository.

## Project Overview

AniaBot is a plugin-driven multi-platform bot framework built with Go. It connects to platforms via pluggable adapters — QQ through NapCat (WebSocket or HTTP adapter using the OneBot v11 protocol), QQ Official through the QQ Open Platform API v2 (WebSocket gateway + REST OpenAPI, hand-rolled resty/gorilla client), Feishu/Lark through the official oapi-sdk-go (WebSocket long-connection or webhook), Telegram through the Bot API (long polling, hand-rolled resty client), Discord through bwmarrin/discordgo (Gateway WebSocket + REST), WeChat through the iLink bot HTTP long-polling API (QR-code login, CDN media with AES-128-ECB, protocol reference: Tencent's open-source openclaw-weixin) — and features an AI chat engine supporting three LLM API formats (OpenAI Chat Completions / OpenAI Responses / Anthropic Messages) with tool calling, MCP (Model Context Protocol) integration, and a skill system.

**Multi-platform model**: the framework normalizes every platform to the OneBot v11 segment format (`OB11Segment{Type, Data}`) as its canonical message shape — adapters translate at the boundary (inbound: platform event → segments; outbound: segments → platform API). IDs are platform-prefixed (`qo:<openid>` for QQ Official, `fs:oc_xxx` for Feishu, `tg:<chat_id>:<message_id>` for Telegram messages, `dc:<channel_id>:<message_id>` for Discord messages, `wx:<user_id>:<message_id>` for WeChat messages); QQ legacy numeric IDs carry no prefix and route to the default adapter. Platform-specific capabilities are exposed via optional interfaces (`adapter.QQExt` / plugin-facing `bot.QQ`; `adapter.ContactsExt` backs the panel's multi-platform address book) — plugins type-assert to probe them, so a plugin written for QQ degrades gracefully on other platforms. Adding a platform = a new adapter package + one blank import in `cmd/main.go`; the core is untouched.

## Commands

### Run

```bash
go run ./cmd    # 不能用 go run cmd/main.go：cmd 下有插件市场生成的注册文件，单文件运行会编译失败
```

### Build (cross-compile)

Requires Go 1.25+ (see `go.mod`).

```bash
make linux     # → build/AniaBot     (GOOS=linux GOARCH=amd64)
make windows   # → build/AniaBot.exe
make web       # rebuild admin panel frontend (cd web && npm ci && npm run build → bot/adminpanel/dist)
make clean     # remove build/

# Manual
cd cmd && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ../build/AniaBot
```

The panel frontend's built `dist/` is gitignored (build artifact). `bot/adminpanel` embeds it via `go:embed`, so run `make web` (or `cd web && npm run build`) at least once before `go build` on a fresh clone.

### Test

```bash
go test ./...                           # all tests
go test -v -race ./...                  # verbose with race detector
go test ./bot/utils/...                 # specific package
go test -run TestParseCommand ./...     # single test by name
```

### Vet & Verify

```bash
go vet ./...
go mod verify
```

## Configuration

All configuration lives in the **database** (the persistent storage's `ania_kv` table), managed via the built-in **Web admin panel** (`bot/adminpanel`, Vue 3 + Tailwind frontend in `web/`, embedded via `go:embed`). There is no `config.yaml` anymore.

- `bot/core/configstore/` — config center. Keys are dot-paths identical to the historical viper keys (`plugin.ai_chat_bot.base_url`, ...), values JSON-encoded. `Init()` seeds framework defaults from the embedded `config_tmpl.yaml` on first run and flags the first-run setup wizard. `ToViper()` builds an in-memory viper so plugin `Start(ctx, *viper.Viper)` semantics are unchanged. `EnsureDefaults()` fills registered defaults for missing keys (never overwrites).
- `common/pluginconfig/` — config field registry + struct binding. Plugins implement the optional `plugin.ConfigSchemaProvider` interface (`ConfigSchema() any`) returning a pointer to a config struct with `cfg` tags (key/label/type/group/help/sensitive/default as struct tags; type inferred from the Go field type, slices comma-separated defaults, pointer scalars = optional params that stay nil when unset). Core reflects the struct before `ToViper()` (`RegisterStruct` — registers panel fields and seeds defaults), then fills it before the plugin's `Start` (`Load`) — plugins read typed struct fields instead of hand-writing `cfg.Get*` calls. The lower-level `plugin.ConfigRegistrar` interface (`ConfigFields() []pluginconfig.Field`) is kept for framework fields (`bot/core/configfields.go`) and dynamic declarations; both share one registry, same-key later registration overwrites in place. The admin panel renders its form dynamically from this registry — adding/removing plugins requires no panel code changes. Called before DI, so implementations must not use injected fields and must return the same pointer on every call.
- **Bootstrap env vars** (needed before the DB opens): `ANIABOT_STORE_DRIVER` (sqlite|mysql), `ANIABOT_SQLITE_PATH` (default `./data/aniabot.db`), `ANIABOT_MYSQL_DSN`.
- MCP servers and per-group/friend prompt overrides are config keys `files.mcp_json` / `files.prompt_json` (raw JSON text), edited graphically in the panel's 扩展配置 (Files) page with an optional raw-JSON source mode.
- Panel auth: random initial password printed to console on first run, SHA-256+salt hash in the `__admin` namespace; config changes take effect after restart.

Config is assembled in `bot/core/core.go` `Run()`: persistent storage (env) → configstore init → collect field registrations (framework + each registered adapter `Definition.ConfigFields` + `RegisterStruct` plugin schemas) → `EnsureDefaults` → `ToViper()` → instantiate every enabled adapter from the registry (each `Definition.New` gated by `bot.platform.<name>.enable`; NapCat sub-mode still picked by `bot.adapter.mode`: ws|http), wiring each with its own `TriggerWrapper` → `Load` each plugin schema struct → cache storage → plugins. Plugins declaring `ConfigSchema()` read typed struct fields in `Start()`; framework-level shared keys (e.g. `files.mcp_json`) are still read from the whole viper passed to `Start()`.

## Architecture

### Layered Structure

```
cmd/main.go              Entry point: blank-imports platform adapter packages (init registers them), registers plugins, runs bot
common/                  Shared interfaces and models (adapter, plugin, storage, bot, msgchain, aitool)
  adapter/                 Platform adapter abstraction: Adapter (public contract) + QQExt (optional capability) + Definition/Register registry + BotWrapper
  bot/                     bot.Bot (public capability facade) + bot.QQ (optional QQ-only capability interface)
  aitool/                  Platform AI-tool injection contract (Tool/Context/Provider/SideEffector) + per-platform tool sets (QQ over adapter.QQExt)
bot/core/                AniaBot orchestrator: plugin lifecycle, event dispatch, multi-adapter container + ID-prefix routing, DI, storage impls
  configstore/             DB-backed config center (seed/migrate/ToViper)
bot/adminpanel/          Web admin panel: config/status APIs + embedded SPA (dist/)
bot/marketplace/         Plugin marketplace: GitHub API index/detail, online install/uninstall/rollback pipelines (rebuild + restart)
bot/adapter/napcat/      NapCat protocol adapters (WebSocket and HTTP), QQ platform
bot/adapter/luckylilia/  Luckylilia (LLBot) OneBot v11 adapters (WebSocket and HTTP), QQ platform (ID prefix lil:)
bot/adapter/qqofficial/  QQ Official adapter (QQ Open Platform API v2: WebSocket gateway + REST, hand-rolled)
bot/adapter/feishu/      Feishu/Lark adapter (larksuite/oapi-sdk-go/v3), WebSocket long-connection + webhook
bot/adapter/telegram/    Telegram adapter (hand-rolled Bot API client, long polling; proxy/api_base config)
bot/adapter/discord/     Discord adapter (bwmarrin/discordgo, Gateway WebSocket; proxy config)
bot/adapter/weixin/      WeChat adapter (iLink bot HTTP long-poll; QR login via panel QRLoginSource API or console, credentials in ./data/weixin, CDN media)
bot/component/           AI chat engine
  aichat/                  ChatBot, LLMClient, MessageBuilder, ToolOrchestrator, messageWindow
  llmtool/                 Tool interface, ToolExecuter, MCP client, SkillManager, schema parser
  functool/                Built-in tools (time, web search, file, msg history, image loading, config get/set)
  oplog/                   Operation audit log (panel + AI tool actions; SQL ania_op_log row-level storage, disabled if SQL probe/DDL fails, package-level singleton)
  sysrestart/              Process self-restart (panel restart/auto-update + /reboot command in pluginsys)
bot/plugins/             Six built-in plugins (sys, log, repeat, interceptor, aichat, news)
bot/utils/               Command parsing, message extraction, URL helpers, time formatting
custom/                  User-created plugin examples and templates
web/                     Admin panel frontend (Vite + Vue 3 + Tailwind v4, builds into bot/adminpanel/dist)
docs/                    VitePress documentation site
```

### Dependency Flow (strictly top-down)

```
cmd/main.go → bot/core, bot/adapter/napcat, bot/adapter/luckylilia, bot/adapter/qqofficial, bot/adapter/feishu, bot/adapter/telegram, bot/adapter/discord, bot/adapter/weixin, bot/plugins/*
bot/adapter/weixin → common/adapter, common/bot, common/model/message, common/msgchain, external (resty, go-qrcode)
bot/core → common/*, bot/utils
bot/adapter/napcat → common/adapter, common/aitool, common/bot, common/model/message, common/msgchain
bot/adapter/luckylilia → common/adapter, common/aitool, common/bot, common/model/message, common/msgchain, external (gorilla/websocket, resty)
bot/adapter/qqofficial → common/adapter, common/bot, common/model/message, common/msgchain, external (resty, gorilla/websocket)
bot/adapter/feishu → common/adapter, common/bot, common/model/message, common/msgchain, external (lark SDK)
bot/adapter/telegram → common/adapter, common/bot, common/model/message, common/msgchain, external (resty, x/net/proxy)
bot/adapter/discord → common/adapter, common/bot, common/model/message, common/msgchain, external (discordgo, gorilla/websocket, x/net/proxy)
bot/plugins/* → common/plugin, common/bot, common/storage, bot/component/*
bot/marketplace → common/pluginmeta, bot/component/oplog, bot/component/sysrestart, external (GitHub API)
bot/component/aichat → bot/component/llmtool, external (openai-go, anthropic-sdk-go)
bot/component/functool → bot/component/llmtool, bot/component/oplog, bot/component/sysrestart, common/pluginconfig, bot/utils
bot/component/llmtool → common/aitool, external (openai-go, MCP SDK)
common/* → leaf packages (no upward dependencies)
```

### Plugin System

Plugins implement `common/plugin.Plugin` by embedding `plugin.Meta` and overriding only needed methods. Key mechanics:

- **Ordered execution**: `LevelLog = -1000`, `LevelNormal = 0`, `LevelPostHandle = 1000`. Plugins sorted by `Order` at startup.
- **Middleware chain**: `OnGroupMsg`/`OnFriendMsg` return `(bool, error)`. Return `false` to stop propagation to subsequent plugins.
- **Broadcast notices**: All 14 notice event types (recall, poke, ban, etc.) are delivered to every plugin — no short-circuit. QQ-only notices (poke/lucky_king/honor/essence/card/ban/upload) simply never fire on non-QQ platforms.
- **Platform scoping**: `plugin.Meta.Platforms []string` declares which platforms a plugin supports (`[]string{"qq"}` or `[]string{"qq","feishu"}`); empty = all platforms (backward compatible). Core filters plugins per-event by the source adapter's platform.
- **Platform-specific events**: optional `plugin.PlatformEventHandler` — `OnPlatformEvent(ctx, bot, message.PlatformEvent) error` receives events that can't map to public notices (e.g. Feishu card actions, bot-added). Broadcast (non-interrupting), filtered by `Meta.Platforms`.
- **Platform capabilities via type assertion**: in message/notice handlers the `bot.Bot` argument is the event source's capability-wrapped facade (`adapter.WrapBot`). Plugins probe optional capability interfaces: `if qb, ok := b.(bot.QQ); ok { qb.GetNCrkey() ... }`. QQ-only plugins (e.g. anti-withdrawal, which needs merge-forward + rkey) declare `Platforms: []string{"qq"}` and assert `bot.QQ`.
- **DI injection**: Core injects `Storage` (cache) and `PersistentStorage` alongside `RestyClient`, `Logger`, `SystemConfig`, and `ConfigEditor` before calling `Start()`. `SystemConfig.AdminId` is a `message.QID` from `bot.admin_id` (string, may carry a platform prefix). `ConfigEditor` is the config-center read/write facade (`plugin.ConfigEditor`, implemented by `configstore.Store`; nil when persistent storage is unavailable — check before use) for plugins that need to read/modify framework config (e.g. the AI config tools); normal plugins should still read config via the `Start` viper or `ConfigSchema` struct binding.
- **Lifecycle**: `Start()` → `StartCron()` → `Awake()` → message/notice events → `OnPanic()`
- **Panic recovery**: Every plugin call is wrapped in `safeExecute`; goroutines spawned via `bot.Go()` have crash recovery that notifies all plugins.

### LLM Tool System

Tools are defined as structs embedding `llmtool.BaseTool[ParamsType]`. Parameter structs use `json` tags for names and `desc` tags for descriptions. The `parser.go` reflection engine auto-generates OpenAI-compatible function schemas from these structs — no manual JSON schema needed.

Registration hierarchy:

1. `functool.CreateDefaultTools()` — registers built-in tools. Always on: `time`, `web_search`, `web_explore` (both via Jina), `private_file`, `load_images` (LLM-invoked, hash-based selective loading: image messages shown to the AI carry `[图片 <hash> url:<url>]` markers, and the tool loads only the images whose hashes the AI passes — covering current/quoted messages, platform history-tool results and merged forwards; recognition via the multimodal model or OCR fallback in the callback). History viewing is NOT a built-in tool anymore — it moved to the platform tool injection mechanism (see item 4, `aitool.NewMsgHistoryTool`). Opt-in (gated behind config flags for safety): `bash` (executes on the host with whitelist/blacklist regex; configurable timeout and head+tail output retention, optional fixed or persisted-across-calls working directory — the persisted cwd is shared by all sessions), the `read_file`/`write_file`/`edit_file`/`glob`/`grep` file tools group (`plugin.ai_chat_bot.file_tools.enable`, default off; optional root jail via `file_tools.root`, plan mode blocks `write_file`/`edit_file` while the read-only ones stay allowed), `file`/`send_file`, `local_image` (reads host-local image files for the LLM to view; served as a data URI to the multimodal model or OCR fallback in the callback), and `computer_use` — `screenshot`/`mouse_click`/`mousemove`/`mouse_scroll`/`keyboard_type`/`keyboard_press`/`active_window`/`list_windows` (`plugin.ai_chat_bot.computer_use.enable`, default off; Windows hosts only — `bot/component/computeruse` implements GDI screen capture + SendInput mouse/keyboard via pure syscalls, no CGO, other platforms compile to `ErrUnsupported` stubs; screenshots reuse the `LoadLocalImage` pipeline for multimodal/OCR display and downscale to `computer_use.max_width`; the AI operates in the pixel space of its most recent screenshot, mapped to screen coords by a shared `View`; input actions are audited via oplog and blocked by plan mode, per-call approval available via `approval.tools`). Registered separately by the aichat plugin (not in `CreateDefaultTools`): `config_get`/`config_set` (`plugin.ai_chat_bot.config_tool.enable`, default off — read/modify framework config via the DI-injected `ConfigEditor`; sensitive fields masked against the pluginconfig registry, only registered keys writable, changes take effect after restart — the AI is instructed to tell the user an admin must send the `/reboot` command, handled by pluginsys via `bot/component/sysrestart`).
2. `functool.CreateToolsWithMCP()` — adds MCP tools (`mcpLazyLoad` selects discovery mode vs eager registration)
3. `functool.CreateToolsWithSkill()` — adds `skill_read` and `skill_reload` tools
4. **Platform tool injection** (`common/aitool`) — platform adapters contribute their own AI tools: an adapter's bot-facade wrapper (registered via `adapter.RegisterBotWrapper`) implements the optional `aitool.Provider` interface (`AITools(ctx) []aitool.Tool`); the aichat plugin asserts it on the event-source facade when creating a session (`registerPlatformTools`, also applied to subagents) and bridges each tool into the `SessionToolExecutor` via `llmtool.AdaptProviderTool`. The `aitool.Tool` contract lives in common (structural: `Name`/`Description`/`Params`/`Execute(ctx, params)` — no `CallBackFuncs`; tools close over the `aitool.Context{Bot, Target, IsGroup, OnMessages}` captured at session creation), so adapter packages don't import llmtool. `Context.OnMessages` (optional `MessageSink`) hands fetched history messages back to the host so images get registered for `load_images`. QQ (shared by NapCat and Luckylilia, implemented once in `aitool.QQTools` over `adapter.QQExt`): `qq_get_group_list`/`qq_get_friend_list`/`qq_get_group_user_info`/`qq_get_group_member_list`/`qq_get_ai_characters`/`qq_get_msg_history` (read-only) + `qq_send_ai_voice`/`qq_send_poke`/`qq_send_group_sign` (declare the optional `aitool.SideEffector` interface → collected at registration into a plugin-level name set that extends the plan-mode gate's `planBlockedTools`, so `/plan on` blocks them too). Telegram injects `tg_get_msg_history` via its `tgBot` wrapper. The history tool supports per-member filtering (`user_id` param paginates back through recent messages collecting that member's messages, capped at `historyMaxFetched` fetched with loop guards for adapters that ignore the seq cursor). Clock-task sessions (no platform facade) register the same tool directly as `get_msg_history` bound to the task target, and facades that don't implement `Provider` get the same implementation injected as a generic `get_msg_history` fallback (built on the required `bot.Bot` history interface), so history viewing stays available on every platform. Naming: platform-prefixed (`qq_`, `tg_`, …); collisions with built-in shared tools are skipped with a log (session-level registration would silently shadow). **Deterministic ordering**: providers must build their slice in a fixed literal order (no map ranges), and the final serialization order is guaranteed by `ToolExecuter.toolsWithSession` sorting all tool names — the `tools` array stays byte-stable across requests so upstream prompt prefix caches are never invalidated.

Each user session gets a `SessionToolExecutor` with isolated dynamic tools. MCP tools default to a two-phase lazy loading pattern (discover → load per session) to avoid context window explosion; `plugin.ai_chat_bot.mcp.lazy_load` (default on) toggles this — off means all MCP tools are registered upfront (stable tool list, better upstream prompt-cache hit rate, higher context cost). The `ToolExecuter` is RWMutex-guarded and supports runtime `AddMCP`/`RemoveMCP`/`ReconnectMCP`, backing the AI's MCP self-management tools (`mcp_list`/`mcp_add`/`mcp_remove`/`mcp_reconnect`, gated by `plugin.ai_chat_bot.mcp_tool.enable`, default off — add/remove persist to `files.mcp_json` via the DI-injected `ConfigEditor` and hot-register/unregister immediately). `skill_reload` re-reads skill files from disk after the AI edits them directly (e.g. via `bash`); panel/tool-driven install/remove already hot-reloads on its own.

### AI Chat Component

`ChatBot` orchestrates per-session conversations:

- `LLMClient` is a thin shell around pluggable per-format backends (`llmBackend` interface in `llmbackend.go`): `chat_completions` (OpenAI-compatible, via `openai-go/v3`), `responses` (OpenAI Responses API, same SDK's `responses` package), and `anthropic` (Anthropic Messages API via `anthropics/anthropic-sdk-go`). The shell owns app-level retry and fallback-model switching; each backend owns message conversion, tool defs, streaming accumulation, and usage mapping. Format is selected per client via `WithAPIFormat` (plugin config `api_format` on the main/compressor/fallback model configs, empty sub-config values inherit the main format; subagents/team members always follow the main format). Anthropic extended thinking is supported end-to-end: `thinking.mode` maps to `budget_tokens`, and thinking blocks (with signature / redacted data) are persisted on `Message.ThinkingBlocks` and replayed verbatim across tool-calling turns, as the API requires. DeepSeek-style `reasoning_content` is still extracted on the chat-completions format. Prompt caching: `chat_completions`/`responses` rely on provider automatic prefix caching (system prompt must stay byte-stable), while `anthropic` needs explicit `cache_control` breakpoints — enabled by default via `plugin.ai_chat_bot.prompt_cache.enable`/`.ttl` (5m/1h), `aichat.WithPromptCache` sets breakpoints on the last system block and the last cacheable block (text/image/tool_result) of the last message; dynamic content (e.g. long-term memory injection) must be appended to the message tail, never to system, otherwise the whole prefix cache is invalidated
- `MessageBuilder` constructs message arrays with system prompt, skill registry, chat history, tool results
- `messageWindow` (in `memorywindow.go`) is a token-budget context window, not a fixed-turn slider: it records prompt-token usage and, once that exceeds 80% of `max_context_tokens`, compresses prior history via an LLM summarizer (`MaybeCompress` / `NewContextCompressor`). History is persisted across restarts via an injected `HistoryStore` (namespaced per group/friend) with `Load`/`Append`/`Replace`/`Clear` semantics: plain appends sync **incrementally** (only new messages), compression/truncation rewrites the whole window (`Replace`), `clear` wipes it — all with a background context; `ChatBot.LoadHistory` replays on session creation. On SQL backends (probed via `storage.SQLBackend`, see Storage below) the store is row-level: `ania_chat_session` (one row per session, `msg_count` doubles as the seq allocator) + `ania_chat_message` (one row per message, `(session_id, seq)` PK, no FK), so appends only INSERT new rows; when the SQL probe or table creation fails history is not persisted at all (in-memory window only, lost on restart). Image parts (remote http(s) URLs and `data:` URIs alike) are degraded to `[图片 <hash>]` text markers before persist (base64 data URIs are MB-scale and would bloat the store), so history replays as text-only.
- **Session cache reclamation** — `pluginaichat` keeps per-session `ChatBot` instances in a `sync.Map` of `chatEntry` (chat + last-active timestamp). A janitor (`chatcache.go`, 1-minute tick) evicts entries idle longer than `plugin.ai_chat_bot.session.max_idle_minutes` (default 120, 0 disables) and enforces an LRU cap of `session.max_sessions` (default 128, 0 disables), so memory no longer grows linearly with lifetime session count. Eviction probes the session lock (sessions mid-response are skipped) and re-checks the pending queue under the lock before `CompareAndDelete`; only the in-memory object is dropped — persisted history reloads on the next message (side effect: tools dynamically loaded via `mcp_load` die with the entry, same as a restart). Only AI-directed messages refresh activity (un-@'d group chatter does not).
- `ToolOrchestrator` runs the multi-turn agent loop: LLM → tool call → result → LLM (up to `maxIterations`, built-in default 100). Main chats and clock-triggered chats get it from `plugin.ai_chat_bot.max_iterations` via `ChatBot.SetMaxIterations`
- `CallBackFuncs` bridges tool execution back to QQ messaging (send text, image, file)
- **AI scheduled tasks (clock)** — `pluginaichat/clock.go`'s `clockManager` owns its **own `*cron.Cron`** (separate from the framework's `StartCron` cron) and persists dynamic, AI/user-managed tasks to `PersistentStorage` (`clock:` namespace). On trigger it runs a **fresh ephemeral ChatBot** (nil history store → discarded after) with full tools, under a per-task timeout, logged via `bot/component/tasklog`. Trigger prompt = `【定时任务】<title>\n<content>`. On trigger execution, the `SendText` callback **discards** intermediate-round text (logs only); only the **final reply** is sent to the trigger target, while tools' own image/file sends (`SendImage`/`SendFile`, e.g. `file`) still go through. Clock tasks also get a dedicated **async subagent** variant (`pluginaichat/clocksubagent.go`, always on): the task AI can launch background sub-agents via `subagent_run` (plus `subagent_list`/`subagent_cancel`), tracked per run in a `clockSubagentSet` — unlike session subagents, results are NOT injected into any pending queue; instead `drainClockSubagents` waits for **all** subagents to finish at wrap-up (reserving `clockSubagentWaitReserve` = 30s of the task budget for the final synthesis, sub-agent timeouts already shrink to the remaining budget via `resolveSubagentTimeout`), feeds the collected results back into the same ephemeral ChatBot (up to `clockSubagentMaxRounds` = 5 synthesis rounds), and only the final synthesized reply is pushed to the target. Users manage via `/clock`; AI manages via `clock_create/list/update/delete/log` tools (registered per session in `getChat`). Gated by `plugin.ai_chat_bot.clock.enable`.
- **AI long-term memory** — `pluginaichat/memory.go`'s `memoryManager` persists AI-managed long-term memories per session scope (`g:<group>` / `f:<qq>`) behind a small `memoryStore` backend interface: each memory is one row in `ania_memory` (`(scope, id)` PK, tags/embedding as JSON columns, `created_at` stored in a fixed-width UTC layout so text order = chronological order); when the SQL probe or table creation fails the whole feature is disabled (`newMemoryManager` returns nil, callers nil-check). Dedup/cap/search logic lives in the manager and is backend-agnostic. The AI manages memories itself via `memory_save`/`memory_search`/`memory_update`/`memory_forget` tools (registered per session in `getChat` with the scope bound at registration, so memories can never leak across groups/chats). `memory_search` returns all entries in scope when no query is given (newest first); with a query it keyword-scores entries (tag hits weigh more than content hits), drops zero-score entries, and sorts by score. `memory_update` overwrites content by ID while preserving untouched fields (nil `user_id`/`tags` keep the old values; explicit empty clears) and recomputes the embedding. Identical content is deduped; near-duplicates (token-set Dice ≥ 0.55 over the shared CJK tokenizer) are not blocked but answered with a merge hint (update the kept entry, forget the extra one); `memory.max_entries` (default 200) caps per-scope count. Optional **proactive injection** (`memory.auto_inject`, default off, plus `inject_max` default 3): `autoInject` searches the scope per turn and prepends hits as a `【长期记忆】` block (header tells the AI to correct stale entries via `memory_update`) to the user message — tail injection only (system untouched → prefix cache preserved; the user message is not persisted, so injected content never pollutes history). Retrieval is keyword scoring via `queryTerms` (shared CJK tokenizer), upgraded to keyword+semantic hybrid when the shared `embedder` (`kb.embedding`) is enabled: the plugin embeds the user message once per turn (`EmbedOneCached`, 128-entry FIFO cache, 10s timeout) and both knowledge-base and memory injection reuse that vector; both managers also run a background backfill goroutine on startup that recomputes embeddings for pre-vector entries. Gated by `plugin.ai_chat_bot.memory.enable`. The plugin also implements the panel's optional `adminpanel.MemorySource` interface (`memorypanel.go`), so the 记忆管理 page can list scopes and create/update/delete entries with live effect (scope input is validated against `^[gf]:\d+$`).
- **AI knowledge base** — `pluginaichat/knowledge.go`'s `knowledgeManager` stores AI/panel-managed documents per scope (`global` / `g:<group>` / `f:<qq>`) behind a small `kbStore` backend interface; the SQL implementation lives in `knowledge_sql.go`. Each document is one row in `ania_kb_doc` (`(scope, id)` PK, tags/embedding as JSON columns, `created_at` fixed-width UTC text); when the SQL probe or table creation fails the feature is disabled (`newKnowledgeManager` returns nil, callers nil-check). `newKnowledgeManager` also performs a one-time idempotent migration of legacy per-scope JSON-array blobs from the old `kb:` KV namespace into the table (empty-array residue keys are dropped; unreadable keys are kept for the next attempt). Long documents are chunked (600 runes/block, 60 rune overlap) at retrieval time; `add`/`update` compute per-chunk embeddings when the shared `embedder` is enabled, otherwise retrieval is pure keyword scoring. `kb_search` and per-turn `autoInject` (session KB + global KB) rank chunks by keyword score plus cosine-similarity bonus; `backfillEmbeddings` fills missing vectors in the background on startup by updating single rows. Dedup (normalized title+content, via a vector-less digest listing), the `kb.max_docs` (default 500) per-scope cap (via `COUNT(*)`), and 8000-rune content truncation are enforced in the manager. The plugin implements the panel's `adminpanel.KnowledgeBaseSource` interface (`knowledgepanel.go`), backing the 知识库 page (scope list with `SELECT DISTINCT scope` counts, vector-less document listing, CRUD, URL import via Jina Reader).
- **AI subagents** — the `subagent_run` session tool (registered per session in `getChat`, always on) lets the main AI delegate a complex/time-consuming subtask to an **ephemeral sub-agent**: a fresh `ChatBot` (nil history store → discarded after) with its own `SessionToolExecutor`, running synchronously inside the parent's tool call. The sub-agent gets the same session-scoped tools (clock/memory via the shared `registerScopedTools` helper) but **not** `subagent_run` itself (no recursive delegation). Its intermediate-round `SendText` is discarded (logged only) while image/file sends and history reads pass through to the current session; the image-loading callbacks are NOT inherited (they close over per-request `loadedImages`/`loaded` state and the two tool loops would drain the same queue) — following the `makeClockCallback` pattern, the sub-agent gets its own image queue for `LoadLocalImage` and a stub `LoadImages` deferring to the main AI. Only the final reply returns to the parent as the tool result, prefixed with run metadata (duration, LLM iterations, tokens); the result is not truncated (the sub-agent's full output is fed back to the main context). Sub-agents follow the main model's execution parameters: `ChatBot.SetMaxIterations(p.mainMaxIterations())` (the main model's max tool-iteration config) and the same generation params via `buildChatOptions`. Timeout resolution (`resolveSubagentTimeout`): default timeout is `p.subagentTimeout()` = the framework's per-message budget (`bot.msg_event_timeout_sec`, default 5min — read in `Start`), per-call override clamped as int **before** multiplying by `time.Second` (1800s cap, overflow-proof), then shrunk to fit the parent context deadline minus a 30s reserve so the sub-agent's own deadline fires first and the timeout surfaces as a graceful tool result instead of killing the whole request; `/stop` still cancels it through the parent request context.

### Message Chain Builder

Fluent builder pattern for constructing bot responses:

```go
msgchain.Builder().Group(target).Text("hello").Mention(qid).Build()
msgchain.Builder().GroupForward(target).Node(sender, content).Build()
```

### Storage

AniaBot exposes two interface-adapted storage layers, both with Clone-based `base64(pluginName)` namespacing injected per plugin:

- **CACHE** (`common/storage.Storage`, injected as `p.Storage`): volatile; supports TTL + Redis-list semantics. Backends: `memory` (default, process-local) | `redis` (opt-in, shared across instances).
- **PERSISTENT** (`common/storage.PersistentStorage`, injected as `p.PersistentStorage`): durable KV/document store, survives restart, no TTL/list semantics. Backends: `sqlite` (default) | `mysql` (opt-in).

SQL persistent backends additionally implement the **optional** `storage.SQLPersistentStorage` interface (`SQLDB()`/`SQLDialect()`); probe it with `storage.SQLBackend(store)` (type assertion, same convention as `bot.QQ`) and create plugin-owned relational tables via `storage.EnsureTables(ctx, db, dialect, storage.TableDDL{Name, SQLite, MySQL}...)` (idempotent, per-dialect DDL). Clone-derived namespace sub-stores share the same `*sql.DB` and probe successfully too. Built-in row-level components (chat history, memory, knowledge base, query/task/op logs) disable themselves with an error log when the SQL probe or DDL fails — degradation must never block startup. Plugin-created tables likewise: probe/DDL failures must only log and degrade, never block plugin start. Plugin-created tables use the `ania_` prefix (built-ins so far: `ania_chat_session`/`ania_chat_message`, `ania_memory`, `ania_kb_doc`, `ania_query_log`, `ania_task_log`, `ania_op_log`). Conventions for these tables: MySQL string keys `VARCHAR(255) COLLATE utf8mb4_bin`, large payloads `MEDIUMTEXT`, timestamps as INTEGER unix seconds or fixed-width UTC text (lexicographic = chronological); redundant filter columns only narrow candidates in WHERE — the Go-side matcher remains the final judge so SQL/KV paths stay semantically identical.

All SQL backends use pure-Go drivers (`modernc.org/sqlite`, `github.com/go-sql-driver/mysql`) — no CGO, cross-compile friendly. Cache config lives under `bot.store.cache` in the DB config; the persistent backend itself is bootstrapped via env vars (`ANIABOT_STORE_DRIVER` / `ANIABOT_SQLITE_PATH` / `ANIABOT_MYSQL_DSN`); factories in `bot/core/storage_factory.go`.

## Key Conventions

- **Language**: Code comments and user-facing strings are in Chinese
- **Logging**: Mixed `log/slog` (core/plugins) and `log.Printf` (adapter/tools). Use `slog` for new code.
- **Error handling**: Adapter/storage methods return `(value, bool)` not `(value, error)`. Use `fmt.Errorf` with `%w` wrapping in component-layer code.
- **Package naming**: Lowercase, concatenated (e.g., `pluginaichat`, `llmtool`, `functool`, `aniaerror`)
- **No linting config**: CI runs `go vet ./...` only. No golangci-lint.
- **Generics**: Used for `BaseTool[T]`, `MessageQueue[T]`, `safeExecuteWithReturn[T]`
- **Functional options**: `Option func(*AniaBot)` pattern for bot configuration
- **OneBot v11**: All QQ message types use the OneBot v11 segment format (`OB11Segment`)
- **Changelog**: Only record in `CHANGELOG.md` changes that alter behavior relative to previously released versions (features, fixes, behavior changes); trivial tweaks don't need entries. Bugs introduced and fixed within the same unreleased version are never recorded (no released version was affected). Add entries under the current unreleased/next version section, one concise sentence each — no long explanations

## CI/CD

Four GitHub Actions workflows in `.github/workflows/`:

- **test.yaml** — runs on push/PR to `main`: `go vet`, `go test -v -race -coverprofile=coverage.out ./...`
- **release.yaml** — runs on version tags (`v*.*.*`): tests, then creates the GitHub Release with the body extracted from the tag's section in `CHANGELOG.md` (falls back to conventional-commit generation if the section is missing) — update `CHANGELOG.md` before tagging
- **docs.yaml** — builds VitePress docs and deploys to GitHub Pages on `main` push
- **docker.yaml** — builds and pushes the AniaBot Docker image: version + short-sha tags on `v*.*.*` tag pushes, and `latest` on manual `workflow_dispatch`; the image's `org.opencontainers.image.description` label is taken from the corresponding `CHANGELOG.md` section

## External Dependencies

| Dependency                       | Purpose                                    |
| -------------------------------- | ------------------------------------------ |
| `openai-go/v3`                   | OpenAI-compatible LLM API client (Chat Completions + Responses) |
| `anthropics/anthropic-sdk-go`    | Anthropic Messages API client (Claude)                          |
| `modelcontextprotocol/go-sdk`    | MCP protocol client                        |
| `gorilla/websocket`              | WebSocket for NapCat / QQ Official / Discord adapters |
| `bwmarrin/discordgo`             | Discord adapter (Gateway WebSocket + REST)            |
| `skip2/go-qrcode`                | WeChat adapter QR login (terminal QR rendering)       |
| `go-resty/resty/v2`              | HTTP client                                |
| `redis/go-redis/v9`              | Redis cache storage backend                |
| `modernc.org/sqlite`             | Pure-Go SQLite, persistent storage default |
| `github.com/go-sql-driver/mysql` | Pure-Go MySQL, persistent storage opt-in   |
| `robfig/cron/v3`                 | Cron scheduling for timed plugins          |
| `spf13/viper`                    | In-memory config view for plugins           |
| `lmittmann/tint`                 | Colored slog handler                       |
