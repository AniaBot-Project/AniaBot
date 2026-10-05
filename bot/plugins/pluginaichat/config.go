package pluginaichat

// AI 对话插件的配置结构体。实现 plugin.ConfigSchemaProvider 后，框架启动时
// 自动注册字段（面板渲染 + 默认值补齐），并在 Start 前填充完成——Start 里
// 直接读 p.cfg，无需再手写 cfg.Get* 逐个读取。
//
// 框架级共享键（files.mcp_json / files.prompt_json）不属于本插件配置，
// 仍在 Start 中通过 viper 读取。

// 默认系统提示词（与 Prompt 字段 default 标签保持一致；Start 中作为空值兜底）
const defaultPrompt = `你是一个运行在即时通讯平台上的 AniaBot 助手，在群聊和私聊中帮用户解决实际问题。回答要准确、简洁、自然，不堆砌术语，不要长篇大论，也不要复述工具说明。只有当前问题确实需要查证、补上下文、看图、操作或调用能力时才使用工具；普通闲聊不要频繁调用。

## 一般处理流程
1. 先理解用户意图。上下文不清、指代不明或涉及过去的事时，先调用历史消息工具（qq_get_msg_history 等，按当前平台实际注入的名称）查看最近消息，可用 user_id 参数只看某个群成员的发言，再用 memory_search / kb_search 找已有背景；仍不足再问用户。
2. 按“最小必要”原则选择工具：能直接回答的不查，能读不写，能一次完成的不反复调用。
3. 工具结果不等于最终回复。拿到结果后提炼成用户能看懂的回答，不要原样堆工具输出。
4. 工具失败时先检查参数和说法，换一种表达重试一次；仍失败就如实说明原因和建议，不要假装成功。
5. 用户意图不明确时，先问一句简短问题，不要擅自执行删除、修改、重启等有副作用操作。
6. 只使用本次会话实际注册的工具，不要编造不存在的工具；不要把 API Key、数据库、私密文件等敏感信息泄露给用户。

## 工具场景选择
- time：问当前时间、日期、星期，或回答需要以当前时间为准的问题。
- 历史消息工具（qq_get_msg_history 等，按当前平台实际注入的名称）：问题依赖前文、群聊上下文不完整、用户提到刚才说过或引用过，需要看更早消息时；user_id 参数可只看某个群成员的发言。
- load_images：消息中的图片以 [图片 <hash> url:<url>] 标识（当前消息、历史消息记录、合并转发里都有）；需要看图时用 hashes 参数传入要查看的图片哈希，只加载需要的图片，不要一次性全部加载。
- get_private_file_url：私聊收到文件但消息里没有下载链接，需要读取或处理该文件时。
- webSearch：查最新资讯、不确定的事实、外部资料或找资源链接时。
- webExplore：已有明确 URL，需要打开网页读取正文/详情时；搜索信息不足时用搜索结果里的链接进一步确认。
- memory_search：涉及用户以前说过的事、偏好、本群或私聊的约定时，先检索记忆。
- memory_save：用户透露了值得长期记住的称呼、偏好、重要事实，或群里形成约定时；保存成完整自洽的一句话事实。
- memory_forget：用户明确要求忘记，或记忆明显错误或过时。
- kb_search：用户问知识库/资料库内容，或问题可能已有存档资料时。
- kb_add：用户明确要求保存教程、文章、资料，或给出了值得长期归档的完整知识内容时。
- skill_read：当前任务匹配 available_skills 中的技能时，先读取完整指令再执行。
- skill_reload：通过 bash 等工具直接改过本地 skill 文件后刷新缓存。
- bash：需要在宿主机执行命令、运行脚本、检查或操作文件时（未注册表示未启用）。
- read_file：需要查看宿主机某个文本文件/代码文件内容时，配合 offset/limit 分段读大文件（未注册表示未启用）。
- write_file：需要新建文件或整体重写文件内容时（修改已有文件优先 edit_file）；内容较长时分段写入：先写开头一段，再用 append=true 逐段追加，避免单次输出过长被截断；写完不必再读回全文（未注册表示未启用）。
- edit_file：需要对已有文件做小范围精确修改时；old_string 必须与文件内容逐字符一致且唯一，改前先 read_file 确认原文，改完看替换结果反馈确认生效（未注册表示未启用）。
- glob：按文件名模式找文件（支持 ** 跨目录），如 **/*.go（未注册表示未启用）。
- grep：按正则在文件内容里搜索（如找函数定义、引用位置），配合 include 过滤文件类型（未注册表示未启用）。
- 编码任务流程：glob/grep/read_file 定位 → edit_file/write_file 修改 → bash 编译/测试验证，根据报错继续修复，直到通过再向用户汇报。
- file：用户明确要求读取宿主机文件并发送时（未注册表示未启用）。
- local_image：用户明确要求查看宿主机某张本地图片并给出路径时（未注册表示未启用）。
- screenshot / mouse_click / mousemove / mouse_scroll / keyboard_type / keyboard_press / active_window / list_windows：用户明确要求查看或操作宿主机屏幕/界面时；先 screenshot 看到画面，再按图中坐标操作，操作后可再次 screenshot 确认效果（未注册表示未启用）。
- clock_create/list/update/delete/log：用户要求定时提醒、周期任务、管理任务或查看执行记录时。
- subagent_run/list/cancel：独立、耗时、多步骤且不依赖当前上下文的子任务（如深度调研、多轮搜索总结）适合委派子代理；简单问题不要委派。
- team_run/create/list/delete：需要多视角并行处理、交叉验证或复杂分工时。
- todo_write：需要 3 步以上的复杂任务开始时建立任务清单，逐项推进并及时更新状态（未注册表示未启用）。
- config_get/config_set：仅当用户明确要求查看或修改 Bot 框架配置时；修改操作需要管理员审批（系统会私聊通知管理员确认），审批通过后才会写入；修改后提醒用户重启才能生效，重启需由管理员发送 /reboot 命令（普通用户无权限），你不要自己尝试重启。
- config_file_get/config_file_set：查看或修改扩展配置（MCP 服务器、Prompt 覆盖、AI 钩子、自定义命令的 JSON 文件）时；config_file_set 同样需要管理员审批；hooks/commands/prompt 保存后数秒生效，mcp 重启后生效。
- mcp_list/add/remove/reconnect：用户明确要求管理 MCP 服务器时。
- skill_list/install/remove：用户明确要求查看、安装或卸载技能时。
- MCP 懒加载工具：先 mcp_discover_<服务器> 看工具，再 mcp_load_<服务器> 加载需要的工具，最后调用具体工具。

## 遇到这些情况怎么办
- 普通闲聊或你已有可靠知识：直接回答，不调用工具。
- 信息不足：先历史消息工具 / memory_search / kb_search 补齐；仍不足再问用户。
- 需要最新资料或外部事实：先 webSearch，搜索结果不够再用 webExplore 打开具体链接，交叉确认后再回答。
- 图片或文件：只有必须看图才 load_images，并通过 hashes 传入图片哈希（历史消息/合并转发里的图片哈希同样可加载）；私聊文件无链接用 get_private_file_url；本地文件/图片只在用户明确要求时用 file / local_image。
- 定时任务：先确认 cron 含义、目标、内容、单次或重复，再 clock_create；完成后简短确认已生效。
- 长期记忆/知识库：重要且明确的用户偏好或约定才保存；问过去的事先检索；删除必须用户明确要求。
- 复杂任务：适合后台执行的用 subagent_run，完成后结果会回到当前会话；需要多视角/交叉验证时用 team_run。
- 计划模式：用户开启 /plan 后只做分析与规划并输出实施计划，不调用会产生副作用的工具（修改文件、运行命令、改配置等会被系统阻止）；等用户退出计划模式再执行。
- 执行命令：先确认命令含义和影响，不做删除数据、格式化、重启系统等危险操作。
- 工具报错：调整参数或换说法重试一次；持续失败就如实回复，并说明可能的解决办法（如未启用、需要 token、参数不正确）。`

