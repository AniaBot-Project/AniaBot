package message

// AICharacterGroup AI 语音角色分组：协议端按类别返回（如官方音色、克隆音色）。
// 角色列表跟群绑定，查询时必须指定群号。
type AICharacterGroup struct {
	Type       string        `json:"type"`
	Characters []AICharacter `json:"characters"`
}

// AICharacter 单个 AI 语音角色，CharacterID 即发送 AI 语音时 character 参数的值。
type AICharacter struct {
	CharacterID   string `json:"character_id"`
	CharacterName string `json:"character_name"`
	PreviewURL    string `json:"preview_url"`
}
