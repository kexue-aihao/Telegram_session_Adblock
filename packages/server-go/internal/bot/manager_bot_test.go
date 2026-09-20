package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 管理机器人是一条安全边界。
//
// 它在 Telegram 上**公开可私聊** —— 任何人搜到用户名都能发消息。
// 而它能增删托管其他机器人、能读出每个机器人的管理群与 token 掩码。
//
// 所以这组测试盯的是「谁被放行」，而不是「命令能不能用」：
// 放行逻辑写错的话，命令全都「能用」—— 对一个陌生人也能用。

// newTestRuntime 造一个只差真实 Telegram 的运行时。
//
// 返回桩件是因为「回给管理员的那句话是什么」本身就是这些测试要验的东西
// （见 TestAddFlowCancellation）。
func newTestRuntime(t *testing.T, isManager bool, adminTgID int64) (*Runtime, *stubTG) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	settings := store.DefaultGlobalSettings()
	settings.AdminTgUserID = adminTgID
	if err := st.WriteGlobalSettings(context.Background(), settings); err != nil {
		t.Fatalf("写全局设置失败: %v", err)
	}

	tg := newStubTG(t)
	return &Runtime{
		isManager: isManager,
		db:        st,
		api:       tg.client(),
		// 测试里不需要真的输出日志
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// 与 NewRuntime 对齐：零值的 Runtime 不是能用的 Runtime，
		// 而后面几条测试会走「添加机器人」的对话流程，那要写这张表。
		mgrStates: make(map[int64]*addFlow),
	}, tg
}

// stubTG 是一个假的 Telegram API。
//
// 存在的理由：管理命令的**每一条**路径都会回消息，而这些测试要验的是
// 「哪些输入会被放行」。没有客户端的话，每个用例都会在回消息那一步
// 变成 nil 解引用 —— 测的就不再是放行逻辑了。
//
// 走 httptest 而不是给 Runtime 抽一层接口：tgapi.Options 本来就有 BaseURL
// （为自建反代准备的），测试直接复用，生产代码一行都不用为测试让步。
type stubTG struct {
	srv *httptest.Server

	mu   sync.Mutex
	sent []map[string]any
}

func newStubTG(t *testing.T) *stubTG {
	t.Helper()

	s := &stubTG{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		s.mu.Lock()
		s.sent = append(s.sent, body)
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// 所有方法都回一条足以让调用方继续走下去的 message
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	t.Cleanup(s.srv.Close)

	return s
}

func (s *stubTG) client() *tgapi.Client {
	return tgapi.New("123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
		tgapi.Options{BaseURL: s.srv.URL})
}

// texts 返回所有已发出消息的正文，按发送顺序。
func (s *stubTG) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.sent))
	for _, m := range s.sent {
		if text, ok := m["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestManagerAdminGate(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		isManager bool
		adminTgID int64
		sender    int64
		want      bool
	}{
		{
			name:      "已配置的管理员放行",
			isManager: true, adminTgID: 111, sender: 111, want: true,
		},
		{
			name:      "其他任何人一律拒绝",
			isManager: true, adminTgID: 111, sender: 222, want: false,
		},
		{
			// 这是最容易写错的一条：未配置时如果判成「没限制」，
			// 等于把整个系统的控制权公开出去
			name:      "未配置 adminTgUserId 时全部拒绝",
			isManager: true, adminTgID: 0, sender: 0, want: false,
		},
		{
			name:      "未配置时连发 0 号以外的也拒绝",
			isManager: true, adminTgID: 0, sender: 111, want: false,
		},
		{
			// 普通机器人即使有人配了 adminTgUserId 也不该响应管理命令
			name:      "非管理机器人拒绝（哪怕 ID 匹配）",
			isManager: false, adminTgID: 111, sender: 111, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRuntime(t, tc.isManager, tc.adminTgID)
			if got := r.isManagerAdmin(ctx, tc.sender); got != tc.want {
				t.Errorf("isManagerAdmin(sender=%d) = %v，期望 %v", tc.sender, got, tc.want)
			}
		})
	}
}

