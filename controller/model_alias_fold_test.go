package controller

import (
	"reflect"
	"testing"
)

// /v1/models 别名折叠回归测试：比价管理渠道登记了同一模型的「OR 点号规范名 +
// 上游连字符原名」，客户端拉模型列表会看到重复。折叠后每个模型只留规范名。
// 同时必须不误伤带日期后缀的原生 ID（claude-opus-4-5-20251101 不得被折叠成
// 点号版本），也不得把不同模型合并。
func TestPcModelAliasFold(t *testing.T) {
	entries := map[string]pcPriorityEntry{
		"claude-fable-5.2": {
			Name: "claude-fable-5.2",
			Offers: []pcPriorityOffer{
				{Channel: "ikun", Ref: "claude-fable-5-2", Harness: "claude-fable-5-2"},
				{Channel: "apikl", Ref: "claude-fable-5-2", Harness: "claude-fable-5-2"},
			},
		},
		"claude-opus-5.5": {
			Name: "claude-opus-5.5",
			Offers: []pcPriorityOffer{
				{Channel: "ikun", Ref: "claude-opus-5-5", Harness: "claude-opus-5-5"},
			},
		},
	}
	fold := pcModelAliasFold(entries)
	if fold == nil {
		t.Fatal("fold 不应为空")
	}
	for _, alias := range []string{"claude-fable-5-2", "claude-fable-5.2"} {
		if got := fold[alias]; got != "claude-fable-5.2" {
			t.Errorf("%s → %q，期望 claude-fable-5.2", alias, got)
		}
	}
	for _, alias := range []string{"claude-opus-5-5", "claude-opus-5.5"} {
		if got := fold[alias]; got != "claude-opus-5.5" {
			t.Errorf("%s → %q，期望 claude-opus-5.5", alias, got)
		}
	}

	t.Run("列表折叠去重且保序", func(t *testing.T) {
		in := []string{"claude-fable-5-2", "claude-fable-5.2", "glm-5.3", "claude-opus-5-5", "claude-opus-5.5"}
		got := pcFoldModelNames(in, fold)
		want := []string{"claude-fable-5.2", "glm-5.3", "claude-opus-5.5"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("带日期后缀的原生 ID 不被误折叠", func(t *testing.T) {
		// claude-opus-4-5-20251101：结尾 -20251101 是日期，不是次版本号
		if v := pcDotVariant("claude-opus-4-5-20251101"); v != "" {
			t.Fatalf("误把日期 ID 折叠成点号变体：%q", v)
		}
		// 正常版本号应能转换
		if v := pcDotVariant("claude-fable-5-2"); v != "claude-fable-5.2" {
			t.Fatalf("claude-fable-5-2 → %q，期望 claude-fable-5.2", v)
		}
		if v := pcDashVariant("claude-fable-5.2"); v != "claude-fable-5-2" {
			t.Fatalf("claude-fable-5.2 → %q，期望 claude-fable-5-2", v)
		}
	})


	t.Run("OR 带厂商前缀的 ref 也折叠", func(t *testing.T) {
		entries := map[string]pcPriorityEntry{
			"glm53": {
				Name: "glm-5.3",
				Offers: []pcPriorityOffer{
					{Channel: "openrouter", Ref: "z-ai/glm-5.3", Harness: "glm-5.3"},
				},
			},
		}
		fold := pcModelAliasFold(entries)
		for _, alias := range []string{"z-ai/glm-5.3", "glm-5.3"} {
			if got := fold[alias]; got != "glm-5.3" {
				t.Errorf("%s → %q，期望 glm-5.3", alias, got)
			}
		}
		got := pcFoldModelNames([]string{"glm-5.3", "z-ai/glm-5.3", "glm-5.3-flash"}, fold)
		want := []string{"glm-5.3", "glm-5.3-flash"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("空条目返回 nil", func(t *testing.T) {
		if got := pcModelAliasFold(nil); got != nil {
			t.Fatalf("期望 nil，得到 %v", got)
		}
	})
}
