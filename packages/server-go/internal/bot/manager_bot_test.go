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
// 「哪些输入会被放行、哪些输入会变成一次转发」。没有客户端的话，
// 每个用例都会在回消息那一步变成 nil 解引用 —— 测的就不再是路由逻辑了。
//
// 走 httptest 而不是给 Runtime 抽一层接口：tgapi.Options 本来就有 BaseURL
// （为自建反代准备的），测试直接复用，生产代码一行都不用为测试让步。
type stubTG struct {
	srv *httptest.Server

	mu    sync.Mutex
	calls []stubCall
}

// stubCall 是一次 API 调用。方法名从 URL 末段取，与真实客户端的拼法一致。
type stubCall struct {
	method string
	params map[string]any
}

func newStubTG(t *testing.T) *stubTG {
	t.Helper()

	s := &stubTG{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)

		method := r.URL.Path
		if i := strings.LastIndex(method, "/"); i >= 0 {
			method = method[i+1:]
		}

		s.mu.Lock()
		s.calls = append(s.calls, stubCall{method: method, params: params})
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

	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		if text, ok := c.params["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// methods 返回所有被调用过的方法名（如 sendMessage、createForumTopic）。
func (s *stubTG) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, c.method)
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

// 控制台**不是**中继机器人 —— 这条性质必须在分发层成立，因为它是一条
// 全局性质：谁给控制台发消息都不该变成话题、不该被转发进管理群。
//
// 所以这里从 dispatch 进，而不是直接调 handleManagerPrivate：
// 那样就绕开了「分流本身对不对」这个最该被盯住的地方。
func TestManagerNeverRelays(t *testing.T) {
	ctx := context.Background()

	private := func(from int64, text string) *tgapi.Message {
		return &tgapi.Message{
			From: &tgapi.User{ID: from},
			Chat: tgapi.Chat{ID: from, Type: "private"},
			Text: text,
		}
	}
	group := func(from int64, text string) *tgapi.Message {
		return &tgapi.Message{
			From: &tgapi.User{ID: from},
			Chat: tgapi.Chat{ID: -1001234567890, Type: "supergroup"},
			Text: text,
		}
	}

	cases := []struct {
		name    string
		from    int64
		message *tgapi.Message
	}{
		{"陌生人发的普通消息", 222, private(222, "你好")},
		{"陌生人发的 /start", 222, private(222, "/start")},
		{"陌生人发的 /join 之类的群指令", 222, private(222, "/status")},
		{"管理员在群里发命令", 111, group(111, "/status")},
		{"陌生人在群里发消息", 222, group(222, "你好")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, tg := newTestRuntime(t, true, 111)

			r.dispatch(ctx, tgapi.Update{Message: tc.message})

			// 一、没有发出任何请求。中继的每一步（建话题、转发、回复）
			// 都要调 Telegram，零请求就等于「什么都没发生」。
			if texts := tg.texts(); len(texts) != 0 {
				t.Errorf("控制台不该回话，实际回了：%v", texts)
			}
			if methods := tg.methods(); len(methods) != 0 {
				t.Errorf("控制台不该调用任何 Telegram 接口，实际调了：%v", methods)
			}

			// 二、也没有进中继管线 —— 进过的痕迹是留下一个联系人
			if _, err := r.db.GetContactByTgID(ctx, 0, tc.from); err == nil {
				t.Error("消息进了中继管线：库里多了一个联系人")
			}
		})
	}
}

// 管理员自己的命令当然要能用 —— 上面那条测试很容易写成「谁都不理」。
func TestManagerAnswersAdmin(t *testing.T) {
	ctx := context.Background()
	r, tg := newTestRuntime(t, true, 111)

	r.dispatch(ctx, tgapi.Update{Message: &tgapi.Message{
		From: &tgapi.User{ID: 111},
		Chat: tgapi.Chat{ID: 111, Type: "private"},
		Text: "/start",
	}})

	texts := tg.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "管理台") {
		t.Errorf("管理员发 /start 应当收到主菜单，实际：%v", texts)
	}
}

// 未配置 adminTgUserId 时，谁发都不处理 —— 这是最容易被写成
// 「没配置就是没限制」的一条。
func TestManagerDisabledWithoutAdminID(t *testing.T) {
	ctx := context.Background()
	r, tg := newTestRuntime(t, true, 0)

	r.dispatch(ctx, tgapi.Update{Message: &tgapi.Message{
		From: &tgapi.User{ID: 111},
		Chat: tgapi.Chat{ID: 111, Type: "private"},
		Text: "/start",
	}})

	if texts := tg.texts(); len(texts) != 0 {
		t.Errorf("未配置管理员时不该回任何消息，实际：%v", texts)
	}
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