type thinkingConfig struct {
	Enable bool   `cfg:"enable" label:"启用深度思考" group:"AI 对话 · 模型" default:"false"`
	Mode   string `cfg:"mode" label:"思考模式" type:"select" options:"none,low,medium,high,auto" group:"AI 对话 · 模型" default:"auto"`
}

type searchConfig struct {
	Token string `cfg:"token" label:"Jina AI Token" type:"password" sensitive:"true" group:"AI 对话 · 模型" help:"网页浏览与搜索功能"`
}

type bashToolConfig struct {
	Enable     bool     `cfg:"enable" label:"启用 Bash 工具" group:"AI 对话 · 工具" help:"直接在宿主机执行 shell 命令，注意安全风险" default:"false"`
	Shell      string   `cfg:"shell" label:"Shell 路径" group:"AI 对话 · 工具" help:"留空使用系统默认（Linux/macOS 为 sh，Windows 为 cmd），可填 /bin/bash、/bin/ash 等"`
	Env        []string `cfg:"env" label:"环境变量" group:"AI 对话 · 工具" help:"KEY=VALUE，每行一个"`
	Whitelist  []string `cfg:"whitelist" label:"命令白名单(正则)" group:"AI 对话 · 工具" help:"非空时仅允许匹配的命令，每行一个"`
	Blacklist  []string `cfg:"blacklist" label:"命令黑名单(正则)" group:"AI 对话 · 工具" help:"匹配的命令被禁止，每行一个" default:"config(\\.dev)?\\.(yaml|yml|json),^mkfs,^shutdown,^reboot"`
	WorkingDir string   `cfg:"working_dir" label:"工作目录" group:"AI 对话 · 工具" help:"bash 命令的起始执行目录；「持久化工作目录」关闭时每次命令都从该目录开始"`
	PersistCwd bool     `cfg:"persist_cwd" label:"持久化工作目录" group:"AI 对话 · 工具" help:"开启后命令内 cd 会延续到后续调用，适合在同一项目目录内连续操作；关闭时每次都回到「工作目录」重新开始。注意：该目录在所有群聊/私聊会话之间共享" default:"false"`
	TimeoutSec int      `cfg:"timeout_sec" label:"命令超时(秒)" group:"AI 对话 · 工具" help:"单条命令的最长执行时间，超时会被终止并提示；0 表示使用默认值 300" default:"300"`
	MaxOutput  int      `cfg:"max_output" label:"输出保留字符数" group:"AI 对话 · 工具" help:"命令输出超出时保留头尾、隐藏中段（编译报错等长输出头部最关键）；0 表示使用默认值 30000" default:"30000"`
}

type fileToolConfig struct {
	Enable bool `cfg:"enable" label:"启用文件发送工具" group:"AI 对话 · 工具" help:"可读取宿主机任意文件并发送给用户，默认关闭" default:"false"`
}

type localImageToolConfig struct {
	Enable bool `cfg:"enable" label:"启用本地图片工具" group:"AI 对话 · 工具" help:"可读取宿主机本地图片，默认关闭" default:"false"`
}

// fileToolsConfig 文件读写工具组配置（read_file/write_file/edit_file/glob/grep）：
// 让 AI 具备直接编辑宿主机代码/文本文件的能力，默认关闭（提示词注入可能借此
// 读取敏感文件），Root 可将访问限制在指定目录内。
type fileToolsConfig struct {
	Enable bool   `cfg:"enable" label:"启用文件读写工具" group:"AI 对话 · 工具" help:"read_file / write_file / edit_file / glob / grep 五个工具，AI 可直接读取、编辑宿主机的代码与文本文件，注意安全风险，默认关闭" default:"false"`
	Root   string `cfg:"root" label:"工作根目录" group:"AI 对话 · 工具" help:"非空时工具的相对路径基于该目录解析，且所有路径必须位于该目录内；留空不设访问边界"`
}

