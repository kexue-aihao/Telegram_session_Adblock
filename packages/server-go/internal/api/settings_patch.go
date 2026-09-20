package api

import (
	"encoding/json"
	"fmt"

	"github.com/tgs/server/internal/store"
)

// 设置的部分更新。
//
// 走的路径是「读出现有 → 应用 JSON 补丁 → 整行写回」，而不是让前端
// 提交整份设置。理由是前端少传一个字段就会把它清空 —— 那是很难察觉的
// 一类数据丢失，表现为「改了个开关，结果话题模板被重置了」。
//
// 用 JSON 往返做补丁：设置项有二十多个，手写二十个 if 既冗长又容易漏。

// applySettingsPatch 把补丁合并进现有设置。
func applySettingsPatch(current store.BotSettings, patch map[string]any) (store.BotSettings, error) {
	base, err := toMap(current)
	if err != nil {
		return current, err
	}

	for key, value := range patch {
		// botId 由路径决定，不接受客户端指定
		if key == "botId" {
			continue
		}
		if _, known := base[key]; !known {
			return current, fmt.Errorf("未知的设置项：%s", key)
		}
		base[key] = normalizeJSON(value)
	}
	// 写回时不带上 botId：它是主键，由调用方从路径里取
	delete(base, "botId")

	payload, err := json.Marshal(base)
	if err != nil {
		return current, fmt.Errorf("序列化设置：%w", err)
	}

	var merged store.BotSettings
	if err := json.Unmarshal(payload, &merged); err != nil {
		return current, fmt.Errorf("解析设置：%w", err)
	}

	// 主键与阶梯配置需要特别保护：
	//   - botId 由调用方从路径里取；
	//   - 阶梯配置如果被清空，用户会突然发现所有处罚档位都没了，
	//     而那是「设置被改坏了」而不是「用户想要关掉处罚」。
	merged.BotID = current.BotID
	if len(merged.Escalation.Steps) == 0 {
		merged.Escalation.Steps = current.Escalation.Steps
	}
	if merged.Escalation.MaxScore <= 0 {
		merged.Escalation.MaxScore = current.Escalation.MaxScore
	}
	if merged.Escalation.DecayDays == nil {
		merged.Escalation.DecayDays = current.Escalation.DecayDays
	}

	return merged, nil
}

// normalizeJSON 把 JSON 解码出来的值归一成可比较 / 可序列化的形态。
//
// 关键处理是数字：encoding/json 把所有 JSON 数字解成 float64，
// 而我们的字段是 int。不归一的话，写回时 400 会变成 400.0，
// 前端读到的类型与它写进去的不一致 —— 表现为「输入框里的值莫名其妙变成小数」。
func normalizeJSON(v any) any {
	switch value := v.(type) {
	case float64:
		if value == float64(int64(value)) {
			return int64(value)
		}
		return value
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = normalizeJSON(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = normalizeJSON(item)
		}
		return out
	default:
		return v
	}
}

// toMap 把设置结构体转成 map[string]any，走的是它的 JSON 标签。
func toMap(v any) (map[string]any, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("序列化设置：%w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("解析设置：%w", err)
	}
	return out, nil
}
