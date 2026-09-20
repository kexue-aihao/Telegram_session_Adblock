package bot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/secret"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 机器人开通流程。
//
// 原先这套逻辑长在 HTTP 层（api/telegram.go），只有面板能用。
// 管理机器人出现后，Telegram 里也要走同样的「校验 token → 体检群 → 入库 → 启动」
// 流程 —— 两份实现必然漂移，而漂移的地方恰好是权限判断这类
// 出错代价很高的地方。所以整块下沉到这里，两条入口共用。

// LooksLikeBotToken 做一次廉价的前置校验，避免为明显错误的输入发一次网络请求。
//
// 真实的 token 形如 `123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw`：
// 冒号前是纯数字的 bot id，冒号后是 35 位左右的 base64url。
func LooksLikeBotToken(token string) bool {
	// 下限 6 位：BotFather 不签发更短的 id，而这条能把「把整段文案粘进来了」
	// 这类输入挡在网络请求之前。
	//
	// 上限取 int64 的位数（19）而不是某个「看起来合理」的数字：Telegram 的 id
	// 一直在增长，写死 12 时，一个 13 位 id 的**合法** token 会被判成格式不对 ——
	// 而这条判定的全部价值就是别把合法输入拦下来。
	colon := strings.IndexByte(token, ':')
	if colon < 6 || colon > 19 {
		return false
	}
	for i := 0; i < colon; i++ {
		if token[i] < '0' || token[i] > '9' {
			return false
		}
	}
	if len(token)-colon-1 < 30 {
		return false
	}
	for _, ch := range token[colon+1:] {
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z',
			ch >= '0' && ch <= '9', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}

// ValidateToken 用 token 调 getMe。
//
// 刻意用一次性的客户端实例而不是复用运行时：这里要能验证一个
// **尚未入库**的 token，复用运行时会污染它的限速器与重试状态。
func ValidateToken(ctx context.Context, token string) (domain.BotValidation, error) {
	client := tgapi.New(token, tgapi.Options{Timeout: 15 * time.Second})

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	me, err := client.GetMe(probeCtx)
	if err != nil {
		msg := DescribeTelegramError(err)
		return domain.BotValidation{OK: false, Error: &msg}, nil
	}

	// can_read_all_group_messages 为 false 意味着机器人处于 Privacy Mode，
	// 读不到群里的普通消息，话题中继会完全失效。这是最常见的一个坑。
	canRead := me.CanReadAllGroupMessages
	return domain.BotValidation{
		OK:                      true,
		TelegramID:              &me.ID,
		Name:                    strPtr(me.FirstName),
		Username:                &me.Username,
		CanJoinGroups:           &me.CanJoinGroups,
		CanReadAllGroupMessages: &canRead,
	}, nil
}

// CheckGroup 做管理群体检，逐项列出缺什么权限。
func CheckGroup(ctx context.Context, token string, chatID int64) (domain.GroupCheck, error) {
	client := tgapi.New(token, tgapi.Options{Timeout: 15 * time.Second})

	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	check := domain.GroupCheck{ChatID: chatID, Problems: []string{}}

	chat, err := client.GetChat(probeCtx, chatID)
	if err != nil {
		check.Problems = append(check.Problems, "无法读取该群信息："+DescribeTelegramError(err))
		return check, nil
	}
	if chat.Title != "" {
		check.Title = &chat.Title
	}
	check.IsForum = chat.IsForum

	if !check.IsForum {
		check.Problems = append(check.Problems,
			"该群没有开启「话题（Topics）」功能：群设置 → 话题 → 开启")
	}

	me, err := client.GetMe(probeCtx)
	if err != nil {
		check.Problems = append(check.Problems, "无法确认机器人身份："+DescribeTelegramError(err))
		return check, nil
	}

	member, err := client.GetChatMember(probeCtx, chatID, me.ID)
	if err != nil {
		check.Problems = append(check.Problems, "无法读取机器人在群内的权限："+DescribeTelegramError(err))
		return check, nil
	}

	isAdmin := member.IsAdmin()
	check.IsAdmin = isAdmin
	if !isAdmin {
		check.Problems = append(check.Problems, "机器人还不是群管理员，请把它提升为管理员")
	}

	// 群主天然拥有一切权限；管理员则要看具体位
	check.CanManageTopics = member.Status == "creator" || (isAdmin && member.CanManageTopics)
	check.CanDeleteMessages = member.Status == "creator" || (isAdmin && member.CanDeleteMessages)
	check.CanRestrictMembers = member.Status == "creator" || (isAdmin && member.CanRestrictMembers)

	if !check.CanManageTopics {
		check.Problems = append(check.Problems, "缺少「管理话题」权限，无法为用户创建话题")
	}
	if !check.CanDeleteMessages {
		check.Problems = append(check.Problems, "缺少「删除消息」权限，命中规则时无法撤回消息")
	}
	if !check.CanRestrictMembers {
		// 这一项是可选增强而非必需：终端用户通常不在管理群里，
		// 真正的处罚在机器人层级实现，这一条只影响「恰好是群成员」的用户。
		check.Problems = append(check.Problems,
			"缺少「封禁用户」权限（可选）：仅影响额外叠加 Telegram 原生处罚")
	}

	check.OK = len(check.Problems) == 0
	return check, nil
}

// DescribeTelegramError 把 Telegram 的错误翻译成能指导操作的中文。
//
// 英文原文对管理员没有帮助：「Bad Request: chat not found」不告诉
// 他该去检查群 ID 还是机器人是否在群里。
func DescribeTelegramError(err error) string {
	var apiErr *tgapi.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 400:
			return fmt.Sprintf("请求被拒绝：%s（通常是群 ID 不对，或机器人不在该群里）", apiErr.Description)
		case 401:
			return "token 无效或已失效，请到 @BotFather 重新获取"
		case 403:
			return fmt.Sprintf("机器人无权访问：%s", apiErr.Description)
		case 409:
			return "另一个进程正在用同一个 token 轮询（可能是重复启动，或 Telegram 侧 webhook 未清除）"
		case 429:
			return "请求过于频繁，请稍后重试"
		}
		return fmt.Sprintf("Telegram 返回错误 %d：%s", apiErr.Code, apiErr.Description)
	}

	// 超时要分两种判：请求级超时是 context 到期，而拨号/读写超时是
	// os.ErrDeadlineExceeded —— errors.Is 认不出后者，但它实现了 net.Error。
	// 只看前者的话，最难排查的那种「连不上」会漏出英文原文。
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "连接 Telegram 超时 —— 请检查服务器网络能否访问 api.telegram.org"
	}
	return err.Error()
}

