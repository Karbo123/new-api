package controller

import (
	"os"
	"regexp"
	"testing"
)

// 回归测试：apikl model-plaza 是 300KB+ 大载荷，经 CF 大陆链路经常只传回部分
// 数据就被 RST/超时（2026-11-21 实测 wsarecv 强制关闭）。截断容错解析必须能
// 从真实截断体中抽出分组与定价。fixture 为真实链路抓到的 254KB 残片
//（完整载荷 300KB+，尾部被切断——正规 json.Unmarshal 必失败）
func TestPcParseApiklPlazaPartial(t *testing.T) {
	raw, err := os.ReadFile("testdata/apikl_plaza_truncated.json")
	if err != nil {
		t.Skipf("fixture 不可读: %v", err)
	}
	if n := len(regexp.MustCompile(`"rate_multiplier":([0-9.]+)`).FindAllString(string(raw), -1)); n == 0 {
		t.Fatalf("fixture 本身无 rate_multiplier（读取异常），len=%d", len(raw))
	} else {
		t.Logf("fixture rate_multiplier 命中=%d", n)
	}
	resp := pcParseApiklPlazaPartial(string(raw))
	if len(resp.Data.Groups) == 0 {
		t.Fatal("截断体未解析出任何分组")
	}
	found := map[string][2]float64{}
	for _, g := range resp.Data.Groups {
		if g.Rate <= 0 {
			continue
		}
		for _, m := range g.Models {
			if m.Pricing.InputPrice != nil {
				in := *m.Pricing.InputPrice * 1e6 * g.Rate
				out := 0.0
				if m.Pricing.OutputPrice != nil {
					out = *m.Pricing.OutputPrice * 1e6 * g.Rate
				}
				found[m.Name+"@"+g.Name] = [2]float64{in, out}
			}
		}
	}
	// 真实截断体中应包含 claude 系（残片已传到该字母段）
	opus, ok := found["claude-opus-5-5@Krio"]
	if !ok {
		t.Fatalf("未解析出 claude-opus-5-5@Krio；已解析组=%d", len(resp.Data.Groups))
	}
	// Krio 组 0.06 倍率 × 组内价 $4/M（input_price 0.000004/token）→ 0.24
	if opus[0] < 0.2 || opus[0] > 0.28 {
		t.Fatalf("opus-5-5@Krio 输入价异常: %v", opus[0])
	}
	t.Logf("解析出组=%d，opus-5-5@Krio in/out = %v", len(resp.Data.Groups), opus)
}
