package controller

// 中转站定价接入（buzzai.cc / apikl.ai / apib.ai / ikun），与 OpenCode Go / DeepSeek
// 官方同一套「拉取→缓存→并入统一行情」模式。各家充值经济口径不同（以用户实际
// 付款为准），抓取时统一折算：
//   buzzai.cc  new-api 系：模型价为名义美元，但充值按真实美元结算
//              （/api/status 的 usd_exchange_rate≈6.81，$10 实付 60 多元）
//              → CNY = model_ratio×2 × 组倍率 × usd_exchange_rate；
//   apikl.ai   sub2api 系：充值 1:1（¥1 = $1 额度）
//              → CNY = 官方$ × 组倍率 ×1（/api/v1/model-plaza 公开）；
//   ikun       sub2api 系公益站：充值 1:10（¥1 = $10 额度）
//              → CNY = 官方$ × 组倍率 ×0.1（/model-square/data 运行时快照）；
//   apib.ai    美元原生结算（1 credit=$0.1，充 100 credits=$10=¥70）：站方定价页
//              有 307 门禁暂无公开接口，定价未接入行情（仅 key 保险箱/余额）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	pcResellerTTL = 6 * 3600
	pcApiklBase   = "https://apikl.ai"
	pcApiklURL    = pcApiklBase + "/api/v1/model-plaza?timezone=Etc%2FGMT-8"
	pcBuzzaiBase  = "https://api.buzzgw.com"
	pcIkunBase    = "https://subapi.xiaoye.lol"
)

// pcResellerModel 中转站报价归一化行。Channel ∈ {buzzai, apikl, apib}；
// Currency=CNY 表示抓取时已按该站充值口径折算完毕，USD 表示美元原生
// （构建行情时按实时汇率换算，同 OpenRouter 口径）。
type pcResellerModel struct {
	Channel  string  `json:"channel"`
	Name     string  `json:"name"`
	Currency string  `json:"currency"`
	In       float64 `json:"in"`
	Out      float64 `json:"out"`
	Read     float64 `json:"read"`
	Write    float64 `json:"write"`
	Note     string  `json:"note,omitempty"`
	// GroupRate 报价实际采用的分组倍率（同模型多组取实付最低组）。前端用它把
	// 「市场最低组价」换算成某把 key 自己的组价：key 价 = 行价 × key组倍率/本倍率
	GroupRate float64 `json:"group_rate,omitempty"`
}

func pcFetchReseller(force bool, at *int64, cur *[]pcResellerModel, n int,
	fetch func() ([]pcResellerModel, error)) {
	pcMu.Lock()
	if !force && len(*cur) > 0 && time.Now().Unix()-(*at) < pcResellerTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	rows, err := fetch()
	if err != nil || len(rows) == 0 {
		// 失败保留旧缓存（与 LiveBench 等源的容错口径一致），只记日志
		if err != nil {
			common.SysLog(fmt.Sprintf("price_compare reseller[%d] fetch err: %v", n, err))
		}
		return
	}
	pcMu.Lock()
	*cur = rows
	*at = time.Now().Unix()
	pcMu.Unlock()
}

// ---- BuzzAI（api.buzzgw.com，new-api 系）：ratio 计价，充值按真实美元结算 ----

func pcFetchBuzzaiModels(force bool) {
	pcFetchReseller(force, &pcCache.BzAt, &pcCache.BzModels, 1, func() ([]pcResellerModel, error) {
		return pcFetchBuzzaiModelsImpl()
	})
}

