package controller

import "testing"

// 回归测试：pcFetchGoPayg 曾因 html[np[1]:np[3]]（匹配尾 > 分组尾）切片越界
// panic，冷启动刷新一跑到 OpenCode Go 源整个进程闪退。fixture 复现 console 页
// 结构（sr-only 限额 + data-plan 值），验证解析出单价与限额且不再 panic
func TestTmpGoPaygParse(t *testing.T) {
	mdx := `| Model | Input | Output | Cached Read | Cached Write |
| --- | --- | --- | --- | --- |
| DeepSeek V4.1 Flash | $0.30 | $1.20 | $0.006 | - |
| Kimi K3 | $3.00 | $15.00 | $0.30 | $3.00 |
`
	html := `<table>
<tr><th class="console-go-models-name-line"><span class="console-go-models-model-name" data-layer="model-name" title="DeepSeek V4.1 Flash 1017">DeepSeek V4.1 Flash 1017</span></th>
<td><span class="sr-only">Go: $100; Go Plus: $200</span></td></tr>
<tr><th><span class="console-go-models-model-name" data-layer="model-name" title="Kimi K3">Kimi K3</span></th>
<td><span class="sr-only">Go: $15; Go Plus: $60</span></td></tr>
</table>`

	rows, ok := pcParseGoPayg(mdx, html)
	if !ok {
		t.Fatal("parse returned ok=false")
	}
	byName := map[string]pcGoModel{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	ds, ok1 := byName["DeepSeek V4.1 Flash"]
	if !ok1 {
		t.Fatalf("DeepSeek V4.1 Flash missing: %+v", rows)
	}
	if ds.Input != 0.30 || ds.Output != 1.20 || ds.CacheRead != 0.006 {
		t.Errorf("DeepSeek prices wrong: %+v", ds)
	}
	if ds.Limit != 100 {
		t.Errorf("DeepSeek limit = %v, want 100", ds.Limit)
	}
	k3, ok2 := byName["Kimi K3"]
	if !ok2 {
		t.Fatalf("Kimi K3 missing: %+v", rows)
	}
	if k3.Input != 3.00 || k3.Output != 15.00 || k3.CacheRead != 0.30 || k3.CacheWrite == nil || *k3.CacheWrite != 3.00 {
		t.Errorf("Kimi K3 prices wrong: %+v", k3)
	}
	if k3.Limit != 15 {
		t.Errorf("Kimi K3 limit = %v, want 15", k3.Limit)
	}
	t.Logf("parsed %d rows OK", len(rows))
}
