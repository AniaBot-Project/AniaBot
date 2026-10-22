package functool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

var errToolExecute = errors.New("function tool执行错误")

// FileConfig file 工具配置
type FileConfig struct {
	// Enable 是否启用本地文件发送工具。默认关闭：该工具可读取宿主机上任意可读
	// 文件并发送给用户，LLM（含被注入提示词的情形）可能借此泄露敏感文件，
	// 故需显式开启（与 bash 工具一致采用 opt-in）。
	Enable bool `json:"enable" mapstructure:"enable"`
}

// SendFileTool 读取本地文件并发送
type SendFileParams struct {
	Name string `json:"name" desc:"要发送的文件名(包括后缀),用于显示,如 1.txt"`
	Path string `json:"path" desc:"本地文件完整路径,如 /tmp/1.txt"`
}

type SendFileTool struct {
	llmtool.BaseTool[SendFileParams]
}

func NewSendFileTool() *SendFileTool {
	return &SendFileTool{
		BaseTool: llmtool.MakeBaseTool("file", "用于读取本地文件并发送给用户", SendFileParams{}),
	}
}

// sendFileMaxBytes 单次发送文件的体积上限：base64 编码后峰值内存约为文件的
// 2.33 倍，且内容要经回调进入平台上传链路，必须有上限。
const sendFileMaxBytes = 64 << 20

func (t *SendFileTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*SendFileParams)
	log.Println("执行file... 参数:", p)

	// 后台一次性会话（定时任务 / 主动互动）可能未注入该回调：这里优雅报错。
	// 不做判空的话，nil 函数调用只能等工具层 recover 兜住，模型收到的会是一个
	// 没有任何指导意义的运行时错误。
	if callbacks.SendFile == nil {
		return "当前会话不支持发送文件", errToolExecute
	}

	if SensitiveHostPath(p.Path) {
		return "禁止发送aniabot数据库文件", errToolExecute
	}

	// 体积上限：内容要经 base64 编码（峰值内存 ≈ 文件大小 ×2.33）后走回调/上传
	// 链路，GB 级文件会直接打爆进程内存。先 Stat 再读，避免"先读进内存再判断"。
	info, err := os.Stat(p.Path)
	if err != nil {
		return fmt.Sprintf("读取文件失败: %v", err), errToolExecute
	}
	if info.IsDir() {
		return "目标是目录，无法作为文件发送", errToolExecute
	}
	if info.Size() > sendFileMaxBytes {
		return fmt.Sprintf("文件过大（%.1fMB，上限 %.1fMB），未发送",
			float64(info.Size())/(1<<20), float64(sendFileMaxBytes)/(1<<20)), errToolExecute
	}

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return fmt.Sprintf("读取文件失败: %v", err), errToolExecute
	}
	if len(data) > sendFileMaxBytes {
		// Stat 与实际读取之间文件可能被写大，读后再兜一次
		return fmt.Sprintf("文件过大（%.1fMB，上限 %.1fMB），未发送",
			float64(len(data))/(1<<20), float64(sendFileMaxBytes)/(1<<20)), errToolExecute
	}
	_, err = callbacks.SendFile(p.Name, base64.StdEncoding.EncodeToString(data))
	if err != nil {
		// 原样返回回调错误：其中含「上传超时、内容可能稍后送达、请勿重复发送」
		// 等对模型有指导意义的信息，避免模型自动重发导致用户收到重复文件
		return "", err
	}
	return "发送成功", nil
}
