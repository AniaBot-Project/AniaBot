package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// kbImportTimeout URL 导入抓取超时（Jina Reader）。
const kbImportTimeout = 60 * time.Second

// 知识库面板管理接口（实现 adminpanel.KnowledgeBaseSource）。
// 改动直接落盘，AI 工具每次调用实时读写存储，因此无需重启即生效。

// kbScopePattern 合法的知识库作用域：global / g:会话ID / f:用户ID（其他平台带前缀，如 g:fs:oc_xxx）。
// 面板传入的 scope 必须严格匹配，防止越权读写 kb: 命名空间下的任意键。
var kbScopePattern = regexp.MustCompile(`^(global|[gf]:.+)$`)

func validKbScope(scope string) bool {
	return kbScopePattern.MatchString(scope)
}

// errKnowledgeDisabled 知识库功能未启用（plugin.ai_chat_bot.kb.enable=false）时的统一错误。
var errKnowledgeDisabled = errors.New("知识库功能未启用")

// KnowledgeScopes 返回已有知识库的作用域列表及各自文档条数（供 Web 面板展示）。
func (p *AIChatPlugin) KnowledgeScopes() []plugininfo.KnowledgeScopeInfo {
	if p.knowledgeManager == nil {
		return nil
	}
	scopes := p.knowledgeManager.scopes()
	infos := make([]plugininfo.KnowledgeScopeInfo, 0, len(scopes))
	for _, scope := range scopes {
		infos = append(infos, plugininfo.KnowledgeScopeInfo{
			Scope: scope,
			Kind:  scopeKind(scope),
			Count: p.knowledgeManager.count(scope),
		})
	}
	return infos
}

// scopeKind 返回作用域种类：global / group / friend。
func scopeKind(scope string) string {
	switch {
	case scope == kbScopeGlobal:
		return "global"
	case strings.HasPrefix(scope, "g:"):
		return "group"
	case strings.HasPrefix(scope, "f:"):
		return "friend"
	default:
		return "unknown"
	}
}

// KnowledgeList 返回指定 scope 的全部文档，按时间倒序（新文档在前）。
func (p *AIChatPlugin) KnowledgeList(scope string) ([]plugininfo.KnowledgeDocInfo, error) {
	if p.knowledgeManager == nil {
		return nil, errKnowledgeDisabled
	}
	if !validKbScope(scope) {
		return nil, fmt.Errorf("非法的知识库作用域: %s", scope)
	}
	docs := p.knowledgeManager.listMeta(scope)
	infos := make([]plugininfo.KnowledgeDocInfo, 0, len(docs))
	for i := len(docs) - 1; i >= 0; i-- {
		d := docs[i]
		infos = append(infos, plugininfo.KnowledgeDocInfo{
			ID:        d.ID,
			Scope:     d.Scope,
			Title:     d.Title,
			Content:   d.Content,
			Tags:      d.Tags,
			Source:    d.Source,
			CreatedAt: d.CreatedAt,
		})
	}
	return infos, nil
}

// KnowledgeCreate 在指定 scope 下新增一条文档，返回生成的 ID。
func (p *AIChatPlugin) KnowledgeCreate(up plugininfo.KnowledgeDocUpsert) (string, error) {
	if p.knowledgeManager == nil {
		return "", errKnowledgeDisabled
	}
	if !validKbScope(up.Scope) {
		return "", fmt.Errorf("非法的知识库作用域: %s", up.Scope)
	}
	doc, err := p.knowledgeManager.add(up.Scope, up.Title, up.Content, up.Tags, up.Source)
	if err != nil {
		return "", err
	}
	p.Logger.Info("知识库文档已通过 Web 面板新增", "scope", up.Scope, "id", doc.ID)
	return doc.ID, nil
}

// KnowledgeUpdate 按 ID 更新一条文档的标题、内容、标签与来源。
func (p *AIChatPlugin) KnowledgeUpdate(up plugininfo.KnowledgeDocUpsert) error {
	if p.knowledgeManager == nil {
		return errKnowledgeDisabled
	}
	if !validKbScope(up.Scope) {
		return fmt.Errorf("非法的知识库作用域: %s", up.Scope)
	}
	if strings.TrimSpace(up.ID) == "" {
		return fmt.Errorf("id 不能为空")
	}
	if err := p.knowledgeManager.update(up.Scope, up.ID, up.Title, up.Content, up.Tags, up.Source); err != nil {
		return err
	}
	p.Logger.Info("知识库文档已通过 Web 面板更新", "scope", up.Scope, "id", up.ID)
	return nil
}

// KnowledgeDelete 按 ID 删除指定 scope 中的一条文档。
func (p *AIChatPlugin) KnowledgeDelete(scope, id string) error {
	if p.knowledgeManager == nil {
		return errKnowledgeDisabled
	}
	if !validKbScope(scope) {
		return fmt.Errorf("非法的知识库作用域: %s", scope)
	}
	if !p.knowledgeManager.remove(scope, id) {
		return fmt.Errorf("文档不存在: %s", id)
	}
	p.Logger.Info("知识库文档已通过 Web 面板删除", "scope", scope, "id", id)
	return nil
}

// KnowledgeImportURL 抓取网页正文导入知识库。复用 Jina Reader API
// （与 webExplore 工具同一端点），抓取结果截断后入库。
// 标题取抓取文本中的一级 Markdown 标题，否则用 URL。
func (p *AIChatPlugin) KnowledgeImportURL(scope, targetURL string) (string, error) {
	if p.knowledgeManager == nil {
		return "", errKnowledgeDisabled
	}
	if !validKbScope(scope) {
		return "", fmt.Errorf("非法的知识库作用域: %s", scope)
	}
	targetURL = strings.TrimSpace(targetURL)
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		return "", fmt.Errorf("URL 必须以 http:// 或 https:// 开头")
	}

	// 带请求超时：Jina 挂起时不能让面板 API 永久悬挂
	client := resty.New().SetTimeout(kbImportTimeout)
	resp, err := client.R().
		SetContext(context.Background()).
		SetHeader("Authorization", "Bearer "+p.cfg.Search.Token).
		SetHeader("X-Retain-Images", "none").
		SetHeader("X-With-Links-Summary", "true").
		SetHeader("X-Engine", "cf-browser-rendering").
		Get("https://r.jina.ai/" + targetURL)
	if err != nil {
		return "", fmt.Errorf("抓取网页失败: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		// 402（无配额）/404/5xx 返回的是错误页而非正文，不能入库
		return "", fmt.Errorf("抓取网页失败: Jina 返回 HTTP %d（请检查 URL 是否有效及搜索 token 配额）", resp.StatusCode())
	}
	text := resp.String()
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("抓取到的网页内容为空")
	}

	title, content := extractKbPageTitle(text)
	doc, err := p.knowledgeManager.add(scope, title, content, nil, "url:"+targetURL)
	if err != nil {
		return "", err
	}
	p.Logger.Info("知识库已通过 Web 面板导入 URL", "scope", scope, "id", doc.ID, "url", targetURL)
	return doc.ID, nil
}

// extractKbPageTitle 从抓取文本提取标题与正文。
// Jina Reader 返回 Markdown，首个 `# ` 一级标题作为文档标题；
// 无标题时标题为空（管理器允许），正文为抓取文本本身。
func extractKbPageTitle(text string) (string, string) {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "# "); ok {
			t := strings.TrimSpace(after)
			if t != "" {
				return t, text
			}
		}
	}
	return "", text
}
