package luckylilia

import (
	"log/slog"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/spf13/viper"
)

// NewAdapter 按配置 bot.luckylilia.mode 创建适配器：ws（默认）或 http。
// 无法识别的取值回退为 ws 并记录警告。超时在构造时读取（Serve 启动前插件即可
// 能调用发送接口），配置修改需重启生效。
func NewAdapter(cfg *viper.Viper) adapter.Adapter {
	reqTimeout := resolveTimeoutSec(cfg, "bot.luckylilia.request_timeout_sec", defaultRequestTimeout)
	uploadTimeout := resolveTimeoutSec(cfg, "bot.luckylilia.upload_timeout_sec", defaultUploadTimeout)
	switch cfg.GetString("bot.luckylilia.mode") {
	case "http":
		slog.Info("使用 HTTP 适配器连接 Luckylilia(LLBot)")
		return newLuckyliliaHttpAdapter(reqTimeout, uploadTimeout)
	case "ws", "":
		slog.Info("使用 WebSocket 适配器连接 Luckylilia(LLBot)")
		return newLuckyliliaWebSocketAdapter(reqTimeout, uploadTimeout)
	default:
		slog.Warn("未知的适配器模式，回退为 WebSocket", "mode", cfg.GetString("bot.luckylilia.mode"))
		return newLuckyliliaWebSocketAdapter(reqTimeout, uploadTimeout)
	}
}

// NewLuckyliliaHttpAdapter 以默认超时创建 HTTP 适配器（测试与直接构造用；
// 正式路径经 NewAdapter 读取面板配置）。
func NewLuckyliliaHttpAdapter() adapter.Adapter {
	return newLuckyliliaHttpAdapter(defaultRequestTimeout, defaultUploadTimeout)
}

func newLuckyliliaHttpAdapter(reqTimeout, uploadTimeout time.Duration) adapter.Adapter {
	return &luckyliliaHttpAdapter{
		// httpClient 必须在构造时就绪（与 ws 适配器预建 ackMng 同理）：
		// 首次启动等待设置向导时 Serve 尚未运行，插件仍可能调用发送接口，
		// 此时应优雅失败（请求出错）而非 nil 指针 panic。
		httpClient:    resty.New(),
		reqTimeout:    reqTimeout,
		uploadTimeout: uploadTimeout,
	}
}

// NewLuckyliliaWebSocketAdapter 以默认超时创建 WebSocket 适配器（测试与直接构造用；
// 正式路径经 NewAdapter 读取面板配置）。
func NewLuckyliliaWebSocketAdapter() adapter.Adapter {
	return newLuckyliliaWebSocketAdapter(defaultRequestTimeout, defaultUploadTimeout)
}

func newLuckyliliaWebSocketAdapter(reqTimeout, uploadTimeout time.Duration) adapter.Adapter {
	return &luckyliliaWebSocketAdapter{
		// ackMng 必须在构造时就绪：首次启动等待设置向导时 Serve 尚未运行，
		// 插件（如系统插件 Awake 通知）仍可能调用发送接口，此时应优雅失败而非 panic。
		ackMng:        &ackManager{timeout: reqTimeout},
		uploadTimeout: uploadTimeout,
	}
}
