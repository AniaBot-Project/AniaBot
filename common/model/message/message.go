package message

import "encoding/json"

type Message struct {
	Time        uint          `json:"time"`
	PostType    string        `json:"post_type"`
	MessageType string        `json:"message_type"`
	SubType     string        `json:"sub_type"`
	MessageId   QID           `json:"message_id"`
	MessageSeq  int           `json:"message_seq"`
	UserId      QID           `json:"user_id"`
	GroupId     QID           `json:"group_id"`
	Message     []OB11Segment `json:"message"`
	RawMessage  string        `json:"raw_message"`
	Sender      MessageSender `json:"sender"`
	SelfId      QID           `json:"self_id"`
	// Platform 平台标识（如 "qq"、"feishu"），由产生该消息的适配器填充，
	// 不参与协议序列化；core 按它对插件做 Meta.Platforms 过滤。
	Platform string `json:"-"`
}

// IsGroup 报告该消息是否来自群聊。
//
// 判断以 OneBot v11 的 message_type 为准：微信、飞书、Telegram、Discord 等
// 适配器的私聊消息同样会填 GroupId（平台上的会话 ID），按「GroupId 是否为空」
// 推断会把私聊当成群聊，进而错分会话键、把回复按群聊发出（面板「消息日志」
// 就会把同一个会话拆成群聊 + 私聊两条）。message_type 缺失时（自定义适配器
// 只填了 GroupId）退回按 GroupId 是否为空推断。
func (m Message) IsGroup() bool {
	switch m.MessageType {
	case "group":
		return true
	case "private":
		return false
	}
	return m.GroupId != ""
}

type ForwardMessageSegment struct {
	Messages []NodeMsg        `json:"messages"`
	News     []map[string]any `json:"news"`
	Prompt   string           `json:"prompt"`
	Summary  string           `json:"summary"`
	Source   string           `json:"source"`
}

type GroupForwardMessage struct {
	GroupId QID `json:"group_id"`
	ForwardMessageSegment
}

type FriendForwardMessage struct {
	UserId QID `json:"user_id"`
	ForwardMessageSegment
}

type NodeMsg struct {
	Type string `json:"type"` // node
	Data struct {
		UserId   QID           `json:"user_id"`
		Nickname string        `json:"nickname"`
		Content  []OB11Segment `json:"content"`
	} `json:"data"`
}

type OB11Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type MessageSender struct {
	UserId   QID    `json:"user_id"`
	Nickname string `json:"nickname"`
	Sex      string `json:"sex"`
	Card     string `json:"card"`
	Role     string `json:"role"`
}

type Response[T any] struct {
	Status  string `json:"status"`
	RetCode int    `json:"retcode"`
	Data    T      `json:"data"`
	Message string `json:"message"`
	Echo    string `json:"echo"`
	Wording string `json:"wording"`
}

func (r *Response[T]) OK() bool {
	return r.Status == "ok"
}

type JsonMessage struct {
	App    string `json:"app"`
	Bizsrc string `json:"bizsrc"`
	Config struct {
		Ctime   int64  `json:"ctime"`
		Forward int    `json:"forward"`
		Token   string `json:"token"`
		Type    string `json:"type"`
	} `json:"config"`
	Extra  json.RawMessage `json:"extra"`
	Meta   json.RawMessage `json:"meta"`
	Prompt string          `json:"prompt"`
	Ver    string          `json:"ver"`
	View   string          `json:"view"`
}

type JsonNews struct {
	News struct {
		AppType int    `json:"app_type"`
		Appid   int64  `json:"appid"`
		Ctime   int64  `json:"ctime"`
		Desc    string `json:"desc"`
		JumpUrl string `json:"jumpUrl"`
		Preview string `json:"preview"`
		Tag     string `json:"tag"`
		TagIcon string `json:"tagIcon"`
		Title   string `json:"title"`
		Uin     int64  `json:"uin"`
	} `json:"news"`
}

type JsonDetailMeta struct {
	Detail struct {
		Title string `json:"title"`
		Desc  string `json:"desc"`
	} `json:"detail_1"`
}

type AiVoiceMsg struct {
	GroupId   QID    `json:"group_id"`
	Character string `json:"character"`
	Text      string `json:"text"`
}

type GroupUserInfo struct {
	GroupID         QID    `json:"group_id"`
	UserID          QID    `json:"user_id"`
	Nickname        string `json:"nickname"`
	Card            string `json:"card"`
	Sex             string `json:"sex"`
	Age             int    `json:"age"`
	JoinTime        uint   `json:"join_time"`
	LastSentTime    uint   `json:"last_sent_time"`
	Level           string `json:"level"`
	QqLevel         int    `json:"qq_level"`
	Role            string `json:"role"`
	Title           string `json:"title"`
	Area            string `json:"area"`
	Unfriendly      bool   `json:"unfriendly"`
	TitleExpireTime uint   `json:"title_expire_time"`
	CardChangeable  bool   `json:"card_changeable"`
	ShutUpTimestamp uint   `json:"shut_up_timestamp"`
	IsRobot         bool   `json:"is_robot"`
}
