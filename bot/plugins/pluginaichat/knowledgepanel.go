package pluginaichat

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

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

// KnowledgeDeleteMany 批量删除指定 scope 中的多篇文档，返回实际删除条数。
// 与单条删除不同，不存在的 ID 直接忽略（面板多选后可能含并发删除的文档）。
func (p *AIChatPlugin) KnowledgeDeleteMany(scope string, ids []string) (int, error) {
	if p.knowledgeManager == nil {
		return 0, errKnowledgeDisabled
	}
	if !validKbScope(scope) {
		return 0, fmt.Errorf("非法的知识库作用域: %s", scope)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("id 列表不能为空")
	}
	deleted := p.knowledgeManager.removeMany(scope, ids)
	p.Logger.Info("知识库文档已通过 Web 面板批量删除", "scope", scope, "requested", len(ids), "deleted", deleted)
	return deleted, nil
}
