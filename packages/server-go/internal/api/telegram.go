package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/tgapi"
)

// 「测试连接」与「群体检」。
//
// 它们要用**尚未入库**的 token 跑：向导第二步的整个意义就是
// 「先验证，通过了再保存」。所以这里用一次性的客户端实例，
// 而不是复用运行时 —— 复用会污染它的限速器与重试状态。

// validateToken 用 token 调 getMe。
func validateToken(ctx context.Context, token string) (domain.BotValidation, error) {
	client := tgapi.New(token, tgapi.Options{Timeout: 15 * time.Second})

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	me, err := client.GetMe(probeCtx)
	if err != nil {
		msg := describeTelegramError(err)
		return domain.BotValidation{OK: false, Error: &msg}, nil
	}

	// can_read_all_group_messages 为 false 意味着机器人在 Privacy Mode 下，
	// 读不到群里的普通消息，话题中继会完全失效。这是最常见的一个坑，
	// 必须明确回传给面板。
	return domain.BotValidation{
		OK:                      true,
		TelegramID:              &me.ID,
		Name:                    strPtr(me.FirstName),
		Username:                strPtr(me.Username),
		CanJoinGroups:           &me.CanJoinGroups,
		CanReadAllGroupMessages: &me.CanReadAllGroupMessages,
	}, nil
}

// checkGroup 做管理群体检，逐项列出缺什么权限。
func checkGroup(ctx context.Context, token string, chatID int64) (domain.GroupCheck, error) {
	client := tgapi.New(token, tgapi.Options{Timeout: 15 * time.Second})

	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	check := domain.GroupCheck{ChatID: chatID, Problems: []string{}}

	chat, err := client.GetChat(probeCtx, chatID)
	if err != nil {
		check.Problems = append(check.Problems, "无法读取该群信息："+describeTelegramError(err))
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
		check.Problems = append(check.Problems, "无法确认机器人身份："+describeTelegramError(err))
		return check, nil
	}

	member, err := client.GetChatMember(probeCtx, chatID, me.ID)
	if err != nil {
		check.Problems = append(check.Problems, "无法读取机器人在群内的权限："+describeTelegramError(err))
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

// describeTelegramError 把 Telegram 的错误翻译成能指导操作的中文，
// 而不是把英文原文抛给管理员。
func describeTelegramError(err error) string {
	var apiErr *tgapi.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 400:
			return fmt.Sprintf("请求被拒绝：%s（通常是群 ID 不对，或机器人不在该群里）", apiErr.Description)
		case 401:
			return "token 无效或已失效，请到 @BotFather 重新获取"
		case 403:
			return fmt.Sprintf("机器人无权访问：%s", apiErr.Description)
		case 429:
			return "请求过于频繁，请稍后重试"
		}
		return fmt.Sprintf("Telegram 返回错误 %d：%s", apiErr.Code, apiErr.Description)
	}

	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "连接 Telegram 超时 —— 请检查服务器网络能否访问 api.telegram.org"
	default:
		return msg
	}
}
