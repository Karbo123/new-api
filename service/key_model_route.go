package service

import "sync/atomic"

// 多 key 渠道的 key 级模型可见性注册表：channelID → (上游模型名 → 可调该模型
// 的 key 下标集合)。数据源是比价面板的逐 key /v1/models 探测（上游分组白名单
// 的忠实反映）。多 key 渠道里各 key 的分组可能不同（如同一中转站一把 key 只能
// 生图、另一把只能 GPT 组），按模型选 key 避免轮到无权限的 key 白跑一趟。
// 无数据（未探测/单 key 渠道/渠道非面板托管）时选 key 回退默认轮询，行为不变。

var keyModelRoute atomic.Pointer[map[int]map[string][]int]

// SetKeyModelRoute 全量替换注册表（比价面板探测/渠道同步后发布）。
func SetKeyModelRoute(m map[int]map[string][]int) {
	if m == nil {
		m = map[int]map[string][]int{}
	}
	keyModelRoute.Store(&m)
}

// PreferredKeyIndexes 返回该渠道该模型可见的 key 下标集合；无数据返回 nil。
func PreferredKeyIndexes(channelID int, upstreamModel string) map[int]bool {
	p := keyModelRoute.Load()
	if p == nil {
		return nil
	}
	vm := (*p)[channelID]
	if vm == nil {
		return nil
	}
	idx := vm[upstreamModel]
	if len(idx) == 0 {
		return nil
	}
	set := make(map[int]bool, len(idx))
	for _, i := range idx {
		set[i] = true
	}
	return set
}