// computerUseConfig 电脑操作工具配置：AI 可截图查看宿主机屏幕并控制鼠标键盘
// （screenshot / mouse_click / mousemove / mouse_scroll / keyboard_type /
// keyboard_press / active_window / list_windows），等于把宿主机桌面交给 AI
// 操作，默认关闭；截图经 local_image 同款管线回传（多模态直看 / OCR 兜底），
// 当前仅支持 Windows 宿主机。
type computerUseConfig struct {
	Enable   bool `cfg:"enable" label:"启用电脑操作工具" group:"AI 对话 · 工具" help:"允许 AI 截图查看宿主机屏幕并控制鼠标键盘（仅 Windows）：可把 mouse_click 等加入「工具审批」的工具列表，实现每次操作人工确认；默认关闭" default:"false"`
	MaxWidth int  `cfg:"max_width" label:"截图最大宽度(像素)" group:"AI 对话 · 工具" help:"超出时等比缩小截图以节省 token，0 表示不缩放" default:"1280"`
}

// configToolConfig AI 配置管理工具配置：允许 AI 查看与修改框架配置及扩展配置
// （config_get/config_set/config_file_get/config_file_set，敏感字段对 AI 掩码，
// 修改操作需管理员审批，重启后生效，重启由管理员通过 /reboot 命令执行）。
type configToolConfig struct {
	Enable bool `cfg:"enable" label:"启用配置管理工具" group:"AI 对话 · 工具" help:"允许 AI 查看与修改框架配置及扩展配置（MCP 服务器/Prompt 覆盖/AI 钩子/自定义命令等 JSON 文件）：config_get/config_set/config_file_get/config_file_set，敏感字段对 AI 掩码，修改需管理员审批，默认关闭" default:"false"`
}

// skillToolConfig AI Skill 管理工具配置：允许 AI 自行安装/卸载/查看技能
// （skill_list / skill_install / skill_remove），无需后台面板手动上传。
// 安装支持 zip 链接下载（含 GitHub 仓库自动转换）或直接撰写 SKILL.md 内容，
// 落盘后热重载立即生效；默认关闭以控制任意代码/提示注入风险。
type skillToolConfig struct {
	Enable bool `cfg:"enable" label:"启用 Skill 管理工具" group:"AI 对话 · 工具" help:"允许 AI 安装/卸载/查看技能（skill_list/skill_install/skill_remove）：AI 可先用 webSearch/webExplore 上网搜索技能资源，再从 zip 链接或 GitHub 仓库下载安装，也可直接撰写 SKILL.md 内容创建；安装后热重载立即生效，默认关闭" default:"false"`
}

// mcpConfig MCP 服务器加载策略配置
type mcpConfig struct {
	LazyLoad bool `cfg:"lazy_load" label:"MCP 工具懒加载" group:"AI 对话 · 工具" help:"开启后 MCP 工具按需发现/加载（mcp_discover/mcp_load），工具定义不进初始上下文、节省 token；但会话内动态加载会改变 tools 列表，可能降低上游 prompt 缓存命中率。关闭后启动时全量注册所有 MCP 工具（工具列表恒定、缓存友好，但工具较多时上下文开销大）" default:"true"`
}

// mcpToolConfig AI MCP 管理工具配置：允许 AI 自行添加/删除/重连/查看
// MCP 服务器（mcp_list / mcp_add / mcp_remove / mcp_reconnect），配置写入
// files.mcp_json 持久化并即时热注册生效；默认关闭以控制远程代码/工具注入风险。
type mcpToolConfig struct {
	Enable bool `cfg:"enable" label:"启用 MCP 管理工具" group:"AI 对话 · 工具" help:"允许 AI 添加/删除/重连/查看 MCP 服务器（mcp_list/mcp_add/mcp_remove/mcp_reconnect）：支持 stdio 命令与 streamable/sse HTTP 端点，配置写入 files.mcp_json 持久化并即时生效，默认关闭" default:"false"`
}

type ocrConfig struct {
	Enable      bool     `cfg:"enable" label:"启用备用识图模型" group:"AI 对话 · 备用识图模型" help:"主模型不支持多模态时将图片转文字" default:"false"`
	BaseURL     string   `cfg:"base_url" label:"Base URL" group:"AI 对话 · 备用识图模型" default:"https://api.siliconflow.cn/v1"`
	APIKey      string   `cfg:"api_key" label:"API Key" type:"password" sensitive:"true" group:"AI 对话 · 备用识图模型"`
	Model       string   `cfg:"model" label:"模型" group:"AI 对话 · 备用识图模型" default:"Qwen/Qwen3-VL-8B-Instruct"`
	MaxToken    *int     `cfg:"max_token" label:"最大输出 Token" group:"AI 对话 · 备用识图模型" default:"600"`
	Temperature *float64 `cfg:"temperature" label:"Temperature" group:"AI 对话 · 备用识图模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	TopP        *float64 `cfg:"top_p" label:"Top P" group:"AI 对话 · 备用识图模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	TopK        *int     `cfg:"top_k" label:"Top K" group:"AI 对话 · 备用识图模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	Prompt      string   `cfg:"prompt" label:"识图提示词" type:"text" group:"AI 对话 · 备用识图模型" default:"你负责将看到的图片用markdown格式描述出来，不要有无关的其他对话"`
}

type clockConfig struct {
	Enable            bool `cfg:"enable" label:"启用 AI 定时任务" group:"AI 对话 · 定时任务" default:"true"`
	DefaultTimeoutSec int  `cfg:"default_timeout_sec" label:"默认超时(秒)" group:"AI 对话 · 定时任务" default:"120"`
	MaxLogEntries     int  `cfg:"max_log_entries" label:"日志保留条数" group:"AI 对话 · 定时任务" default:"20000"`
}

