package pluginaichat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// skill 管理工具：让 AI 无需后台面板即可自行安装/卸载/查看技能。
// 由 plugin.ai_chat_bot.skill_tool.enable 门控（默认关闭），安装/卸载走
// 面板同款磁盘逻辑（skillpanel.go），落盘后热重载立即生效。

const (
	// skillDownloadTimeout 远程 skill 下载超时
	skillDownloadTimeout = 2 * time.Minute
	// skillDownloadMaxBytes 远程 skill 压缩包/文件体积上限（与面板解压上限一致）
	skillDownloadMaxBytes = 64 << 20 // 64 MiB
)

// skillToolBase 为 skill 管理工具共享插件引用。
type skillToolBase struct {
	plugin *AIChatPlugin
}

// newSkillTools 创建 skill 管理工具（skill_list / skill_install / skill_remove），
// 注册到会话执行器中（主会话与子代理一致），仅在配置开启时被 registerScopedTools 调用。
// 找技能资源不单独做搜索工具：让 AI 用内置 web 技能联网搜索
// （GitHub API 搜索有频控，通用网页搜索更稳），再把找到的链接交给 skill_install。
func newSkillTools(p *AIChatPlugin) []llmtool.Tool {
	base := skillToolBase{plugin: p}
	return []llmtool.Tool{
		&skillListTool{
			BaseTool:      llmtool.MakeBaseTool("skill_list", "列出当前已安装且启用的技能（skill）。当用户提到『技能』『skill』、或你需要确认是否已有某项技能时，先调用本工具；若没有用户需要的技能，可先 skill_read 读取内置 web 技能并联网搜索技能资源（如 GitHub 上的 skill 仓库、zip 直链），或请用户提供 zip 链接，或用 skill_install 的 content 参数自行编写技能后安装", skillListParams{}),
			skillToolBase: base,
		},
		&skillInstallTool{
			BaseTool:      llmtool.MakeBaseTool("skill_install", "安装新技能（skill），安装后立即生效、后续对话即可使用。两种方式：1) url：zip 压缩包直链或 GitHub 仓库地址（自动转换为 codeload zip 下载；单个 SKILL.md / .md 文件直链按内容安装）——用户未提供链接时，可先用内置 web 技能搜索技能仓库或 zip 直链，再用找到的链接安装；2) content：直接撰写 SKILL.md 全文（需含 --- frontmatter，name/description 必填）创建技能。内置技能不可覆盖。查看已装技能用 skill_list，卸载用 skill_remove。仅在用户明确要求安装技能时使用", skillInstallParams{}),
			skillToolBase: base,
		},
		&skillRemoveTool{
			BaseTool:      llmtool.MakeBaseTool("skill_remove", "卸载指定名称的技能（skill），name 用 skill_list 查看；内置技能不能卸载。仅在用户明确要求删除技能时使用", skillRemoveParams{}),
			skillToolBase: base,
		},
	}
}

// ---- skill_list ----

type skillListParams struct{}

type skillListTool struct {
	llmtool.BaseTool[skillListParams]
	skillToolBase
}