// CreateRequest 是开通一个新机器人的入参。
type CreateRequest struct {
	Token        string
	AdminGroupID *int64
	Name         string
	// Manager 标记为管理机器人
	Manager bool
	// Actor 写审计时用，Telegram 入口传管理员用户名，面板传当前登录用户
	Actor string
}

// Create 走完整的开通流程：校验 token → 判重 → 加密入库 → 建默认设置 → 启动。
//
// 面板与管理机器人共用这一条路径，因此「从 Telegram 加进来的机器人」
// 与「从面板加进来的」在库里的形态完全一致。
func (m *Manager) Create(ctx context.Context, req CreateRequest) (store.BotRow, error) {
	req.Token = strings.TrimSpace(req.Token)
	if !LooksLikeBotToken(req.Token) {
		return store.BotRow{}, errors.New("这不像是 BotFather 签发的 token 格式")
	}

	validation, err := ValidateToken(ctx, req.Token)
	if err != nil {
		return store.BotRow{}, err
	}
	if !validation.OK || validation.TelegramID == nil || validation.Username == nil {
		msg := "token 校验失败"
		if validation.Error != nil {
			msg = *validation.Error
		}
		return store.BotRow{}, errors.New(msg)
	}

	// 判重：同一个机器人加两次会让 Telegram 报 409，
	// 而那个报错看起来像系统故障而不是配置问题。
	if existing, err := m.db.GetBotByUsername(ctx, *validation.Username); err == nil {
		return store.BotRow{}, fmt.Errorf("机器人 @%s 已经添加过了（id=%d）", existing.Username, existing.ID)
	}

	sealed, err := secret.Seal(m.key, req.Token)
	if err != nil {
		return store.BotRow{}, fmt.Errorf("加密 token 失败：%w", err)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		if validation.Name != nil && *validation.Name != "" {
			name = *validation.Name
		} else {
			name = *validation.Username
		}
	}

	// 顺带把群名问出来。失败不阻断：管理员可能填了个还没把机器人拉进去的群，
	// 那种情况下机器人群名取不到，但保存下来之后再体检即可。
	var groupTitle *string
	if req.AdminGroupID != nil {
		if check, err := CheckGroup(ctx, req.Token, *req.AdminGroupID); err == nil {
			groupTitle = check.Title
		}
	}

	created, err := m.db.CreateBot(ctx, store.CreateBotInput{
		Name:            name,
		Username:        *validation.Username,
		Sealed:          sealed,
		TokenMask:       secret.MaskToken(req.Token),
		TelegramID:      validation.TelegramID,
		AdminGroupID:    req.AdminGroupID,
		AdminGroupTitle: groupTitle,
		Settings:        store.DefaultBotSettings(0),
	})
	if err != nil {
		return store.BotRow{}, err
	}

	if req.Manager {
		// 走 SetManagerBot 而不是直接写库：控制台只有一台，绑定新的会
		// 解绑旧的，而旧的那台运行时必须跟着变回中继机器人。
		//
		// 它顺带会把新建的这台启起来，所以成功时不必再 Start 一次。
		if err := m.SetManagerBot(ctx, created.ID, true); err != nil {
			m.log.Warn("绑定为管理机器人失败，已按普通机器人启动", "botId", created.ID, "err", err)
			if startErr := m.Start(ctx, created.ID); startErr != nil {
				m.log.Warn("机器人创建成功但启动失败", "botId", created.ID, "err", startErr)
			}
		} else {
			created.IsManager = true
		}

		m.log.Info("已开通机器人", "botId", created.ID, "username", created.Username,
			"manager", true, "actor", req.Actor)
		return created, nil
	}

	// 立即启动。刻意不判断 AdminGroupID 是否已绑定 —— 未绑定的机器人
	// 依然能收私聊、回 /start、被设为管理机器人。
	if err := m.Start(ctx, created.ID); err != nil {
		m.log.Warn("机器人创建成功但启动失败", "botId", created.ID, "err", err)
	}

	m.log.Info("已开通机器人", "botId", created.ID, "username", created.Username, "actor", req.Actor)
	return created, nil
}

