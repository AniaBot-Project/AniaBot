package plugininfo

import "time"

// KnowledgeScopeInfo 是一个知识库作用域（全局 / 群聊 / 私聊）的概览，供面板左侧列表展示。
type KnowledgeScopeInfo struct {
	Scope string `json:"scope"` // 作用域，global / g:会话ID / f:用户ID
	Kind  string `json:"kind"`  // global / group / friend
	Count int    `json:"count"` // 该作用域下的文档条数
}

// KnowledgeDocInfo 是一条知识库文档，供面板展示。
type KnowledgeDocInfo struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"` // 来源，可空（如网页链接）
	CreatedAt time.Time `json:"created_at"`
}

// KnowledgeDocUpsert 是面板新增/编辑文档的请求体（新增时 ID 为空）。
type KnowledgeDocUpsert struct {
	Scope   string   `json:"scope"`
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
	Source  string   `json:"source,omitempty"`
}
