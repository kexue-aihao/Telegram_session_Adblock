// Package sanction 是阶梯处罚状态机。
//
// 这是整个项目里最容易写错的一块，因为它要同时满足三个互相拉扯的要求：
//
//  1. **累计**：违规分是累加的，第 5 次命中和第 1 次命中的处置不同；
//  2. **幂等**：用户在第 5 分上连发 10 条广告，不能被禁言 10 次、
//     也不能每命中一次就往私聊里灌一条「你已被禁言」；
//  3. **可降级**：静默/禁言到期、或管理员手动解封后，状态要真的回去。
//
// 采取的模型是「状态收敛」而不是「事件累加」：
// 每次违规只做一件事 —— 根据**当前分数**算出它应该处于哪一档，
// 与**当前实际所处**的档位比较，不同就迁移过去。
// 这样重复命中天然幂等，因为「算出应该在哪一档」是纯函数。
package sanction

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/store"
)

// tier 给出档位的高低。数字大 = 更严，用于比较是否需要升级。
//
// 这个顺序不是字母序也不是定义序，必须显式写出来 ——
// 靠常量定义顺序隐式排序，某天有人在中间插一档就会静默出错。
var tier = map[string]int{
	domain.SanctionWarn:    1,
	domain.SanctionSilence: 2,
	domain.SanctionMute:    3,
	domain.SanctionBan:     4,
}

// Engine 执行阶梯处罚。
type Engine struct {
	db  *store.Store
	log *slog.Logger
}

// New 构造状态机。
func New(db *store.Store, log *slog.Logger) *Engine {
	return &Engine{db: db, log: log}
}

// ViolationInput 是一次违规的输入。
type ViolationInput struct {
	BotID     int64
	ContactID int64
	RuleID    *int64
	// Severity 是本次命中的违规分
	Severity int
	Reason   string
	// CreatedBy 只有手动处罚时才有值
	CreatedBy *string
}

// Outcome 是一次违规处理的结果。
type Outcome struct {
	Score        int
	PreviousType *string
	CurrentType  *string
	// Escalated 表示本次发生了档位变化 —— **只有为 true 时才该发通知**，
	// 这是避免重复骚扰用户的关键
	Escalated bool
	Sanction  *store.SanctionRow
	Step      *domain.EscalationStep
	Decayed   bool
}

// Apply 记一次违规并推进阶梯。
//
// 它**不发送任何消息** —— 发什么文案、发给谁，由调用方（bot 包）决定。
// 状态机只管状态，这样它才能被面板的「模拟处罚」功能安全复用。
func (e *Engine) Apply(ctx context.Context, in ViolationInput) (Outcome, error) {
	settings, err := e.db.GetBotSettings(ctx, in.BotID)
	if err != nil {
		return Outcome{}, fmt.Errorf("读取机器人设置: %w", err)
	}
	esc := settings.Escalation

	contact, err := e.db.GetContact(ctx, in.ContactID)
	if err != nil {
		return Outcome{}, fmt.Errorf("读取联系人: %w", err)
	}

	// 1. 时间衰减 + 累加
	decayedScore, decayed := applyDecay(contact.ViolationScore, contact.LastViolationAt, esc.DecayDays)
	score := decayedScore + in.Severity
	if score > esc.MaxScore {
		score = esc.MaxScore
	}

	if err := e.db.UpdateViolationScore(ctx, in.ContactID, score); err != nil {
		return Outcome{}, fmt.Errorf("更新违规分: %w", err)
	}

	// 2. 算出「应该在哪一档」与「实际在哪一档」
	before, err := e.currentTop(ctx, in.ContactID)
	if err != nil {
		return Outcome{}, err
	}
	target := StepForScore(esc.Steps, score)

	previousType := typeOf(before)

	outcome := Outcome{
		Score:        score,
		PreviousType: previousType,
		// CurrentType 先填旧档位；升级时会在下面覆盖
		CurrentType: previousType,
		Decayed:     decayed,
	}

	// 3. 档位没变（或降了）就不动处罚记录。
	//
	// 降档交给「过期」与「手动解封」处理 —— 在「记违规」这条路径上顺手降档，
	// 会让管理员的手动解封反复被覆盖：他刚解封，用户下一条消息又把状态
	// 按分数推回高档位。
	if target == nil {
		return outcome, nil
	}
	if before != nil && tier[target.Type] <= tier[before.Type] {
		return outcome, nil
	}

	// 4. 升级：旧档位作废，写入新档位
	if before != nil {
		if err := e.db.DeactivateContactSanctions(ctx, in.ContactID); err != nil {
			return outcome, fmt.Errorf("作废旧处罚: %w", err)
		}
	}

	var expiresAt *int64
	if target.Type == domain.SanctionMute && target.DurationMinutes != nil {
		exp := time.Now().Add(time.Duration(*target.DurationMinutes) * time.Minute).UnixMilli()
		expiresAt = &exp
	}

	row := store.SanctionRow{
		BotID:     in.BotID,
		ContactID: in.ContactID,
		Type:      target.Type,
		Reason:    in.Reason,
		RuleID:    in.RuleID,
		ExpiresAt: expiresAt,
		CreatedBy: in.CreatedBy,
		ScoreAt:   score,
		CreatedAt: time.Now().UnixMilli(),
	}
	id, err := e.db.CreateSanction(ctx, row)
	if err != nil {
		return outcome, fmt.Errorf("写入处罚记录: %w", err)
	}
	row.ID = id

	e.log.Info("阶梯处罚升级",
		"contactId", in.ContactID,
		"from", previousType,
		"to", target.Type,
		"score", score)

	outcome.Escalated = true
	outcome.CurrentType = &target.Type
	outcome.Sanction = &row
	outcome.Step = target
	return outcome, nil
}

