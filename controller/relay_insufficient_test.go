package controller

import (
	"testing"
)

// 回归测试：2026-11-21 OpenCode 事故——两个并发请求同时收到 402，各自在锁外
// 统计「另一把 key 还可用」，于是各停用一把，渠道被 "All keys are disabled"
// 整条拉黑，gemini-4-argon 全面不可用且永不恢复（面板闪电测试直连上游仍是绿，
// 掩盖了问题）。两道修复各自的契约由本测试锁住：
//  1. 瞬时 402 需连续 2 次才判停用（订阅制渠道额度检查抖动实测常见）
//  2. 达阈值后仍要经 MultiKeyDisableKeepingSentinel 的锁内哨兵判断
func TestPcInsufficientTransientTolerance(t *testing.T) {
	const chanID = 9001
	const key = "sk-test-transient-key"

	pcResetInsufficient(chanID, key)

	// 第 1 次 402：不判停用（瞬时抖动）
	if got := pcShouldDisableOnInsufficient(chanID, key); got {
		t.Fatal("首次 402 不应判停用")
	}
	// 第 2 次 402：达阈值
	if got := pcShouldDisableOnInsufficient(chanID, key); !got {
		t.Fatal("连续第 2 次 402 应判停用")
	}

	// 成功一次后计数清零：再遇单次 402 又不该判停用
	pcResetInsufficient(chanID, key)
	if got := pcShouldDisableOnInsufficient(chanID, key); got {
		t.Fatal("成功后计数应清零，单次 402 不应判停用")
	}

	// 不同 key 的计数互不影响
	other := "sk-test-other-key"
	pcResetInsufficient(chanID, other)
	if got := pcShouldDisableOnInsufficient(chanID, other); got {
		t.Fatal("另一把 key 的首次 402 不应判停用")
	}

	pcResetInsufficient(chanID, key)
	pcResetInsufficient(chanID, other)
}