// Delete 停掉并删除一个机器人（含它的会话、规则与命中记录，走外键级联）。
func (m *Manager) Delete(ctx context.Context, botID int64) error {
	row, err := m.db.GetBot(ctx, botID)
	if err != nil {
		return err
	}

	m.Stop(ctx, botID)
	if err := m.db.DeleteBot(ctx, botID); err != nil {
		return err
	}

	m.log.Info("已删除机器人", "botId", botID, "username", row.Username)
	return nil
}

func strPtr(s string) *string { return &s }

// CheckGroupForBot 对某个**已入库**的机器人做管理群体检。
//
// 需要它是因为：体检要那个机器人自己的 token，而解密 token 的主密钥
// 只有 Manager 持有 —— Runtime 只拿得到自己那一个客户端的明文 token。
// 管理机器人要体检**别的**机器人，就必须走这里。
func (m *Manager) CheckGroupForBot(ctx context.Context, botID int64) (domain.GroupCheck, error) {
	row, err := m.db.GetBot(ctx, botID)
	if err != nil {
		return domain.GroupCheck{}, fmt.Errorf("读取机器人失败: %w", err)
	}
	if row.AdminGroupID == nil {
		return domain.GroupCheck{}, errors.New("这个机器人还没有绑定管理群")
	}

	token, err := secret.Open(m.key, row.Sealed())
	if err != nil {
		return domain.GroupCheck{}, err
	}

	return CheckGroup(ctx, token, *row.AdminGroupID)
}