// promptCacheConfig 上游 prompt 缓存配置：仅 anthropic 格式需要显式声明
// cache_control 断点（chat_completions / responses 由提供方自动前缀缓存）。
// 断点打在 system 与最后一条消息上，system 内容保持稳定即可稳定命中；
// 动态内容（如未来要做的长期记忆注入）必须追加到消息尾部而非 system。
type promptCacheConfig struct {
	Enable bool   `cfg:"enable" label:"启用 Prompt 缓存" group:"AI 对话 · 模型" help:"anthropic 格式下为 system 与对话历史设置 cache_control 断点（需模型与上游支持）；chat_completions / responses 为自动前缀缓存，不受此开关影响" default:"true"`
	TTL    string `cfg:"ttl" label:"缓存保留时长" type:"select" options:"5m,1h" group:"AI 对话 · 模型" help:"仅 anthropic 格式有效：5m 写入成本 1.25x、1h 为 2x，读取均为 0.1x；会话间隔短留 5m，长时间闲置用 1h" default:"5m"`
}

type memoryConfig struct {
	Enable     bool `cfg:"enable" label:"启用长期记忆" group:"AI 对话 · 记忆" default:"true"`
	MaxEntries int  `cfg:"max_entries" label:"单会话记忆上限" group:"AI 对话 · 记忆" default:"200"`
	// AutoInject 每轮按用户消息纯关键词检索相关记忆并注入到用户消息前
	// （尾部注入：system 不变，不影响上游前缀缓存；用户消息不落盘，历史无污染）
	AutoInject bool `cfg:"auto_inject" label:"主动注入相关记忆" group:"AI 对话 · 记忆" default:"false" help:"每轮按用户消息检索相关记忆并追加到用户消息前（system 保持不变，不影响上游前缀缓存；用户消息不落盘，历史无污染）。启用向量检索后按语义+关键词混合检索，否则纯关键词"`
	InjectMax  int  `cfg:"inject_max" label:"每轮注入条数上限" group:"AI 对话 · 记忆" default:"3" help:"主动注入的最大记忆条数，0 表示不限制"`
}

type kbEmbeddingConfig struct {
	Enable  bool   `cfg:"enable" label:"启用向量检索" group:"AI 对话 · 知识库" default:"false" help:"入库时计算语义向量，检索与自动注入按语义+关键词混合打分（自动注入每轮一次 embed，带缓存）；启动时自动回填存量数据的向量。需要 embedding 服务支持，provider 不支持时自动退回纯关键词"`
	BaseURL string `cfg:"base_url" label:"Embedding Base URL" group:"AI 对话 · 知识库" help:"留空使用主模型的 Base URL；如主模型无 embedding 接口（如 DeepSeek），可填 https://api.jina.ai/v1 或其它 OpenAI 兼容服务"`
	APIKey  string `cfg:"api_key" label:"Embedding API Key" type:"password" sensitive:"true" group:"AI 对话 · 知识库" help:"留空使用主模型的 API Key（用 Jina 时可填 Jina AI Token）"`
	Model   string `cfg:"model" label:"Embedding 模型" group:"AI 对话 · 知识库" default:"jina-embeddings-v3" help:"如 jina-embeddings-v3、text-embedding-3-small、BAAI/bge-large-zh-v1.5"`
}

type kbConfig struct {
	Enable     bool `cfg:"enable" label:"启用知识库" group:"AI 对话 · 知识库" default:"true"`
	MaxDocs    int  `cfg:"max_docs" label:"单作用域文档上限" group:"AI 对话 · 知识库" default:"500"`
	AutoInject bool `cfg:"auto_inject" label:"自动注入上下文" group:"AI 对话 · 知识库" default:"true" help:"每次对话前自动检索相关文档并注入上下文。启用向量检索后按语义+关键词混合检索（每轮一次 embed，带缓存），未启用则纯关键词"`

	Embedding kbEmbeddingConfig `cfg:"embedding"`
}

type subagentConfig struct {
	Enable        bool   `cfg:"enable" label:"启用子代理" group:"AI 对话 · 子代理" help:"允许主 AI 把复杂子任务委派给一次性子代理执行，子代理拥有全部工具能力且上下文独立" default:"true"`
	TimeoutSec    int    `cfg:"timeout_sec" label:"默认超时(秒)" group:"AI 对话 · 子代理" default:"300"`
	MaxIterations int    `cfg:"max_iterations" label:"最大工具迭代轮数" group:"AI 对话 · 子代理" default:"100"`
	MaxResultLen  int    `cfg:"max_result_len" label:"结果最大字符数" group:"AI 对话 · 子代理" help:"子代理返回结果超出该长度时截断，防止污染主对话上下文" default:"4000"`
	BaseURL       string `cfg:"base_url" label:"子代理 Base URL" group:"AI 对话 · 子代理" help:"留空使用主模型配置；可填更便宜的模型以降低子任务成本"`
	APIKey        string `cfg:"api_key" label:"子代理 API Key" type:"password" sensitive:"true" group:"AI 对话 · 子代理" help:"留空使用主模型配置"`
	Model         string `cfg:"model" label:"子代理模型" group:"AI 对话 · 子代理" help:"留空使用主模型；子代理与 AI 定时任务共用该模型"`
	APIFormat     string `cfg:"api_format" label:"子代理 API 格式" type:"select" options:"chat_completions,responses,anthropic" group:"AI 对话 · 子代理" help:"留空跟随主模型格式"`
}

type teamConfig struct {
	Enable        bool `cfg:"enable" label:"启用 Agent 团队" group:"AI 对话 · Agent 团队" help:"允许主 AI 组建多代理团队，把子任务派发给多个成员代理并行执行" default:"false"`
	TimeoutSec    int  `cfg:"timeout_sec" label:"成员默认超时(秒)" group:"AI 对话 · Agent 团队" default:"300"`
	MaxIterations int  `cfg:"max_iterations" label:"成员最大工具迭代轮数" group:"AI 对话 · Agent 团队" default:"100"`
	MaxResultLen  int  `cfg:"max_result_len" label:"单成员结果最大字符数" group:"AI 对话 · Agent 团队" help:"每个成员返回的结果超出该长度时截断，防止汇总报告污染主对话上下文" default:"4000"`
	MaxMembers    int  `cfg:"max_members" label:"单次最多并行成员数" group:"AI 对话 · Agent 团队" default:"5"`
}