func (t *skillListTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	all, _, _ := t.plugin.SkillList()
	infos := make([]plugininfo.SkillInfo, 0, len(all))
	for _, info := range all {
		// 被禁用的 skill 描述不进入上下文（面板「技能管理」页可重新启用）
		if info.Disabled {
			continue
		}
		infos = append(infos, info)
	}
	if len(infos) == 0 {
		return "当前没有已启用的 skill。若用户要求你掌握某项新能力，可先 skill_read 内置 web 技能并联网搜索技能资源（如 GitHub skill 仓库、zip 直链）后用 skill_install 安装，也可由你调用 skill_install 的 content 参数自行编写技能。", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "已启用的 skill（共 %d 个）：\n", len(infos))
	for _, info := range infos {
		fmt.Fprintf(&sb, "- %s", info.Name)
		if info.Builtin {
			sb.WriteString("（内置）")
		}
		fmt.Fprintf(&sb, "：%s", info.Description)
		if info.Location != "" {
			fmt.Fprintf(&sb, "（位置 %s", info.Location)
			if len(info.Refs) > 0 {
				fmt.Fprintf(&sb, "，附属文档 %d 个", len(info.Refs))
			}
			if len(info.Extras) > 0 {
				fmt.Fprintf(&sb, "，脚本/文件 %d 个", len(info.Extras))
			}
			sb.WriteString("）")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// ---- skill_install ----

type skillInstallParams struct {
	URL     string `json:"url,omitempty" desc:"skill 压缩包（zip）直链，或 GitHub 仓库地址（自动转换下载）；也可填单个 SKILL.md / .md 文件直链。与 content 二选一"`
	Content string `json:"content,omitempty" desc:"直接撰写 SKILL.md 全文（需含 --- frontmatter，name/description 必填）来创建技能；与 url 二选一"`
	Name    string `json:"name,omitempty" desc:"技能目录名。content 方式推荐填写（留空则用 frontmatter 的 name）；url 方式可选（默认取 zip 结构/文件名）"`
}

type skillInstallTool struct {
	llmtool.BaseTool[skillInstallParams]
	skillToolBase
}

func (t *skillInstallTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	paramsP := params.(*skillInstallParams)
	urlStr := strings.TrimSpace(paramsP.URL)
	content := strings.TrimSpace(paramsP.Content)
	name := strings.TrimSpace(paramsP.Name)

	if urlStr == "" && content == "" {
		return "", fmt.Errorf("必须提供 url（zip 链接 / GitHub 仓库 / SKILL.md 直链）或 content（SKILL.md 全文）之一")
	}
	if urlStr != "" && content != "" {
		return "", fmt.Errorf("url 与 content 只能提供其一")
	}

	var (
		info plugininfo.SkillInfo
		err  error
	)
	if urlStr != "" {
		info, err = t.plugin.installSkillFromURL(ctx, urlStr, name)
	} else {
		info, err = t.plugin.SkillInstallFromContent(name, content)
	}
	if err != nil {
		return "", err
	}

	oplog.Record(oplog.CategoryAI, "skill_install", fmt.Sprintf("AI 安装 skill %s（%s）", info.Name, info.Description))
	t.plugin.Logger.Info("AI 安装 skill", "skill", info.Name, "location", info.Location)
	return fmt.Sprintf("技能「%s」已安装并立即生效：%s（位置 %s）。后续对话即可按该技能执行任务。", info.Name, info.Description, info.Location), nil
}

// ---- skill_remove ----

type skillRemoveParams struct {
	Name string `json:"name" desc:"要卸载的 skill 名称（skill_list 可查看）"`
}

type skillRemoveTool struct {
	llmtool.BaseTool[skillRemoveParams]
	skillToolBase
}

func (t *skillRemoveTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*skillRemoveParams)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", fmt.Errorf("name 不能为空")
	}
	if err := t.plugin.SkillDelete(name); err != nil {
		return "", err
	}
	oplog.Record(oplog.CategoryAI, "skill_remove", fmt.Sprintf("AI 卸载 skill %s", name))
	t.plugin.Logger.Info("AI 卸载 skill", "skill", name)
	return fmt.Sprintf("技能「%s」已卸载", name), nil
}

// ---- 远程安装 ----

// installSkillFromURL 下载并安装远程 skill。
// 支持：GitHub 仓库地址（自动转 codeload zip）、zip 直链、单个 SKILL.md/.md 直链。
func (p *AIChatPlugin) installSkillFromURL(ctx context.Context, rawURL, name string) (plugininfo.SkillInfo, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return plugininfo.SkillInfo{}, fmt.Errorf("仅支持 http/https 链接")
	}

	// GitHub 仓库地址（/owner/repo）：转 codeload zip，依次尝试 main/master 默认分支
	if parts := githubRepoPath(u); len(parts) == 2 {
		owner, repo := parts[0], parts[1]
		var data []byte
		var zipURL string
		for _, cand := range []string{
			fmt.Sprintf("https://codeload.github.com/%s/%s/zip/refs/heads/main", owner, repo),
			fmt.Sprintf("https://codeload.github.com/%s/%s/zip/refs/heads/master", owner, repo),
		} {
			d, err := p.fetchSkill(ctx, cand)
			if err != nil {
				continue
			}
			data, zipURL = d, cand
			break
		}
		if data == nil {
			return plugininfo.SkillInfo{}, fmt.Errorf("GitHub 仓库下载失败（main/master 分支均不可用）")
		}
		// codeload 文件名形如 owner-repo-main.zip：根 SKILL.md 形式时以此作为目录名
		return p.installSkillZip(repo+"-"+path.Base(zipURL)+".zip", data, name)
	}

	data, err := p.fetchSkill(ctx, rawURL)
	if err != nil {
		return plugininfo.SkillInfo{}, err
	}

	// 单个 markdown 直链：按内容安装
	lowerPath := strings.ToLower(u.Path)
	if strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".markdown") {
		return p.SkillInstallFromContent(name, string(data))
	}

	filename := path.Base(u.Path)
	if filename == "" || filename == "." || filename == "/" {
		filename = "skill.zip"
	}
	return p.installSkillZip(filename, data, name)
}

// githubRepoPath 返回 GitHub 仓库地址的 /owner/repo 段；非 GitHub 仓库地址返回 nil。
func githubRepoPath(u *url.URL) []string {
	host := strings.ToLower(u.Host)
	if host != "github.com" && host != "www.github.com" {
		return nil
	}
	parts := splitURLPath(u.Path)
	if len(parts) != 2 {
		return nil
	}
	return parts
}

// splitURLPath 清理并拆分 URL 路径（去首尾斜杠与空段）。
func splitURLPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// fetchSkill 下载 URL 内容（http/https），限制体积后返回原始字节。
func (p *AIChatPlugin) fetchSkill(ctx context.Context, rawURL string) ([]byte, error) {
	client := resty.New().SetTimeout(skillDownloadTimeout)
	resp, err := client.R().SetContext(ctx).Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("下载失败: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode())
	}
	if resp.Size() > skillDownloadMaxBytes {
		return nil, fmt.Errorf("下载内容过大（上限 %d MB）", skillDownloadMaxBytes>>20)
	}
	return resp.Body(), nil
}
