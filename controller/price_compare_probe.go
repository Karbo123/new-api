package controller

// 比价面板的渠道探测：
//   ① GET  /api/price_compare/keys/access  逐 key 调上游 /v1/models（免费），
//      探测每把 key 实际可见/可调的模型集合——中转站常把 key 绑定到特定分组
//      （如 apikl 的 Gemini 0.6x 组只能调 Gemini），面板行情里的报价未必真的可调。
//   ② POST /api/price_compare/test_offer   对单条报价发一个 max_tokens=1 的最小
//      请求（输入 "hi" 一个词，成本 ≈ 百万分之一元），验证 渠道×模型×key 真实连通；
//      OpenRouter 报价额外钉供应商 slug + 禁 fallback，测的就是该行本身。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// 各渠道的默认网关基地址（优先用 DB 里该渠道自己配置的 base_url）
var pcProbeDefaultBase = map[string]string{
	"openrouter": "https://openrouter.ai/api",
	"opencode":   "https://opencode.ai/zen/go",
	"deepseek":   "https://api.deepseek.com",
	"buzzai":     "https://api.buzzgw.com",
	"apib":       "https://api.apib.ai",
	"apikl":      "https://apikl.ai",
	"ikun":       "https://subapi.xiaoye.lol",
}

type pcProbeKeyResult struct {
	Key    string   `json:"key"`
	Ok     bool     `json:"ok"`
	Count  int      `json:"count,omitempty"`
	Models []string `json:"models,omitempty"`
	Error  string   `json:"error,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

type pcProbeKindResult struct {
	Keys []pcProbeKeyResult `json:"keys"`
}

var pcAccessMu sync.Mutex
var pcAccessCache struct {
	at   time.Time
	data map[string]pcProbeKindResult
	keys map[string][]string // 探测时各渠道实际用的 key 列表：data[kind].Keys 按下标与它对齐
}

// pcProbeTarget 解析某渠道测试应使用的网关基地址与附加头：
// 基地址取名称含渠道关键字的启用渠道自己的 base_url（否则内置默认）；
// OpenCode 这类要求会话头的渠道继承渠道 header_override 里的自定义头
func pcProbeTarget(kind string) (string, map[string]string) {
	headers := map[string]string{}
	base := ""
	if model.DB != nil {
		var bases, overrides []string
		model.DB.Table("channels").
			Where("status = ? AND LOWER(name) LIKE ?", common.ChannelStatusEnabled, "%"+kind+"%").
			Order("id").Pluck("base_url", &bases)
		for _, b := range bases {
			if b = strings.TrimRight(strings.TrimSpace(b), "/"); b != "" {
				base = b
				break
			}
		}
		model.DB.Table("channels").
			Where("status = ? AND LOWER(name) LIKE ? AND header_override IS NOT NULL AND header_override LIKE ?",
				common.ChannelStatusEnabled, "%"+kind+"%", "%x-opencode-session%").
			Order("id").Pluck("header_override", &overrides)
		for _, ov := range overrides {
			var m map[string]string
			if json.Unmarshal([]byte(ov), &m) == nil {
				if s := strings.TrimSpace(m["x-opencode-session"]); s != "" {
					headers["x-opencode-session"] = s
					break
				}
			}
		}
	}
	if base == "" {
		base = pcProbeDefaultBase[kind]
	}
	return base, headers
}

// pcProbeModels 用一把 key 调上游 /v1/models，返回该 key 可见的模型 id 列表
func pcProbeModels(base, key string, headers map[string]string) ([]string, int, string, string) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/v1/models", nil)
	if err != nil {
		return nil, 0, "request_failed", err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "request_failed", err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // OR /v1/models 有数 MB
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, pcBalErrCode(resp.StatusCode), strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	var out struct {
		Data []struct {
			Id string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Data) == 0 {
		return nil, resp.StatusCode, "bad_response", "no models in response"
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.Id != "" {
			ids = append(ids, m.Id)
		}
	}
	sort.Strings(ids)
	return ids, resp.StatusCode, "", ""
}

// GetPriceCompareKeyAccess 逐渠道逐 key 探测可见模型（/v1/models 免费），10 分钟缓存
func GetPriceCompareKeyAccess(c *gin.Context) {
	pcLoadCacheOnce() // 确保 OR 供应商 slug 对照表等冷启动数据就绪
	force := c.Query("refresh") == "1"
	pcAccessMu.Lock()
	if !force && pcAccessCache.data != nil && time.Since(pcAccessCache.at) < 10*time.Minute {
		d := pcAccessCache.data
		pcAccessMu.Unlock()
		c.JSON(200, gin.H{"success": true, "data": d, "cached": true})
		return
	}
	pcAccessMu.Unlock()

	pcRefreshAccessCache()

	pcAccessMu.Lock()
	d := pcAccessCache.data
	pcAccessMu.Unlock()
	c.JSON(200, gin.H{"success": true, "data": d})
}

// pcRefreshAccessCache 逐渠道逐 key 探测可见模型并发布 key 级路由注册表（/v1/models
// 免费端点，不带 token 不花钱）。面板打开、apply 渠道同步与保险箱保存后都会调用：
// 否则刚部署/刷新后注册表为空或过期，多 key 管理渠道轮询可能先选中「看不见该模型」
// 的 key 直接 404（上游分组白名单因 key 而异，中转站尤其常见）
func pcRefreshAccessCache() {
	kv := pcLoadKeys()
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]pcProbeKindResult{}
	probed := map[string][]string{} // 探测时的 key 列表快照：使用 data 前须比对，防增删 key 后下标错位
	for _, kk := range pcKeyKinds {
		keys := *kk.Vault(&kv)
		if len(keys) == 0 {
			continue
		}
		probed[kk.Kind] = keys
		base, headers := pcProbeTarget(kk.Kind)
		wg.Add(1)
		go func(kind, base string, keys []string) {
			defer wg.Done()
			res := pcProbeKindResult{}
			for _, k := range keys {
				models, code, errCode, detail := pcProbeModels(base, k, headers)
				if errCode != "" {
					if detail == "" {
						detail = fmt.Sprintf("http %d", code)
					}
					res.Keys = append(res.Keys, pcProbeKeyResult{Key: pcMaskKey(k), Ok: false, Error: errCode, Detail: detail})
					continue
				}
				res.Keys = append(res.Keys, pcProbeKeyResult{Key: pcMaskKey(k), Ok: true, Count: len(models), Models: models})
			}
			mu.Lock()
			out[kind] = res
			mu.Unlock()
		}(kk.Kind, base, keys)
	}
	wg.Wait()

	pcAccessMu.Lock()
	pcAccessCache.at = time.Now()
	pcAccessCache.data = out
	pcAccessCache.keys = probed
	pcAccessMu.Unlock()
	pcPublishKeyModelRoute() // 可见性数据变化 → 重新发布按模型选 key 注册表
}

// pcPublishKeyModelRoute 把逐 key 可见模型发布给请求链路（service 注册表）：
// 多 key 管理渠道收到请求时按上游模型在「可见它的 key」里轮询。探测数据缺失
// 时发布空表（选 key 回退默认轮询，行为不变）。key 下标与渠道 Key 串的行序
// 一致（都来自保险箱顺序），发布前重新读渠道 ID 防同步后 id 变化；保险箱
// key 集与探测快照不一致（探测后增删过 key）的渠道跳过，防下标错位发布。
func pcPublishKeyModelRoute() {
	out := map[int]map[string][]int{}
	if model.DB == nil {
		service.SetKeyModelRoute(out)
		return
	}
	pcAccessMu.Lock()
	acc, accAt, accKeys := pcAccessCache.data, pcAccessCache.at, pcAccessCache.keys
	pcAccessMu.Unlock()
	// 探测数据过期（与试测同样按 10 分钟计）视同缺失：宁空表回退默认轮询，不发布过时路由
	if acc == nil || time.Since(accAt) >= 10*time.Minute {
		service.SetKeyModelRoute(out)
		return
	}
	kv := pcLoadKeys()
	for _, kind := range []string{"openrouter", "opencode", "deepseek", "apib", "buzzai", "apikl", "ikun"} {
		kr, ok := acc[kind]
		if !ok {
			continue
		}
		keys := pcKindVaultKeys(&kv, kind)
		if !slices.Equal(keys, accKeys[kind]) {
			continue
		}
		vm := map[string][]int{}
		for i := range keys {
			if i >= len(kr.Keys) || !kr.Keys[i].Ok {
				continue
			}
			for _, m := range kr.Keys[i].Models {
				vm[m] = append(vm[m], i)
			}
		}
		if len(vm) == 0 {
			continue
		}
		var ids []int
		if err := model.DB.Table("channels").
			Where("name = ? AND status = ?", pcManagedChanName(kind), common.ChannelStatusEnabled).
			Pluck("id", &ids).Error; err != nil {
			continue
		}
		for _, id := range ids {
			out[id] = vm
		}
	}
	service.SetKeyModelRoute(out)
}

// ---- 单条报价连通性测试 ----

type pcTestOfferResult struct {
	Ok          bool   `json:"ok"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	ServedModel string `json:"served_model,omitempty"`
	Provider    string `json:"provider,omitempty"` // OpenRouter 实际服务的供应商
	PromptTok   int    `json:"prompt_tokens,omitempty"`
	Completion  int    `json:"completion_tokens,omitempty"`
	Status      int    `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	KeyMask     string `json:"key_mask,omitempty"`
	Base        string `json:"base,omitempty"`
}

var pcTestMu sync.Mutex
var pcTestCache = map[string]struct {
	at time.Time
	r  pcTestOfferResult
}{}

// pcTestHttpPost 发一次最小 chat 请求，返回 (结果, 是否因 max_tokens 太小失败)
func pcTestHttpPost(base, key, model string, providerPin []string, maxTokens int, headers map[string]string) (pcTestOfferResult, bool) {
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     false,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	if len(providerPin) > 0 {
		body["provider"] = map[string]interface{}{"order": providerPin, "allow_fallbacks": false}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return pcTestOfferResult{Ok: false, Error: err.Error()}, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return pcTestOfferResult{Ok: false, LatencyMs: latency, Error: err.Error()}, false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var out struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Message string `json:"message"` // 部分 new-api 系错误形态
		Code    any    `json:"code"`
	}
	_ = json.Unmarshal(raw, &out)
	r := pcTestOfferResult{LatencyMs: latency, ServedModel: out.Model, Provider: out.Provider,
		PromptTok: out.Usage.PromptTokens, Completion: out.Usage.CompletionTokens, Status: resp.StatusCode}
	if resp.StatusCode == http.StatusOK {
		r.Ok = true
		return r, false
	}
	msg := strings.TrimSpace(out.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(out.Message)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	r.Error = msg
	if r.Error == "" {
		r.Error = fmt.Sprintf("http %d", resp.StatusCode)
	}
	small := strings.Contains(strings.ToLower(msg), "max_token") // 含 max_tokens 等全部变体
	return r, small
}

// pcTestRateLimited 上游限流（HTTP 429 或限流文案）：这类失败是我们发得太快
// 或账号配额临时耗尽，不代表报价死了——拿去判死会造成假阴性（尤其免费档批量
// 实测与 apply 过滤都是 8~4 并发突发），只视为「不定」：不标记、不剔除。
func pcTestRateLimited(r pcTestOfferResult) bool {
	if r.Status == http.StatusTooManyRequests {
		return true
	}
	e := strings.ToLower(r.Error)
	return strings.Contains(e, "rate limit") || strings.Contains(e, "rate_limit") ||
		strings.Contains(e, "too many requests")
}

// pcKindVaultKeys 按 kind 取保险箱里该渠道的 key 列表
func pcKindVaultKeys(kv *pcKeyVault, kind string) []string {
	for _, kk := range pcKeyKinds {
		if kk.Kind == kind {
			return *kk.Vault(kv)
		}
	}
	return nil
}

// PostPriceCompareTestOffer 手动测试按钮：一条报价，结果缓存 60s
func PostPriceCompareTestOffer(c *gin.Context) {
	pcLoadCacheOnce() // slug 对照表等冷启动数据就绪后再发探测请求
	var req struct {
		Channel  string `json:"channel"`
		Ref      string `json:"ref"`
		Provider string `json:"provider"`
		Quant    string `json:"quant"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Channel == "" || req.Ref == "" {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	ttl := 60 * time.Second
	if c.Query("refresh") == "1" {
		// refresh=1：按钮重复点击=真重测，绕过 60s 缓存读取；ttl=0 使缓存永不
		// 命中，只发一次真实请求且结果仍回写缓存
		ttl = 0
	}
	r := pcTestOfferCached(strings.ToLower(req.Channel), req.Ref, req.Provider, req.Quant,
		ttl, false)
	c.JSON(200, gin.H{"success": true, "data": r})
}

// pcTestCand 一条待测报价（Period 不参与：同渠道同供应商同时段上下档生死一致）
type pcTestCand struct {
	Kind     string
	Ref      string
	Provider string
	Quant    string
}

func (c pcTestCand) key() string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s", c.Kind, c.Ref, c.Provider, c.Quant)
}