type queryLogConfig struct {
	Enable         bool `cfg:"enable" label:"启用 Query 日志" group:"AI 对话 · 查询日志" help:"在面板记录每次 AI 回复的完整执行过程（耗时、token、工具调用详情）" default:"true"`
	MaxEntries     int  `cfg:"max_entries" label:"日志保留条数" group:"AI 对话 · 查询日志" default:"20000"`
	MaxToolRecords int  `cfg:"max_tool_records" label:"单条日志工具明细上限" group:"AI 对话 · 查询日志" help:"每条 Query 日志最多保留的工具调用明细条数，超出部分仅保留总数不存明细；0 表示不限制（实际条数受最大工具轮数约束）" default:"200"`
	MaxResultRunes int  `cfg:"max_result_runes" label:"日志中工具结果截断上限(字符)" group:"AI 对话 · 查询日志" help:"仅控制工具执行结果写入 Query 日志时的保留长度，不影响 AI 实际收到的完整输出；0 表示不限制。仅此字段限长（网页抓取、命令执行等外部输出可能很大），用户输入、工具参数、最终回复始终完整记录" default:"1000"`
}

// hooksConfig AI 钩子配置：按 files.hooks_json（面板「扩展配置」页编辑）在会话事件
// （SessionStart/UserPromptSubmit/PreToolUse/PostToolUse/Stop/SubagentStop/PreCompact）
// 上执行管理员配置的 shell 命令（Claude Code 语义：stdin 传 JSON 载荷，
// 退出码 0 通过 / 2 阻断 / 其他仅记日志）。
type hooksConfig struct {
	Enable     bool `cfg:"enable" label:"启用 AI 钩子" group:"AI 对话 · 钩子" help:"按 files.hooks_json 配置在会话事件上执行 shell 命令（PreToolUse/UserPromptSubmit 可阻断）；钩子在宿主机执行，请仅配置可信命令" default:"false"`
	TimeoutSec int  `cfg:"timeout_sec" label:"单个钩子默认超时(秒)" group:"AI 对话 · 钩子" help:"单钩子超时可另行在 JSON 中按条覆盖；上限 60 秒" default:"10"`
}

// todoConfig 任务清单配置：AI 用 todo_write 维护当前会话的任务清单（内存态，
// 全量替换语义）；有未完成项时在后续对话尾部注入提醒。
type todoConfig struct {
	Enable bool `cfg:"enable" label:"启用任务清单" group:"AI 对话 · 工具" help:"启用后 AI 可用 todo_write 维护当前会话的任务清单，复杂多步任务更有条理" default:"true"`
}

// approvalConfig 工具审批配置：列出的工具执行前向会话发送确认消息，由请求发送者或
// 机器人管理员回复「允许/拒绝」授权，超时自动拒绝；bash 中既不在白名单也不在黑名单
// 的命令同样走审批（命令级粒度，无需在此列出 bash；审批未启用时这些命令默认放行，
// 只认黑名单）。配置修改类工具（config_set/config_file_set）恒需管理员审批，无需在此列出。
type approvalConfig struct {
	Enable     bool   `cfg:"enable" label:"启用工具审批" group:"AI 对话 · 工具审批" help:"危险工具执行前需人工确认（请求发送者或管理员回复「允许/拒绝」）；同时作为 bash 未列名命令的审批通道（关闭时未列名命令默认放行，只认黑名单）" default:"false"`
	Tools      string `cfg:"tools" label:"需审批的工具" group:"AI 对话 · 工具审批" help:"逗号分隔的工具名；bash 有命令级黑白名单+审批三段式，无需列入；config_set/config_file_set 恒需管理员审批，无需列入" default:"file"`
	TimeoutSec int    `cfg:"timeout_sec" label:"审批超时(秒)" group:"AI 对话 · 工具审批" help:"超时无回复自动拒绝；范围 10~240" default:"120"`
}

// compressorConfig 上下文压缩专用模型配置：留空回退主模型配置。
type compressorConfig struct {
	BaseURL   string `cfg:"base_url" label:"压缩器 Base URL" group:"AI 对话 · 备用模型" help:"留空使用主模型配置"`
	APIKey    string `cfg:"api_key" label:"压缩器 API Key" type:"password" sensitive:"true" group:"AI 对话 · 备用模型" help:"留空使用主模型配置"`
	Model     string `cfg:"model" label:"压缩器模型" group:"AI 对话 · 备用模型" help:"留空使用主模型；建议填更便宜的模型降低历史压缩成本"`
	APIFormat string `cfg:"api_format" label:"压缩器 API 格式" type:"select" options:"chat_completions,responses,anthropic" group:"AI 对话 · 备用模型" help:"留空跟随主模型格式"`
}

// retryConfig 应用层重试配置：429/5xx/网络错误时指数退避重试。
type retryConfig struct {
	MaxAttempts  int `cfg:"max_attempts" label:"最大尝试次数" group:"AI 对话 · 模型" help:"0 或 1 表示不重试；429/5xx/网络错误时指数退避重试（SDK 已内置 429/5xx 重试，此值为应用层补充）" default:"3"`
	BaseDelaySec int `cfg:"base_delay_sec" label:"退避基准(秒)" group:"AI 对话 · 模型" help:"每次重试等待 基准×2^n 秒（带随机抖动）" default:"2"`
}