func pcFetchBuzzaiModelsImpl() ([]pcResellerModel, error) {
	body, err := pcHttpGet(pcBuzzaiBase+"/api/pricing", 30*time.Second, nil)
	if err != nil {
		return nil, err
	}
	var pricingResp struct {
		Data []struct {
			ModelName       string   `json:"model_name"`
			ModelRatio      float64  `json:"model_ratio"`
			ModelPrice      float64  `json:"model_price"`
			CompletionRatio float64  `json:"completion_ratio"`
			CacheRatio      float64  `json:"cache_ratio"`
			CreateCache     float64  `json:"create_cache_ratio"`
			EnableGroups    []string `json:"enable_groups"`
			QuotaType       int      `json:"quota_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &pricingResp); err != nil {
		return nil, err
	}
	// 组倍率表（公开端点；比例 0 的 Free 组是受限福利组，不作为价格口径）
	grBody, err := pcHttpGet(pcBuzzaiBase+"/api/user/groups", 30*time.Second, nil)
	if err != nil {
		return nil, err
	}
	var grResp struct {
		Data map[string]struct {
			// ratio 宽松解码：new-api 站启用 auto 组时该端点输出字符串 ratio
			//（「自动」），严格 float64 会让整份 JSON 解析失败
			Ratio interface{} `json:"ratio"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(grBody), &grResp); err != nil {
		return nil, err
	}
	// 充值按真实美元结算（$10 实付 60 多元）：折算因子取站方美元汇率
	factor := pcFetchBuzzaiExchangeRate()
	out := make([]pcResellerModel, 0, len(pricingResp.Data))
	for _, m := range pricingResp.Data {
		if m.QuotaType != 0 || m.ModelPrice > 0 {
			continue // 按次计价的模型（生图等）不进 token 比价
		}
		// 最低有效组倍率：enable_groups 顺序由服务端 map 迭代产生（不定），
		// 取 ratio>0 的最小组（同 ikun 口径）；无有效组时 gr=0，由下方 in<=0 跳过
		gr, gname := 0.0, ""
		for _, g := range m.EnableGroups {
			ratio, ok := grResp.Data[g].Ratio.(float64)
			if !ok || ratio <= 0 {
				continue
			}
			if gr == 0 || ratio < gr {
				gr, gname = ratio, g
			}
		}
		in := m.ModelRatio * 2 * gr * factor // $/M = ratio×2，×美元汇率 = ¥/M
		out2 := m.ModelRatio * m.CompletionRatio * 2 * gr * factor
		if in <= 0 {
			continue
		}
		out = append(out, pcResellerModel{
			Channel: "buzzai", Name: m.ModelName, Currency: "CNY",
			In: in, Out: out2, Read: in * m.CacheRatio, Write: in * m.CreateCache,
			Note: "组 " + gname + " ×" + strconv.FormatFloat(gr, 'f', -1, 64) +
				" ×汇率" + strconv.FormatFloat(factor, 'f', -1, 64),
		})
	}
	return out, nil
}

// ---- APIKL（apikl.ai，sub2api 系）：官方$ ×组倍率 = 人民币（充值 1:1） ----

func pcFetchApiklModels(force bool) {
	pcFetchReseller(force, &pcCache.AkAt, &pcCache.AkModels, 2, func() ([]pcResellerModel, error) {
		return pcFetchApiklModelsImpl()
	})
}

func pcFetchApiklModelsImpl() ([]pcResellerModel, error) {
	body, err := pcHttpGet(pcApiklURL, 30*time.Second, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Groups []struct {
				Name   string  `json:"name"`
				Rate   float64 `json:"rate_multiplier"`
				Models []struct {
					Name    string `json:"name"`
					Pricing struct {
						BillingMode    string   `json:"billing_mode"`
						InputPrice     *float64 `json:"input_price"`
						OutputPrice    *float64 `json:"output_price"`
						CacheReadPrice *float64 `json:"cache_read_price"`
						CacheWrite     *float64 `json:"cache_write_price"`
					} `json:"pricing"`
				} `json:"models"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	// 同一模型多分组报价取实付最低组（组名进 Note）；充值 1:1（¥1=$1 额度）
	// → 实付人民币 = 官方$/M × 组倍率
	best := map[string]*pcResellerModel{}
	for _, g := range resp.Data.Groups {
		if g.Rate <= 0 {
			continue
		}
		f := g.Rate
		for _, m := range g.Models {
			if m.Pricing.BillingMode != "token" || m.Pricing.InputPrice == nil {
				continue // 生图按张计价等不进 token 比价
			}
			cand := pcResellerModel{
				Channel: "apikl", Name: m.Name, Currency: "CNY",
				In:    *m.Pricing.InputPrice * 1e6 * f,
				Out:   f64v(m.Pricing.OutputPrice) * 1e6 * f,
				Read:  f64v(m.Pricing.CacheReadPrice) * 1e6 * f,
				Write: f64v(m.Pricing.CacheWrite) * 1e6 * f,
				Note:  "组 " + g.Name, GroupRate: f,
			}
			if prev, ok := best[m.Name]; !ok || cand.In < prev.In {
				cp := cand
				best[m.Name] = &cp
			}
		}
	}
	out := make([]pcResellerModel, 0, len(best))
	for _, m := range best {
		if m.In <= 0 {
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}

func f64v(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- APIB（apib.ai）定价抓取 ----
// 走站内 Next.js RSC 私有通道，对服务器端请求 307 门禁（须浏览器会话 cookie），
// 暂无稳定公开接口，未接入定价行情（key/余额仍可用）。

// ---- 中转站余额（new-api 系网关：apib / buzzai）----
// OpenAI 兼容 billing 端点（key 鉴权）：/v1/dashboard/billing/subscription 的
// hard_limit_usd = 总额度（部分 fork 用 1e8 表示「不限」），/v1/dashboard/billing/usage
// 的 total_usage = 已用（美分）。余额 = 总额度 − 已用。sub2api 系（apikl/ikun）
// 无 key 鉴权的余额端点，不支持。
//
// pcQueryGatewayBalance usdToCny：该站 $1 额度的真实人民币成本（buzzai=站方美元
// 汇率 6.81，apib=面板实时汇率），余额/已用折算后再返回（Currency=CNY）。
func pcQueryGatewayBalance(base string, usdToCny func() float64, keys []string) pcProviderBalance {
	if len(keys) == 0 {
		return pcProviderBalance{Error: "no_key"}
	}
	f := usdToCny()
	var keysOut []pcKeyBalance
	okCount := 0
	unlimited := false
	var totBal, totUsed float64
	for _, k := range keys {
		kb := pcKeyBalance{Key: pcMaskKey(k)}
		sub, code, err := pcBalFetchJSON(base+"/v1/dashboard/billing/subscription", k)
		if err == nil && code == http.StatusOK {
			hard := pcBalToF64(sub["hard_limit_usd"])
			kb.Ok = true
			kb.Currency = "CNY" // 下方余额/已用均已折人民币
			var used *float64
			if u, ucode, uerr := pcBalFetchJSON(base+"/v1/dashboard/billing/usage", k); uerr == nil && ucode == http.StatusOK {
				v := pcBalToF64(u["total_usage"]) / 100 // 美分 → 美元
				used = &v
				kb.Used = cnvUSD(&v, f)
				totUsed += v
			}
			if hard >= 1e6 {
				// 站方「不限」哨兵（sk- token 无限额度型，真实剩余在用户钱包层）：
				// APIMart 系（apib）自定义 /v1/user/balance 用 sk- key 即可读真实剩余
				//（remain_balance 美元 + remain_credits 额度点，1 credit=$0.1）；
				// 端点后端节点不均、偶发挂起（实测 0.26s~15s+），失败重试一次；
				// 命中则覆盖哨兵行为，未命中保持「只报已用」
				ub, ucode, uerr := pcBalFetchJSON(base+"/v1/user/balance", k)
				if uerr != nil || ucode != http.StatusOK {
					ub, ucode, uerr = pcBalFetchJSON(base+"/v1/user/balance", k)
				}
				rem, has := ub["remain_balance"].(float64)
				if uerr != nil || ucode != http.StatusOK || !has || rem < 0 {
					// 未取到钱包余额（含 200 但缺 remain_balance 字段）：该 key
					// 只报已用，并抑制聚合余额，避免发布缺了该 key 的虚低总额
					unlimited = true
				} else {
					kb.Balance = cnvUSD(&rem, f)
					totBal += rem
				}
			} else {
				bal := hard - f64v(used)
				kb.Balance = cnvUSD(&bal, f)
				totBal += bal
			}
			okCount++
		} else {
			if err != nil {
				kb.Error, kb.Detail = "request_failed", err.Error()
			} else {
				kb.Error, kb.Detail = pcBalErrCode(code), fmt.Sprintf("http %d", code)
			}
		}
		keysOut = append(keysOut, kb)
	}
	if okCount == 0 {
		return pcProviderBalance{Error: keysOut[0].Error, Detail: keysOut[0].Detail, Keys: keysOut}
	}
	r := pcProviderBalance{Ok: true, Currency: "CNY", Keys: keysOut}
	if !unlimited {
		r.Balance = cnvUSD(&totBal, f)
	}
	r.Used = cnvUSD(&totUsed, f)
	if okCount < len(keys) {
		r.Detail = fmt.Sprintf("%d/%d keys failed", len(keys)-okCount, len(keys))
	}
	return r
}

// cnvUSD 美元金额 × 汇率（返回新指针，避免顺带改掉累计变量）
func cnvUSD(p *float64, rate float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p * rate
	return &v
}

func pcFetchBuzzaiExchangeRate() float64 {
	if stBody, err := pcHttpGet(pcBuzzaiBase+"/api/status", 30*time.Second, nil); err == nil {
		var st struct {
			Data struct {
				UsdExchangeRate float64 `json:"usd_exchange_rate"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(stBody), &st) == nil && st.Data.UsdExchangeRate > 0 {
			return st.Data.UsdExchangeRate
		}
	}
	return 6.81
}

// ---- IKUN（subapi.xiaoye.lol，sub2api 公益站）----
// 定价快照由站方模型广场页公开再生成（/model-square/data/sub2api-runtime.json，
// 页面 /custom/model-square 即指向它）：models[].pricing 为官方美元/token
// （LiteLLM 口径），models[].groups[].rate_multiplier 为分组倍率，页面显示
// 「最低分组价」。充值口径（用户实际付款）：1:10，¥1 = $10 官方额度
// → 折算因子 0.1。
const (
	pcIkunURL          = pcIkunBase + "/model-square/data/sub2api-runtime.json"
	pcIkunCnyPerCredit = 0.1
)

func pcFetchIkunModels(force bool) {
	pcFetchReseller(force, &pcCache.IkAt, &pcCache.IkModels, 4, func() ([]pcResellerModel, error) {
		return pcFetchIkunModelsImpl()
	})
}

func pcFetchIkunModelsImpl() ([]pcResellerModel, error) {
	body, err := pcHttpGet(pcIkunURL, 40*time.Second, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Models []struct {
			Name    string `json:"model_name"`
			Pricing struct {
				BillingMode string   `json:"billing_mode"`
				In          *float64 `json:"input_cost_per_token"`
				Out         *float64 `json:"output_cost_per_token"`
				Read        *float64 `json:"cache_read_input_token_cost"`
				Write       *float64 `json:"cache_creation_input_token_cost"`
			} `json:"pricing"`
			Groups []struct {
				ID     int64   `json:"id"`
				Name   string  `json:"name"`
				Rate   float64 `json:"rate_multiplier"`
				Status string  `json:"status"`
			} `json:"groups"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	out := make([]pcResellerModel, 0, len(resp.Models))
	for _, m := range resp.Models {
		if m.Pricing.BillingMode != "token" || m.Pricing.In == nil || *m.Pricing.In <= 0 {
			continue
		}
		// 最低可用分组倍率（与站方页面「最低分组价」同口径）
		rate, gname := 0.0, ""
		for _, g := range m.Groups {
			if g.Status != "active" || g.Rate <= 0 {
				continue
			}
			if rate == 0 || g.Rate < rate {
				rate = g.Rate
				gname = g.Name
			}
		}
		if rate <= 0 {
			continue
		}
		f := rate * pcIkunCnyPerCredit
		out = append(out, pcResellerModel{
			Channel: "ikun", Name: m.Name, Currency: "CNY",
			In:    f64v(m.Pricing.In) * 1e6 * f,
			Out:   f64v(m.Pricing.Out) * 1e6 * f,
			Read:  f64v(m.Pricing.Read) * 1e6 * f,
			Write: f64v(m.Pricing.Write) * 1e6 * f,
			Note:  fmt.Sprintf("组 %s ×%g", gname, rate), GroupRate: rate,
		})
	}
	return out, nil
}

// pcQuerySub2apiBalance sub2api 系（apikl/ikun）余额：GET /v1/usage 是官方给
// CC Switch 集成用的 key 鉴权端点（gateway 组，sk- key 可过）。钱包模式
// balance=账户钱包余额（同账号多 key 返回同一值，聚合按唯一值求和防重复计）；
// 订阅模式没有 balance，退用 remaining。余额单位是站内"USD"额度，×cnyPerCredit
// 折人民币（ikun 充值 1:10→0.1、apikl 1:1→1）。再查 /v1/sub2api/billing 拿该
// key 所属组的倍率，并从 model_stats 提近期模型——同站多 key 常各限特定模型
// （如 ikun 一个 key 只能生图、另一个只能 GPT 组），这两个信息让每条 key 的身份可见。
func pcQuerySub2apiBalance(base string, cnyPerCredit float64, keys []string) pcProviderBalance {
	if len(keys) == 0 {
		return pcProviderBalance{Error: "no_key"}
	}
	var keysOut []pcKeyBalance
	okCount := 0
	sumCny := 0.0
	seenBal := map[float64]bool{}
	for _, k := range keys {
		kb := pcKeyBalance{Key: pcMaskKey(k)}
		m, code, err := pcBalFetchJSON(base+"/v1/usage?days=7", k)
		switch {
		case err != nil:
			kb.Error, kb.Detail = "request_failed", err.Error()
		case code != http.StatusOK:
			kb.Error, kb.Detail = pcBalErrCode(code), fmt.Sprintf("http %d", code)
		default:
			bal, has := m["balance"].(float64)
			if !has {
				bal, has = m["remaining"].(float64) // 订阅模式没有 balance 字段
			}
			if !has {
				kb.Error, kb.Detail = "bad_response", "balance missing"
				break
			}
			cny := bal * cnyPerCredit
			kb.Ok, kb.Balance, kb.BalanceCny = true, &bal, &cny
			if plan, _ := m["planName"].(string); plan != "" {
				kb.Plan = plan
			}
			if u, ok2 := m["usage"].(map[string]interface{}); ok2 {
				if tot, ok3 := u["total"].(map[string]interface{}); ok3 {
					if c, ok4 := tot["cost"].(float64); ok4 {
						kb.Used, kb.UsedCny = &c, cnvUSD(&c, cnyPerCredit)
					}
				}
			}
			type statRow struct {
				name string
				cost float64
			}
			var stats []statRow
			if arr, _ := m["model_stats"].([]interface{}); len(arr) > 0 {
				for _, st := range arr {
					sm, _ := st.(map[string]interface{})
					name, _ := sm["model"].(string)
					if name == "" {
						continue
					}
					stats = append(stats, statRow{name, pcBalToF64(sm["cost"])})
				}
			}
			sort.Slice(stats, func(i, j int) bool { return stats[i].cost > stats[j].cost })
			for _, s := range stats {
				kb.Models = append(kb.Models, s.name)
				if len(kb.Models) >= 3 {
					break
				}
			}
			okCount++
			if !seenBal[bal] { // 同账号多 key 钱包相同：唯一值才计入聚合
				seenBal[bal] = true
				sumCny += cny
			}
		}
		keysOut = append(keysOut, kb)
	}
	// 组倍率：第二个请求（失败不影响余额显示）；keysOut 与 keys 同下标
	for i := range keysOut {
		if !keysOut[i].Ok {
			continue
		}
		if bm, bcode, berr := pcBalFetchJSON(base+"/v1/sub2api/billing", keys[i]); berr == nil && bcode == http.StatusOK {
			if gr := pcBalToF64(bm["group_rate_multiplier"]); gr > 0 {
				keysOut[i].GroupRate = &gr
			}
		}
	}
	if okCount == 0 {
		return pcProviderBalance{Error: keysOut[0].Error, Detail: keysOut[0].Detail, Keys: keysOut}
	}
	r := pcProviderBalance{Ok: true, Currency: "CNY", Balance: &sumCny, Keys: keysOut}
	if okCount < len(keys) {
		r.Detail = fmt.Sprintf("%d/%d keys failed", len(keys)-okCount, len(keys))
	}
	return r
}

// pcResellerUnifiedRows 归一化报价 → 统一行情行（挂基准、按渠道各自币种）
func pcResellerUnifiedRows(ix *pcResolveIdx) []pcUnifiedRow {
	pcMu.Lock()
	rows := make([]pcResellerModel, 0, len(pcCache.BzModels)+len(pcCache.AkModels)+len(pcCache.IkModels))
	rows = append(rows, pcCache.BzModels...)
	rows = append(rows, pcCache.AkModels...)
	rows = append(rows, pcCache.IkModels...)
	pcMu.Unlock()
	out := []pcUnifiedRow{}
	seen := map[string]bool{}
	for _, m := range rows {
		dk := m.Channel + "\x00" + m.Name
		if seen[dk] {
			continue
		}
		seen[dk] = true
		rowKey, rowName, _, _ := ix.resolve(m.Name, m.Name)
		if rowKey == "" {
			rowKey, rowName = pcCanonKey(m.Name), m.Name
		}
		lbName, lbSim := pcMatchLivebench(rowName, m.Name)
		var lbs map[string]*float64
		if lbName != "" {
			lbs = pcLbSummary(lbName)
		}
		row := pcUnifiedRow{
			Channel: m.Channel, Ref: m.Name, Name: rowName,
			FullName: m.Name, Key: rowKey,
			Vendor:   pcVendorOf(m.Name),
			Currency: m.Currency, Multiplier: 1,
			In: m.In, Out: m.Out, Read: m.Read, Write: m.Write,
			Provider: "—", Quant: "—",
			Note: m.Note, GroupRate: m.GroupRate,
		}
		if lbName != "" {
			row.LbName = &lbName
			row.LbSim = lbSim
			row.Lb = lbs
		}
		row.Arena = pcMatchArenaCached(m.Name)
		out = append(out, row)
	}
	return out
}
