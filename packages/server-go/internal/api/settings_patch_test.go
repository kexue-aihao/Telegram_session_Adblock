package api

import (
	"testing"

	"github.com/tgs/server/internal/store"
)

// 设置是**局部补丁**语义：PATCH 里只带要改的键，其余保持不变。
//
// 这条性质很容易被数字类型的细节破坏 —— 补丁里的数字经过
// normalizeJSON 变成 int64，而 base（当前设置）是 JSON 往返来的
// float64。两边类型不一致时，「没传的字段」会被当成「传了个坏值」。
func TestApplyGlobalSettingsPatch(t *testing.T) {
	current := store.DefaultGlobalSettings()
	current.AdminTgUserID = 111
	current.Timezone = "Asia/Shanghai"

	t.Run("只改一个键时其余设置原样保留", func(t *testing.T) {
		merged, err := applyGlobalSettingsPatch(current, map[string]any{
			"regexTimeoutMs": int64(120),
		})
		if err != nil {
			t.Fatalf("局部补丁不该失败: %v", err)
		}
		if merged.RegexTimeoutMs != 120 {
			t.Errorf("regexTimeoutMs = %d，期望 120", merged.RegexTimeoutMs)
		}
		if merged.AdminTgUserID != 111 {
			t.Errorf("adminTgUserId 被改动了：%d", merged.AdminTgUserID)
		}
	})

	t.Run("管理员 ID 可以设置与清空", func(t *testing.T) {
		merged, err := applyGlobalSettingsPatch(current, map[string]any{"adminTgUserId": int64(222)})
		if err != nil {
			t.Fatalf("设置 adminTgUserId 失败: %v", err)
		}
		if merged.AdminTgUserID != 222 {
			t.Errorf("adminTgUserId = %d，期望 222", merged.AdminTgUserID)
		}

		// 0 是「未配置」的意思，是一条会关闭全部管理命令的边界，
		// 因此必须能设回去
		merged, err = applyGlobalSettingsPatch(current, map[string]any{"adminTgUserId": int64(0)})
		if err != nil {
			t.Fatalf("清空 adminTgUserId 失败: %v", err)
		}
		if merged.AdminTgUserID != 0 {
			t.Errorf("adminTgUserId = %d，期望 0", merged.AdminTgUserID)
		}
	})

	t.Run("负数与非法值被拒绝", func(t *testing.T) {
		if _, err := applyGlobalSettingsPatch(current, map[string]any{"adminTgUserId": int64(-1)}); err == nil {
			t.Error("负数应当被拒绝")
		}
		if _, err := applyGlobalSettingsPatch(current, map[string]any{"adminTgUserId": "abc"}); err == nil {
			t.Error("字符串应当被拒绝")
		}
	})

	t.Run("未知设置项被拒绝", func(t *testing.T) {
		if _, err := applyGlobalSettingsPatch(current, map[string]any{"nope": 1}); err == nil {
			t.Error("未知设置项应当被拒绝")
		}
	})
}