// fallbackConfig 备用模型配置：主模型重试耗尽或遇到不可重试错误时自动切换重试一次。
type fallbackConfig struct {
	BaseURL   string `cfg:"base_url" label:"备用模型 Base URL" group:"AI 对话 · 备用模型" help:"留空使用主模型配置"`
	APIKey    string `cfg:"api_key" label:"备用模型 API Key" type:"password" sensitive:"true" group:"AI 对话 · 备用模型" help:"留空使用主模型配置"`
	Model     string `cfg:"model" label:"备用模型" group:"AI 对话 · 备用模型" help:"留空表示不启用备用模型；主模型重试耗尽后自动切换重试一次（仅主对话与上下文压缩）"`
	APIFormat string `cfg:"api_format" label:"备用模型 API 格式" type:"select" options:"chat_completions,responses,anthropic" group:"AI 对话 · 备用模型" help:"留空跟随主模型格式"`
}

// streamConfig 流式回复配置：平台支持「先发后改」时逐字展示回复
// （如飞书卡片/Telegram 消息实时更新）；平台不支持或出错时自动退化为一次性回复。
type streamConfig struct {
	Enable bool `cfg:"enable" label:"启用流式回复" group:"AI 对话 · 回复" help:"平台支持时逐字展示回复（飞书卡片/Telegram/Discord 消息实时更新）；不支持或出错时自动退化为一次性回复" default:"true"`
}

// quotaConfig 每日 Token 配额限制配置：按会话与全局两个维度限制每日消耗。
type quotaConfig struct {
	Enable            bool `cfg:"enable" label:"启用每日配额限制" group:"AI 对话 · 配额" help:"按会话与全局两个维度限制每日 Token 消耗，超限后 AI 请求被拒绝" default:"false"`
	DailyTokens       int  `cfg:"daily_tokens" label:"每会话每日 Token 上限" group:"AI 对话 · 配额" help:"0 表示不限制；超出后该会话当日 AI 请求将被拒绝（含子代理、定时任务消耗）" default:"0"`
	GlobalDailyTokens int  `cfg:"global_daily_tokens" label:"全局每日 Token 上限" group:"AI 对话 · 配额" help:"0 表示不限制；所有会话合计消耗超限后 AI 请求全部拒绝" default:"0"`
}

// sessionConfig 会话内存回收配置：限制内存中驻留的 ChatBot 会话数量与时长，
// 防止活跃会话增多导致内存线性增长。淘汰只丢弃内存对象，对话历史已持久化，
// 下次发言时自动重建会话并回放历史。
type sessionConfig struct {
	MaxIdleMinutes int `cfg:"max_idle_minutes" label:"闲置会话回收（分钟）" group:"AI 对话 · 会话驻留" default:"120" help:"超过该时长无 AI 交互的会话从内存淘汰（历史在数据库，下次发言自动恢复；会话内 mcp_load 动态加载的工具会随淘汰失效，等同重启）；0 表示不按闲置淘汰"`
	MaxSessions    int `cfg:"max_sessions" label:"最大驻留会话数" group:"AI 对话 · 会话驻留" default:"128" help:"内存中最多驻留的会话数，超出时淘汰最久未活跃的；0 表示不限制"`
	NoMentionClear int `cfg:"no_mention_clear" label:"未@自动清空阈值（条）" group:"AI 对话 · 会话驻留" default:"30" help:"群聊连续未@消息达到该条数后自动清空该群对话历史并卸载动态加载的 MCP 工具，下次 @ 即开新对话；计数跨重启保留（每 10 条落盘一次，重启后继续累计），会话被闲置回收时计数不清零、在下次 @ 时补清；0 表示关闭自动清空"`
}

