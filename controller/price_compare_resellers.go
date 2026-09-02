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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const (
	pcResellerTTL = 6 * 3600
	pcApiklBase   = "https://api.apikl.ai" // 2026-10 起旧主站/东京/香港端点停用，仅 api.apikl.ai 可用
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
// 「最低分组价」。充值口径（用户实际付款）：2026-11-16 站方公告起改为 1:1
// （¥1 = $1 站内额度，原 1:10；站方倍率同步整体 ÷10，净价不变）→ 折算因子 1.0
const (
	// pcIkunCnyPerCredit 每元人民币对应的站内「刀」额度（2026-11-16 起 1:1，
	// 原 1:10；目录接口 data.credits_per_cny 优先，此值为兜底）
	pcIkunCnyPerCredit = 1.0
)

func pcFetchIkunModels(force bool) {
	pcFetchReseller(force, &pcCache.IkAt, &pcCache.IkModels, 4, func() ([]pcResellerModel, error) {
		return pcFetchIkunModelsImpl()
	})
}

// ---- iKun 登录态（2026-11 站方改版：公开运行时快照下线，模型目录改为登录态 API）----
// 目录接口 GET /api/v1/workspace/models 需要控制台会话（JWT），API key 无效；
// 会话保存在 options 表（仅用于拉取模型目录）。刷新端点每次轮换 refresh_token，
// 必须把新的一对写回存储，否则下一次续期失败。

const (
	pcIkunAuthKey   = "price_compare_ikun_auth"
	pcIkunModelsAPI = pcIkunBase + "/api/v1/workspace/models?timezone=Etc%2FGMT-8"
)

type pcIkunAuth struct {
	Token   string `json:"token"`
	Refresh string `json:"refresh"`
	Exp     int64  `json:"exp"` // access token 过期时间（unix 秒）
	// Email/Password 最终兜底：token 与 refresh 双双失效（站方强制重登/轮换链
	// 断掉）时自动重新登录，免人工粘贴 token。仅存于本机 options 表
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

func pcIkunLoadAuth() pcIkunAuth {
	var a pcIkunAuth
	if s := pcDbGet(pcIkunAuthKey); s != "" {
		_ = json.Unmarshal([]byte(s), &a)
	}
	return a
}

func pcIkunSaveAuth(a pcIkunAuth) {
	if b, err := json.Marshal(a); err == nil {
		pcDbSet(pcIkunAuthKey, string(b))
	}
}

// pcIkunJwtExp 解出 JWT 的 exp（expires_in 缺失时兜底）
func pcIkunJwtExp(tok string) int64 {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var p struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return 0
	}
	return p.Exp
}

// pcIkunRefreshToken 用 refresh_token 换新会话并写回（站方每次刷新都轮换 refresh）
func pcIkunRefreshToken(a *pcIkunAuth) error {
	if a.Refresh == "" {
		return fmt.Errorf("ikun auth: refresh_token missing")
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": a.Refresh})
	req, err := http.NewRequest(http.MethodPost, pcIkunBase+"/api/v1/auth/refresh", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", pcUA)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	var out struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Data.AccessToken == "" {
		return fmt.Errorf("ikun refresh failed: http %d", resp.StatusCode)
	}
	a.Token = out.Data.AccessToken
	if out.Data.RefreshToken != "" {
		a.Refresh = out.Data.RefreshToken
	}
	if out.Data.ExpiresIn > 0 {
		a.Exp = time.Now().Unix() + out.Data.ExpiresIn
	} else {
		a.Exp = pcIkunJwtExp(out.Data.AccessToken)
	}
	pcIkunSaveAuth(*a)
	return nil
}

// pcIkunLogin 账号密码登录（token 与 refresh 双双失效时的最终兜底）。站方登录
// 可能要求人机验证或 2FA——此时返回明确错误，不会静默失败
func pcIkunLogin(a *pcIkunAuth) error {
	if a.Email == "" || a.Password == "" {
		return fmt.Errorf("ikun auth: 未配置账号密码，无法自动登录")
	}
	body, _ := json.Marshal(map[string]string{"email": a.Email, "password": a.Password})
	req, err := http.NewRequest(http.MethodPost, pcIkunBase+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", pcUA)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Data.AccessToken == "" {
		msg := strings.TrimSpace(out.Message)
		if msg == "" {
			msg = strings.TrimSpace(common.LocalLogPreview(string(raw)))
		}
		return fmt.Errorf("ikun 自动登录失败（http %d）：%s", resp.StatusCode, msg)
	}
	a.Token = out.Data.AccessToken
	if out.Data.RefreshToken != "" {
		a.Refresh = out.Data.RefreshToken
	}
	if out.Data.ExpiresIn > 0 {
		a.Exp = time.Now().Unix() + out.Data.ExpiresIn
	} else {
		a.Exp = pcIkunJwtExp(out.Data.AccessToken)
	}
	pcIkunSaveAuth(*a)
	return nil
}

// pcIkunRefreshAhead 主动续期提前量：每次抓取（约 6 小时一轮）只要 token 剩余
// 有效期低于该值就滚动刷新一次，让 refresh 链持续翻滚、永不过期——比「临期前
// 10 分钟才续」留出大得多的容错窗口（进程短暂停机/网络抖动都不会断链）。
// 登录接口被 Cloudflare Turnstile 人机验证挡住，账号密码自动重登不可行，
// 保持 refresh 链常青是零人工方案的关键
const pcIkunRefreshAhead = 20 * 3600

// pcIkunEnsureSession 保证可用会话：有效 token 直接用 → 临近过期/失效先 refresh
// （主动滚动续期）→ 仍不行尝试账号密码登录（站方有人机验证时此层不可用，仅
// 作为理论兜底）。force=true 表示当前 token 已被服务端拒绝（401），立即走兜底链。
// 全部失败时返回最后一层错误供日志/面板提示
func pcIkunEnsureSession(force bool) (string, error) {
	a := pcIkunLoadAuth()
	now := time.Now().Unix()
	usable := a.Token != "" && (a.Exp == 0 || a.Exp-now > 120)
	if usable && !force && (a.Exp == 0 || a.Exp-now > pcIkunRefreshAhead) {
		return a.Token, nil
	}
	var lastErr error
	if a.Refresh != "" {
		if err := pcIkunRefreshToken(&a); err == nil && a.Token != "" {
			return a.Token, nil
		} else if err != nil {
			lastErr = err
		}
	}
	if a.Email != "" && a.Password != "" {
		if err := pcIkunLogin(&a); err == nil && a.Token != "" {
			return a.Token, nil
		} else if err != nil {
			lastErr = err
		}
	}
	if usable {
		// 兜底链失败但本地看来源 token 尚未过期：继续用（服务端是否接受由调用方判定）
		return a.Token, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ikun 需要登录态：POST /api/price_compare/ikun_auth 设置 token 或账号密码")
	}
	return "", lastErr
}

// pcIkunGetModels 带会话拉取模型目录（登录态 API，返回 {data:{credits_per_cny,groups[]}}）
func pcIkunGetModels(token string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, pcIkunModelsAPI, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", pcUA)
	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(raw), resp.StatusCode, err
}

func pcFetchIkunModelsImpl() ([]pcResellerModel, error) {
	token, err := pcIkunEnsureSession(false)
	if err != nil {
		return nil, err
	}
	body, code, err := pcIkunGetModels(token)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized {
		// 会话被服务端判失效（强制重登/他端挤掉/轮换链断）：走兜底链重新拿
		// token（refresh → 账号密码登录）后重试一次
		if token, err = pcIkunEnsureSession(true); err != nil {
			return nil, err
		}
		if body, code, err = pcIkunGetModels(token); err != nil {
			return nil, err
		}
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("ikun models api http %d", code)
	}
	var resp struct {
		Data struct {
			CreditsPerCny float64 `json:"credits_per_cny"`
			Groups        []struct {
				Name   string  `json:"name"`
				Rate   float64 `json:"rate_multiplier"`
				Models []struct {
					Name    string `json:"name"`
					Pricing struct {
						BillingMode string   `json:"billing_mode"`
						In          *float64 `json:"input_price"`
						Out         *float64 `json:"output_price"`
						Read        *float64 `json:"cache_read_price"`
						Write       *float64 `json:"cache_write_price"`
						Write1h     *float64 `json:"cache_write_1h_price"`
						Write12h    *float64 `json:"cache_write_12h_price"`
					} `json:"pricing"`
				} `json:"models"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	creditsPerCny := resp.Data.CreditsPerCny
	if creditsPerCny <= 0 {
		creditsPerCny = pcIkunCnyPerCredit // 服务端未给口径时回退常量
	}
	// 每模型取「最低有效价分组」（与站方模型广场「最低分组价」同口径）：
	// 有效因子 f = 分组倍率 × 每元刀数；同模型多分组出现时取最小 f
	type pick struct {
		row pcResellerModel
		f   float64
	}
	best := map[string]pick{}
	for _, g := range resp.Data.Groups {
		if g.Rate <= 0 {
			continue
		}
		f := g.Rate * creditsPerCny
		for _, m := range g.Models {
			if m.Pricing.BillingMode != "token" || m.Pricing.In == nil || *m.Pricing.In <= 0 {
				continue
			}
			write := m.Pricing.Write
			if write == nil {
				write = m.Pricing.Write1h
			}
			if write == nil {
				write = m.Pricing.Write12h
			}
			row := pcResellerModel{
				Channel: "ikun", Name: m.Name, Currency: "CNY",
				In:    f64v(m.Pricing.In) * 1e6 * f,
				Out:   f64v(m.Pricing.Out) * 1e6 * f,
				Read:  f64v(m.Pricing.Read) * 1e6 * f,
				Write: f64v(write) * 1e6 * f,
				Note:  fmt.Sprintf("组 %s ×%g", g.Name, g.Rate), GroupRate: g.Rate,
			}
			if prev, ok := best[m.Name]; !ok || f < prev.f {
				best[m.Name] = pick{row: row, f: f}
			}
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names) // 输出顺序稳定（缓存 diff 友好）
	out := make([]pcResellerModel, 0, len(names))
	for _, n := range names {
		out = append(out, best[n].row)
	}
	return out, nil
}

// pcQuerySub2apiBalance sub2api 系（apikl/ikun）余额：GET /v1/usage 是官方给
// CC Switch 集成用的 key 鉴权端点（gateway 组，sk- key 可过）。钱包模式
// balance=账户钱包余额（同账号多 key 返回同一值，聚合按唯一值求和防重复计）；
// 订阅模式没有 balance，退用 remaining。余额单位是站内"USD"额度，×cnyPerCredit
// 折人民币（ikun/apikl 均 1:1→1；ikun 2026-11-16 起原 1:10 改 1:1）。再查 /v1/sub2api/billing 拿该
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

// PostPriceCompareIkunAuth 设置 iKun 控制台登录态（站方改版后模型目录改为登录态
// API）：从站方控制台 F12 取 localStorage 的 auth_token / refresh_token 传入即可；
// access token 过期前自动用 refresh_token 续期，续期失败时需重新登录再设置一次
func PostPriceCompareIkunAuth(c *gin.Context) {
	var req struct {
		Token    string `json:"token"`
		Refresh  string `json:"refresh"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	cur := pcIkunLoadAuth()
	a := cur // 未提供的字段沿用当前值（允许只更新 token 或只更新账号密码）
	if t := strings.TrimSpace(req.Token); t != "" {
		a.Token = t
		a.Exp = pcIkunJwtExp(t)
	}
	if r := strings.TrimSpace(req.Refresh); r != "" {
		a.Refresh = r
	}
	if e := strings.TrimSpace(req.Email); e != "" {
		a.Email = e
	}
	if p := req.Password; p != "" {
		a.Password = p
	}
	if a.Token == "" && (a.Email == "" || a.Password == "") {
		c.JSON(200, gin.H{"success": false, "message": "token 或 账号密码 至少提供一组"})
		return
	}
	pcIkunSaveAuth(a)
	// 只给账号密码时：立即走登录兜底链验证并刷新行情
	go func() {
		if _, err := pcIkunEnsureSession(true); err != nil {
			common.SysLog("ikun auth: ensure session failed: " + err.Error())
		}
		pcFetchIkunModels(true)
	}()
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"exp": a.Exp, "has_token": a.Token != "", "has_credentials": a.Email != "" && a.Password != "",
	}})
}

// GetPriceCompareIkunAuth 查询 iKun 登录态状态（不返回 token/密码原文）
func GetPriceCompareIkunAuth(c *gin.Context) {
	a := pcIkunLoadAuth()
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"has_token": a.Token != "", "has_refresh": a.Refresh != "", "exp": a.Exp,
		"has_credentials": a.Email != "" && a.Password != "",
	}})
}