// handleManagerPrivate 才是真正的入口，isManagerAdmin 只是它用的一道判断。
// 这里验入口本身的两个行为：
//
//	陌生人 → 交回中继管线，并且**一个字都不回**
//	管理员 → 消费掉，不进中继
func TestManagerPrivateRouting(t *testing.T) {
	ctx := context.Background()

	const adminID = 111
	msg := func(from int64, text string) *tgapi.Message {
		return &tgapi.Message{
			From: &tgapi.User{ID: from},
			Chat: tgapi.Chat{ID: from, Type: "private"},
			Text: text,
		}
	}

	t.Run("陌生人发来的消息不进管理命令，也不回话", func(t *testing.T) {
		r, tg := newTestRuntime(t, true, adminID)

		if r.handleManagerPrivate(ctx, msg(222, "/start")) {
			t.Error("陌生人的消息不该被管理命令消费")
		}
		// 回一句「你不是管理员」等于向探测者确认这台机器人有管理功能
		if texts := tg.texts(); len(texts) != 0 {
			t.Errorf("不该给陌生人任何回复，实际回了：%v", texts)
		}
	})

	t.Run("管理员 /start 拿到菜单，且不进中继", func(t *testing.T) {
		r, tg := newTestRuntime(t, true, adminID)

		if !r.handleManagerPrivate(ctx, msg(adminID, "/start")) {
			t.Fatal("管理员的命令应当被消费，否则会掉进中继管线")
		}
		texts := tg.texts()
		if len(texts) != 1 || !strings.Contains(texts[0], "管理台") {
			t.Errorf("应当回一条主菜单，实际：%v", texts)
		}
	})

	t.Run("未配置 adminTgUserId 时谁发都不处理", func(t *testing.T) {
		r, tg := newTestRuntime(t, true, 0)

		if r.handleManagerPrivate(ctx, msg(adminID, "/start")) {
			t.Error("未配置管理员时不该消费任何私聊")
		}
		if texts := tg.texts(); len(texts) != 0 {
			t.Errorf("不该回任何消息，实际：%v", texts)
		}
	})
}

func TestParseMgrCallback(t *testing.T) {
	cases := []struct {
		data       string
		wantAction string
		wantID     int64
		wantOK     bool
	}{
		{"mgr:view:42", "view", 42, true},
		{"mgr:list:0", "list", 0, true},
		{"mgr:delok:7", "delok", 7, true},
		// 话题按钮的回调不该被管理机器人接住
		{"tgs:close:3", "", 0, false},
		{"random", "", 0, false},
		{"", "", 0, false},
		{"mgr:view", "", 0, false},
	}

	for _, tc := range cases {
		action, id, ok := parseMgrCallback(tc.data)
		if ok != tc.wantOK {
			t.Errorf("parseMgrCallback(%q) ok = %v，期望 %v", tc.data, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if action != tc.wantAction || id != tc.wantID {
			t.Errorf("parseMgrCallback(%q) = (%q, %d)，期望 (%q, %d)",
				tc.data, action, id, tc.wantAction, tc.wantID)
		}
	}
}

func TestLooksLikeBotToken(t *testing.T) {
	cases := []struct {
		token string
		want  bool
	}{
		// 真实 token 的形态：数字 id + 冒号 + 35 位 base64url
		{"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", true},
		{"1234567890123:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", true},
		{"", false},
		{"notatoken", false},
		{"abc:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", false},       // id 不是数字
		{"123456789:short", false},                              // 密文太短
		{"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsa!", false}, // 非法字符
		{"12345:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", false},     // id 太短
	}

	for _, tc := range cases {
		if got := LooksLikeBotToken(tc.token); got != tc.want {
			t.Errorf("LooksLikeBotToken(%q) = %v，期望 %v", tc.token, got, tc.want)
		}
	}
}

// 添加流程里的 /cancel 与 /skip 必须在解析 token / 群 ID 之前被挡掉，
// 否则 "/cancel" 会被当成 token 去校验，用户看到的是「这不像 token」
// 而不是「已取消」。
func TestAddFlowCancellation(t *testing.T) {
	r, tg := newTestRuntime(t, true, 111)
	ctx := context.Background()

	r.setAddFlow(111, &addFlow{step: "token"})
	if r.getAddFlow(111) == nil {
		t.Fatal("流程没有建立")
	}

	// /cancel 应当清掉状态并返回 true（表示已消费）
	if !r.consumeAddFlow(ctx, 111, "/cancel") {
		t.Error("/cancel 应当被流程消费")
	}
	if r.getAddFlow(111) != nil {
		t.Error("/cancel 之后流程状态应当被清除")
	}

	// 回给管理员的必须是「已取消」，而不是「这不像 token」
	texts := tg.texts()
	if len(texts) != 1 {
		t.Fatalf("应当只回一条消息，实际回了 %d 条：%v", len(texts), texts)
	}
	if !strings.Contains(texts[0], "已取消") {
		t.Errorf("/cancel 的回复不对，收到：%q", texts[0])
	}

	// 没有流程时，普通文本不该被消费
	if r.consumeAddFlow(ctx, 111, "你好") {
		t.Error("没有进行中的流程时不该消费普通文本")
	}
}