type aiChatConfig struct {
	BaseURL          string `cfg:"plugin.ai_chat_bot.base_url" label:"Base URL" group:"AI 对话 · 模型" help:"API 地址；anthropic 格式填 https://api.anthropic.com，其余填 OpenAI 兼容地址" default:"https://api.deepseek.com"`
	APIKey           string `cfg:"plugin.ai_chat_bot.api_key" label:"API Key" type:"password" sensitive:"true" group:"AI 对话 · 模型"`
	Model            string `cfg:"plugin.ai_chat_bot.model" label:"模型" group:"AI 对话 · 模型" default:"deepseek-chat"`
	APIFormat        string `cfg:"plugin.ai_chat_bot.api_format" label:"API 格式" type:"select" options:"chat_completions,responses,anthropic" group:"AI 对话 · 模型" help:"chat_completions：OpenAI 兼容（DeepSeek 等，默认）；responses：OpenAI Responses API；anthropic：Anthropic Messages API（Claude）" default:"chat_completions"`
	Multimodal       bool   `cfg:"plugin.ai_chat_bot.multimodal" label:"多模态" group:"AI 对话 · 模型" help:"主模型是否支持图片输入" default:"false"`
	RateLimit        int    `cfg:"plugin.ai_chat_bot.rate_limit" label:"并发限制" group:"AI 对话 · 模型" help:"同时处理的 AI 请求数上限，超出后直接拒绝" default:"2"`
	MaxContextTokens int    `cfg:"plugin.ai_chat_bot.max_context_tokens" label:"上下文 Token 上限" group:"AI 对话 · 模型" default:"128000"`
	MaxIterations    int    `cfg:"plugin.ai_chat_bot.max_iterations" label:"最大工具调用轮数" group:"AI 对话 · 模型" help:"单次回复中 AI 最多连续调用工具的轮数，超出后强制结束" default:"100"`
	// 指针字段：nil 表示不向下游 LLM 传该参数（保持未设置语义）。
	// MaxToken 不设默认值：留空即不传，用模型 API 的最大输出上限——单次输出
	// 被人为限低会把长内容（如 write_file 的参数）拦腰截断
	MaxToken    *int     `cfg:"plugin.ai_chat_bot.max_token" label:"最大输出 Token" group:"AI 对话 · 模型" help:"留空则不传该参数，使用模型 API 最大输出上限（anthropic 格式必填，留空时按 8192 下发）；已有值可清空保存恢复为不传"`
	Temperature *float64 `cfg:"plugin.ai_chat_bot.temperature" label:"Temperature" group:"AI 对话 · 模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	TopP        *float64 `cfg:"plugin.ai_chat_bot.top_p" label:"Top P" group:"AI 对话 · 模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	TopK        *int     `cfg:"plugin.ai_chat_bot.top_k" label:"Top K" group:"AI 对话 · 模型" help:"留空则不传该参数，使用模型 API 默认值；已有值可清空保存恢复为不传"`
	// 自定义请求头：附加到全部 LLM 请求（主对话/子代理/定时任务/上下文压缩/
	// 备用识图/备用模型），用于网关鉴权、路由标记等场景；每行一个，
	// 支持 "Name: Value" 或 "Name=Value"，同名覆盖默认头（含 User-Agent）。
	Headers []string `cfg:"plugin.ai_chat_bot.headers" label:"自定义请求头" group:"AI 对话 · 模型" help:"附加到全部 LLM 请求（主对话/子代理/定时任务/压缩器/备用识图/备用模型），用于网关鉴权、路由标记等；每行一个，格式 Name: Value 或 Name=Value，同名覆盖默认头（含 User-Agent）；非法行跳过并记录日志"`
	// 与 defaultPrompt 常量保持一致（标签内 \n 会被解析为换行）
	Prompt   string         `cfg:"plugin.ai_chat_bot.prompt" label:"系统提示词" type:"text" group:"AI 对话 · 模型" default:"你是一个运行在即时通讯平台上的 AniaBot 助手，在群聊和私聊中帮用户解决实际问题。回答要准确、简洁、自然，不堆砌术语，不要长篇大论，也不要复述工具说明。只有当前问题确实需要查证、补上下文、看图、操作或调用能力时才使用工具；普通闲聊不要频繁调用。\n\n## 一般处理流程\n1. 先理解用户意图。上下文不清、指代不明或涉及过去的事时，先调用历史消息工具（qq_get_msg_history 等，按当前平台实际注入的名称）查看最近消息，可用 user_id 参数只看某个群成员的发言，再用 memory_search / kb_search 找已有背景；仍不足再问用户。\n2. 按“最小必要”原则选择工具：能直接回答的不查，能读不写，能一次完成的不反复调用。\n3. 工具结果不等于最终回复。拿到结果后提炼成用户能看懂的回答，不要原样堆工具输出。\n4. 工具失败时先检查参数和说法，换一种表达重试一次；仍失败就如实说明原因和建议，不要假装成功。\n5. 用户意图不明确时，先问一句简短问题，不要擅自执行删除、修改、重启等有副作用操作。\n6. 只使用本次会话实际注册的工具，不要编造不存在的工具；不要把 API Key、数据库、私密文件等敏感信息泄露给用户。\n\n## 工具场景选择\n- time：问当前时间、日期、星期，或回答需要以当前时间为准的问题。\n- 历史消息工具（qq_get_msg_history 等，按当前平台实际注入的名称）：问题依赖前文、群聊上下文不完整、用户提到刚才说过或引用过，需要看更早消息时；user_id 参数可只看某个群成员的发言。\n- load_images：消息中的图片以 [图片 <hash> url:<url>] 标识（当前消息、历史消息记录、合并转发里都有）；需要看图时用 hashes 参数传入要查看的图片哈希，只加载需要的图片，不要一次性全部加载。\n- get_private_file_url：私聊收到文件但消息里没有下载链接，需要读取或处理该文件时。\n- webSearch：查最新资讯、不确定的事实、外部资料或找资源链接时。\n- webExplore：已有明确 URL，需要打开网页读取正文/详情时；搜索信息不足时用搜索结果里的链接进一步确认。\n- memory_search：涉及用户以前说过的事、偏好、本群或私聊的约定时，先检索记忆。\n- memory_save：用户透露了值得长期记住的称呼、偏好、重要事实，或群里形成约定时；保存成完整自洽的一句话事实。\n- memory_forget：用户明确要求忘记，或记忆明显错误或过时。\n- kb_search：用户问知识库/资料库内容，或问题可能已有存档资料时。\n- kb_add：用户明确要求保存教程、文章、资料，或给出了值得长期归档的完整知识内容时。\n- skill_read：当前任务匹配 available_skills 中的技能时，先读取完整指令再执行。\n- skill_reload：通过 bash 等工具直接改过本地 skill 文件后刷新缓存。\n- bash：需要在宿主机执行命令、运行脚本、检查或操作文件时（未注册表示未启用）。\n- read_file：需要查看宿主机某个文本文件/代码文件内容时，配合 offset/limit 分段读大文件（未注册表示未启用）。\n- write_file：需要新建文件或整体重写文件内容时（修改已有文件优先 edit_file）；内容较长时分段写入：先写开头一段，再用 append=true 逐段追加，避免单次输出过长被截断；写完不必再读回全文（未注册表示未启用）。\n- edit_file：需要对已有文件做小范围精确修改时；old_string 必须与文件内容逐字符一致且唯一，改前先 read_file 确认原文，改完看替换结果反馈确认生效（未注册表示未启用）。\n- glob：按文件名模式找文件（支持 ** 跨目录），如 **/*.go（未注册表示未启用）。\n- grep：按正则在文件内容里搜索（如找函数定义、引用位置），配合 include 过滤文件类型（未注册表示未启用）。\n- 编码任务流程：glob/grep/read_file 定位 → edit_file/write_file 修改 → bash 编译/测试验证，根据报错继续修复，直到通过再向用户汇报。\n- file：用户明确要求读取宿主机文件并发送时（未注册表示未启用）。\n- local_image：用户明确要求查看宿主机某张本地图片并给出路径时（未注册表示未启用）。\n- screenshot / mouse_click / mousemove / mouse_scroll / keyboard_type / keyboard_press / active_window / list_windows：用户明确要求查看或操作宿主机屏幕/界面时；先 screenshot 看到画面，再按图中坐标操作，操作后可再次 screenshot 确认效果（未注册表示未启用）。\n- clock_create/list/update/delete/log：用户要求定时提醒、周期任务、管理任务或查看执行记录时。\n- subagent_run/list/cancel：独立、耗时、多步骤且不依赖当前上下文的子任务（如深度调研、多轮搜索总结）适合委派子代理；简单问题不要委派。\n- team_run/create/list/delete：需要多视角并行处理、交叉验证或复杂分工时。\n- todo_write：需要 3 步以上的复杂任务开始时建立任务清单，逐项推进并及时更新状态（未注册表示未启用）。\n- config_get/config_set：仅当用户明确要求查看或修改 Bot 框架配置时；修改操作需要管理员审批（系统会私聊通知管理员确认），审批通过后才会写入；修改后提醒用户重启才能生效，重启需由管理员发送 /reboot 命令（普通用户无权限），你不要自己尝试重启。\n- config_file_get/config_file_set：查看或修改扩展配置（MCP 服务器、Prompt 覆盖、AI 钩子、自定义命令的 JSON 文件）时；config_file_set 同样需要管理员审批；hooks/commands/prompt 保存后数秒生效，mcp 重启后生效。\n- mcp_list/add/remove/reconnect：用户明确要求管理 MCP 服务器时。\n- skill_list/install/remove：用户明确要求查看、安装或卸载技能时。\n- MCP 懒加载工具：先 mcp_discover_<服务器> 看工具，再 mcp_load_<服务器> 加载需要的工具，最后调用具体工具。\n\n## 遇到这些情况怎么办\n- 普通闲聊或你已有可靠知识：直接回答，不调用工具。\n- 信息不足：先历史消息工具 / memory_search / kb_search 补齐；仍不足再问用户。\n- 需要最新资料或外部事实：先 webSearch，搜索结果不够再用 webExplore 打开具体链接，交叉确认后再回答。\n- 图片或文件：只有必须看图才 load_images，并通过 hashes 传入图片哈希（历史消息/合并转发里的图片哈希同样可加载）；私聊文件无链接用 get_private_file_url；本地文件/图片只在用户明确要求时用 file / local_image。\n- 定时任务：先确认 cron 含义、目标、内容、单次或重复，再 clock_create；完成后简短确认已生效。\n- 长期记忆/知识库：重要且明确的用户偏好或约定才保存；问过去的事先检索；删除必须用户明确要求。\n- 复杂任务：适合后台执行的用 subagent_run，完成后结果会回到当前会话；需要多视角/交叉验证时用 team_run。\n- 计划模式：用户开启 /plan 后只做分析与规划并输出实施计划，不调用会产生副作用的工具（修改文件、运行命令、改配置等会被系统阻止）；等用户退出计划模式再执行。\n- 执行命令：先确认命令含义和影响，不做删除数据、格式化、重启系统等危险操作。\n- 工具报错：调整参数或换说法重试一次；持续失败就如实回复，并说明可能的解决办法（如未启用、需要 token、参数不正确）。"`
	Thinking thinkingConfig `cfg:"plugin.ai_chat_bot.thinking"`
	Search   searchConfig   `cfg:"plugin.ai_chat_bot.search"`

	PromptCache promptCacheConfig `cfg:"plugin.ai_chat_bot.prompt_cache"`
	Retry       retryConfig       `cfg:"plugin.ai_chat_bot.retry"`
	Fallback    fallbackConfig    `cfg:"plugin.ai_chat_bot.fallback"`
	Compressor  compressorConfig  `cfg:"plugin.ai_chat_bot.compressor"`

	SkillsDir   string               `cfg:"plugin.ai_chat_bot.skills_dir" label:"Skills 目录" group:"AI 对话 · 工具" default:"./skills"`
	Skills      []string             `cfg:"plugin.ai_chat_bot.skills" label:"Skills 白名单" group:"AI 对话 · 工具" help:"为空则加载全部，每行一个"`
	Bash        bashToolConfig       `cfg:"plugin.ai_chat_bot.bash"`
	File        fileToolConfig       `cfg:"plugin.ai_chat_bot.file"`
	LocalImage  localImageToolConfig `cfg:"plugin.ai_chat_bot.local_image"`
	FileTools   fileToolsConfig      `cfg:"plugin.ai_chat_bot.file_tools"`
	ComputerUse computerUseConfig    `cfg:"plugin.ai_chat_bot.computer_use"`
	ConfigTool  configToolConfig     `cfg:"plugin.ai_chat_bot.config_tool"`
	SkillTool   skillToolConfig      `cfg:"plugin.ai_chat_bot.skill_tool"`
	Todo        todoConfig           `cfg:"plugin.ai_chat_bot.todo"`
	MCP         mcpConfig            `cfg:"plugin.ai_chat_bot.mcp"`
	MCPTool     mcpToolConfig        `cfg:"plugin.ai_chat_bot.mcp_tool"`

	OCR ocrConfig `cfg:"plugin.ai_chat_bot.ocr"`

	Clock    clockConfig    `cfg:"plugin.ai_chat_bot.clock"`
	Memory   memoryConfig   `cfg:"plugin.ai_chat_bot.memory"`
	Kb       kbConfig       `cfg:"plugin.ai_chat_bot.kb"`
	Subagent subagentConfig `cfg:"plugin.ai_chat_bot.subagent"`
	Team     teamConfig     `cfg:"plugin.ai_chat_bot.team"`
	QueryLog queryLogConfig `cfg:"plugin.ai_chat_bot.query_log"`

	Stream   streamConfig   `cfg:"plugin.ai_chat_bot.stream"`
	Quota    quotaConfig    `cfg:"plugin.ai_chat_bot.quota"`
	Session  sessionConfig  `cfg:"plugin.ai_chat_bot.session"`
	Hooks    hooksConfig    `cfg:"plugin.ai_chat_bot.hooks"`
	Approval approvalConfig `cfg:"plugin.ai_chat_bot.approval"`
}

// ConfigSchema 实现 plugin.ConfigSchemaProvider：返回配置结构体指针，
// 框架在 Start 前自动完成字段注册、默认值补齐与配置填充。
func (p *AIChatPlugin) ConfigSchema() any {
	return &p.cfg
}
