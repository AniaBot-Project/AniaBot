package pluginaichat

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 记忆面板管理接口（实现 adminpanel.MemorySource）。
// 改动直接落盘，AI 工具每次调用实时读写存储，因此无需重启即生效。

// scopePattern 合法的会话 scope：g:会话ID / f:用户ID（QQ 为 g:qq:数字，其他平台带前缀，如 g:fs:oc_xxx）。
// 面板传入的 scope 必须严格匹配，防止越权读写 memory: 命名空间下的任意键。
var scopePattern = regexp.MustCompile(`^[gf]:.+$`)

func validScope(scope string) bool {
	return scopePattern.MatchString(scope)
}

// errMemoryDisabled 记忆功能未启用（plugin.ai_chat_bot.memory.enable=false）时的统一错误。
var errMemoryDisabled = fmt.Errorf("记忆功能未启用")

// MemoryScopes 返回已有记忆的会话 scope 列表及各自条数（供 Web 面板展示）。
func (p *AIChatPlugin) MemoryScopes() []plugininfo.MemoryScopeInfo {
	if p.memoryManager == nil {
		return nil
	}
	scopes := p.memoryManager.scopes()
	infos := make([]plugininfo.MemoryScopeInfo, 0, len(scopes))
	for _, scope := range scopes {
		info := plugininfo.MemoryScopeInfo{
			Scope: scope,
			Count: len(p.memoryManager.list(scope)),
		}
		if kind, target, ok := strings.Cut(scope, ":"); ok {
			info.Target = target
			if kind == "g" {
				info.Kind = "group"
			} else {
				info.Kind = "friend"
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// MemoryList 返回指定 scope 的全部记忆，按时间倒序（新记忆在前）。
func (p *AIChatPlugin) MemoryList(scope string) ([]plugininfo.MemoryEntryInfo, error) {
	if p.memoryManager == nil {
		return nil, errMemoryDisabled
	}
	if !validScope(scope) {
		return nil, fmt.Errorf("非法的会话 scope: %s", scope)
	}
	entries := p.memoryManager.list(scope)
	infos := make([]plugininfo.MemoryEntryInfo, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		infos = append(infos, plugininfo.MemoryEntryInfo{
			ID:        e.ID,
			UserID:    e.UserID,
			Content:   e.Content,
			Tags:      e.Tags,
			CreatedAt: e.CreatedAt,
		})
	}
	return infos, nil
}

// MemoryCreate 在指定 scope 下新增一条记忆，返回生成的 ID。
func (p *AIChatPlugin) MemoryCreate(up plugininfo.MemoryEntryUpsert) (string, error) {
	if p.memoryManager == nil {
		return "", errMemoryDisabled
	}
	if !validScope(up.Scope) {
		return "", fmt.Errorf("非法的会话 scope: %s", up.Scope)
	}
	entry, err := p.memoryManager.add(up.Scope, up.UserID, up.Content, up.Tags)
	if err != nil {
		return "", err
	}
	p.Logger.Info("记忆已通过 Web 面板新增", "scope", up.Scope, "id", entry.ID)
	return entry.ID, nil
}

// MemoryUpdate 按 ID 更新一条记忆的内容、关联用户 ID 与标签。
func (p *AIChatPlugin) MemoryUpdate(up plugininfo.MemoryEntryUpsert) error {
	if p.memoryManager == nil {
		return errMemoryDisabled
	}
	if !validScope(up.Scope) {
		return fmt.Errorf("非法的会话 scope: %s", up.Scope)
	}
	if strings.TrimSpace(up.ID) == "" {
		return fmt.Errorf("id 不能为空")
	}
	if err := p.memoryManager.update(up.Scope, up.ID, up.UserID, up.Content, up.Tags); err != nil {
		return err
	}
	p.Logger.Info("记忆已通过 Web 面板更新", "scope", up.Scope, "id", up.ID)
	return nil
}

// MemoryDelete 按 ID 删除指定 scope 中的一条记忆。
func (p *AIChatPlugin) MemoryDelete(scope, id string) error {
	if p.memoryManager == nil {
		return errMemoryDisabled
	}
	if !validScope(scope) {
		return fmt.Errorf("非法的会话 scope: %s", scope)
	}
	if !p.memoryManager.remove(scope, id) {
		return fmt.Errorf("记忆不存在: %s", id)
	}
	p.Logger.Info("记忆已通过 Web 面板删除", "scope", scope, "id", id)
	return nil
}

// MemoryDeleteMany 批量删除指定 scope 中的多条记忆，返回实际删除条数。
// 与单条删除不同，不存在的 ID 直接忽略（面板多选后可能含并发删除的条目）。
func (p *AIChatPlugin) MemoryDeleteMany(scope string, ids []string) (int, error) {
	if p.memoryManager == nil {
		return 0, errMemoryDisabled
	}
	if !validScope(scope) {
		return 0, fmt.Errorf("非法的会话 scope: %s", scope)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("id 列表不能为空")
	}
	deleted := p.memoryManager.removeMany(scope, ids)
	p.Logger.Info("记忆已通过 Web 面板批量删除", "scope", scope, "requested", len(ids), "deleted", deleted)
	return deleted, nil
}
