package store

import (
	"context"
	"strings"
	"time"
)

// 按日聚合的统计。
//
// 仪表盘上的折线图如果每次刷新都去 messages 表扫全表，这个面板会随着
// 使用时间推移越来越慢 —— 而它恰恰是运维每天要打开的第一屏。
// 所以在写入端就把计数累加好，读的时候只查几十行。

// GlobalBotID 是汇总行的 bot_id。真实机器人 id 从 1 自增，0 是保留值。
const GlobalBotID int64 = 0

// StatDelta 是一次统计增量。
type StatDelta struct {
	MessagesIn    int `json:"messagesIn"`
	MessagesOut   int `json:"messagesOut"`
	TopicsCreated int `json:"topicsCreated"`
	AdsBlocked    int `json:"adsBlocked"`
}

// TodayIn 返回指定时区下的 YYYY-MM-DD。
//
// 统计必须按运维看到的「今天」切日，而不是 UTC ——
// 北京时间早上 8 点之前，UTC 还停在前一天，那时面板会显示昨天的数据。
func TodayIn(loc *time.Location) string {
	return time.Now().In(loc).Format("2006-01-02")
}

// BumpStats 记一次业务事件。
//
// 同时写两行：该机器人自己的行，以及 bot_id=0 的全局汇总行。
// 这样「单个机器人趋势」与「全站趋势」都是一次索引扫描，
// 不需要在读的时候对 N 个机器人的行做 GROUP BY。
func (s *Store) BumpStats(ctx context.Context, loc *time.Location, botID int64, delta StatDelta) error {
	date := TodayIn(loc)

	// 一次事务写两行：统计是旁路，但「两行要么都写要么都不写」仍然是
	// 值得保证的 —— 否则单机器人与汇总会对不上，而那种偏差极难解释。
	return s.WithTx(ctx, func(tx *Tx) error {
		for _, id := range []int64{botID, GlobalBotID} {
			_, err := tx.Exec(`
				INSERT INTO stats_daily (date, bot_id, messages_in, messages_out, topics_created, ads_blocked)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(date, bot_id) DO UPDATE SET
					messages_in    = stats_daily.messages_in    + excluded.messages_in,
					messages_out   = stats_daily.messages_out   + excluded.messages_out,
					topics_created = stats_daily.topics_created + excluded.topics_created,
					ads_blocked    = stats_daily.ads_blocked    + excluded.ads_blocked`,
				date, id, delta.MessagesIn, delta.MessagesOut, delta.TopicsCreated, delta.AdsBlocked)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// TimeseriesPoint 是折线上的一个点。
type TimeseriesPoint struct {
	Date          string `json:"date"`
	MessagesIn    int    `json:"messagesIn"`
	MessagesOut   int    `json:"messagesOut"`
	TopicsCreated int    `json:"topicsCreated"`
	AdsBlocked    int    `json:"adsBlocked"`
}

// GetTimeseries 取最近 N 天的折线数据。
//
// 缺失的日期补 0：SQLite 只会返回有数据的那些天，而前端拿到的数组
// 必须等长，否则画出来的线会凭空缩短一段。
func (s *Store) GetTimeseries(ctx context.Context, loc *time.Location, days int, botID int64) ([]TimeseriesPoint, error) {
	if days <= 0 || days > 90 {
		days = 14
	}

	today := time.Now().In(loc)
	start := today.AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	rows, err := s.read.QueryContext(ctx, `
		SELECT date, messages_in, messages_out, topics_created, ads_blocked
		FROM stats_daily
		WHERE bot_id = ? AND date >= ? AND date <= ?`,
		botID, start, TodayIn(loc))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		in, out, topics, ads int
	}
	byDate := make(map[string]row, days)
	for rows.Next() {
		var date string
		var r row
		if err := rows.Scan(&date, &r.in, &r.out, &r.topics, &r.ads); err != nil {
			return nil, err
		}
		byDate[date] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	points := make([]TimeseriesPoint, 0, days)
	for i := 0; i < days; i++ {
		date := today.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		r := byDate[date]
		points = append(points, TimeseriesPoint{
			Date:          date,
			MessagesIn:    r.in,
			MessagesOut:   r.out,
			TopicsCreated: r.topics,
			AdsBlocked:    r.ads,
		})
	}
	return points, nil
}

// BotStat 是单个机器人的统计汇总。
type BotStat struct {
	BotID         int64  `json:"botId"`
	BotName       string `json:"botName"`
	MessagesIn    int    `json:"messagesIn"`
	MessagesOut   int    `json:"messagesOut"`
	TopicsCreated int    `json:"topicsCreated"`
	AdsBlocked    int    `json:"adsBlocked"`
}

// GetStatsByBot 按机器人分组汇总最近 N 天的统计。
func (s *Store) GetStatsByBot(ctx context.Context, loc *time.Location, days int) ([]BotStat, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	start := time.Now().In(loc).AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	rows, err := s.read.QueryContext(ctx, `
		SELECT d.bot_id,
		       COALESCE(b.name, '#' || d.bot_id),
		       SUM(d.messages_in), SUM(d.messages_out),
		       SUM(d.topics_created), SUM(d.ads_blocked)
		FROM stats_daily d
		LEFT JOIN bots b ON b.id = d.bot_id
		WHERE d.date >= ? AND d.bot_id <> ?
		GROUP BY d.bot_id
		ORDER BY SUM(d.ads_blocked) DESC`, start, GlobalBotID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []BotStat
	for rows.Next() {
		var b BotStat
		if err := rows.Scan(&b.BotID, &b.BotName, &b.MessagesIn, &b.MessagesOut,
			&b.TopicsCreated, &b.AdsBlocked); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ────────────────────────────── 仪表盘总览 ──────────────────────────────

// Overview 是仪表盘的全部数据。
type Overview struct {
	Bots     OverviewBots     `json:"bots"`
	Sessions OverviewSessions `json:"sessions"`
	Contacts OverviewContacts `json:"contacts"`
	Messages OverviewMessages `json:"messages"`
	Ads      OverviewAds      `json:"ads"`
	Rules    OverviewRules    `json:"rules"`
	Deltas   OverviewDeltas   `json:"deltas"`

	GeneratedAt int64 `json:"generatedAt"`
}

type OverviewBots struct {
	Total    int `json:"total"`
	Online   int `json:"online"`
	Error    int `json:"error"`
	Disabled int `json:"disabled"`
}

type OverviewSessions struct {
	Open         int `json:"open"`
	Closed       int `json:"closed"`
	CreatedToday int `json:"createdToday"`
}

type OverviewContacts struct {
	Total   int `json:"total"`
	Blocked int `json:"blocked"`
	Flagged int `json:"flagged"`
}

type OverviewMessages struct {
	InToday  int `json:"inToday"`
	OutToday int `json:"outToday"`
	Total    int `json:"total"`
}

type OverviewAds struct {
	BlockedToday int `json:"blockedToday"`
	Blocked24h   int `json:"blocked24h"`
	BlockedTotal int `json:"blockedTotal"`
}

type OverviewRules struct {
	Total        int `json:"total"`
	Enabled      int `json:"enabled"`
	AutoDisabled int `json:"autoDisabled"`
}

// OverviewDeltas 是环比变化百分比；null 表示昨日无数据、无法比较。
type OverviewDeltas struct {
	MessagesIn      *int `json:"messagesIn"`
	AdsBlocked      *int `json:"adsBlocked"`
	SessionsCreated *int `json:"sessionsCreated"`
}

// ComputeOverview 计算仪表盘数据。
//
// 这是**唯一**的计算入口：HTTP 路由与 WebSocket 定时推送都用它 ——
// 两个地方各算一遍必然会漂移，表现出来就是「页面刷新前后的数字对不上」，
// 而这类 bug 因为不是每次都复现，排查成本极高。
func (s *Store) ComputeOverview(ctx context.Context, loc *time.Location, ruleStats OverviewRules) (Overview, error) {
	var o Overview
	o.GeneratedAt = time.Now().UnixMilli()
	o.Rules = ruleStats

	today := TodayIn(loc)
	now := time.Now().In(loc)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	// 用 time.Date 重建当天零点，而不是 Truncate(24h)：后者按 UTC 小时对齐，
	// 对 UTC+5:30 这类非整点偏移的时区会算错半天。
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// 机器人状态。
	//
	// 不含控制台（is_manager = 1）：它不参与转发，算进「托管了几个机器人」
	// 会让仪表盘的数字虚高一个，也会把它的健康状态混进中继健康度里。
	if err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN health_status = 'online' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN health_status = 'error' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN is_enabled = 0 THEN 1 ELSE 0 END), 0)
		FROM bots
		WHERE is_manager = 0`).Scan(&o.Bots.Total, &o.Bots.Online, &o.Bots.Error, &o.Bots.Disabled); err != nil {
		return o, err
	}

	// 会话
	if err := s.read.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0)
		FROM topics`).Scan(&o.Sessions.Open, &o.Sessions.Closed); err != nil {
		return o, err
	}
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM topics WHERE created_at >= ?`,
		startOfToday.UnixMilli()).Scan(&o.Sessions.CreatedToday); err != nil {
		return o, err
	}

	// 联系人
	if err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN is_blocked = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN violation_score > 0 THEN 1 ELSE 0 END), 0)
		FROM contacts`).Scan(&o.Contacts.Total, &o.Contacts.Blocked, &o.Contacts.Flagged); err != nil {
		return o, err
	}

	// 消息总数
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages`).Scan(&o.Messages.Total); err != nil {
		return o, err
	}

	// 命中总数与近 24 小时
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM rule_hits`).Scan(&o.Ads.BlockedTotal); err != nil {
		return o, err
	}
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rule_hits WHERE created_at >= ?`,
		time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&o.Ads.Blocked24h); err != nil {
		return o, err
	}

	// 今日 / 昨日的日聚合行一次查回来
	rows, err := s.read.QueryContext(ctx, `
		SELECT date, messages_in, messages_out, topics_created, ads_blocked
		FROM stats_daily WHERE bot_id = ? AND date IN (?, ?)`,
		GlobalBotID, today, yesterday)
	if err != nil {
		return o, err
	}
	defer func() { _ = rows.Close() }()

	var todayIn, todayOut, todayTopics, todayAds int
	var yIn, yTopics, yAds int
	for rows.Next() {
		var date string
		var in, out, topicsCreated, ads int
		if err := rows.Scan(&date, &in, &out, &topicsCreated, &ads); err != nil {
			return o, err
		}
		if date == today {
			todayIn, todayOut, todayTopics, todayAds = in, out, topicsCreated, ads
		} else {
			yIn, yTopics, yAds = in, topicsCreated, ads
		}
	}
	o.Messages.InToday = todayIn
	o.Messages.OutToday = todayOut
	o.Ads.BlockedToday = todayAds
	o.Sessions.CreatedToday = todayTopics

	o.Deltas.MessagesIn = deltaPercent(todayIn, yIn)
	o.Deltas.AdsBlocked = deltaPercent(todayAds, yAds)
	o.Deltas.SessionsCreated = deltaPercent(todayTopics, yTopics)

	return o, nil
}

// deltaPercent 计算环比变化；昨日为 0 时返回 nil（无法比较，而不是 +∞%）。
func deltaPercent(current, previous int) *int {
	if previous == 0 {
		return nil
	}
	v := (current - previous) * 100 / previous
	return &v
}

// PruneRuleHits 按保留策略清理历史命中记录，返回删除条数。
func (s *Store) PruneRuleHits(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UnixMilli()
	res, err := s.write.ExecContext(ctx,
		`DELETE FROM rule_hits WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BotNameOf 取机器人名字，用于日志与展示。
func (s *Store) BotNameOf(ctx context.Context, botID int64) string {
	var name string
	if err := s.read.QueryRowContext(ctx,
		`SELECT name FROM bots WHERE id = ?`, botID).Scan(&name); err != nil {
		return "unknown"
	}
	return strings.TrimSpace(name)
}