// currentTop 取当前生效的最高档位。
func (e *Engine) currentTop(ctx context.Context, contactID int64) (*store.SanctionRow, error) {
	active, err := e.db.ListActiveSanctions(ctx, contactID)
	if err != nil {
		return nil, err
	}

	var top *store.SanctionRow
	for i := range active {
		row := &active[i]
		if top == nil || tier[row.Type] > tier[top.Type] {
			top = row
		}
	}
	return top, nil
}

// BlockingState 是「这条消息该不该拦」的判断结果。
//
// 拉黑与静默/禁言对**用户**的可见性完全不同，但中继管线只关心
// 「转不转」，所以统一成一个判断。
type BlockingState struct {
	Blocked   bool
	Type      string
	ExpiresAt *int64
}

// Blocking 返回当前是否应当拦截该用户的消息。
func (e *Engine) Blocking(ctx context.Context, contactID int64) (BlockingState, error) {
	active, err := e.db.ListActiveSanctions(ctx, contactID)
	if err != nil {
		return BlockingState{}, err
	}

	var top *store.SanctionRow
	for i := range active {
		row := &active[i]
		if !isBlocking(row.Type) {
			continue
		}
		if top == nil || tier[row.Type] > tier[top.Type] {
			top = row
		}
	}
	if top == nil {
		return BlockingState{}, nil
	}
	return BlockingState{Blocked: true, Type: top.Type, ExpiresAt: top.ExpiresAt}, nil
}

func isBlocking(sanctionType string) bool {
	for _, t := range domain.BlockingSanctions {
		if t == sanctionType {
			return true
		}
	}
	return false
}

// StepForScore 返回该分数落在哪一档；未达到任何门槛时返回 nil。
func StepForScore(steps []domain.EscalationStep, score int) *domain.EscalationStep {
	var matched *domain.EscalationStep
	for i := range steps {
		step := &steps[i]
		if !step.Enabled {
			continue
		}
		if score < step.AtScore {
			continue
		}
		// steps 不保证有序，取满足条件的最高档
		if matched == nil || step.AtScore >= matched.AtScore {
			matched = step
		}
	}
	return matched
}

// NextStep 返回距离下一档还差多少分；已在最高档时返回 nil。
// 供面板显示「再违规 N 分将被禁言」。
func NextStep(steps []domain.EscalationStep, score int) *struct {
	AtScore   int
	Type      string
	Remaining int
} {
	var best *domain.EscalationStep
	for i := range steps {
		step := &steps[i]
		if !step.Enabled || step.AtScore <= score {
			continue
		}
		if best == nil || step.AtScore < best.AtScore {
			best = step
		}
	}
	if best == nil {
		return nil
	}
	return &struct {
		AtScore   int
		Type      string
		Remaining int
	}{AtScore: best.AtScore, Type: best.Type, Remaining: best.AtScore - score}
}

// Lift 手动解封 / 解除静默。
func (e *Engine) Lift(ctx context.Context, contactID int64, onlyType string) (int64, error) {
	active, err := e.db.ListActiveSanctions(ctx, contactID)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, row := range active {
		if onlyType != "" && row.Type != onlyType {
			continue
		}
		if err := e.db.DeactivateContactSanctions(ctx, contactID); err != nil {
			return count, err
		}
		count++
		break
	}
	return count, nil
}

// Reset 清零违规分并解除全部处罚。
func (e *Engine) Reset(ctx context.Context, contactID int64) error {
	return e.db.ResetViolations(ctx, contactID)
}

// ExpireDue 解除到期的禁言。由定时任务调用。
func (e *Engine) ExpireDue(ctx context.Context) (int64, error) {
	n, err := e.db.ExpireDueSanctions(ctx)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		e.log.Debug("已解除到期处罚", "count", n)
	}
	return n, nil
}

// ────────────────────────────── 辅助 ──────────────────────────────

// applyDecay 计算时间衰减后的违规分。
//
// 没有衰减的话，一个半年前误触发过 4 次的用户会永远停在「静默」档，
// 管理员根本没机会知道。
//
// 衰减是**读时计算** —— 不改写库里的分数，否则每次读都要写一次，
// 还会让「原始违规分」这个事实消失。
func applyDecay(score int, lastViolationAt *int64, decayDays *int) (int, bool) {
	if decayDays == nil || lastViolationAt == nil || score <= 0 {
		return score, false
	}

	elapsedDays := float64(time.Now().UnixMilli()-*lastViolationAt) / 86_400_000
	if elapsedDays < float64(*decayDays) {
		return score, false
	}

	// 每过一个周期减半，而不是一次性清零：连续多个周期不违规的用户
	// 最终会自然回到 0，而刚过线一点点的用户不会被突然赦免。
	halvings := int(elapsedDays) / *decayDays
	decayed := score
	for i := 0; i < halvings && decayed > 0; i++ {
		decayed /= 2
	}
	return decayed, true
}

func typeOf(row *store.SanctionRow) *string {
	if row == nil {
		return nil
	}
	t := row.Type
	return &t
}

func stepType(step *domain.EscalationStep) *string {
	if step == nil {
		return nil
	}
	t := step.Type
	return &t
}