// pcTestOfferCached 带 TTL 的缓存包装（按钮 60s、apply 批量 6h）
func pcTestOfferCached(kind, ref, provider, quant string, ttl time.Duration, strictAccess bool) pcTestOfferResult {
	cacheKey := (pcTestCand{Kind: kind, Ref: ref, Provider: provider, Quant: quant}).key()
	pcTestMu.Lock()
	if hit, ok := pcTestCache[cacheKey]; ok && time.Since(hit.at) < ttl {
		pcTestMu.Unlock()
		return hit.r
	}
	pcTestMu.Unlock()
	r := pcTestOfferCore(kind, ref, provider, quant, strictAccess)
	pcTestMu.Lock()
	pcTestCache[cacheKey] = struct {
		at time.Time
		r  pcTestOfferResult
	}{time.Now(), r}
	// 缓存防膨胀：超过 512 条直接清空（结果本身短 TTL，损失可忽略）
	if len(pcTestCache) > 512 {
		pcTestCache = map[string]struct {
			at time.Time
			r  pcTestOfferResult
		}{}
	}
	pcTestMu.Unlock()
	return r
}

// pcTestOfferCore 测一条报价：逐把 vault key 尝试（access 探测结果可用时优先试
// “可见该 ref”的 key，失败的请求不产生费用），任一成功即返回。
// strictAccess=true（分组白名单已证实强约束的 sub2api 系）：access 数据新鲜、
// key 集与探测时一致且全部 key 探测成功时，没有任何 key 可见该 ref → 直接判死，
// 零成本零请求；任一 key 探测失败（429/超时/网络错误）= 不定，不据此判死
func pcTestOfferCore(kind, ref, provider, quant string, strictAccess bool) pcTestOfferResult {
	kv := pcLoadKeys()
	keys := pcKindVaultKeys(&kv, kind)
	if len(keys) == 0 {
		return pcTestOfferResult{Ok: false, Error: "no_key"}
	}
	base, headers := pcProbeTarget(kind)

	pcAccessMu.Lock()
	acc, accAt, accKeys := pcAccessCache.data, pcAccessCache.at, pcAccessCache.keys
	pcAccessMu.Unlock()
	// key 集与探测快照一致才可按下标对齐使用可见性数据（探测后增删过 key 即失效）
	if kr, found := acc[kind]; found && time.Since(accAt) < 10*time.Minute && slices.Equal(keys, accKeys[kind]) {
		var visible []string
		undetermined := false
		for i, k := range keys {
			if !kr.Keys[i].Ok {
				// 429/超时/网络错误（request_failed）≠ 看不见该模型，是不定；
				// 只有 key 本身失效（invalid_key）才确定看不见
				if kr.Keys[i].Error != "invalid_key" {
					undetermined = true
				}
				continue
			}
			for _, m := range kr.Keys[i].Models {
				if m == ref {
					visible = append(visible, k)
					break
				}
			}
		}
		if len(visible) > 0 {
			keys = visible
		} else if strictAccess && !undetermined {
			return pcTestOfferResult{Ok: false,
				Error: "key 可见模型里没有该模型（/v1/models 探测，分组白名单）"}
		}
	}

	// OpenRouter 供应商钉扎 slug：官方对照表优先（InferenceNet→inference-net 等
	// 不规则映射），量化后缀照旧
	var pin []string
	if kind == "openrouter" && provider != "" {
		q := quant
		if q == "—" {
			q = ""
		}
		if s := orProviderSlug(provider, q); s != "" {
			pin = []string{s}
		}
	}

	var last pcTestOfferResult
	for _, k := range keys {
		r, smallMax := pcTestHttpPost(base, k, ref, pin, 1, headers)
		if !r.Ok && smallMax {
			// 推理模型常拒绝 max_tokens=1：放宽到 2048 重试一次（成本仍可忽略）
			r, _ = pcTestHttpPost(base, k, ref, pin, 2048, headers)
		}
		r.KeyMask = pcMaskKey(k)
		r.Base = base
		last = r
		if r.Ok {
			break
		}
	}
	return last
}

// pcTestOffersParallel 并行测一批报价（conc 并发；<=0 取 8），返回候选 key → 结果。
// ttl=结果缓存时长（apply 批量 6h：同一轮自动更新内不重复花钱）；strictKinds 里
// 的渠道在 access 数据可用时零成本预判剔除。并发是给上游限流的节流阀：批量
// 打同一账号时建议 4，避免挤占生产流量的每分钟配额
func pcTestOffersParallel(cands []pcTestCand, ttl time.Duration, strictKinds map[string]bool, conc int) map[string]pcTestOfferResult {
	if conc <= 0 {
		conc = 8
	}
	out := make(map[string]pcTestOfferResult, len(cands))
	var mu sync.Mutex
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, c := range cands {
		wg.Add(1)
		go func(c pcTestCand) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := pcTestOfferCached(c.Kind, c.Ref, c.Provider, c.Quant, ttl, strictKinds[c.Kind])
			mu.Lock()
			out[c.key()] = r
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	return out
}
