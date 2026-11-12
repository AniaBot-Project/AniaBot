package core

import (
	"github.com/jeanhua/AniaBot/bot/adminpanel"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
)

// frameworkConfigFields 框架自身的配置字段元信息（bot.*）。
// 平台适配器字段由各适配器包通过 adapter.Register 的 ConfigFields 声明
// （含 bot.platform.<name>.enable 启用开关），面板动态展示。
var frameworkConfigFields = []pluginconfig.Field{
	// Bot 基础
	{Key: "bot.admin_ids", Label: "管理员 ID（可多个）", Type: "strings", Group: "Bot 基础", Help: "管理员 ID 列表，每行一个，可配置多个；任一管理员均拥有最高权限（/reboot、/exit、查看全部定时任务、接收启动/异常通知、审批 AI 配置修改等）。QQ 为 qq:QQ号（如 qq:123456789）；其他平台为带平台前缀的 ID（如飞书 fs:ou_xxx、Telegram tg:123456、Discord dc:123456）"},
	{Key: "bot.msg_event_timeout_sec", Label: "消息处理超时(秒)", Type: "int", Group: "Bot 基础", Help: "单条消息事件（如一次 AI 回复）的最大执行时长，超时强制中止；AI 执行复杂任务（多轮工具调用/子代理）被超时中断时调大，0 或负数回退默认 300 秒", Default: 300},

	// Web 面板
	{Key: "bot.admin_panel.enable", Label: "启用面板", Type: "bool", Group: "Web 面板", Help: "是否启用 Web 控制面板；关闭后可用环境变量 ANIA_BOT_ADMIN_PANEL_ENABLE=true 覆盖重新开启", Default: true},
	{Key: "bot.admin_panel.listen", Label: "监听地址", Type: "string", Group: "Web 面板", Help: "如 127.0.0.1:7700；改为 0.0.0.0:7700 可局域网访问（面板有密码保护）", Default: "127.0.0.1:7700"},
	{Key: "bot.admin_panel.media_direct", Label: "消息图片直连", Type: "bool", Group: "Web 面板", Help: "消息日志里的图片优先由浏览器直连平台 CDN，失败时自动回退面板代理（省服务器带宽）；关闭后全部经面板代理转发。改动后刷新面板生效", Default: true},

	// 缓存存储
	{Key: "bot.store.cache.driver", Label: "缓存驱动", Type: "select", Options: []string{"memory", "redis"}, Group: "缓存存储", Help: "memory（进程内内存，重启清空）或 redis（重启日志不丢失，建议使用）", Default: "memory"},
	{Key: "bot.store.cache.redis.address", Label: "Redis 地址", Type: "string", Group: "缓存存储", Default: "localhost:6379"},
	{Key: "bot.store.cache.redis.password", Label: "Redis 密码", Type: "password", Group: "缓存存储", Sensitive: true},
	{Key: "bot.store.cache.redis.db", Label: "Redis DB", Type: "int", Group: "缓存存储", Default: 0},

	// 自动更新
	{Key: "bot.update.source_dir", Label: "源码目录", Type: "string", Group: "自动更新", Help: "AniaBot 仓库的克隆路径（独立目录），自动更新在此拉取并编译；留空则禁用自动更新"},
	{Key: "bot.update.git_url", Label: "Git 地址", Type: "string", Group: "自动更新", Help: "非空时更新前覆盖源码目录的 origin 地址"},
	{Key: "bot.update.branch", Label: "跟踪分支", Type: "string", Group: "自动更新", Default: "main"},

	// API 余额查询（面板概览页展示，修改后即时生效，无需重启）
	{Key: "bot.balance.enable", Label: "启用余额查询", Type: "bool", Group: "API 余额查询", Help: "在面板概览页显示 AI API 余额；默认适配 DeepSeek 风格接口，其他厂商可按下方配置调整", Default: false},
	{Key: "bot.balance.cache_sec", Label: "缓存时间(秒)", Type: "int", Group: "API 余额查询", Help: "余额查询结果的缓存时长，避免频繁请求余额接口", Default: 300},
	{Key: "bot.balance.url", Label: "请求地址", Type: "string", Group: "API 余额查询", Help: "支持 ${base_url} ${api_key} ${model} 占位符（取自 AI 对话插件的 API 配置）", Default: adminpanel.DefaultBalanceURL},
	{Key: "bot.balance.method", Label: "请求方法", Type: "select", Options: []string{"GET", "POST"}, Group: "API 余额查询", Default: "GET"},
	{Key: "bot.balance.headers", Label: "请求头(JSON)", Type: "text", Group: "API 余额查询", Help: "JSON 对象，值中支持 ${base_url} ${api_key} ${model} 占位符", Default: adminpanel.DefaultBalanceHeaders},
	{Key: "bot.balance.body", Label: "请求体", Type: "text", Group: "API 余额查询", Help: "可选，POST 请求时填写；支持 ${base_url} ${api_key} ${model} 占位符；留空则不发送请求体"},
	{Key: "bot.balance.format", Label: "显示模板", Type: "string", Group: "API 余额查询", Help: "余额显示文本，{路径} 会被替换为响应 JSON 中对应 gjson 路径的值，如 ¥ {data.balances.0.total_balance}", Default: adminpanel.DefaultBalanceFormat},
}
