package controller

import (
	"strings"
	"sync"
	"time"
)

// /v1/models 别名折叠：比价管理的渠道会在 models 里登记同一模型的多个名字——
// 条目规范名（如 claude-fable-5.2，OpenRouter 点号命名，harness/面板请求它）、
// 各上游原生名（如 claude-fable-5-2，Anthropic 连字符 ID），并用 model_mapping
// 把别名翻译回本渠道上游名。这套别名是跨渠道 fallback 的地基（ability 按请求名
// 精确匹配，不登记别名则各渠道候选集凑不齐），但客户端拉 /v1/models 时会看到
// 同一模型的多张脸（Claude Code desktop 实测显示两个 fable）。
//
// 折叠规则：以优先级列表每条目的 Name 为规范名，条目下所有报价的 Ref/Harness
// 及其归一形式都折叠到该规范名；只在别名组内保留规范名一个出口。非比价管理的
// 渠道模型不受影响（无别名信息，原样保留）。

// pcModelAliasFold 由优先级配置构造「别名 → 规范名」映射。无别名时返回 nil。
func pcModelAliasFold(entries map[string]pcPriorityEntry) map[string]string {
	fold := map[string]string{}
	for _, e := range entries {
		canon := e.Name
		if canon == "" {
			continue
		}
		// 条目内可能出现的所有别名形态：报价 ref、harness 裸名、及归一变体
		var aliases []string
		for _, of := range e.Offers {
			aliases = append(aliases, of.Ref, of.Harness, of.Name)
		}
		aliases = append(aliases, e.Name)
		for _, a := range aliases {
			if a == "" {
				continue
			}
			// 原始形态本身也折叠（OpenRouter 报价 ref 带厂商标识前缀，如
			// z-ai/glm-5.3——剥前缀只用于匹配，展示层里它同样是一张重复的脸）
			fold[a] = canon
			for _, v := range pcCanonVariants(a) {
				if v != "" {
					fold[v] = canon
				}
			}
			// 剥厂商标识前缀的裸名（pcBareModelName 口径，与条目名一致）
			if bare := pcBareModelName(a); bare != "" {
				fold[bare] = canon
			}
			// 点号/连字符互换是 Claude 系最常见的双名来源（官方 ID 用连字符）
			if dash := pcDashVariant(a); dash != "" {
				fold[dash] = canon
			}
			if dot := pcDotVariant(a); dot != "" {
				fold[dot] = canon
			}
		}
		fold[canon] = canon
	}
	if len(fold) == 0 {
		return nil
	}
	return fold
}

// pcDashVariant "claude-fable-5.2" → "claude-fable-5-2"（点号转连字符，仅当
// 名字含点号且不含下划线式版本号时应用）。Anthropic 原生 ID 惯例。
func pcDashVariant(name string) string {
	if !strings.Contains(name, ".") {
		return ""
	}
	// 仅处理「主版本.次版本」形态：x.y 且两侧是数字
	parts := strings.Split(name, ".")
	if len(parts) != 2 {
		return ""
	}
	left, right := parts[0], parts[1]
	if left == "" || right == "" {
		return ""
	}
	if !isAllDigits(lastRune(left)) || !isAllDigits(firstRune(right)) {
		return ""
	}
	if !isAllDigits(right) {
		return ""
	}
	return left + "-" + right
}

// pcDotVariant "claude-fable-5-2" → "claude-fable-5.2"（连字符版本号转点号）。
// 只在「-数字-数字」结尾时应用，避免误伤 claude-opus-4-5-20251101 这类日期串
func pcDotVariant(name string) string {
	i := strings.LastIndex(name, "-")
	if i <= 0 {
		return ""
	}
	rev := name[i+1:]
	if !isAllDigits(rev) {
		return ""
	}
	before := name[:i]
	j := strings.LastIndex(before, "-")
	if j <= 0 {
		return ""
	}
	mid := before[j+1:]
	if !isAllDigits(mid) {
		return ""
	}
	// 再往前一段必须是非数字（版本号前的主体）
	head := before[:j]
	if head == "" || isAllDigits(lastRune(head)) {
		return ""
	}
	return head + "-" + mid + "." + rev
}

func lastRune(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[len(r)-1])
}

func firstRune(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[0])
}

// pcFoldModelNames 把一组模型名按别名表折叠去重，保持首次出现顺序；被折叠掉的
// 别名替换为其规范名。返回折叠后的列表。
func pcFoldModelNames(names []string, fold map[string]string) []string {
	if len(fold) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		canon := n
		if c, ok := fold[n]; ok {
			canon = c
		}
		if seen[canon] {
			continue
		}
		seen[canon] = true
		out = append(out, canon)
	}
	return out
}

// ---- 折叠表缓存：/v1/models 是高频端点，不能每请求读盘 ----

var (
	pcFoldMu   sync.RWMutex
	pcFoldTab  map[string]string
	pcFoldAt   time.Time
)

// pcModelAliasFoldCached 带缓存的折叠表（60s TTL）：优先级列表变化后最迟一分钟
// 生效，期间复用同一份映射。/v1/models 热路径只做一次 map 查表，无磁盘 IO
func pcModelAliasFoldCached() map[string]string {
	pcFoldMu.RLock()
	if pcFoldTab != nil && time.Since(pcFoldAt) < time.Minute {
		tab := pcFoldTab
		pcFoldMu.RUnlock()
		return tab
	}
	pcFoldMu.RUnlock()

	tab := pcModelAliasFold(pcLoadPriority().Entries)

	pcFoldMu.Lock()
	pcFoldTab = tab
	pcFoldAt = time.Now()
	pcFoldMu.Unlock()
	return tab
}

// pcInvalidateAliasFold 优先级配置变更后立即失效缓存（保存/apply 时调用）
func pcInvalidateAliasFold() {
	pcFoldMu.Lock()
	pcFoldTab = nil
	pcFoldAt = time.Time{}
	pcFoldMu.Unlock()
}
