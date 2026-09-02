package controller

import (
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/google/uuid"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// ============================================================
// 模型比价面板（管理员）：跨渠道统一比价
// 数据源：OpenRouter /endpoints 官方 API、OpenCode Go 订阅文档、DeepSeek 官方定价页、
//        LiveBench CSV、arena.ai Agent 榜（服务端渲染表格）、one-api logs 用量倍率。
// 移植自独立版 app.py（D:\new-api\自动获取最便宜最好的供应商）。
// ============================================================

const (
	pcExchangeTTL      = 6 * 3600
	pcExchangeRetryTTL = 600 // 抓取失败后的负缓存：10 分钟内不重试，避免离线时每请求阻塞
	pcModelsTTL        = 6 * 3600
	pcPricesTTL        = 12 * 3600
	pcGoTTL            = 12 * 3600
	pcDsTTL            = 12 * 3600
	pcLivebenchTTL     = 12 * 3600
	pcArenaTTL         = 12 * 3600
	pcUnifiedTTL       = 120

	pcFallbackRate = 6.7
)

var pcUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// 共享 client：连接复用（预抓取 260+ 个模型时避免每请求重建连接池）
var pcHTTPClient = &http.Client{
	Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
}

var pcExchangeURLs = []string{
	"https://open.er-api.com/v6/latest/USD",
	"https://api.exchangerate-api.com/v4/latest/USD",
}

const pcGoMdxURL = "https://raw.githubusercontent.com/anomalyco/opencode/dev/" +
	"packages/web/src/content/docs/zh-cn/go.mdx"

// jsdelivr CDN 镜像兜底：raw.githubusercontent 偶发 404/限流
const pcGoMdxURLMirror = "https://cdn.jsdelivr.net/gh/anomalyco/opencode@dev/" +
	"packages/web/src/content/docs/zh-cn/go.mdx"

var pcDeepseekURLs = []string{
	"https://api-docs.deepseek.com/zh-cn/quick_start/pricing/",
	"https://api-docs.deepseek.com/quick_start/pricing/",
}

// arena.ai 榜单分类：agent 两榜（信号表）+ LMArena 系（Elo 表，lmarena.ai 已并入 arena.ai）。
// 生成类榜单（image-edit/text-to-video/video-edit）与按 token 计价无关，不抓。
var pcArenaURLs = map[string]string{
	"code":     "https://arena.ai/leaderboard/agent/code",
	"overall":  "https://arena.ai/leaderboard/agent/overall",
	"text":     "https://arena.ai/leaderboard/text",
	"webdev":   "https://arena.ai/leaderboard/code/webdev",
	"i2w":      "https://arena.ai/leaderboard/code/image-to-webdev",
	"vision":   "https://arena.ai/leaderboard/vision",
	"search":   "https://arena.ai/leaderboard/search",
	"t2i":      "https://arena.ai/leaderboard/text-to-image",
	"document": "https://arena.ai/leaderboard/document",
}

// LiveBench 表版本（取自 livebench.ai 前端 bundle，新的排最后）
var pcLivebenchTables = []string{
	"2024-06-24", "2024-07-26", "2024-08-31", "2024-11-25", "2025-04-02",
	"2025-04-25", "2025-05-30", "2025-11-25", "2025-12-23", "2026-01-08",
	"2026-06-25", "2026-07-22", "2026-09-10",
}

// 预抓取范围：目录全量（见 pcOrFetchIds，排除 ~ 别名与 openrouter/ 自运营）

// 厂商识别（长前缀在前，避免 "hy" 误吃 "hunyuan"）：前缀 → 显示名 / lobehub 图标名
var pcVendors = []struct {
	Prefix string
	Name   string
	Icon   string
}{
	{"meta-llama", "Meta Llama", "Meta"},
	{"bytedance-seed", "字节 Seed", "ByteDance"},
	{"thinkingmachines", "Thinking Machines", ""},
	{"inclusionai", "InclusionAI", ""},
	{"moonshotai", "Moonshot Kimi", "Moonshot"},
	{"mistralai", "Mistral", "Mistral"},
	{"anthropic", "Anthropic", "Claude"},
	{"deepseek", "DeepSeek", "DeepSeek"},
	{"microsoft", "Microsoft", "Microsoft"},
	{"minimax", "MiniMax", "Minimax"},
	{"perplexity", "Perplexity", "Perplexity"},
	{"aion-labs", "Aion Labs", "AionLabs"},
	{"longcat", "美团 LongCat", "LongCat"},
	{"hunyuan", "腾讯混元", "Hunyuan"},
	{"openai", "OpenAI", "OpenAI"},
	{"google", "Google Gemini", "Gemini"},
	{"amazon", "Amazon Nova", "Amazon"},
	{"qwen", "阿里千问", "Qwen"},
	{"tencent", "腾讯混元", "Hunyuan"},
	{"baidu", "百度文心", "Baidu"},
	{"cohere", "Cohere", "Cohere"},
	{"z-ai", "智谱 GLM", "Zhipu"},
	{"nvidia", "NVIDIA", "Nvidia"},
	{"kimi", "Moonshot Kimi", "Moonshot"},
	{"grok", "xAI Grok", "Grok"},
	{"x-ai", "xAI Grok", "Grok"},
	{"glm", "智谱 GLM", "Zhipu"},
	{"mimo", "小米 MiMo", "XiaomiMiMo"},
	{"xiaomi", "小米 MiMo", "XiaomiMiMo"},
	{"poolside", "Poolside", "Poolside"},
	{"sakana", "Sakana AI", ""},
	{"stealth", "Stealth", ""},
	{"upstage", "Upstage", "Upstage"},
	{"gemini", "Google Gemini", "Gemini"},
	{"claude", "Anthropic", "Claude"},
	{"gpt", "OpenAI", "OpenAI"},
	{"llama", "Meta Llama", "Meta"},
	{"meta", "Meta Llama", "Meta"},
	{"muse", "Meta Llama", "Meta"},
	{"mistral", "Mistral", "Mistral"},
	{"ernie", "百度文心", "Baidu"},
	{"hy4", "腾讯混元", "Hunyuan"},
	{"hy3", "腾讯混元", "Hunyuan"},
	{"hy", "腾讯混元", "Hunyuan"},
}

var pcVendorFallback = map[string]string{"id": "", "name": "其他", "icon": ""}

// LiveBench 全部类目（综合=全类目均值的均值），8 维全量暴露
var pcLbDims = []struct {
	Key string
	Cat string
}{
	{"average", ""},
	{"reasoning", "Reasoning"},
	{"coding", "Coding"},
	{"agentic_coding", "Agentic Coding"},
	{"math", "Mathematics"},
	{"data_analysis", "Data Analysis"},
	{"language", "Language"},
	{"instruction_following", "IF"},
}

// ---------------- 内部数据结构 ----------------

type pcRatio struct {
	Cache      float64 `json:"cache"`
	Output     float64 `json:"output"`
	Uncached   float64 `json:"uncached"`
	CacheWrite float64 `json:"cache_write"`
	Prompt     int64   `json:"prompt"`
}

type pcOrProvider struct {
	Provider        string   `json:"provider"`
	Quantization    string   `json:"quantization"`
	Prompt          float64  `json:"prompt"`
	Completion      float64  `json:"completion"`
	CacheRead       float64  `json:"cache_read"`
	CacheWrite      float64  `json:"cache_write"`
	LatencyP50      *float64 `json:"latency_p50"`
	LatencyP75      *float64 `json:"latency_p75"`
	ThroughputP50   *float64 `json:"throughput_p50"`
	ThroughputP75   *float64 `json:"throughput_p75"`
	Uptime1d        *float64 `json:"uptime_1d"`
	ImplicitCache   bool     `json:"implicit_cache"`
	MaxPromptTokens *int     `json:"max_prompt_tokens"`
}

type pcOrPrice struct {
	Model         string         `json:"model"`
	CanonicalSlug string         `json:"canonical_slug"`
	Providers     []pcOrProvider `json:"providers"`
	FetchedAt     int64          `json:"fetched_at"`
	Error         string         `json:"error,omitempty"`
}

type pcCatalogEntry struct {
	Id            string `json:"id"`
	CanonicalSlug string `json:"canonical_slug"`
	Name          string `json:"name"`
	// 以下供 harness 同步写 ZCode 模型配置用（上下文/输出上限/输入模态/能力参数）
	OutputModalities []string `json:"output_modalities,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	ContextLength    int      `json:"context_length,omitempty"`
	MaxCompletionTok int      `json:"max_completion_tokens,omitempty"`
	SupportedParams  []string `json:"supported_params,omitempty"`
}

type pcGoModel struct {
	Name       string   `json:"name"`
	ModelId    string   `json:"model_id"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  float64  `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Limit      float64  `json:"limit"`
	Note       string   `json:"note"`
}

type pcDsModel struct {
	Name      string    `json:"name"`
	Id        string    `json:"id"`
	CacheHit  []float64 `json:"cache_hit"`
	CacheMiss []float64 `json:"cache_miss"`
	Output    []float64 `json:"output"`
}

type pcUnifiedRow struct {
	Channel    string            `json:"channel"`
	Ref        string            `json:"ref"`
	Name       string            `json:"name"`
	FullName   string            `json:"full_name"`
	Key        string            `json:"key"`
	Vendor     map[string]string `json:"vendor"`
	Currency   string            `json:"currency"`
	Multiplier float64           `json:"multiplier"`
	In         float64           `json:"in"`
	Out        float64           `json:"out"`
	Read       float64           `json:"read"`
	Write      float64           `json:"write"`
	Provider   string            `json:"provider"`
	Quant      string            `json:"quant"`
	Throughput *float64          `json:"throughput"`
	Latency    *float64          `json:"latency"`
	Uptime1d   *float64          `json:"uptime_1d"`
	Period     string            `json:"period"`
	Split      bool              `json:"split"`
	Limit      *float64          `json:"limit,omitempty"`
	Note       string            `json:"note,omitempty"`
	// GroupRate 中转站报价采用的分组倍率（同模型多组取实付最低组；0=无分组概念）。
	// 前端按「某把 key 的组倍率 / 本倍率」把行价换算成该 key 的实付价
	GroupRate float64 `json:"group_rate,omitempty"`
	// Dead 实测不可调（刷新时对全部零价报价批量实测；测试失败≠免费，仅按实测判定）
	Dead   bool                   `json:"dead,omitempty"`
	LbName *string                `json:"lb_name"`
	LbSim  *float64               `json:"lb_sim"`
	Lb     map[string]*float64    `json:"lb"`
	Arena  map[string]interface{} `json:"arena"`
	// Bench 全量基准得分（按榜单源分组）：
	//   livebench → scores{average,reasoning,coding,agentic_coding,math,data_analysis,language,instruction_following}
	//   arena     → scores{code_net,code_success,code_praise,code_steer,code_bash,code_halluc,code_rank,
	//                      overall_*(同上), text_elo,text_rank,webdev_elo,vision_elo,search_elo,t2i_elo}
	Bench map[string]*pcBenchEntry `json:"bench"`
}

type pcBenchEntry struct {
	Name   string             `json:"name"`
	Sim    float64            `json:"sim"`
	Scores map[string]float64 `json:"scores"`
}

type pcCacheData struct {
	ExchangeAt   int64                `json:"exchange_at"`
	ExchangeRate float64              `json:"exchange_rate"`
	ExchangeLive bool                 `json:"exchange_live"`
	ModelsAt     int64                `json:"models_at"`
	Models       []pcCatalogEntry     `json:"models"`
	Canonical    map[string]string    `json:"canonical"`
	Prices       map[string]pcOrPrice `json:"prices"`
	GoAt         int64                `json:"go_at"`
	GoModels     []pcGoModel          `json:"go_models"`
	DsAt         int64                `json:"ds_at"`
	DsModels     []pcDsModel          `json:"ds_models"`
	// DsFlashTarget：OpenRouter 官方别名 ~deepseek/deepseek-flash-latest 当前指向的
	// 模型 id（如 deepseek/deepseek-v4.1-flash）。官方升级后此处自动跟随。
	DsFlashAt     int64                          `json:"ds_flash_at"`
	DsFlashTarget string                         `json:"ds_flash_target"`
	LbAt          int64                          `json:"lb_at"`
	LbRows        map[string]map[string]*float64 `json:"lb_rows"`
	LbCats        map[string][]string            `json:"lb_cats"`
	ArenaAt       int64                          `json:"arena_at"`
	// ArenaBoards：agent 两榜（code/overall，信号表）+ Elo 七榜（text/webdev/i2w/vision/search/t2i/document）
	ArenaBoards map[string][]map[string]interface{} `json:"arena_boards"`
	// 扩展榜单源：OR 用量榜 / Artificial Analysis / Design Arena / OpenCompass 司南 / LLM Stats
	OrAt     int64       `json:"or_at"`
	OrRows   []pcNamed   `json:"or_rows"`
	AaAt     int64       `json:"aa_at"`
	AaRows   []pcNamed   `json:"aa_rows"`
	AaCa     []pcAaCa    `json:"aa_ca"`
	DaAt     int64       `json:"da_at"`
	DaRows   []pcNamed   `json:"da_rows"`
	OcAt     int64       `json:"oc_at"`
	OcRows   []pcNamed   `json:"oc_rows"`
	LsAt     int64       `json:"ls_at"`
	LsBoards []pcLsBoard `json:"ls_boards"`
	// AI IQ（aiiq.org）：综合 IQ + 六维 IQ + EQ + 各底层基准分
	AiqAt   int64             `json:"aiq_at"`
	AiqRows []pcNamed         `json:"aiq_rows"`
	AiqMeta map[string]string `json:"aiq_meta"`
	// 中转站报价（buzzai/apikl/ikun 抓取时已折算 CNY；apib 定价有 307 门禁未接入）
	BzAt     int64             `json:"bz_at"`
	BzModels []pcResellerModel `json:"bz_models"`
	AkAt     int64             `json:"ak_at"`
	AkModels []pcResellerModel `json:"ak_models"`
	IkAt     int64             `json:"ik_at"`
	IkModels []pcResellerModel `json:"ik_models"`
	// DeadOffers 实测不可调的报价集合（key=pcTestCand.key()）。每次刷新对全部
	// 零价报价（免费档）批量实测后重建免费键；apply 连通性过滤剔除的付费报价
	//（含 402 欠费致死）追加写回、实测通过的移除——按实测而非后缀判定——供应
	// 商档位免费与否只有真实调用说了算。行构建时据此打 dead 标记，前端默认隐藏
	DeadOffers map[string]bool `json:"dead_offers,omitempty"`
}

// pcNamed 通用榜单行：Name 展示名，Alias 备用标识（如 API model_id），Scores 分数集
type pcNamed struct {
	Name   string             `json:"name"`
	Alias  string             `json:"alias,omitempty"`
	Scores map[string]float64 `json:"scores"`
}

// pcAaCa AA Coding Agents 榜：agent×模型组合的指数与各评测集 reward（0-1，入缓存时已 ×100）
type pcAaCa struct {
	Agent  string             `json:"agent"`
	Model  string             `json:"model"`
	Slug   string             `json:"slug"`
	Scores map[string]float64 `json:"scores"`
}

// pcLsBoard LLM Stats 单基准榜：该基准下全部已测模型的分数与排名
type pcLsBoard struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Rows []pcNamed `json:"rows"`
}

var (
	pcMu          sync.Mutex
	pcCache       = pcCacheData{ExchangeRate: pcFallbackRate, Canonical: map[string]string{}, Prices: map[string]pcOrPrice{}, LbRows: map[string]map[string]*float64{}, LbCats: map[string][]string{}}
	pcUnifiedAt   int64
	pcUnifiedRows []pcUnifiedRow
	pcOrInflight  = map[string]bool{}
	pcOrKey       = ""
	pcOrKeyAt     int64
	pcArenaMemo   = map[string]map[string]interface{}{}
	pcRefresh     = pcRefreshState{Finished: true}
	pcAutoOnce    sync.Once
)

// pcRefreshState 后台刷新进度（JSON 字段与前端 PcRefreshStatus 对应）
type pcRefreshState struct {
	Running  bool   `json:"running"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Current  string `json:"current"`
	Finished bool   `json:"finished"`
}

func pcRefreshSet(f func(*pcRefreshState)) {
	pcMu.Lock()
	f(&pcRefresh)
	pcMu.Unlock()
}

func pcDataDir() string {
	dir := filepath.Join("data", "price-compare")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

func pcCacheFile() string    { return filepath.Join(pcDataDir(), "cache.json") }
func pcPriorityFile() string { return filepath.Join(pcDataDir(), "priority.json") }

// ---------------- 基础工具 ----------------

func pcHttpGet(url string, timeout time.Duration, headers map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", pcUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := pcHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func pcCanonKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimSuffix(s, ":free")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var pcParenRe = regexp.MustCompile(`\([^)]*\)`)

// pcCanonVariants 归一键变体：原名 + 去括号名（渠道名常带 "(> 272K tokens)" 之类上下文注释）
func pcCanonVariants(raw string) []string {
	out := []string{pcCanonKey(raw)}
	if s := pcCanonKey(pcParenRe.ReplaceAllString(raw, "")); s != "" && s != out[0] {
		out = append(out, s)
	}
	return out
}

func pcNormTokenize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func pcVendorOf(name string) map[string]string {
	s := pcNormTokenize(name)
	if s == "" {
		return map[string]string(pcVendorFallback)
	}
	best := ""
	for _, v := range pcVendors {
		if strings.HasPrefix(s, v.Prefix) && len(v.Prefix) > len(best) {
			best = v.Prefix // 长前缀优先，避免 "hy" 误吃 "hunyuan"
		}
	}
	for _, v := range pcVendors {
		if v.Prefix == best {
			return map[string]string{"id": v.Prefix, "name": v.Name, "icon": v.Icon}
		}
	}
	return map[string]string(pcVendorFallback)
}

func pcLevDist(a, b string) int {
	la, lb := len([]rune(a)), len([]rune(b))
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	ra, rb := []rune(a), []rune(b)
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func pcLevRatio(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	m := len(a)
	if len(b) > m {
		m = len(b)
	}
	return 1.0 - float64(pcLevDist(a, b))/float64(m)
}

func pcF2P(f float64) *float64 { return &f }

// ---------------- 汇率 ----------------

func pcFetchExchange(force bool) {
	pcMu.Lock()
	ttl := int64(pcExchangeTTL)
	if !pcCache.ExchangeLive {
		ttl = pcExchangeRetryTTL // 失败后走负缓存，成功后 6 小时
	}
	if !force && time.Now().Unix()-pcCache.ExchangeAt < ttl {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	for _, u := range pcExchangeURLs {
		body, err := pcHttpGet(u, 15*time.Second, nil)
		if err != nil {
			continue
		}
		var data struct {
			Rates map[string]float64 `json:"rates"`
		}
		if json.Unmarshal([]byte(body), &data) == nil {
			if r, ok := data.Rates["CNY"]; ok && r > 5 && r < 9 {
				pcMu.Lock()
				pcCache.ExchangeRate = r
				pcCache.ExchangeLive = true
				pcCache.ExchangeAt = time.Now().Unix()
				pcMu.Unlock()
				return
			}
		}
	}
	pcMu.Lock()
	pcCache.ExchangeAt = time.Now().Unix()
	pcCache.ExchangeLive = false
	pcMu.Unlock()
}

func pcExchangeRate() float64 {
	pcMu.Lock()
	defer pcMu.Unlock()
	return pcCache.ExchangeRate
}

// ---------------- 数据库用量倍率（6 个时间窗） ----------------

var (
	pcRatiosCache map[string]*pcRatio
	pcRatiosAt    int64
)

// pcReadRatios 带 60s 短缓存：日志全表聚合开销大，overview/unified 都会调用
func pcReadRatios() map[string]*pcRatio {
	pcMu.Lock()
	if pcRatiosCache != nil && time.Now().Unix()-pcRatiosAt < 60 {
		out := pcRatiosCache
		pcMu.Unlock()
		return out
	}
	pcMu.Unlock()
	out := pcReadRatiosDB()
	pcMu.Lock()
	pcRatiosCache = out
	pcRatiosAt = time.Now().Unix()
	pcMu.Unlock()
	return out
}

func pcReadRatiosDB() map[string]*pcRatio {
	out := map[string]*pcRatio{}
	type aggRow struct {
		DayP    float64 `gorm:"column:day_p"`
		DayC    float64 `gorm:"column:day_c"`
		DayCa   float64 `gorm:"column:day_ca"`
		DayCw   float64 `gorm:"column:day_cw"`
		WeekP   float64 `gorm:"column:week_p"`
		WeekC   float64 `gorm:"column:week_c"`
		WeekCa  float64 `gorm:"column:week_ca"`
		WeekCw  float64 `gorm:"column:week_cw"`
		MonthP  float64 `gorm:"column:month_p"`
		MonthC  float64 `gorm:"column:month_c"`
		MonthCa float64 `gorm:"column:month_ca"`
		MonthCw float64 `gorm:"column:month_cw"`
		HyP     float64 `gorm:"column:hy_p"`
		HyC     float64 `gorm:"column:hy_c"`
		HyCa    float64 `gorm:"column:hy_ca"`
		HyCw    float64 `gorm:"column:hy_cw"`
		YearP   float64 `gorm:"column:year_p"`
		YearC   float64 `gorm:"column:year_c"`
		YearCa  float64 `gorm:"column:year_ca"`
		YearCw  float64 `gorm:"column:year_cw"`
		AllP    float64 `gorm:"column:all_p"`
		AllC    float64 `gorm:"column:all_c"`
		AllCa   float64 `gorm:"column:all_ca"`
		AllCw   float64 `gorm:"column:all_cw"`
	}
	now := time.Now().Unix()
	jc := "CAST(json_extract(`other`,'$.cache_tokens') AS INTEGER)"
	jcw := "CAST(json_extract(`other`,'$.cache_write_tokens') AS INTEGER)"
	win := func(tag, col string, days int) string {
		if days <= 0 {
			return fmt.Sprintf("COALESCE(SUM(%s),0) AS %s", col, tag)
		}
		return fmt.Sprintf("COALESCE(SUM(CASE WHEN created_at >= %d THEN %s END),0) AS %s", now-int64(days)*86400, col, tag)
	}
	sql := fmt.Sprintf(`SELECT %s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s
		FROM logs WHERE type=2 AND created_at >= ?`,
		win("day_p", "prompt_tokens", 1), win("day_c", "completion_tokens", 1), win("day_ca", jc, 1), win("day_cw", jcw, 1),
		win("week_p", "prompt_tokens", 7), win("week_c", "completion_tokens", 7), win("week_ca", jc, 7), win("week_cw", jcw, 7),
		win("month_p", "prompt_tokens", 30), win("month_c", "completion_tokens", 30), win("month_ca", jc, 30), win("month_cw", jcw, 30),
		win("hy_p", "prompt_tokens", 182), win("hy_c", "completion_tokens", 182), win("hy_ca", jc, 182), win("hy_cw", jcw, 182),
		win("year_p", "prompt_tokens", 365), win("year_c", "completion_tokens", 365), win("year_ca", jc, 365), win("year_cw", jcw, 365),
		win("all_p", "prompt_tokens", 0), win("all_c", "completion_tokens", 0), win("all_ca", jc, 0), win("all_cw", jcw, 0))
	var row aggRow
	err := fmt.Errorf("log db not ready")
	if model.LOG_DB != nil {
		err = model.LOG_DB.Raw(sql, now-int64(365)*86400).Scan(&row).Error
	}
	if err != nil {
		// 数据库不可用/方言不支持 → 给兜底默认（与独立版一致）
		def := func() *pcRatio {
			return &pcRatio{Cache: 0.921, Output: 0.0057, Uncached: 0.079, CacheWrite: 0.001}
		}
		for _, k := range []string{"day", "week", "month", "halfyear", "year", "all"} {
			out[k] = def()
		}
		return out
	}
	get := func(p, c, ca, cw float64) *pcRatio {
		if p <= 0 {
			return &pcRatio{Cache: 0.921, Output: 0.0057, Uncached: 0.079, CacheWrite: 0.001}
		}
		u := 1 - ca/p - cw/p
		if u < 0 {
			u = 0
		}
		return &pcRatio{Cache: ca / p, Output: c / p, Uncached: u, CacheWrite: cw / p, Prompt: int64(p)}
	}
	out["day"] = get(row.DayP, row.DayC, row.DayCa, row.DayCw)
	out["week"] = get(row.WeekP, row.WeekC, row.WeekCa, row.WeekCw)
	out["month"] = get(row.MonthP, row.MonthC, row.MonthCa, row.MonthCw)
	out["halfyear"] = get(row.HyP, row.HyC, row.HyCa, row.HyCw)
	out["year"] = get(row.YearP, row.YearC, row.YearCa, row.YearCw)
	out["all"] = get(row.AllP, row.AllC, row.AllCa, row.AllCw)
	return out
}

// pcCST 北京时间（DeepSeek 高峰时段按时区定义，不依赖服务器本地时区）
var pcCST = time.FixedZone("CST", 8*3600)

func pcIsPeak(now time.Time) bool {
	now = now.In(pcCST)
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	t := float64(now.Hour()) + float64(now.Minute())/60.0
	return (t >= 9 && t < 12) || (t >= 14 && t < 18)
}

// ---------------- OpenRouter 目录与供应商价 ----------------

func pcGetOrKey() string {
	pcMu.Lock()
	if pcOrKey != "" && time.Now().Unix()-pcOrKeyAt < 600 {
		k := pcOrKey
		pcMu.Unlock()
		return k
	}
	pcMu.Unlock()
	k := ""
	if kv := pcLoadKeys(); len(kv.Openrouter) > 0 {
		k = kv.Openrouter[0] // 面板 key 保险箱优先，其次才回退扫渠道表
	} else {
		var key string
		if model.DB != nil {
			if err := model.DB.Table("channels").Select("`key`").
				Where("type = ? AND LOWER(name) LIKE ? AND `key` IS NOT NULL AND `key` <> ''", 1, "%openrouter%").
				Order("id").Limit(1).Scan(&key).Error; err == nil {
				k = strings.TrimSpace(key)
			}
		}
	}
	pcMu.Lock()
	pcOrKey = k
	pcOrKeyAt = time.Now().Unix()
	pcMu.Unlock()
	return k
}

func pcFetchCatalog(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.Models) > 0 && time.Now().Unix()-pcCache.ModelsAt < pcModelsTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet("https://openrouter.ai/api/v1/models", 40*time.Second, nil)
	if err != nil {
		return
	}
	var data struct {
		Data []struct {
			Id            string `json:"id"`
			CanonicalSlug string `json:"canonical_slug"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				OutputModalities []string `json:"output_modalities"`
				InputModalities  []string `json:"input_modalities"`
			} `json:"architecture"`
			TopProvider struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil {
		return
	}
	models := []pcCatalogEntry{}
	canon := map[string]string{}
	for _, m := range data.Data {
		if m.Id == "" || strings.HasSuffix(m.Id, ":batch") {
			continue // :batch 批量查询模型不参与比价
		}
		models = append(models, pcCatalogEntry{Id: m.Id, CanonicalSlug: m.CanonicalSlug,
			Name: m.Name, OutputModalities: m.Architecture.OutputModalities,
			InputModalities: m.Architecture.InputModalities, ContextLength: m.ContextLength,
			MaxCompletionTok: m.TopProvider.MaxCompletionTokens, SupportedParams: m.SupportedParameters})
		canon[m.Id] = m.CanonicalSlug
	}
	if len(models) == 0 {
		return
	}
	pcMu.Lock()
	pcCache.Models = models
	pcCache.Canonical = canon
	pcCache.ModelsAt = time.Now().Unix()
	// 目录对照清理：Prices 里已从 OR 目录消失的模型（官方下架）整体剔除，
	// 否则下架报价永远以活价格残留在行情里，用户按面板选购实际请求会失败
	live := map[string]bool{}
	for _, m := range models {
		live[m.Id] = true
	}
	for id := range pcCache.Prices {
		if !live[id] {
			delete(pcCache.Prices, id)
		}
	}
	pcMu.Unlock()
}

func pcFetchOrPrice(mid string, force bool) {
	pcMu.Lock()
	if !force {
		// 已有供应商价且未过 TTL 才直接复用；过期条目照抓——否则刷新编排按
		// TTL 挑出的补抓在这里全部空转，非 force 刷新永远无法续期已有条目
		if p, ok := pcCache.Prices[mid]; ok && len(p.Providers) > 0 &&
			time.Now().Unix()-p.FetchedAt <= pcPricesTTL {
			pcMu.Unlock()
			return
		}
		if pcOrInflight[mid] {
			pcMu.Unlock()
			return
		}
		pcOrInflight[mid] = true
	} else if pcOrInflight[mid] {
		pcMu.Unlock()
		return
	} else {
		pcOrInflight[mid] = true
	}
	pcMu.Unlock()
	defer func() {
		pcMu.Lock()
		delete(pcOrInflight, mid)
		pcMu.Unlock()
	}()
	pcMu.Lock()
	slug := mid
	if s, ok := pcCache.Canonical[mid]; ok && s != "" {
		slug = s
	}
	pcMu.Unlock()
	// 锁外再取 key：pcGetOrKey 内部也要拿 pcMu，持锁调用会自死锁
	key := pcGetOrKey()
	headers := map[string]string{}
	if key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	body, err := pcHttpGet("https://openrouter.ai/api/v1/models/"+slug+"/endpoints", 30*time.Second, headers)
	if err != nil {
		pcMu.Lock()
		pcCache.Prices[mid] = pcOrPrice{Model: mid, Error: err.Error()}
		pcMu.Unlock()
		return
	}
	var data struct {
		Data struct {
			Endpoints []struct {
				ProviderName string `json:"provider_name"`
				Quantization string `json:"quantization"`
				Pricing      struct {
					Prompt         string `json:"prompt"`
					Completion     string `json:"completion"`
					InputCacheRead string `json:"input_cache_read"`
					// 缓存写入价：Anthropic/OpenAI 等按 1.25×prompt 计（$5 vs $4），
					// 缺了该字段会把 write 列系统性低报
					InputCacheWrite string `json:"input_cache_write"`
				} `json:"pricing"`
				Latency struct {
					P50 *float64 `json:"p50"`
					P75 *float64 `json:"p75"`
				} `json:"latency_last_30m"`
				Throughput struct {
					P50 *float64 `json:"p50"`
					P75 *float64 `json:"p75"`
				} `json:"throughput_last_30m"`
				Uptime1d     *float64 `json:"uptime_last_1d"`
				Implicit     bool     `json:"supports_implicit_caching"`
				MaxPromptTok *int     `json:"max_prompt_tokens"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil {
		return
	}
	provs := []pcOrProvider{}
	for _, e := range data.Data.Endpoints {
		pf := func(s string) float64 {
			f, _ := strconv.ParseFloat(s, 64)
			return f
		}
		provs = append(provs, pcOrProvider{
			Provider: e.ProviderName, Quantization: e.Quantization,
			Prompt: pf(e.Pricing.Prompt), Completion: pf(e.Pricing.Completion),
			CacheRead:  pf(e.Pricing.InputCacheRead),
			CacheWrite: pf(e.Pricing.InputCacheWrite),
			LatencyP50: e.Latency.P50, LatencyP75: e.Latency.P75,
			ThroughputP50: e.Throughput.P50, ThroughputP75: e.Throughput.P75,
			Uptime1d: e.Uptime1d, ImplicitCache: e.Implicit, MaxPromptTokens: e.MaxPromptTok,
		})
	}
	if len(provs) == 0 {
		pcMu.Lock()
		pcCache.Prices[mid] = pcOrPrice{Model: mid, Error: "no endpoints"}
		pcMu.Unlock()
		return
	}
	pcMu.Lock()
	pcCache.Prices[mid] = pcOrPrice{Model: mid, CanonicalSlug: slug, Providers: provs, FetchedAt: time.Now().Unix()}
	pcMu.Unlock()
}

// pcOrFetchIds 目录里需要抓供应商价的模型：排除 ~ 官方别名条目与 openrouter/
// 自运营路由模型（无固定供应商报价），其余全量——白名单会漏新厂商（如 xiaomi/）。
func pcOrFetchIds() []string {
	pcMu.Lock()
	defer pcMu.Unlock()
	ids := []string{}
	for _, m := range pcCache.Models {
		if strings.HasPrefix(m.Id, "~") || strings.HasPrefix(m.Id, "openrouter/") {
			continue
		}
		ids = append(ids, m.Id)
	}
	return ids
}

func pcPrefetchOr(force bool, status func(cur string, done, total int)) {
	pcFetchCatalog(false)
	ids := pcOrFetchIds()
	total := len(ids)
	const workers = 5
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer func() {
				<-sem
				wg.Done()
			}()
			pcFetchOrPrice(id, force)
			mu.Lock()
			done++
			n := done
			mu.Unlock()
			if status != nil {
				status("", n, total)
			}
		}(id)
	}
	wg.Wait()
	if status != nil {
		status("", total, total)
	}
}

// ---------------- OpenCode Go 订阅价格 ----------------

var pcGoPeriodRe = regexp.MustCompile(`[(](Off-Peak|Peak)[)]\s*$`)

// pcParenContentRe 取括号内注释（上下文档位 "(≤ 256K tokens)" 等）
var pcParenContentRe = regexp.MustCompile(`\(([^)]*)\)`)

// pcAliasTargetRe OpenRouter latest 别名页里的指向链接
var pcAliasTargetRe = regexp.MustCompile(`data-testid="latest-alias-target"[^>]*href="/([a-z0-9.\-/]+)"`)
var pcGoPriceRe = regexp.MustCompile(`\$(\d+(?:\.\d+)?)`)
var pcGoIdRe = regexp.MustCompile(`^[a-z0-9.\-_]+$`)

func pcParsePrice(s string) *float64 {
	if strings.TrimSpace(s) == "-" {
		return nil
	}
	vals := pcGoPriceRe.FindAllStringSubmatch(s, -1)
	if len(vals) == 0 {
		return nil
	}
	f, err := strconv.ParseFloat(vals[len(vals)-1][1], 64)
	if err != nil {
		return nil
	}
	return &f
}

func pcFetchGoModels(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.GoModels) > 0 && time.Now().Unix()-pcCache.GoAt < pcGoTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	// 双源兜底；正文必须含定价表头才算拿到（防止 404 页被当文档解析）
	body := ""
	for _, u := range []string{pcGoMdxURL, pcGoMdxURLMirror} {
		if b, err := pcHttpGet(u, 25*time.Second, nil); err == nil && strings.Contains(b, "每月限制") {
			body = b
			break
		}
	}
	if body == "" {
		return // 保持旧缓存
	}
	// 行首容忍空白：官方文档改版后表格在 <Tabs> 内缩进 4 空格，^\| 会匹配不到
	tableRe := regexp.MustCompile(`(?m)^[ \t]*\|[^\n]+\|(?:\n[ \t]*\|[^\n]+\|)*`)
	// 按表头内容选表（表序会随文档改版漂移，不能按下标）
	var priceTbl, idTbl string
	for _, t := range tableRe.FindAllString(body, -1) {
		head := strings.Split(t, "\n")[0]
		if priceTbl == "" && strings.Contains(head, "每月限制") && strings.Contains(head, "输入") {
			priceTbl = t
		}
		if idTbl == "" && strings.Contains(head, "模型 ID") {
			idTbl = t
		}
	}
	if priceTbl == "" {
		return
	}
	ids := map[string]string{}
	idLine := regexp.MustCompile(`(?m)^[ \t]*\|(.+)\|$`)
	if idTbl != "" {
		for _, l := range strings.Split(idTbl, "\n") {
			cells := strings.Split(strings.Trim(strings.TrimSpace(idLine.FindString(l)), "|"), "|")
			if len(cells) >= 2 {
				c1 := strings.TrimSpace(cells[0])
				c2 := strings.TrimSpace(cells[1])
				if pcGoIdRe.MatchString(c2) {
					ids[c1] = c2
					ids[strings.ReplaceAll(strings.ToLower(c1), " ", "-")] = c2
					ids[strings.ReplaceAll(strings.ToLower(c1), "-", " ")] = c2
				}
			}
		}
	}
	rows := []pcGoModel{}
	usage := strings.Split(priceTbl, "\n")
	sepRe := regexp.MustCompile(`^[\s\-|]+$`)
	started := false
	zeroLimit := 0.0
	// 按表头名定位列（文档加列/换序时按下标会错拿数据）
	cols := map[string]int{}
	get := func(name string, cells []string) string {
		if i, ok := cols[name]; ok && i < len(cells) {
			return cells[i]
		}
		return ""
	}
	for _, l := range usage {
		if strings.TrimSpace(l) == "" || sepRe.MatchString(l) {
			continue
		}
		// TrimSpace 先剥行首缩进，否则首列会带着前导空格变成空表头
		cells := strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if !started {
			started = true
			for i, c := range cells {
				cols[c] = i
			}
			// 关键列缺失说明表头改版，放弃保旧缓存
			if _, ok := cols["模型"]; !ok {
				return
			}
			if _, ok := cols["输入"]; !ok {
				return
			}
			continue
		}
		if len(cells) < 2 {
			continue
		}
		limitCell := get("每月限制", cells)
		limit := pcParsePrice(limitCell)
		if limit == nil {
			// 免费档：限制格非数字（如「**无限制**」）不再丢整行，0 表示不限
			lc := strings.ToLower(limitCell)
			if !strings.Contains(lc, "无限制") && !strings.Contains(lc, "不限") && !strings.Contains(lc, "unlimited") {
				continue
			}
			limit = &zeroLimit
		}
		name := get("模型", cells)
		note := ""
		if strings.Contains(limitCell, "4x") {
			note = "限时 4x 额度"
			if strings.Contains(limitCell, "9 月 27 日") {
				note += "至 9/27"
			}
		}
		if strings.Contains(limitCell, "限时") && !strings.Contains(note, "限时") {
			note = strings.TrimSpace(note + " 限时")
		}
		base := pcGoPeriodRe.ReplaceAllString(strings.TrimSpace(name), "")
		// 上下文档位括号（"(≤ 256K tokens)"）剥掉后再查 ID 表，ModelId 保持干净 slug
		lookup := strings.TrimSpace(pcParenRe.ReplaceAllString(base, ""))
		norm := strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(lookup), "-")), "--", "-")
		mid := ids[lookup]
		if mid == "" {
			mid = ids[norm]
		}
		if mid == "" {
			mid = ids[strings.ReplaceAll(strings.ToLower(lookup), "-", " ")]
		}
		if mid == "" {
			mid = norm
		}
		def := func(s string) float64 {
			if f := pcParsePrice(s); f != nil {
				return *f
			}
			return 0
		}
		rows = append(rows, pcGoModel{Name: name, ModelId: mid,
			Input: def(get("输入", cells)), Output: def(get("输出", cells)), CacheRead: def(get("缓存读取", cells)),
			CacheWrite: pcParsePrice(get("缓存写入", cells)), Limit: *limit, Note: note})
	}
	if len(rows) > 0 {
		pcMu.Lock()
		pcCache.GoModels = rows
		pcCache.GoAt = time.Now().Unix()
		pcMu.Unlock()
	}
	// 抓取失败且无缓存：保持旧数据（可能为空，前端显示 —）
}

// ---------------- DeepSeek 官方价格 ----------------

// pcFetchDsFlashAlias 解析 OpenRouter 官方别名页 ~deepseek/deepseek-flash-latest
// 的指向（页面内 data-testid="latest-alias-target" 的 href）。官方出新版后别名
// 自动改指新模型，此处跟随，使 deepseek-flash 一类无版本号的官方名永远落到最新款。
func pcFetchDsFlashAlias(force bool) {
	pcMu.Lock()
	if !force && pcCache.DsFlashTarget != "" && time.Now().Unix()-pcCache.DsFlashAt < pcDsTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet("https://openrouter.ai/~deepseek/deepseek-flash-latest", 30*time.Second, nil)
	if err != nil {
		return // 保持旧指向
	}
	m := pcAliasTargetRe.FindStringSubmatch(body)
	if m == nil || !strings.HasPrefix(m[1], "deepseek/") {
		return // 页面结构变化时保持旧指向
	}
	pcMu.Lock()
	pcCache.DsFlashTarget = m[1]
	pcCache.DsFlashAt = time.Now().Unix()
	pcMu.Unlock()
}

func pcFetchDeepseek(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.DsModels) > 0 && time.Now().Unix()-pcCache.DsAt < pcDsTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	for _, u := range pcDeepseekURLs {
		body, err := pcHttpGet(u, 25*time.Second, nil)
		if err != nil {
			continue
		}
		if models := pcParseDeepseek(body); models != nil {
			pcMu.Lock()
			pcCache.DsModels = models
			pcCache.DsAt = time.Now().Unix()
			pcMu.Unlock()
			return
		}
	}
}

func pcParseDeepseek(html string) []pcDsModel {
	tblRe := regexp.MustCompile(`(?s)<table[^>]*>.*?</table>`)
	numRe := regexp.MustCompile(`(\d+(?:\.\d+)?)元`)
	// 动态发现官方模型 id（官方上新自动跟进，不写死）
	idRe := regexp.MustCompile(`deepseek-[a-z0-9.\-]+`)
	for _, tbl := range tblRe.FindAllString(html, -1) {
		if !strings.Contains(tbl, "空闲时段") {
			continue
		}
		numStrs := numRe.FindAllStringSubmatch(tbl, -1)
		if len(numStrs) < 12 {
			continue
		}
		nums := []float64{}
		for _, m := range numStrs {
			f, _ := strconv.ParseFloat(m[1], 64)
			nums = append(nums, f)
		}
		idList := []string{}
		seen := map[string]bool{}
		for _, m := range idRe.FindAllString(tbl, -1) {
			if !seen[m] {
				seen[m] = true
				idList = append(idList, m)
			}
		}
		if len(idList) < 2 {
			continue
		}
		// 表格按指标行展开：3 指标（命中/未命中/输出）× 2 时段（空闲/高峰）× n 模型，
		// html 顺序 = 指标 → 时段子行 → 行内 n 个模型列。数量对不上说明表结构变了，
		// 放弃保旧缓存。
		n := len(idList)
		if len(nums) != n*6 {
			continue
		}
		f2 := func(metric, col int) []float64 {
			base := metric * 2 * n
			return []float64{nums[base+col], nums[base+n+col]}
		}
		out := make([]pcDsModel, 0, n)
		for col, mid := range idList {
			out = append(out, pcDsModel{Name: mid, Id: mid,
				CacheHit: f2(0, col), CacheMiss: f2(1, col), Output: f2(2, col)})
		}
		// 合理性：未命中价 ≥ 命中价、输出价 ≥ 未命中价；峰谷语义下同模型同一指标
		// 高峰价 ≥ 空闲价（违反=布局误读，如列/时段错位）
		for _, m := range out {
			if m.CacheHit[0] <= 0 || m.CacheMiss[0] < m.CacheHit[0] || m.Output[0] < m.CacheMiss[0] {
				return nil
			}
			if m.CacheHit[1] < m.CacheHit[0] || m.CacheMiss[1] < m.CacheMiss[0] || m.Output[1] < m.Output[0] {
				return nil
			}
		}
		if len(out) >= 2 {
			return out
		}
	}
	return nil
}

func pcDsSplit(m pcDsModel) bool {
	return m.CacheHit[0] != m.CacheHit[1] || m.CacheMiss[0] != m.CacheMiss[1] || m.Output[0] != m.Output[1]
}

// ---------------- LiveBench ----------------

var pcLbDateRe = regexp.MustCompile(`20\d{2}-\d{2}-\d{2}`)
var pcLbBundleRe = regexp.MustCompile(`static/js/main\.[0-9a-f]+\.js`)

// pcLbDiscoverTables 从 livebench.ai 前端 bundle 动态发现表版本（新→旧）。bundle 里的
// 日期混有新闻等非表日期，由调用方的 CSV 内容校验过滤；抓取失败返回空，回落硬编码列表。
func pcLbDiscoverTables() []string {
	body, err := pcHttpGet("https://livebench.ai/", 15*time.Second, nil)
	if err != nil {
		return nil
	}
	m := pcLbBundleRe.FindString(body)
	if m == "" {
		return nil
	}
	js, err := pcHttpGet("https://livebench.ai/"+m, 25*time.Second, nil)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, d := range pcLbDateRe.FindAllString(js, -1) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if len(out) > 15 {
		out = out[:15] // 只试最新 15 个候选（多数日期不是表版本），其余交给内容校验淘汰
	}
	return out
}

func pcFetchLivebench(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.LbRows) > 0 && time.Now().Unix()-pcCache.LbAt < pcLivebenchTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	// 候选版本：bundle 动态发现（新→旧）优先，硬编码列表兜底（bundle 结构变化时仍可用）
	cands := pcLbDiscoverTables()
	for i := len(pcLivebenchTables) - 1; i >= 0; i-- {
		cands = append(cands, pcLivebenchTables[i])
	}
	for _, t0 := range cands {
		t := strings.ReplaceAll(t0, "-", "_")
		// 必须 www 域名：apex 域的 CDN 会把 GitHub Pages 的 404 HTML 以 HTTP 200 返回
		csvText, err := pcHttpGet(fmt.Sprintf("https://www.livebench.ai/table_%s.csv", t), 30*time.Second, nil)
		if err != nil {
			continue
		}
		catText, err := pcHttpGet(fmt.Sprintf("https://www.livebench.ai/categories_%s.json", t), 20*time.Second, nil)
		if err != nil {
			catText = "{}"
		}
		rdr := csv.NewReader(strings.NewReader(csvText))
		records, err := rdr.ReadAll()
		if err != nil || len(records) < 2 {
			continue
		}
		cols := records[0]
		// 内容校验：表头首列必须是 model 且至少 5 列——新版本号尚未部署、
		// 或 CDN 返回 404 HTML 时（HTTP 200）落到旧表，绝不把垃圾行写进缓存
		if len(cols) < 5 || strings.ToLower(strings.TrimSpace(cols[0])) != "model" {
			continue
		}
		rows := map[string]map[string]*float64{}
		for _, rec := range records[1:] {
			if len(rec) == 0 || strings.TrimSpace(rec[0]) == "" {
				continue
			}
			m := map[string]*float64{}
			for j := 1; j < len(cols) && j < len(rec); j++ {
				v := strings.TrimSpace(rec[j])
				if v == "" {
					continue
				}
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					m[cols[j]] = &f
				}
			}
			rows[strings.TrimSpace(rec[0])] = m
		}
		if len(rows) == 0 {
			continue
		}
		cats := map[string][]string{}
		_ = json.Unmarshal([]byte(catText), &cats)
		pcMu.Lock()
		pcCache.LbRows = rows
		pcCache.LbCats = cats
		pcCache.LbAt = time.Now().Unix()
		pcMu.Unlock()
		return
	}
}

func pcLbNorm(s string) string {
	s = strings.ToLower(s)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, ":@"); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pcMatchLivebench(names ...string) (string, *float64) {
	pcMu.Lock()
	rows := pcCache.LbRows
	pcMu.Unlock()
	if len(rows) == 0 {
		return "", nil
	}
	type lbCand struct {
		key  string
		norm string
	}
	cands := make([]lbCand, 0, len(rows))
	for key := range rows {
		cands = append(cands, lbCand{key: key, norm: pcLbNorm(key)})
	}
	for _, raw := range names {
		if raw == "" {
			continue
		}
		// 候选顺序优先：先归并正名（deepseek-v4.1-flash），命中即返回，不再看后面的兜底名
		target := pcLbNorm(raw)
		best, bestScore := "", 0.0
		for _, c := range cands {
			ratio := pcLevRatio(target, c.norm)
			if strings.HasPrefix(c.norm, target) || strings.HasPrefix(target, c.norm) {
				ratio = math.Min(1.0, ratio+0.06)
			} else if strings.Contains(c.norm, target) || strings.Contains(target, c.norm) {
				ratio = math.Max(ratio, 0.9)
			}
			// 版本/配置守卫：v4 ≠ v4.1 绝不跨版本；xhigh/max-effort 等配置后缀视为同模型
			ratio = pcFzGuard(raw, c.key, ratio)
			if ratio > bestScore {
				best, bestScore = c.key, ratio
			}
		}
		if bestScore >= 0.62 {
			return best, pcF2P(math.Round(bestScore*1000) / 1000)
		}
	}
	return "", nil
}

func pcLbSummary(key string) map[string]*float64 {
	pcMu.Lock()
	row := pcCache.LbRows[key]
	cats := pcCache.LbCats
	pcMu.Unlock()
	if row == nil {
		return nil
	}
	catMeans := map[string]float64{}
	for cat, cols := range cats {
		sum, n := 0.0, 0
		for _, c := range cols {
			if v, ok := row[c]; ok && v != nil {
				sum += *v
				n++
			}
		}
		if n > 0 {
			catMeans[cat] = sum / float64(n)
		}
	}
	out := map[string]*float64{}
	if len(catMeans) == 0 {
		sum, n := 0.0, 0
		for _, v := range row {
			if v != nil {
				sum += *v
				n++
			}
		}
		var m *float64
		if n > 0 {
			m = pcF2P(math.Round(sum/float64(n)*100) / 100)
		}
		for _, d := range pcLbDims {
			out[d.Key] = m
		}
		return out
	}
	for _, d := range pcLbDims {
		if d.Cat == "" {
			sum, n := 0.0, 0
			for _, v := range catMeans {
				sum += v
				n++
			}
			out[d.Key] = pcF2P(math.Round(sum/float64(n)*100) / 100)
		} else if v, ok := catMeans[d.Cat]; ok {
			out[d.Key] = pcF2P(math.Round(v*100) / 100)
		} else {
			out[d.Key] = nil
		}
	}
	return out
}

// ---------------- Arena（服务端渲染表格解析） ----------------

var (
	pcCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	pcTagRe       = regexp.MustCompile(`<[^>]+>`)
	pcPctRe       = regexp.MustCompile(`(-?\d+(?:\.\d+)?)%`)
	pcNumRe       = regexp.MustCompile(`(\d+(?:\.\d+)?)`)
	pcTitleAttrRe = regexp.MustCompile(`title="([^"]+)"`)
)

func pcArenaNum(cell string) []float64 {
	t := pcTagRe.ReplaceAllString(pcCommentRe.ReplaceAllString(cell, ""), "")
	vals := []float64{}
	for _, m := range pcPctRe.FindAllStringSubmatch(t, -1) {
		f, _ := strconv.ParseFloat(m[1], 64)
		vals = append(vals, f)
	}
	return vals
}

func pcArenaTxt(cell string) string {
	t := pcTagRe.ReplaceAllString(pcCommentRe.ReplaceAllString(cell, ""), "")
	return strings.TrimSpace(t)
}

var pcArenaTagRe = regexp.MustCompile(`(?s)<table[^>]*>.*?</table>`)
var pcArenaTrRe = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
var pcArenaTdRe = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
var pcArenaSpanRe = regexp.MustCompile(`<span[^>]*>(\d+)</span>`)
var pcArenaTitleRe = regexp.MustCompile(`(?s)<title>([^<]{1,40})</title>`)
var pcArenaNameRe = regexp.MustCompile(`<span[^>]*\btitle="([^"]+)"`)
var pcArenaMetaRe = regexp.MustCompile(`class="text-text-secondary[^"]*"[^>]*>([^<]+)<`)

func pcParseArena(html string) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, tbl := range pcArenaTagRe.FindAllString(html, -1) {
		for _, tr := range pcArenaTrRe.FindAllStringSubmatch(tbl, -1)[1:] {
			tds := pcArenaTdRe.FindAllStringSubmatch(tr[1], -1)
			if len(tds) < 12 {
				continue
			}
			spans := []int{}
			for _, m := range pcArenaSpanRe.FindAllStringSubmatch(tds[0][1], -1) {
				n, _ := strconv.Atoi(m[1])
				spans = append(spans, n)
			}
			rank := interface{}(nil)
			if len(spans) > 0 {
				rank = spans[0]
			}
			rankCi := interface{}(nil)
			if len(spans) >= 3 {
				rankCi = []int{spans[1], spans[2]}
			}
			org := ""
			if m := pcArenaTitleRe.FindStringSubmatch(tds[1][1]); m != nil {
				org = strings.TrimSpace(m[1])
			}
			name := ""
			if m := pcArenaNameRe.FindStringSubmatch(tds[1][1]); m != nil {
				name = strings.TrimSpace(m[1])
			}
			if org != "" && strings.HasPrefix(name, org) && len(name) > len(org) {
				name = strings.TrimSpace(name[len(org):])
			}
			if name == "" {
				continue
			}
			meta := ""
			if m := pcArenaMetaRe.FindStringSubmatch(tds[1][1]); m != nil {
				meta = strings.TrimSpace(m[1])
			}
			g := func(i, k int) interface{} {
				vals := pcArenaNum(tds[2+i][1])
				if k < len(vals) {
					return vals[k]
				}
				return nil
			}
			sess := strings.ReplaceAll(pcArenaTxt(tds[8][1]), ",", "")
			sessions := interface{}(nil)
			if n, err := strconv.Atoi(sess); err == nil {
				sessions = n
			}
			cost := strings.ReplaceAll(pcArenaTxt(tds[9][1]), "$", "")
			costPerTask := interface{}(nil)
			if f, err := strconv.ParseFloat(cost, 64); err == nil {
				costPerTask = f
			}
			row := map[string]interface{}{
				"name": name, "org": org, "meta": meta, "rank": rank, "rank_ci": rankCi,
				"net": g(0, 0), "net_ci": g(0, 1), "success": g(1, 0), "praise": g(2, 0),
				"steer": g(3, 0), "bash_recovery": g(4, 0), "tool_halluc": g(5, 0),
				"sessions": sessions, "cost_per_task": costPerTask,
				"out_tokens": pcArenaTxt(tds[10][1]), "price": pcArenaTxt(tds[11][1]),
			}
			out = append(out, row)
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func pcFetchArena(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.ArenaBoards) == len(pcArenaURLs) && time.Now().Unix()-pcCache.ArenaAt < pcArenaTTL {
		// 七个分类榜全部在缓存才跳过（新增分类会自动触发补抓）
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	boards := map[string][]map[string]interface{}{}
	for cat, page := range pcArenaURLs {
		for attempt := 0; attempt < 2; attempt++ {
			body, err := pcHttpGet(page, 40*time.Second, nil)
			if err != nil {
				continue
			}
			var rows []map[string]interface{}
			if cat == "code" || cat == "overall" {
				rows = pcParseArena(body) // 信号表（Net Improvement 等 6 信号）
			} else {
				rows = pcParseArenaElo(body) // Elo 表（text/webdev/vision/search/t2i）
			}
			if len(rows) > 0 {
				boards[cat] = rows
				break
			}
		}
	}
	if len(boards) > 0 {
		pcMu.Lock()
		pcCache.ArenaBoards = boards
		pcCache.ArenaAt = time.Now().Unix()
		pcArenaMemo = map[string]map[string]interface{}{}
		pcMu.Unlock()
	}
}

func pcArenaBase(name string) string {
	return pcCanonKey(pcParenRe.ReplaceAllString(name, ""))
}

// pcStripVendorPrefix 去掉模型名首段厂商前缀（claude-fable-5.1 → fable-5.1）。
// 部分榜单（AI IQ）用无前缀 slug 命名，原名精确未命中时以此兜底（排在候选末位，
// 且仅做 canon 精确比较，不影响原名命中的来源）。
func pcStripVendorPrefix(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	for _, v := range pcVendors {
		if strings.HasPrefix(s, v.Prefix+"-") {
			return s[len(v.Prefix)+1:]
		}
	}
	return s
}

func pcArenaPack(name string, sim float64) map[string]interface{} {
	pcMu.Lock()
	code := append([]map[string]interface{}{}, pcCache.ArenaBoards["code"]...)
	overall := append([]map[string]interface{}{}, pcCache.ArenaBoards["overall"]...)
	pcMu.Unlock()
	find := func(rows []map[string]interface{}) map[string]interface{} {
		for _, r := range rows {
			if r["name"] == name {
				return r
			}
		}
		return nil
	}
	return map[string]interface{}{"name": name, "similarity": sim, "code": find(code), "overall": find(overall)}
}

func pcMatchArena(target string) map[string]interface{} {
	pcMu.Lock()
	rowsAll := append(append([]map[string]interface{}{}, pcCache.ArenaBoards["code"]...), pcCache.ArenaBoards["overall"]...)
	pcMu.Unlock()
	if len(rowsAll) == 0 {
		return nil // 不在此处触发网络抓取；预热由 pcRunRefresh 负责
	}
	// 同款识别逻辑与 Elo 榜共用 pcMatchRows（精确 → 去括号基名 → 带版本守卫的模糊匹配）
	if name, sim, ok := pcMatchRows(rowsAll, target[strings.LastIndex(target, "/")+1:]); ok {
		return pcArenaPack(name, sim)
	}
	return nil
}

func pcMatchArenaCached(target string) map[string]interface{} {
	pcMu.Lock()
	v, ok := pcArenaMemo[target]
	pcMu.Unlock()
	if ok {
		return v
	}
	v = pcMatchArena(target)
	pcMu.Lock()
	pcArenaMemo[target] = v
	pcMu.Unlock()
	return v
}

// arena.ai Elo 榜（text/webdev/vision/search/t2i）：列序 Rank|Rank Spread|Model|Score|Votes[|Price|Context]，
// t2i 等生成榜没有 Price/Context 两列（5 列），按列下标解析并做越界保护
func pcParseArenaElo(html string) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, tbl := range pcArenaTagRe.FindAllString(html, -1) {
		for _, tr := range pcArenaTrRe.FindAllStringSubmatch(tbl, -1)[1:] {
			tds := pcArenaTdRe.FindAllStringSubmatch(tr[1], -1)
			if len(tds) < 5 {
				continue
			}
			spans := []int{}
			for _, m := range pcArenaSpanRe.FindAllStringSubmatch(tds[0][1], -1) {
				n, _ := strconv.Atoi(m[1])
				spans = append(spans, n)
			}
			rank := interface{}(nil)
			if len(spans) > 0 {
				rank = spans[0]
			}
			// 模型名：任一 td 中带 title 属性的 span（与 agent 榜同构）
			name := ""
			for _, td := range tds[1:4] {
				if m := pcTitleAttrRe.FindStringSubmatch(td[1]); m != nil {
					name = strings.TrimSpace(m[1])
					break
				}
			}
			if name == "" {
				continue
			}
			// Elo 分数不带百分号，用纯数值提取（主值 ± CI）
			eloTxt := pcArenaTxt(tds[3][1])
			eloNums := []float64{}
			for _, m := range pcNumRe.FindAllStringSubmatch(eloTxt, -1) {
				f, _ := strconv.ParseFloat(m[1], 64)
				eloNums = append(eloNums, f)
			}
			score, ci := interface{}(nil), interface{}(nil)
			if len(eloNums) > 0 {
				score = eloNums[0]
			}
			if len(eloNums) > 1 {
				ci = eloNums[1]
			}
			votes := pcArenaTxt(tds[4][1])
			votesN := interface{}(nil)
			if n, err := strconv.Atoi(strings.ReplaceAll(votes, ",", "")); err == nil {
				votesN = n
			}
			row := map[string]interface{}{
				"name": name, "rank": rank, "elo": score, "elo_ci": ci, "votes": votesN,
			}
			if len(tds) > 5 {
				row["price"] = pcArenaTxt(tds[5][1])
			}
			out = append(out, row)
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

// pcMatchRows 通用榜单行匹配（同 pcMatchArena 语义，目标行参数化）。
func pcMatchRows(rows []map[string]interface{}, rawNames ...string) (string, float64, bool) {
	if len(rows) == 0 {
		return "", 0, false
	}
	raw := strings.TrimSpace(rawNames[0])
	for _, r0 := range rawNames {
		if r0 != "" {
			raw = r0
			break
		}
	}
	batchRe := regexp.MustCompile(`:(batch|free)$`)
	target := batchRe.ReplaceAllString(strings.ToLower(raw), "")
	key := pcCanonKey(target)
	if key == "" {
		return "", 0, false
	}
	names, baseOf := []string{}, map[string]string{}
	for _, r := range rows {
		n, _ := r["name"].(string)
		if n == "" {
			continue
		}
		if _, seen := baseOf[n]; !seen {
			names = append(names, n)
			baseOf[n] = pcArenaBase(n)
		}
	}
	for _, n := range names {
		if pcCanonKey(n) == key {
			return n, 1.0, true
		}
	}
	for _, n := range names {
		if baseOf[n] == key {
			return n, 0.98, true
		}
	}
	best, bestScore := "", 0.0
	for _, n := range names {
		ratio := pcLevRatio(key, baseOf[n])
		if strings.HasPrefix(baseOf[n], key) || strings.HasPrefix(key, baseOf[n]) {
			ratio = math.Min(1.0, ratio+0.06)
		}
		// 版本/配置守卫（详见 pcFzGuard）：跨版本或残留非配置 token 一律压掉
		ratio = pcFzGuard(target, strings.ToLower(n), ratio)
		if ratio > bestScore {
			best, bestScore = n, ratio
		}
	}
	if best != "" && bestScore >= 0.62 {
		return best, math.Round(bestScore*1000) / 1000, true
	}
	return "", 0, false
}

// ---------------- 模型名归一（跨渠道同款识别） ----------------
// 目标：以 OpenRouter 的模型名（id 的 name 部分，小写连字符）作为正式名。
// 归并链（从严到松）：
//   1. 精确：canon(id/name) 与 OR 名完全一致；
//   2. 展示名：产品展示名（如 "DeepSeek V4.1 Flash"）canon 后与 OR 名一致；
//   3. 家族唯一版本：token 化后 core 家族一致、本方无版本号、且 OR 家族中该 core
//      只有一个版本号 → 归并到它（版本号/日期快照永远严格区分，不会跨版本误并）；
//   4. 用户别名兜底：data/price-compare/aliases.json，{"来源id": "openrouter模型名"}。
// 版本 token：v4.1 / 4 / 3.8 等；日期快照（0731、20260731 等纯数字长串）只做精确匹配。

type orFamilyMember struct {
	Key      string
	Versions [][]int
}

var pcTokRe = regexp.MustCompile(`[a-z]+|\d+(?:\.\d+)*`)
var pcVerRe = regexp.MustCompile(`^\d+(?:\.\d+)*$`)

func pcTokens(name string) (coreKey string, versions [][]int) {
	lower := strings.ToLower(name)
	toks := pcTokRe.FindAllString(lower, -1)
	core := make([]string, 0, len(toks))
	for _, tk := range toks {
		v := tk
		if strings.HasPrefix(v, "v") && pcVerRe.MatchString(v[1:]) {
			v = v[1:]
		}
		if pcVerRe.MatchString(v) {
			versions = append(versions, pcVerInts(v))
		} else {
			core = append(core, tk)
		}
	}
	sort.Strings(core)
	return strings.Join(core, "|"), versions
}

func pcVerInts(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// pcIsDateVer 日期快照（如 0731、20260731）：单个 ≥3 位纯数字段。
func pcIsDateVer(v []int) bool {
	return len(v) == 1 && v[0] >= 100
}

// pcEffVersFlat 展平有效版本号序列（剔除 0731/20260910 日期快照段）：
// "v4.1" 与 "4-1" 两种写法归一为 [4 1]；v4 [4] 与 v4.1 [4 1] 仍不同。
func pcEffVersFlat(vers [][]int) []int {
	out := []int{}
	for _, v := range vers {
		if pcIsDateVer(v) {
			continue
		}
		out = append(out, v...)
	}
	return out
}

// pcEffortToks 推理强度/评测配置类 token：榜单行常比 API 模型名多出这类后缀
// （grok-4.7-xhigh、claude-fable-5-1-max-effort、deepseek-v4.1-flash-max），
// 它们是同一模型在不同 effort 配置下的评测结果，不构成"另一个模型"。
var pcEffortToks = map[string]bool{
	"max": true, "high": true, "xhigh": true, "medium": true, "low": true,
	"effort": true, "thinking": true, "auto": true,
}

// pcCtxAnnRe 上下文窗口注记（64k/1m）：如 claude-...-thinking-64k-high-effort
var pcCtxAnnRe = regexp.MustCompile(`(?:^|-)[0-9]+(?:\.[0-9]+)?[km](?:-|$)`)

// pcFzGuard 模糊名称守卫：对调用方算好的编辑相似度做版本一致性与残留 token 校验。
// 规则：有效版本序列不同（v4 ≠ v4.1）→ 压到 0.55；多出的非 effort token
// （gpt-4 的 "o"、v4-flash 的 "vision-exp"）说明是另一个模型 → 压到 0.55；
// 仅差 effort/版本后缀（grok-4.7 vs grok-4.7-xhigh）且强包含 → 提到 0.9。
func pcFzGuard(tgtRaw, candRaw string, ratio float64) float64 {
	trim := func(s string) string {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.IndexAny(s, ":@"); i >= 0 {
			s = s[:i]
		}
		return s
	}
	tgtTrim, candTrim := trim(tgtRaw), trim(candRaw)
	tCore, tVers := pcTokens(pcCtxAnnRe.ReplaceAllString(tgtTrim, "-"))
	cCore, cVers := pcTokens(pcCtxAnnRe.ReplaceAllString(candTrim, "-"))
	tEff, cEff := pcEffVersFlat(tVers), pcEffVersFlat(cVers)
	if len(tEff) > 0 && len(cEff) > 0 && !slices.Equal(tEff, cEff) {
		return math.Min(ratio, 0.55) // 跨版本
	}
	// OR 目标名钉了日期快照（-2407/-0731）而榜单行带真实版本号（-3）：日期快照可能
	// 是旧代（mistral-large-2407=Large 2 ≠ mistral-large-3），跨代风险保守不并。
	// 反方向不压：榜单行是日期快照（qwen3.8-max-0902）通常是该站对模型的唯一正名，
	// 与 OR 未版本化名（qwen3.8-max，跟随最新代）同款；OR 无版本号目标同样放行。
	if tReal := len(tEff) > 0; !tReal && len(tVers) > 0 && len(cEff) > 0 {
		return math.Min(ratio, 0.55)
	}
	tSet := map[string]bool{}
	for _, t := range strings.Split(tCore, "|") {
		if t != "" {
			tSet[t] = true
		}
	}
	cSet := map[string]bool{}
	for _, t := range strings.Split(cCore, "|") {
		if t != "" {
			cSet[t] = true
		}
	}
	diff := false
	extraOnlyEffort := true
	for t := range cSet {
		if !tSet[t] {
			diff = true
			if !pcEffortToks[t] {
				extraOnlyEffort = false
			}
		}
	}
	for t := range tSet {
		if !cSet[t] {
			diff = true
			if !pcEffortToks[t] {
				extraOnlyEffort = false
			}
		}
	}
	if extraOnlyEffort && diff {
		// 点分/连字符写法归一后（4.5 ≡ 4-5）再做强包含判断
		tN := strings.ReplaceAll(tgtTrim, ".", "-")
		cN := strings.ReplaceAll(candTrim, ".", "-")
		if strings.HasPrefix(cN, tN) || strings.HasPrefix(tN, cN) ||
			strings.Contains(cN, tN) || strings.Contains(tN, cN) {
			return math.Max(ratio, 0.9) // 同模型不同评测配置
		}
	}
	if extraOnlyEffort {
		return ratio
	}
	return math.Min(ratio, 0.55) // 残留差异是另一个模型
}

func pcVerEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 同款识别规则（用户可改，产品名↔API 名自动识别不准时手工指定）。
// 文件：data/price-compare/aliases.json。支持两种格式：
//
//	新格式 {"exact": {"来源": "OR模型名"}, "regex": [{"pattern":"...", "target":"...（支持 $1）"}],
//	        "split": ["强制独立的模型名"]}
//	旧格式（兼容）{"来源": "OR模型名"} 整体视为 exact。
type pcAliasRule struct {
	Pattern *regexp.Regexp
	Target  string
}

type pcAliases struct {
	Exact   map[string]string // canon(来源) → 目标名（原始串，可含厂商前缀）
	Regex   []pcAliasRule
	Split   map[string]bool // canon(来源) → 强制独立，不参与任何归并
	Builtin map[string]string
	// Bench 基准名手工归属：key = "来源|基准行显示名"（如 "livebench|DeepSeek V4 Flash"），
	// value = OR 正名 slug。命中后该 OR 模型在此来源只挂这一行基准分（跳过模糊匹配）。
	Bench map[string]string
}

// 内置映射：DeepSeek 官方 API 名不带版本号，产品线为 V4.1（升级后自动跟随正名）。
var pcBuiltinAliases = map[string]string{
	"deepseek-flash": "deepseek-v4.1-flash",
	// OpenRouter stealth 位与 OpenCode 免费档是同一模型（用户确认）；stealth 下架时 resolve 自动回退独立
	"space-bunny-free": "stealth/space-bunny-alpha",
}

func pcLoadAliases() *pcAliases {
	al := &pcAliases{
		Exact:   map[string]string{},
		Split:   map[string]bool{},
		Builtin: map[string]string{},
		Bench:   map[string]string{},
	}
	for src, dst := range pcBuiltinAliases {
		al.Exact[pcCanonKey(src)] = dst
		al.Builtin[pcCanonKey(src)] = dst
	}
	b, err := os.ReadFile(filepath.Join(pcDataDir(), "aliases.json"))
	if err != nil {
		// 文件缺失（新机器/清理 data 目录）时从实例数据库恢复并回写文件；
		// 两处都没有 → 仅内置映射
		if s := pcDbGet(pcDbAliasesKey); s != "" {
			b = []byte(s)
			_ = os.WriteFile(filepath.Join(pcDataDir(), "aliases.json"), b, 0644)
		}
	}
	if b == nil {
		return al
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return al
	}
	if bn, ok := raw["bench"]; ok {
		var m map[string]string
		if json.Unmarshal(bn, &m) == nil {
			for k, dst := range m {
				if src, name, ok2 := strings.Cut(k, "|"); ok2 && name != "" && dst != "" {
					al.Bench[src+"|"+name] = dst
				}
			}
		}
	}
	if ex, ok := raw["exact"]; ok {
		var m map[string]string
		if json.Unmarshal(ex, &m) == nil {
			for src, dst := range m {
				al.Exact[pcCanonKey(src)] = dst
			}
		}
	}
	if sp, ok := raw["split"]; ok {
		var list []string
		if json.Unmarshal(sp, &list) == nil {
			for _, src := range list {
				al.Split[pcCanonKey(src)] = true
			}
		}
	}
	if rx, ok := raw["regex"]; ok {
		var list []struct {
			Pattern string `json:"pattern"`
			Target  string `json:"target"`
		}
		if json.Unmarshal(rx, &list) == nil {
			for _, r := range list {
				re, err := regexp.Compile(r.Pattern)
				if err != nil {
					continue // 非法正则直接跳过
				}
				al.Regex = append(al.Regex, pcAliasRule{Pattern: re, Target: r.Target})
			}
		}
		return al
	}
	// 旧扁平格式：顶层全是字符串 → 全部当 exact
	var flat map[string]string
	if json.Unmarshal(b, &flat) == nil {
		for src, dst := range flat {
			al.Exact[pcCanonKey(src)] = dst
		}
	}
	return al
}

// ---------------- 扩展榜单源（OR 用量 / AA / Design Arena / 司南 / LLM Stats） ----------------

const pcBoardTTL = 12 * 3600

const (
	pcOrRankURL   = "https://openrouter.ai/api/frontend/v1/rankings/models?model_type=text"
	pcAaURL       = "https://artificialanalysis.ai/models"
	pcAaCaURL     = "https://artificialanalysis.ai/agents/coding-agents"
	pcDaURL       = "https://www.designarena.ai/leaderboard"
	pcOcURL       = "https://opencompass.oss-cn-shanghai.aliyuncs.com/assets/llm/data-llm-ability_official.json"
	pcOcOpenURL   = "https://opencompass.oss-cn-shanghai.aliyuncs.com/assets/llm/data-llm-ability_open_source.json"
	pcOcSecURL    = "https://opencompass.oss-cn-shanghai.aliyuncs.com/assets/llm/data-llm-security_value_alignment.json"
	pcOcArenaURL  = "https://opencompass.org.cn/api/v2/realtime-elo/data?pipeline=text&board=all"
	pcLsListURL   = "https://api.llm-stats.com/v1/models"
	pcLsDetailURL = "https://llm-stats.com/api/models/"
	pcAiqListURL  = "https://www.aiiq.org/api/models"
	pcAiqRankURL  = "https://www.aiiq.org/api/rankings"
)

// pcNextFlight 拼接 Next.js RSC flight 载荷（self.__next_f.push 里的 JSON 字符串段）
func pcNextFlight(html string) string {
	re := regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)
	var b strings.Builder
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		var s string
		if json.Unmarshal([]byte(m[1]), &s) == nil {
			b.WriteString(s)
		}
	}
	return b.String()
}

// pcFetchOrRankings OpenRouter 真实用量榜：按模型聚合 token 用量 → 市场份额% + 排名
func pcFetchOrRankings(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.OrRows) > 0 && time.Now().Unix()-pcCache.OrAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet(pcOrRankURL, 30*time.Second, nil)
	if err != nil {
		return
	}
	var data struct {
		Data []struct {
			Slug  string  `json:"model_permaslug"`
			Usage float64 `json:"total_usage"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil || len(data.Data) == 0 {
		return
	}
	agg := map[string]float64{}
	var total float64
	for _, r := range data.Data {
		if r.Slug == "" {
			continue
		}
		agg[r.Slug] += r.Usage
		total += r.Usage
	}
	type aggItem struct {
		slug  string
		usage float64
	}
	list := make([]aggItem, 0, len(agg))
	for s, u := range agg {
		list = append(list, aggItem{s, u})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].usage > list[j].usage })
	rows := []pcNamed{}
	for i, it := range list {
		if it.usage <= 0 || total <= 0 {
			continue
		}
		share := math.Round(it.usage/total*1e5) / 1e3
		rows = append(rows, pcNamed{Name: it.slug,
			Alias:  it.slug[strings.LastIndex(it.slug, "/")+1:],
			Scores: map[string]float64{"share": share, "usage_rank": float64(i + 1)}})
	}
	if len(rows) == 0 {
		return
	}
	pcMu.Lock()
	pcCache.OrRows = rows
	pcCache.OrAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcFetchAA Artificial Analysis：模型页 RSC 载荷里的智能指数/全知指数
func pcFetchAA(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.AaRows) > 0 && time.Now().Unix()-pcCache.AaAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet(pcAaURL, 40*time.Second, nil)
	if err != nil {
		return
	}
	payload := pcNextFlight(body)
	if len(payload) == 0 {
		return
	}
	// 记录按字段顺序序列化：每个模型的 slug 在前、intelligenceIndex 在后，
	// 以相邻 slug 切窗，在窗口内抓数值字段。
	reSlug := regexp.MustCompile(`"slug":"([^"]+)","name":"([^"]+)"`)
	reIdx := regexp.MustCompile(`"intelligenceIndex":([0-9.eE+-]+)`)
	reOmni := regexp.MustCompile(`"omniscience":([0-9.eE+-]+)`)
	locs := reSlug.FindAllStringSubmatchIndex(payload, -1)
	num := func(re *regexp.Regexp, seg string) (float64, bool) {
		m := re.FindStringSubmatch(seg)
		if m == nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(m[1], 64)
		return v, err == nil
	}
	rows := []pcNamed{}
	reCap := regexp.MustCompile(`"capabilities":\{([^}]*)\}`)
	// 子评测均为 0-1 归一分，×100 存储与主指数（0-100）同量纲；字段 null 时正则不命中。
	// 正则在行循环前一次性编译（热路径不重复 compile）。
	subEvals := []struct{ Field, Key string }{
		{"terminalBench21", "tb21"}, {"terminalBench40", "tb40"},
		{"terminalbenchHard", "tb_hard"}, {"gpqa", "gpqa"}, {"hle", "hle"},
		{"ifbench", "ifbench"}, {"scicode", "scicode"}, {"critpt", "critpt"},
		{"mmmuPro", "mmmupro"}, {"gdpvalNormalized", "gdpval"},
		{"mlcrOverall", "mlcr"}, {"tauBanking", "tau_banking"},
		{"automationBenchPartialScore", "autobench"}, {"enterpriseOpsGym", "ops_gym"},
		{"apexAgents", "apex_agents"},
	}
	type aaSubRe struct {
		re  *regexp.Regexp
		key string
	}
	subs := make([]aaSubRe, len(subEvals))
	for i, se := range subEvals {
		subs[i] = aaSubRe{regexp.MustCompile(`"` + se.Field + `":([0-9.eE+-]+)`), se.Key}
	}
	reBriefcase := regexp.MustCompile(`"briefcaseBreakdown":\{"overall":\{"elo":([0-9.eE+-]+)`)
	for i, loc := range locs {
		end := len(payload)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		seg := payload[loc[0]:end]
		idx, ok1 := num(reIdx, seg)
		if !ok1 {
			continue
		}
		scores := map[string]float64{"intelligence": idx}
		if omni, ok2 := num(reOmni, seg); ok2 {
			scores["omniscience"] = omni
		}
		for _, se := range subs {
			if v, ok2 := num(se.re, seg); ok2 {
				scores[se.key] = math.Round(v*100*100) / 100
			}
		}
		if v, ok2 := num(reBriefcase, seg); ok2 {
			scores["briefcase_elo"] = v
		}
		if m := reCap.FindStringSubmatch(seg); m != nil {
			var caps map[string]float64
			if json.Unmarshal([]byte("{"+m[1]+"}"), &caps) == nil {
				for name, v := range caps {
					scores["cap_"+name] = v
				}
			}
		}
		rows = append(rows, pcNamed{Name: payload[loc[4]:loc[5]], Alias: payload[loc[2]:loc[3]], Scores: scores})
	}
	if len(rows) == 0 {
		return
	}
	// Coding Agents 榜（agent×模型组合，3 评测集 reward + 综合指数）
	caRows := pcFetchAaCodingAgents()
	pcMu.Lock()
	pcCache.AaRows = rows
	pcCache.AaCa = caRows
	pcCache.AaAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcFetchAaCodingAgents 抓 AA Coding Agents 排行榜（RSC 载荷内嵌 agent×模型组合记录）。
// 返回组合原始记录；与 AA 主表行的归并延迟到 pcBuildUnified（复用其匹配索引）。
type aaCaCombo struct {
	AgentName     string  `json:"agentName"`
	HostModelSlug string  `json:"hostModelSlug"`
	IndexScore    float64 `json:"indexScore"`
	Display       struct {
		Model string `json:"model"`
	} `json:"display"`
	Evals []struct {
		Dataset string `json:"datasetIndexName"`
		Mean    struct {
			Reward float64 `json:"reward"`
		} `json:"mean"`
	} `json:"evals"`
}

func pcFetchAaCodingAgents() []pcAaCa {
	body, err := pcHttpGet(pcAaCaURL, 40*time.Second, nil)
	if err != nil {
		return nil
	}
	payload := pcNextFlight(body)
	// AA 的 RSC 载荷把组合行拆在多个流式 chunk 里（数组内联一部分，其余以
	// "$x:props:...:rows:N" 引用形式挂在数组外），无法整段数组解码；
	// 逐个定位 {"id":" 起始的对象解码，取含 indexScore+evals 的组合行
	combos := []aaCaCombo{}
	for i := 0; ; {
		j := strings.Index(payload[i:], `{"id":"`)
		if j < 0 {
			break
		}
		start := i + j
		var cb aaCaCombo
		if dec := json.NewDecoder(strings.NewReader(payload[start:])); dec.Decode(&cb) == nil &&
			cb.IndexScore > 0 && cb.Display.Model != "" {
			combos = append(combos, cb)
		}
		i = start + 7
	}
	caKey := map[string]string{
		"deep-swe-v1.1": "ca_deepswe", "swe-atlas-qna": "ca_sweatlas",
		"terminal-bench-v4": "ca_tb4",
	}
	out := []pcAaCa{}
	for _, cb := range combos {
		if cb.Display.Model == "" {
			continue
		}
		scores := map[string]float64{"ca_index": math.Round(cb.IndexScore*100*100) / 100}
		for _, ev := range cb.Evals {
			if key, ok := caKey[ev.Dataset]; ok {
				scores[key] = math.Round(ev.Mean.Reward*100*100) / 100
			}
		}
		out = append(out, pcAaCa{Agent: cb.AgentName, Model: cb.Display.Model,
			Slug: cb.HostModelSlug, Scores: scores})
	}
	return out
}

// aaCaMerge 把 Coding Agents 组合分归并进 AA 主表行：hostModelSlug 去厂商前缀 →
// 展示名变体，复用 pcSrcIndex 匹配（精确→基名→模糊）。深拷贝行避免并发写共享缓存。
func aaCaMerge(rows []pcNamed, combos []pcAaCa) []pcNamed {
	if len(combos) == 0 || len(rows) == 0 {
		return rows
	}
	cp := make([]pcNamed, len(rows))
	copy(cp, rows)
	for i := range cp {
		m := make(map[string]float64, len(cp[i].Scores)+4)
		for k, v := range cp[i].Scores {
			m[k] = v
		}
		cp[i].Scores = m
	}
	ix := pcBuildSrcIndex(cp)
	rowIdx := map[string]int{}
	for i := range cp {
		for _, nm := range []string{cp[i].Alias, cp[i].Name} {
			if c := pcCanonKey(nm); c != "" {
				if _, ok := rowIdx[c]; !ok {
					rowIdx[c] = i
				}
			}
		}
	}
	for _, cb := range combos {
		slug := cb.Slug
		if i := strings.IndexByte(slug, '_'); i >= 0 && i+1 < len(slug) {
			slug = slug[i+1:]
		}
		n, _, ok := ix.match(append([]string{slug}, pcCanonVariants(cb.Model)...)...)
		if !ok {
			continue
		}
		i, ok2 := rowIdx[pcCanonKey(n.Alias)]
		if !ok2 {
			i, ok2 = rowIdx[pcCanonKey(n.Name)]
		}
		if !ok2 {
			continue
		}
		for k, v := range cb.Scores {
			cp[i].Scores[k] = v
		}
	}
	return cp
}

// pcFetchDesignArena 设计竞技场：RSC 载荷 initialBoards 的 Elo / 胜率
func pcFetchDesignArena(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.DaRows) > 0 && time.Now().Unix()-pcCache.DaAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet(pcDaURL, 40*time.Second, nil)
	if err != nil {
		return
	}
	payload := pcNextFlight(body)
	i := strings.Index(payload, `"initialBoards":`)
	if i < 0 {
		return
	}
	var boards map[string]struct {
		Stats []struct {
			Model   string  `json:"model"`
			WinRate float64 `json:"winRate"`
			Elo     float64 `json:"elo"`
			Total   int     `json:"total"`
		} `json:"modelStats"`
	}
	dec := json.NewDecoder(strings.NewReader(payload[i+len(`"initialBoards":`):]))
	if dec.Decode(&boards) != nil {
		return
	}
	b, ok := boards["allcategories"]
	if !ok || len(b.Stats) == 0 {
		return
	}
	rows := []pcNamed{}
	for _, s := range b.Stats {
		if s.Model == "" || s.Total <= 0 {
			continue
		}
		rows = append(rows, pcNamed{Name: s.Model, Scores: map[string]float64{
			"elo": s.Elo, "win": s.WinRate}})
	}
	// fullstack 分类榜并入同一批行（fs_elo / fs_win）
	if fs, ok := boards["fullstack"]; ok {
		byName := map[string]int{}
		for i := range rows {
			byName[rows[i].Name] = i
		}
		for _, s := range fs.Stats {
			if i, ok2 := byName[s.Model]; ok2 && s.Total > 0 {
				rows[i].Scores["fs_elo"] = s.Elo
				rows[i].Scores["fs_win"] = s.WinRate
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	pcMu.Lock()
	pcCache.DaRows = rows
	pcCache.DaAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcFetchOpenCompass 司南：四个子榜并入同一批行（同名模型分数合并）——
// 官方能力榜（闭源）/ 开源评测榜 / 安全与价值对齐榜 / 竞技场投票 Elo（实时 API）。
func pcFetchOpenCompass(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.OcRows) > 0 && time.Now().Unix()-pcCache.OcAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	rows := []pcNamed{}
	rowIdx := map[string]int{}
	add := func(name string, scores map[string]float64) {
		if name == "" || len(scores) == 0 {
			return
		}
		c := pcCanonKey(name)
		if i, ok := rowIdx[c]; ok {
			for k, v := range scores {
				if v != 0 { // null 反序列化为 0，不能当真实分数覆盖已有值
					rows[i].Scores[k] = v
				}
			}
			return
		}
		nz := map[string]float64{}
		for k, v := range scores {
			if v != 0 {
				nz[k] = v
			}
		}
		if len(nz) == 0 {
			return
		}
		rowIdx[c] = len(rows)
		rows = append(rows, pcNamed{Name: name, Scores: nz})
	}
	type ocRow struct {
		Model string `json:"model"`
		// 官方能力榜
		Average   float64 `json:"Average"`
		Knowledge float64 `json:"Knowledge"`
		Reasoning float64 `json:"Reasoning"`
		Math      float64 `json:"Math"`
		Coding    float64 `json:"Coding"`
		// 开源评测榜
		HLE      float64 `json:"HLE"`
		AIME2025 float64 `json:"AIME2025"`
		MMLUPro  float64 `json:"MMLU-Pro"`
		LCBV6    float64 `json:"LiveCodeBenchV6"`
		GPQA     float64 `json:"GPQA-Diamond"`
		IFEval   float64 `json:"IFEval"`
		// 安全与价值对齐榜
		Score    float64 `json:"Score"`
		Fairness float64 `json:"Fairness"`
		Safety   float64 `json:"Safety"`
		Morality float64 `json:"Morality"`
		Legality float64 `json:"Legality"`
		DataProt float64 `json:"Data_Protection"`
	}
	fetchTable := func(url string, fn func(r ocRow) map[string]float64) {
		body, err := pcHttpGet(url, 25*time.Second, nil)
		if err != nil {
			return
		}
		var data struct {
			OverallTable []ocRow `json:"OverallTable"`
		}
		if json.Unmarshal([]byte(body), &data) != nil {
			return
		}
		for _, r := range data.OverallTable {
			add(r.Model, fn(r))
		}
	}
	// ① 官方能力榜：均值 + 四维
	fetchTable(pcOcURL, func(r ocRow) map[string]float64 {
		return map[string]float64{"average": r.Average, "knowledge": r.Knowledge,
			"reasoning": r.Reasoning, "math": r.Math, "coding": r.Coding}
	})
	// ② 开源评测榜：均值 + 六基准
	fetchTable(pcOcOpenURL, func(r ocRow) map[string]float64 {
		return map[string]float64{"open_avg": r.Average, "open_hle": r.HLE,
			"open_aime25": r.AIME2025, "open_mmlupro": r.MMLUPro,
			"open_lcb6": r.LCBV6, "open_gpqa": r.GPQA, "open_ifeval": r.IFEval}
	})
	// ③ 安全与价值对齐榜：总分 + 五维
	fetchTable(pcOcSecURL, func(r ocRow) map[string]float64 {
		return map[string]float64{"sec_score": r.Score, "sec_fairness": r.Fairness,
			"sec_safety": r.Safety, "sec_morality": r.Morality,
			"sec_legality": r.Legality, "sec_privacy": r.DataProt}
	})
	// ④ 竞技场投票榜（实时 Elo API）
	if body, err := pcHttpGet(pcOcArenaURL, 25*time.Second, nil); err == nil {
		var data struct {
			Data struct {
				EloTable []struct {
					ModelLabel string  `json:"modelLabel"`
					EloRating  float64 `json:"eloRating"`
					RankingElo int     `json:"rankingElo"`
				} `json:"EloTable"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(body), &data) == nil {
			for _, r := range data.Data.EloTable {
				add(r.ModelLabel, map[string]float64{
					"arena_elo": r.EloRating, "arena_rank": float64(r.RankingElo)})
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	pcMu.Lock()
	pcCache.OcRows = rows
	pcCache.OcAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcFetchLlmStats LLM Stats：站点详情页的 benchmark_rankings 即各基准全量榜
// （含全部已测模型的 0-1 归一分数与排名）。抓若干头部模型的详情页即可收齐主要基准。
func pcFetchLlmStats(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.LsBoards) > 0 && time.Now().Unix()-pcCache.LsAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	body, err := pcHttpGet(pcLsListURL, 25*time.Second, nil)
	if err != nil {
		return
	}
	var list []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(body), &list) != nil || len(list) == 0 {
		return
	}
	// 抓全部模型详情（每家详情返回它有分的基准全量榜，并集即完整基准覆盖）
	if len(list) > 120 {
		list = list[:120]
	}
	type lsRankRow struct {
		ModelID   string  `json:"model_id"`
		ModelName string  `json:"model_name"`
		Score     float64 `json:"score"`
		Rank      int     `json:"rank"`
	}
	type lsRanking struct {
		ID   string      `json:"benchmark_id"`
		Name string      `json:"benchmark_name"`
		Rows []lsRankRow `json:"models"`
	}
	type lsDetail struct {
		Rankings []lsRanking `json:"benchmark_rankings"`
	}
	type boardAcc struct {
		name string
		rows map[string]pcNamed // model_id → 行
	}
	acc := map[string]*boardAcc{}
	fetched := 0
	for _, m := range list {
		if m.ID == "" {
			continue
		}
		body, err := pcHttpGet(pcLsDetailURL+m.ID, 20*time.Second, nil)
		if err != nil {
			continue
		}
		var det lsDetail
		if json.Unmarshal([]byte(body), &det) != nil {
			continue
		}
		fetched++
		for _, rk := range det.Rankings {
			if rk.ID == "" || len(rk.Rows) == 0 {
				continue
			}
			b, ok := acc[rk.ID]
			if !ok {
				b = &boardAcc{name: rk.Name, rows: map[string]pcNamed{}}
				acc[rk.ID] = b
			}
			for _, r := range rk.Rows {
				if r.ModelID == "" || r.Score <= 0 {
					continue
				}
				b.rows[r.ModelID] = pcNamed{Name: r.ModelName, Alias: r.ModelID,
					Scores: map[string]float64{rk.ID: math.Round(r.Score*1e4) / 100, rk.ID + "_rank": float64(r.Rank)}}
			}
		}
	}
	if fetched == 0 {
		return
	}
	boards := []pcLsBoard{}
	for bid, b := range acc {
		if len(b.rows) < 8 {
			continue // 覆盖太少的基准没有比较意义
		}
		rows := make([]pcNamed, 0, len(b.rows))
		for _, n := range b.rows {
			rows = append(rows, n)
		}
		boards = append(boards, pcLsBoard{ID: bid, Name: b.name, Rows: rows})
	}
	if len(boards) == 0 {
		return
	}
	sort.Slice(boards, func(i, j int) bool { return len(boards[i].Rows) > len(boards[j].Rows) })
	if len(boards) > 36 {
		boards = boards[:36] // 覆盖最广的前 36 个基准（站点共有 80+ 个基准，过小的丢弃长尾）
	}
	pcMu.Lock()
	pcCache.LsBoards = boards
	pcCache.LsAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcFetchAiq AI IQ（aiiq.org）：/api/models 给出综合 IQ、六维 IQ 与 EQ；
// /api/rankings 列出全部榜单，其中 benchmark 型子端点给出各底层基准的原始分与
// 排名（逐个抓取并按模型 id 归并进同一行）。字段名下划线化；字段展示名存
// AiqMeta，前端与 LLM Stats 同款动态发现维度。
func pcFetchAiq(force bool) {
	pcMu.Lock()
	if !force && len(pcCache.AiqRows) > 0 && time.Now().Unix()-pcCache.AiqAt < pcBoardTTL {
		pcMu.Unlock()
		return
	}
	pcMu.Unlock()
	field := func(s string) string { return strings.ReplaceAll(s, "-", "_") }
	// ① 榜单清单：benchmark 型榜单 id（子端点）+ 维度键展示名（meta 用）
	var rankList struct {
		Rankings []struct {
			ID          string `json:"id"`
			RankingName string `json:"rankingName"`
			RankingType string `json:"rankingType"`
			Dimension   string `json:"dimension"`
		} `json:"rankings"`
	}
	body, err := pcHttpGet(pcAiqRankURL, 25*time.Second, nil)
	if err != nil || json.Unmarshal([]byte(body), &rankList) != nil || len(rankList.Rankings) == 0 {
		return
	}
	dimNames := map[string]string{}
	bench := []string{}
	for _, r := range rankList.Rankings {
		if r.ID == "" || r.RankingName == "" {
			continue
		}
		if r.RankingType == "benchmark" {
			bench = append(bench, r.ID)
		}
		// 维度展示名只取 dimension 型榜单：benchmark 型也带 dimension 字段
		//（如 arc-agi-3 属 abstract-reasoning 维），不能让它覆盖真名
		if r.RankingType == "dimension" && r.Dimension != "" {
			dimNames[r.Dimension] = r.RankingName
		}
	}
	// ② 模型总表：iq / iq_rank / eq / 六维 IQ
	var list struct {
		Models []struct {
			ID         string              `json:"id"`
			Name       string              `json:"name"`
			Rank       int                 `json:"rank"`
			IQ         *float64            `json:"iq"`
			EQ         *float64            `json:"emotionalReasoning"`
			Dimensions map[string]*float64 `json:"dimensions"`
		} `json:"models"`
	}
	body, err = pcHttpGet(pcAiqListURL, 30*time.Second, nil)
	if err != nil || json.Unmarshal([]byte(body), &list) != nil {
		return
	}
	rows := []pcNamed{}
	byKey := map[string]int{}
	meta := map[string]string{"iq": "Composite IQ", "eq": "EQ (Emotional Reasoning)"}
	for _, m := range list.Models {
		if m.ID == "" {
			continue
		}
		sc := map[string]float64{}
		if m.IQ != nil {
			sc["iq"] = *m.IQ
			if m.Rank > 0 {
				sc["iq_rank"] = float64(m.Rank)
			}
		}
		if m.EQ != nil {
			sc["eq"] = *m.EQ
		}
		for d, v := range m.Dimensions {
			if v == nil {
				continue
			}
			f := field(d)
			sc[f] = *v
			if dimNames[d] != "" {
				meta[f] = dimNames[d]
			}
		}
		if len(sc) == 0 {
			continue
		}
		byKey[pcCanonKey(m.ID)] = len(rows)
		rows = append(rows, pcNamed{Name: m.ID, Alias: strings.TrimSpace(m.Name), Scores: sc})
	}
	if len(rows) < 30 {
		return // 覆盖异常（结构改版/截断），保旧缓存
	}
	// ③ benchmark 子榜单：原始分 + 排名并入同一行（field / field_rank）
	for _, id := range bench {
		rb, err := pcHttpGet(pcAiqRankURL+"/"+id, 25*time.Second, nil)
		if err != nil {
			continue
		}
		var rk struct {
			RankingName string `json:"rankingName"`
			Models      []struct {
				ID    string   `json:"id"`
				Rank  int      `json:"rank"`
				Score *float64 `json:"score"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(rb), &rk) != nil || rk.RankingName == "" {
			continue
		}
		f := field(id)
		meta[f] = rk.RankingName
		for _, x := range rk.Models {
			if x.Score == nil || x.ID == "" {
				continue
			}
			i, ok := byKey[pcCanonKey(x.ID)]
			if !ok {
				byKey[pcCanonKey(x.ID)] = len(rows)
				rows = append(rows, pcNamed{Name: x.ID, Scores: map[string]float64{}})
				i = len(rows) - 1
			}
			rows[i].Scores[f] = *x.Score
			if x.Rank > 0 {
				rows[i].Scores[f+"_rank"] = float64(x.Rank)
			}
		}
	}
	pcMu.Lock()
	pcCache.AiqRows = rows
	pcCache.AiqMeta = meta
	pcCache.AiqAt = time.Now().Unix()
	pcMu.Unlock()
}

// pcSrcIndex 单榜单源的匹配索引：canon(展示名/Alias) 精确 → 去括号基名 → 模糊
type pcSrcIndex struct {
	exact  map[string]pcNamed
	byName map[string]pcNamed
	rows   []map[string]interface{}
}

func pcBuildSrcIndex(named []pcNamed) *pcSrcIndex {
	ix := &pcSrcIndex{exact: map[string]pcNamed{}, byName: map[string]pcNamed{}}
	seen := map[string]bool{}
	for _, n := range named {
		for _, nm := range []string{n.Name, n.Alias} {
			if nm == "" {
				continue
			}
			c := pcCanonKey(nm)
			if _, ok := ix.exact[c]; !ok {
				ix.exact[c] = n
			}
			b := pcArenaBase(nm)
			if !seen[b] {
				seen[b] = true
				ix.rows = append(ix.rows, map[string]interface{}{"name": nm})
			}
			if _, ok := ix.byName[nm]; !ok {
				ix.byName[nm] = n
			}
		}
	}
	return ix
}

func (ix *pcSrcIndex) match(rawNames ...string) (pcNamed, float64, bool) {
	for _, raw := range rawNames {
		if raw == "" {
			continue
		}
		if n, ok := ix.exact[pcCanonKey(raw)]; ok {
			return n, 1.0, true
		}
	}
	for _, raw := range rawNames {
		if raw == "" {
			continue
		}
		if n, ok := ix.exact[pcArenaBase(raw)]; ok {
			return n, 0.98, true
		}
	}
	if name, sim, ok := pcMatchRows(ix.rows, rawNames...); ok {
		if n, ok2 := ix.byName[name]; ok2 {
			return n, sim, true
		}
	}
	return pcNamed{}, 0, false
}

// ---------------- 同款归并索引 ----------------

// pcResolveIdx 汇总 OpenRouter 正名索引与别名规则，统一模型库构建与对应关系接口共用。
type pcResolveIdx struct {
	orAlias   map[string]string // canon(id名/展示名) → canonical key
	orNames   map[string]string // canonical key → OR 正式名
	families  map[string][]orFamilyMember
	snapAlias map[string][]string // 去日期基名 → 唯一日期快照 key（多于一个即歧义不并）
	al        *pcAliases
}

func pcBuildResolveIdx() *pcResolveIdx {
	ix := &pcResolveIdx{
		orAlias:  map[string]string{},
		orNames:  map[string]string{},
		families: map[string][]orFamilyMember{},
	}
	pcMu.Lock()
	catModels := append([]pcCatalogEntry{}, pcCache.Models...)
	pcMu.Unlock()
	for _, m := range catModels {
		if m.Id == "" || strings.HasSuffix(m.Id, ":batch") {
			continue
		}
		np := m.Id[strings.LastIndex(m.Id, "/")+1:]
		k := pcCanonKey(np)
		if _, seen := ix.orNames[k]; seen {
			continue
		}
		ix.orNames[k] = np
		ix.orAlias[k] = k
		if dn := pcCanonKey(m.Name); dn != "" {
			if _, ex := ix.orAlias[dn]; !ex {
				ix.orAlias[dn] = k // 展示名别名："DeepSeek V4.1 Flash" → deepseekv41flash
			}
		}
		core, vers := pcTokens(np)
		eff := [][]int{}
		for _, v := range vers {
			if !pcIsDateVer(v) {
				eff = append(eff, v) // 日期快照不参与家族版本判定
			}
		}
		ix.families[core] = append(ix.families[core], orFamilyMember{Key: k, Versions: eff})
	}
	// 日期快照别名："qwen3.8-max-0902" 这类 基名+纯数字日期段 的 OR 名，
	// 去掉日期段后得到基名；基名恰好唯一对应一个快照时才允许自动归并
	ix.snapAlias = map[string][]string{}
	for k, np := range ix.orNames {
		segs := strings.Split(np, "-")
		n := len(segs)
		for n > 0 && len(segs[n-1]) >= 3 && isAllDigits(segs[n-1]) {
			n--
		}
		if n == len(segs) {
			continue
		}
		base := pcCanonKey(strings.Join(segs[:n], "-"))
		if base == "" {
			continue
		}
		ix.snapAlias[base] = append(ix.snapAlias[base], k)
	}
	ix.al = pcLoadAliases()
	// deepseek-flash 的指向用动态别名覆盖静态兜底：官方升级后
	// ~deepseek/deepseek-flash-latest 改指新模型，这里自动跟随
	pcMu.Lock()
	dst := pcCache.DsFlashTarget
	pcMu.Unlock()
	if dst == "" {
		dst = pcBuiltinAliases["deepseek-flash"]
	}
	if dst != "" {
		c := pcCanonKey("deepseek-flash")
		ix.al.Exact[c] = dst
		ix.al.Builtin[c] = dst
	}
	return ix
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// resolve 把渠道侧模型名归并到 OR 正名；ok=false 表示无同款，保留本渠道原名。
// source 返回归并依据：or=精确/展示名 family=家族唯一版本 exact=用户精确别名
// regex=用户正则 builtin=内置映射。
// 顺序：强制独立(split) → 用户精确别名 → 用户正则 → 内置映射 → OR 精确/展示名 → 家族唯一版本。
func (ix *pcResolveIdx) resolve(rawNames ...string) (key, name, source string, ok bool) {
	pcOrTarget := func(dst string) (string, string, bool) {
		np := dst[strings.LastIndex(dst, "/")+1:]
		k := pcCanonKey(np)
		if nm, ok := ix.orNames[k]; ok {
			return k, nm, true
		}
		return "", "", false
	}
	for _, raw := range rawNames {
		if raw == "" {
			continue
		}
		// 括号上下文（如 "GPT 5.6 Luna (> 272K tokens)"）不参与匹配，剥掉后再归一
		for _, c := range pcCanonVariants(raw) {
			if ix.al.Split[c] {
				return "", "", "", false // 用户指定强制独立
			}
			if dst, ok := ix.al.Exact[c]; ok {
				if k, nm, ok2 := pcOrTarget(dst); ok2 {
					// pcLoadAliases 会把内置规则并入 Exact：命中值一致标 builtin，用户覆盖同键才是 exact
					src := "exact"
					if bd, isB := ix.al.Builtin[c]; isB && bd == dst {
						src = "builtin"
					}
					return k, nm, src, true
				}
			}
			for _, rule := range ix.al.Regex {
				if rule.Pattern.MatchString(raw) {
					dst := rule.Pattern.ReplaceAllString(strings.ToLower(raw), rule.Target)
					if k, nm, ok2 := pcOrTarget(dst); ok2 {
						return k, nm, "regex", true
					}
				}
			}
			if dst, ok := ix.al.Builtin[c]; ok {
				if k, nm, ok2 := pcOrTarget(dst); ok2 {
					return k, nm, "builtin", true
				}
			}
			if k, ok := ix.orAlias[c]; ok {
				return k, ix.orNames[k], "or", true
			}
		}
		// 日期快照唯一回退：渠道名无日期，OR 目录恰好只有该名的一个日期快照（如
		// "Qwen3.8 Max" → qwen3.8-max-0902）；有多个快照属歧义，保守不归并
		for _, c := range pcCanonVariants(raw) {
			if keys, ok := ix.snapAlias[c]; ok && len(keys) == 1 {
				k := keys[0]
				return k, ix.orNames[k], "snapshot", true
			}
		}
	}
	// 家族唯一版本：core 家族一致 + 本方无版本号 + OR 家族仅一个版本号
	for _, raw := range rawNames {
		if raw == "" {
			continue
		}
		core, vers := pcTokens(pcParenRe.ReplaceAllString(raw, ""))
		if len(vers) > 0 {
			break // 本方带版本号：版本必须精确一致（规则 1/2 已覆盖），绝不跨版本归并
		}
		members, ok := ix.families[core]
		if !ok {
			continue
		}
		distinct := [][]int{}
		soleKey := ""
		for _, m := range members {
			if len(m.Versions) == 0 {
				continue
			}
			merged := []int{}
			for _, v := range m.Versions {
				merged = append(merged, v...)
			}
			dup := false
			for _, u := range distinct {
				if pcVerEq(u, merged) {
					dup = true
					break
				}
			}
			if !dup {
				distinct = append(distinct, merged)
				soleKey = m.Key
			}
		}
		if len(distinct) == 1 && soleKey != "" {
			return soleKey, ix.orNames[soleKey], "family", true
		}
		return "", "", "", false // 家族多版本且本方无版本：歧义，保守不归并
	}
	return "", "", "", false
}

// ---------------- 统一模型库 ----------------

// pcExtSrcLists 五个扩展榜单源的锁内快照（orr/aa/da/oc 直用；ls 按模型合并各基准分数）。
// pcBuildUnified 的匹配与 mapping 端点的「实际映射/未匹配行」共用，保证两处行集合一致。
func pcExtSrcLists() map[string][]pcNamed {
	srcLists := map[string][]pcNamed{}
	pcMu.Lock()
	srcLists["orr"] = pcCache.OrRows
	srcLists["aa"] = aaCaMerge(pcCache.AaRows, pcCache.AaCa)
	srcLists["da"] = pcCache.DaRows
	srcLists["oc"] = pcCache.OcRows
	srcLists["aiq"] = pcCache.AiqRows
	lsBoards := pcCache.LsBoards
	pcMu.Unlock()
	lsMerged := map[string]pcNamed{}
	lsMerge := func(n pcNamed) {
		key := pcCanonKey(n.Alias)
		if key == "" {
			key = pcCanonKey(n.Name)
		}
		if key == "" {
			return
		}
		cur, ok := lsMerged[key]
		if !ok {
			cur = pcNamed{Name: n.Name, Alias: n.Alias, Scores: map[string]float64{}}
		}
		for k, v := range n.Scores {
			cur.Scores[k] = v
		}
		lsMerged[key] = cur
	}
	for _, b := range lsBoards {
		for _, n := range b.Rows {
			lsMerge(n)
		}
	}
	lsList := make([]pcNamed, 0, len(lsMerged))
	for _, n := range lsMerged {
		lsList = append(lsList, n)
	}
	srcLists["ls"] = lsList
	return srcLists
}

func pcBuildUnified() []pcUnifiedRow {
	pcFetchExchange(false)
	rows := []pcUnifiedRow{}

	// ---- OpenRouter 正名索引（同款归并依据，见上方"模型名归一"说明） ----
	ix := pcBuildResolveIdx()
	// ---- 全量基准装配（LiveBench 全类目 + Arena 全榜信号/Elo） ----
	eloBoards := map[string][]map[string]interface{}{}
	eloMemo := map[string]map[string]interface{}{}
	pcMu.Lock()
	for _, cat := range []string{"text", "webdev", "i2w", "vision", "search", "t2i", "document"} {
		if rows, has := pcCache.ArenaBoards[cat]; has && len(rows) > 0 {
			eloBoards[cat] = rows
			eloMemo[cat] = map[string]interface{}{}
			for _, r := range rows {
				if nm, ok2 := r["name"].(string); ok2 {
					eloMemo[cat][pcCanonKey(nm)] = r
				}
			}
		}
	}
	pcMu.Unlock()
	eloOf := func(cat, raw string) (float64, float64, bool) {
		m, ok := eloMemo[cat][pcCanonKey(raw)]
		if !ok {
			rows := eloBoards[cat]
			if len(rows) == 0 {
				return 0, 0, false
			}
			nm, _, ok2 := pcMatchRows(rows, raw)
			if !ok2 {
				return 0, 0, false
			}
			m = eloMemo[cat][pcCanonKey(nm)]
			if m == nil {
				for _, r := range rows {
					if n, _ := r["name"].(string); n == nm {
						m = r
						break
					}
				}
			}
			if m == nil {
				return 0, 0, false
			}
		}
		mm := m.(map[string]interface{})
		elo, _ := mm["elo"].(float64)
		rank, _ := mm["rank"].(int)
		return elo, float64(rank), true
	}
	// ---- 扩展榜单源：锁内快照 → 匹配索引（orr/aa/da/oc 直用；ls 按模型合并各基准分数） ----
	srcLists := pcExtSrcLists()
	srcIdx := map[string]*pcSrcIndex{}
	for src, named := range srcLists {
		srcIdx[src] = pcBuildSrcIndex(named)
	}
	// 用户基准名归属（aliases.json 的 bench 段）：把指定基准行钉到指定 OR 模型，
	// 命中 pin 的模型在此来源跳过模糊匹配，保证修正后分数归属确定
	pinsByModel := map[string]map[string]pcNamed{}
	al := pcLoadAliases()
	for k, dst := range al.Bench {
		src, name, ok := strings.Cut(k, "|")
		if !ok || srcIdx[src] == nil {
			continue
		}
		if n, ok2 := srcIdx[src].exact[pcCanonKey(name)]; ok2 {
			mk := pcCanonKey(dst)
			if pinsByModel[mk] == nil {
				pinsByModel[mk] = map[string]pcNamed{}
			}
			pinsByModel[mk][src] = n
		}
	}
	// livebench 的 pin 单独走 pcMatchLivebench 结果覆盖（其挂载不经 matchSrc）
	lbPins := map[string]string{}
	for k, dst := range al.Bench {
		if src, name, ok := strings.Cut(k, "|"); ok && src == "livebench" && name != "" {
			lbPins[pcCanonKey(dst)] = name
		}
	}
	lbPinOverride := func(modelKey string) (string, bool) {
		pinned, ok := lbPins[modelKey]
		if !ok {
			return "", false
		}
		pcMu.Lock()
		has := pcCache.LbRows[pinned] != nil
		pcMu.Unlock()
		if has {
			return pinned, true
		}
		if nm, sim2 := pcMatchLivebench(pinned); nm != "" && sim2 != nil {
			return nm, true // 钉住名可模糊定位到具体行
		}
		return "", false
	}
	srcMemo := map[string]map[string]*pcBenchEntry{}
	matchSrc := func(modelKey, src string, rawNames ...string) *pcBenchEntry {
		ix := srcIdx[src]
		if ix == nil {
			return nil
		}
		if srcMemo[src] == nil {
			srcMemo[src] = map[string]*pcBenchEntry{}
		}
		if pn, ok := pinsByModel[modelKey][src]; ok {
			return &pcBenchEntry{Name: pn.Name, Sim: 1.0, Scores: pn.Scores}
		}
		rawKey := strings.Join(rawNames, "|")
		if e, ok := srcMemo[src][rawKey]; ok {
			return e
		}
		var e *pcBenchEntry
		if n, sim, ok2 := ix.match(rawNames...); ok2 {
			e = &pcBenchEntry{Name: n.Name, Sim: sim, Scores: n.Scores}
		}
		srcMemo[src][rawKey] = e
		return e
	}
	// attachBench：给一行装配 livebench 全类目 + arena agent 12 信号 + elo 榜 + 六个扩展源
	attachBench := func(row *pcUnifiedRow) {
		row.Bench = map[string]*pcBenchEntry{}
		if row.LbName != nil && *row.LbName != "" {
			if sc := pcLbSummary(*row.LbName); sc != nil {
				e := pcBenchEntry{Name: *row.LbName, Scores: map[string]float64{}}
				if row.LbSim != nil {
					e.Sim = *row.LbSim
				}
				for k2, v := range sc {
					if v != nil {
						e.Scores[k2] = *v
					}
				}
				row.Bench["livebench"] = &e
			}
		}
		if ar := row.Arena; ar != nil {
			e := pcBenchEntry{Scores: map[string]float64{}}
			if n, ok2 := ar["name"].(string); ok2 {
				e.Name = n
			}
			if sv, ok2 := ar["similarity"].(float64); ok2 {
				e.Sim = sv
			}
			for _, board := range []string{"code", "overall"} {
				bm, _ := ar[board].(map[string]interface{})
				if bm == nil {
					continue
				}
				for _, sig := range []string{"net", "success", "praise", "steer", "bash_recovery", "tool_halluc", "rank"} {
					switch v := bm[sig].(type) {
					case float64:
						e.Scores[board+"_"+sig] = v
					case int:
						e.Scores[board+"_"+sig] = float64(v)
					}
				}
			}
			row.Bench["arena"] = &e
		}
		for cat := range eloBoards {
			elo, rank, ok2 := eloOf(cat, row.Name)
			if !ok2 {
				elo, rank, ok2 = eloOf(cat, row.Ref)
			}
			if !ok2 {
				continue
			}
			e := row.Bench["arena"]
			if e == nil {
				e = &pcBenchEntry{Scores: map[string]float64{}}
				row.Bench["arena"] = e
			}
			e.Scores[cat+"_elo"] = elo
			e.Scores[cat+"_rank"] = rank
		}
		for _, src := range []string{"orr", "aa", "da", "oc", "ls", "aiq"} {
			if e := matchSrc(row.Key, src, row.Name, row.Ref, row.FullName, pcStripVendorPrefix(row.Name)); e != nil {
				row.Bench[src] = e
			}
		}
	}
	pcMu.Lock()
	textById := map[string]bool{}
	for _, m := range pcCache.Models {
		textById[m.Id] = len(m.OutputModalities) == 0 || slices.Contains(m.OutputModalities, "text")
	}
	prices := make([]struct {
		K string
		V pcOrPrice
	}, 0, len(pcCache.Prices))
	for k, v := range pcCache.Prices {
		prices = append(prices, struct {
			K string
			V pcOrPrice
		}{k, v})
	}
	pcMu.Unlock()
	sort.Slice(prices, func(i, j int) bool { return prices[i].K < prices[j].K })

	for _, kv := range prices {
		if strings.HasSuffix(kv.K, ":batch") || kv.V.Error != "" || len(kv.V.Providers) == 0 {
			continue
		}
		// OR 的 :free 用途对用户免费（目录定价 0/0），但 /endpoints 查询会忽略
		// :free 后缀返回基础模型的付费端点价——行构建时强制按 0 计价，否则
		// 免费变体会显示成付费价格
		isFree := strings.HasSuffix(kv.K, ":free")
		name := kv.K[strings.LastIndex(kv.K, "/")+1:]
		lbName, lbSim := pcMatchLivebench(kv.K)
		if pn, ok := lbPinOverride(pcCanonKey(name)); ok {
			lbName, lbSim = pn, pcF2P(1.0)
		}
		var lbMap map[string]*float64
		if lbName != "" {
			lbMap = pcLbSummary(lbName)
		}
		// 完全展开：每个供应商一条报价行（供应商+量化+价格相同视为重复）
		seen := map[string]bool{}
		for _, p := range kv.V.Providers {
			if p.Prompt == 0 && p.Completion == 0 {
				// 已知非文本输出（音乐/图像等按次计费）不参与按 token 比价；
				// 文本模型的 0 价格是免费用途（如 OpenRouter stealth），保留
				if isText, known := textById[kv.K]; known && !isText {
					continue
				}
			}
			dup := p.Provider + "|" + p.Quantization + "|" +
				strconv.FormatFloat(p.Prompt, 'g', 6, 64) + "|" + strconv.FormatFloat(p.Completion, 'g', 6, 64)
			if seen[dup] {
				continue
			}
			seen[dup] = true
			// 缓存写入价：OR 官方 input_cache_write（多数模型 = 1.25×prompt）；
			// 端点未给该值或旧缓存条目缺字段时按 prompt 计（历史兜底）
			write := p.CacheWrite
			if write == 0 {
				write = p.Prompt
			}
			row := pcUnifiedRow{
				Channel: "openrouter", Ref: kv.K, Name: name, FullName: kv.K,
				Key: pcCanonKey(name), Vendor: pcVendorOf(kv.K),
				Currency: "USD", Multiplier: 1.0,
				In:       p.Prompt * 1e6,
				Out:      p.Completion * 1e6,
				Read:     p.CacheRead * 1e6,
				Write:    write * 1e6,
				Provider: p.Provider, Quant: p.Quantization,
				Throughput: p.ThroughputP50, Latency: p.LatencyP50, Uptime1d: p.Uptime1d,
			}
			if lbName != "" {
				row.LbName = &lbName
				row.LbSim = lbSim
				row.Lb = lbMap
			}
			if isFree {
				row.In, row.Out, row.Read, row.Write = 0, 0, 0, 0
			}
			row.Arena = pcMatchArenaCached(name)
			if row.Arena == nil {
				row.Arena = pcMatchArenaCached(kv.K)
			}
			attachBench(&row)
			rows = append(rows, row)
		}
	}

	// OpenCode Go：空闲/高峰各一行（倍率 = 10/每月限额）
	pcMu.Lock()
	goModels := append([]pcGoModel{}, pcCache.GoModels...)
	pcMu.Unlock()
	merged := map[string]map[string]interface{}{}
	order := []string{}
	for _, m := range goModels {
		mperi := pcGoPeriodRe.FindStringSubmatch(m.Name)
		base := strings.TrimSpace(pcGoPeriodRe.ReplaceAllString(m.Name, ""))
		period := "off"
		if mperi != nil && mperi[1] == "Peak" {
			period = "peak"
		}
		k := fmt.Sprintf("%s|%s|%g", base, m.ModelId, m.Limit)
		if _, ok := merged[k]; !ok {
			merged[k] = map[string]interface{}{"name": base, "id": m.ModelId, "limit": m.Limit, "note": m.Note, "off": nil, "peak": nil}
			order = append(order, k)
		}
		merged[k][period] = m
	}
	for _, k := range order {
		it := merged[k]
		_, hasPeak := it["peak"].(pcGoModel)
		split := hasPeak
		name := it["name"].(string)
		lim := it["limit"].(float64)
		rowKey, rowName, _, _ := ix.resolve(it["id"].(string), name)
		if rowKey == "" {
			// 未归并到 OR 的模型（如 OpenCode 独有）：名字用 API slug（与请求名一致），
			// 展示名保留在 FullName
			rowKey, rowName = pcCanonKey(it["id"].(string)), it["id"].(string)
		}
		// 注释列：文档名括号注释（上下文档位等）+ 文档注释（如限时活动）合并；
		// 名字字段保持 OR 正名 slug，不带括号
		note := it["note"].(string)
		if quals := pcParenContentRe.FindAllStringSubmatch(name, -1); len(quals) > 0 {
			if q := strings.TrimSpace(quals[0][1]); q != "" {
				if note != "" {
					note = q + " · " + note
				} else {
					note = q
				}
			}
		}
		// LiveBench 匹配优先用归并正名（渠道 id 可能是无版本号别名，如 deepseek-chat）
		lbName, lbSim := pcMatchLivebench(rowName, it["id"].(string), name)
		if pn, ok := lbPinOverride(rowKey); ok {
			lbName, lbSim = pn, pcF2P(1.0)
		}
		var lbs map[string]*float64
		if lbName != "" {
			lbs = pcLbSummary(lbName)
		}
		for pk, pname := range map[string]string{"off": "空闲", "peak": "高峰"} {
			var m pcGoModel
			var ok bool
			if pk == "off" {
				m, ok = it["off"].(pcGoModel)
			} else {
				m, ok = it["peak"].(pcGoModel)
			}
			if !ok {
				continue
			}
			mul := 10.0 / lim
			if lim <= 0 {
				mul = 0 // 无限制/免费档：无额度换算，actual() 约定 0 视为 ×1
			}
			row := pcUnifiedRow{
				Channel: "opencode", Ref: it["id"].(string), Name: rowName,
				FullName: name + fmt.Sprintf("（%s）", pname), Key: rowKey,
				Vendor:   pcVendorOf(it["id"].(string)),
				Currency: "USD", Multiplier: mul,
				In: m.Input, Out: m.Output, Read: m.CacheRead,
				Write:    m.Input,
				Provider: "OpenCode Go", Quant: "—",
				Period: "", Split: split, Limit: &lim, Note: note,
			}
			if m.CacheWrite != nil {
				row.Write = *m.CacheWrite
			}
			if split {
				row.Period = pk
			}
			if lbName != "" {
				row.LbName = &lbName
				row.LbSim = lbSim
				row.Lb = lbs
			}
			row.Arena = pcMatchArenaCached(it["id"].(string))
			if row.Arena == nil {
				row.Arena = pcMatchArenaCached(name)
			}
			attachBench(&row)
			rows = append(rows, row)
		}
	}

	// DeepSeek 官方：人民币计价
	pcMu.Lock()
	dsModels := append([]pcDsModel{}, pcCache.DsModels...)
	pcMu.Unlock()
	for _, dm := range dsModels {
		split := pcDsSplit(dm)
		rowKey, rowName, _, _ := ix.resolve(dm.Id, dm.Name)
		if rowKey == "" {
			rowKey, rowName = pcCanonKey(dm.Id), dm.Id
		}
		// LiveBench 匹配优先用归并正名/展示名（渠道 id 可能是无版本号别名，如 deepseek-chat）
		lbName, lbSim := pcMatchLivebench(rowName, dm.Name, dm.Id)
		if pn, ok := lbPinOverride(rowKey); ok {
			lbName, lbSim = pn, pcF2P(1.0)
		}
		var lbs map[string]*float64
		if lbName != "" {
			lbs = pcLbSummary(lbName)
		}
		for _, pk := range []string{"off", "peak"} {
			idx := 0
			if pk == "peak" {
				if !split {
					continue
				}
				idx = 1
			}
			pname := "空闲"
			if pk == "peak" {
				pname = "高峰"
			}
			row := pcUnifiedRow{
				Channel: "deepseek", Ref: dm.Id, Name: rowName,
				FullName: dm.Id + fmt.Sprintf("（%s）", pname), Key: rowKey,
				Vendor:   pcVendorOf(dm.Id),
				Currency: "CNY", Multiplier: 1.0,
				In: dm.CacheMiss[idx], Out: dm.Output[idx], Read: dm.CacheHit[idx],
				Write:    dm.CacheMiss[idx],
				Provider: "DeepSeek 官方", Quant: "—",
				Period: "", Split: split,
			}
			if split {
				row.Period = pk
			}
			if lbName != "" {
				row.LbName = &lbName
				row.LbSim = lbSim
				row.Lb = lbs
			}
			row.Arena = pcMatchArenaCached(dm.Id)
			attachBench(&row)
			rows = append(rows, row)
		}
	}

	// 中转站报价（buzzai/apikl/ikun 抓取时已折算 CNY；apib 定价有 307 门禁未接入）
	reseller := pcResellerUnifiedRows(ix)
	rate := pcExchangeRate()
	for i := range reseller {
		r := reseller[i]
		if r.Currency != "CNY" {
			r.In *= rate
			r.Out *= rate
			r.Read *= rate
			r.Write *= rate
			r.Currency = "CNY"
		}
		attachBench(&r)
		rows = append(rows, r)
	}

	pcAttachProviderOrder(rows)
	return rows
}

// 比价实际价的 OR 供应商顺序：OR 模型 id（及 canon）→ 供应商 slug 列表（实际价升序）。
// 请求发往 OpenRouter 时注入 provider.order——OR 官方 sort=price 只按列表价排，
// 与含缓存读写/时段加权的实际价顺序不一致。
var pcProviderOrder atomic.Pointer[map[string][]string]

func pcProviderOrderFor(upstreamModel string) []string {
	if m := pcProviderOrder.Load(); m != nil {
		if o, ok := (*m)[upstreamModel]; ok {
			return o
		}
		return (*m)[pcCanonKey(upstreamModel)]
	}
	return nil
}

// pcAttachProviderOrder 供应商顺序以「模型优先级列表」为准：该模型在列表里的
// OpenRouter 报价顺序（含手工上移/下移）就是 provider.order 的前段，列表没覆盖的
// 供应商再按实际价公式（与面板同款：未缓存×i + 命中×cr + 写入×cw + 输出比×o）
// 补在后段。不在列表里的模型整体退回实际价顺序。
func pcAttachProviderOrder(rows []pcUnifiedRow) {
	ratios := pcReadRatios()
	eff := pcRatio{Uncached: 1}
	if r, ok := ratios["month"]; ok && r != nil {
		eff = *r
	}
	rate := pcExchangeRate()
	perRef := map[string]map[string]float64{} // OR id -> slug -> 最低实际价
	for _, r := range rows {
		if r.Channel != "openrouter" || r.Ref == "" || r.Provider == "" {
			continue
		}
		cur := 1.0
		if r.Currency != "CNY" {
			cur = rate
		}
		a := (eff.Uncached*r.In + eff.Cache*r.Read + eff.CacheWrite*r.Write + eff.Output*r.Out) * cur
		slug := orProviderSlug(r.Provider, r.Quant)
		m := perRef[r.Ref]
		if m == nil {
			m = map[string]float64{}
			perRef[r.Ref] = m
		}
		if prev, dup := m[slug]; !dup || a < prev {
			m[slug] = a
		}
	}
	base := map[string][]string{}
	for ref, slugs := range perRef {
		type sp struct {
			slug string
			a    float64
		}
		list := make([]sp, 0, len(slugs))
		for s, a := range slugs {
			list = append(list, sp{s, a})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].a < list[j].a })
		order := make([]string, 0, len(list))
		for _, x := range list {
			order = append(order, x.slug)
		}
		base[ref] = order
		base[pcCanonKey(ref)] = order
	}
	out := map[string][]string{}
	for k, e := range pcLoadPriority().Entries {
		listed := []string{}
		seen := map[string]bool{}
		ref := ""
		for _, of := range e.Offers {
			if of.Channel != "openrouter" {
				continue
			}
			if ref == "" {
				ref = of.Ref
			}
			slug := orProviderSlug(of.Provider, of.Quant)
			if slug == "" || seen[slug] {
				continue
			}
			seen[slug] = true
			listed = append(listed, slug)
		}
		if len(listed) == 0 || ref == "" {
			continue
		}
		full := listed
		for _, s := range base[ref] {
			if !seen[s] {
				full = append(full, s)
			}
		}
		out[ref] = full
		out[pcCanonKey(ref)] = full
		out[pcCanonKey(k)] = full
	}
	for ref, order := range base {
		if _, ok := out[ref]; !ok {
			out[ref] = order
		}
	}
	pcProviderOrder.Store(&out)
}

// ---- OR 供应商 slug 对照表：显示名 → 路由 slug（InferenceNet→inference-net、
// DekaLLM→dekallm 这类不规则映射无法从显示名推导），来自公开 /api/v1/providers；
// 未命中（对照表未就绪/新供应商）回退小写推导 ----

var pcOrSlugMap atomic.Pointer[map[string]string]
var pcOrSlugFetching atomic.Bool

func pcFetchOrProviderSlugs() {
	if !pcOrSlugFetching.CompareAndSwap(false, true) {
		return
	}
	defer pcOrSlugFetching.Store(false)
	body, err := pcHttpGet("https://openrouter.ai/api/v1/providers", 20*time.Second, nil)
	if err != nil {
		return
	}
	var out struct {
		Data []struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &out) != nil || len(out.Data) == 0 {
		return
	}
	m := make(map[string]string, len(out.Data))
	for _, p := range out.Data {
		if p.Name != "" && p.Slug != "" {
			m[p.Name] = p.Slug
		}
	}
	pcOrSlugMap.Store(&m)
}

// orProviderSlug OpenRouter 路由的端点 slug：对照表命中用官方 slug，否则小写推导；
// 量化后缀（如 deepinfra/fp8；量化 unknown 时用裸名）
func orProviderSlug(provider, quant string) string {
	if provider == "" {
		return ""
	}
	slug := ""
	if m := pcOrSlugMap.Load(); m != nil {
		slug = (*m)[provider]
	}
	if slug == "" {
		slug = strings.ToLower(provider)
	}
	if q := strings.ToLower(quant); q != "" && q != "unknown" {
		slug += "/" + q
	}
	return slug
}

var pcRebuilding atomic.Bool

func pcGetUnified(force bool) ([]pcUnifiedRow, int64) {
	pcMu.Lock()
	if !force && len(pcUnifiedRows) > 0 && time.Now().Unix()-pcUnifiedAt < pcUnifiedTTL {
		r := pcUnifiedRows
		at := pcUnifiedAt
		pcMu.Unlock()
		return r, at
	}
	// 行情过期：先返回旧行（缓存型数据，旧几分钟无碍），后台重建——
	// 同步重建要十几秒，不能卡在请求里；后台刷新完成时也会强制重建一次
	if !force && len(pcUnifiedRows) > 0 {
		pcMu.Unlock()
		if pcRebuilding.CompareAndSwap(false, true) {
			go func() {
				defer pcRebuilding.Store(false)
				rows := pcBuildUnified()
				pcMarkDeadRows(rows)
				at := time.Now().Unix()
				pcMu.Lock()
				pcUnifiedRows = rows
				pcUnifiedAt = at
				pcMu.Unlock()
				pcSaveUnified(rows, at)
			}()
		}
		pcMu.Lock()
		r := pcUnifiedRows
		at := pcUnifiedAt
		pcMu.Unlock()
		return r, at
	}
	// 无行且预热中：立即返回空数据与状态，由前端轮询等待，避免请求悬挂
	if !force && pcRefresh.Running {
		pcMu.Unlock()
		return []pcUnifiedRow{}, 0
	}
	pcMu.Unlock()
	rows := pcBuildUnified()
	pcMarkDeadRows(rows)
	at := time.Now().Unix()
	pcMu.Lock()
	pcUnifiedRows = rows
	pcUnifiedAt = at
	pcMu.Unlock()
	pcSaveUnified(rows, at)
	return rows, at
}

// pcMarkDeadRows 按实测结果给行打 dead 标记（pcCache.DeadOffers 为双写入口：
// 刷新尾部免费档批量实测重建免费键，apply 连通性过滤追加付费键、实测通过移除，
// 详见 pcCacheData.DeadOffers 字段注释）
func pcMarkDeadRows(rows []pcUnifiedRow) {
	pcMu.Lock()
	dead := pcCache.DeadOffers
	pcMu.Unlock()
	if len(dead) == 0 {
		return
	}
	for i := range rows {
		r := &rows[i]
		c := pcTestCand{strings.ToLower(r.Channel), r.Ref, r.Provider, r.Quant}
		if dead[c.key()] {
			r.Dead = true
		}
	}
}

// ---------------- 行情持久化 ----------------

// pcUnifiedPersist 构建结果落盘（unified.json）：重启后直接载入，免去冷启动
// 十几秒的全量重建。行情完全可由 cache.json 重建派生，故只落文件不进 DB。
type pcUnifiedPersist struct {
	At   int64          `json:"at"`
	Rows []pcUnifiedRow `json:"rows"`
}

func pcUnifiedFile() string { return filepath.Join(pcDataDir(), "unified.json") }

// pcUnifiedFileMu 序列化 unified.json 落盘：stale 后台重建、refresh 内的 force
// 重建与管理员别名触发的同时进行时，避免对同一路径并发 O_TRUNC 写出撕裂文件
var pcUnifiedFileMu sync.Mutex

func pcSaveUnified(rows []pcUnifiedRow, at int64) {
	if len(rows) == 0 {
		return
	}
	b, err := json.Marshal(pcUnifiedPersist{At: at, Rows: rows})
	if err != nil {
		return
	}
	pcUnifiedFileMu.Lock()
	defer pcUnifiedFileMu.Unlock()
	_ = os.WriteFile(pcUnifiedFile(), b, 0644)
}

func pcLoadUnified() {
	b, err := os.ReadFile(pcUnifiedFile())
	if err != nil || len(b) == 0 {
		return
	}
	var p pcUnifiedPersist
	if json.Unmarshal(b, &p) != nil || len(p.Rows) == 0 || p.At == 0 {
		return
	}
	pcMu.Lock()
	pcUnifiedRows = p.Rows
	pcUnifiedAt = p.At
	pcMu.Unlock()
}

// ---------------- 缓存持久化 ----------------

// ---------------- 数据库持久化（options 表） ----------------
// 比价的缓存与配置除 JSON 文件外同步写入实例数据库 options 表：
// 换实例/丢文件时仍可从 DB 恢复；旧库没有这些 key 时读不到 → 空数据走立即刷新兜底。

const (
	pcDbCacheKey    = "price_compare_cache"
	pcDbPriorityKey = "price_compare_priority"
	pcDbAliasesKey  = "price_compare_aliases"
)

func pcDbGet(key string) string {
	if model.DB == nil {
		return ""
	}
	// 条件走结构体由 gorm 按方言引用列名，避免 `key` 反引号在 PostgreSQL 报错
	var val string
	if err := model.DB.Model(&model.Option{}).Where(&model.Option{Key: key}).Select("value").Scan(&val).Error; err != nil {
		common.SysLog("price_compare db get " + key + " err: " + err.Error())
		return ""
	}
	return val
}

// pcDbSet 用 gorm 的 OnConflict 子句做 upsert（MySQL 翻译为 ON DUPLICATE KEY
// UPDATE，PG/SQLite 为 ON CONFLICT DO UPDATE），不写原生 SQL 以兼容三方言
func pcDbSet(key, val string) {
	if model.DB == nil {
		return
	}
	opt := model.Option{Key: key, Value: val}
	if err := model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&opt).Error; err != nil {
		common.SysLog("price_compare db set " + key + " err: " + err.Error())
	}
}

// ---------------- 渠道 API Key（唯一录入点） ----------------
// 面板保存的 key 存 DB options 表，并同步进名称匹配的 new-api 渠道；
// 定时自动更新应用规则时也会再同步一次，渠道无需手工维护。

type pcKeyVault struct {
	Openrouter []string `json:"openrouter"`
	Opencode   []string `json:"opencode"`
	Deepseek   []string `json:"deepseek"`
	Apib       []string `json:"apib"`
	Buzzai     []string `json:"buzzai"`
	Apikl      []string `json:"apikl"`
	Ikun       []string `json:"ikun"`
}

// pcKeyKinds 各家服务商的统一遍历表：kind 关键字（渠道名匹配用）+ vault 字段访问器。
// apib/buzzai/apikl/ikun 为中转站类目：与官方三家同样由管理渠道接管（key/models/
// base_url 全覆盖），其中 buzzai/apikl/ikun 有公开定价源接入行情，apib 定价有
// 307 门禁未接入（有 key 也暂无报价可入选）。
var pcKeyKinds = []struct {
	Kind  string
	Vault func(v *pcKeyVault) *[]string
}{
	{"openrouter", func(v *pcKeyVault) *[]string { return &v.Openrouter }},
	{"opencode", func(v *pcKeyVault) *[]string { return &v.Opencode }},
	{"deepseek", func(v *pcKeyVault) *[]string { return &v.Deepseek }},
	{"apib", func(v *pcKeyVault) *[]string { return &v.Apib }},
	{"buzzai", func(v *pcKeyVault) *[]string { return &v.Buzzai }},
	{"apikl", func(v *pcKeyVault) *[]string { return &v.Apikl }},
	{"ikun", func(v *pcKeyVault) *[]string { return &v.Ikun }},
}

const pcDbKeysKey = "price_compare_keys"

func pcCleanKeys(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func pcLoadKeys() pcKeyVault {
	var v pcKeyVault
	if s := pcDbGet(pcDbKeysKey); s != "" {
		if json.Unmarshal([]byte(s), &v) != nil {
			return pcKeyVault{}
		}
	}
	v.Openrouter = pcCleanKeys(v.Openrouter)
	v.Opencode = pcCleanKeys(v.Opencode)
	v.Deepseek = pcCleanKeys(v.Deepseek)
	v.Apib = pcCleanKeys(v.Apib)
	v.Buzzai = pcCleanKeys(v.Buzzai)
	v.Apikl = pcCleanKeys(v.Apikl)
	v.Ikun = pcCleanKeys(v.Ikun)
	return v
}

func pcSaveKeys(v pcKeyVault) {
	v.Openrouter = pcCleanKeys(v.Openrouter)
	v.Opencode = pcCleanKeys(v.Opencode)
	v.Deepseek = pcCleanKeys(v.Deepseek)
	v.Apib = pcCleanKeys(v.Apib)
	v.Buzzai = pcCleanKeys(v.Buzzai)
	v.Apikl = pcCleanKeys(v.Apikl)
	v.Ikun = pcCleanKeys(v.Ikun)
	if b, err := json.Marshal(&v); err == nil {
		pcDbSet(pcDbKeysKey, string(b))
	}
}

// pcManagedChanName 各服务商「比价管理」渠道的固定名称（精确同名即管理身份，
// 由面板全权接管：key/models/base_url/供应商路由参数一律强制覆盖）。
func pcManagedChanName(keyword string) string {
	switch keyword {
	case "openrouter":
		return "openrouter(比价管理)"
	case "opencode":
		return "opencode(比价管理)"
	case "deepseek":
		return "deepseek(比价管理)"
	case "apib":
		return "apib(比价管理)"
	case "buzzai":
		return "buzzai(比价管理)"
	case "apikl":
		return "apikl(比价管理)"
	case "ikun":
		return "ikun(比价管理)"
	}
	return ""
}

// pcManagedModelRefs 当前优先级列表里每个服务商应提供的模型 ref（Offers 已按实际价升序截断）。
func pcManagedModelRefs() map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range pcLoadPriority().Entries {
		for _, of := range e.Offers {
			k := of.Channel + "|" + of.Ref
			if seen[k] {
				continue
			}
			seen[k] = true
			out[of.Channel] = append(out[of.Channel], of.Ref)
		}
	}
	return out
}

type pcKeyConflict struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// pcSyncChannelKeys 比价管理渠道接管：每个有 key 的服务商保证存在一个固定名称的
// 管理渠道，key（多 key 换行分隔 + channel_info 标记）/models/base_url/供应商路由
// 参数全部强制覆盖；OpenRouter 管理渠道带 provider.sort=price（按价格顺序尝试供应商）。
// 随后检测使用相同 key 的非管理渠道作为冲突返回，由用户确认删除。
func pcSyncChannelKeys() ([]pcKeyConflict, int) {
	conflicts := []pcKeyConflict{}
	touched := 0
	if model.DB == nil {
		return conflicts, 0
	}
	kv := pcLoadKeys()
	refs := pcManagedModelRefs()
	type prov struct {
		keyword string
		keys    []string
		baseURL string
		orSort  bool
	}
	pri := pcLoadPriority()
	// 中转站（apib/buzzai/apikl/ikun）与官方三家同样由管理渠道接管：报价入选
	// 优先级列表即自动落地为可调用的渠道，掉出列表/欠费判死时同样收窄停用——
	// 否则中转报价进列表却无渠道承接，调用 503「悬空」。apib 定价未接入（307
	// 门禁）无报价可入选，有 key 也暂不会建渠道
	for _, p := range []prov{
		{keyword: "openrouter", keys: kv.Openrouter, baseURL: "https://openrouter.ai/api", orSort: true},
		{keyword: "opencode", keys: kv.Opencode, baseURL: "https://opencode.ai/zen/go"},
		{keyword: "deepseek", keys: kv.Deepseek, baseURL: "https://api.deepseek.com"},
		{keyword: "apib", keys: kv.Apib, baseURL: "https://api.apib.ai"},
		{keyword: "buzzai", keys: kv.Buzzai, baseURL: pcBuzzaiBase},
		{keyword: "apikl", keys: kv.Apikl, baseURL: pcApiklBase},
		{keyword: "ikun", keys: kv.Ikun, baseURL: pcIkunBase},
	} {
		name := pcManagedChanName(p.keyword)
		if len(p.keys) == 0 {
			// 保险箱已无该服务商 key：停用管理渠道（不删除，重新录入 key 后下次
			// 同步自动恢复）。否则带旧 key 的渠道继续留在轮询里吃真实流量，
			// 「无 key ⇒ 不使用」在渠道层不成立
			var ch model.Channel
			if model.DB.Where("name = ?", name).Order("id").First(&ch).Error == nil &&
				ch.Status != common.ChannelStatusManuallyDisabled {
				ch.Status = common.ChannelStatusManuallyDisabled
				if ch.Update() == nil { // Update 重建 abilities（enabled=false）
					touched++
				}
			}
			continue
		}
		ch := model.Channel{}
		if model.DB.Where("name = ?", name).Order("id").First(&ch).Error != nil {
			ch = model.Channel{Name: name, Type: 1, Group: "default", Status: common.ChannelStatusEnabled}
		}
		// 强制覆盖（管理渠道完全由面板接管）
		keyStr := strings.Join(p.keys, "\n")
		ch.Key = keyStr
		ch.BaseURL = &p.baseURL
		ch.ChannelInfo.IsMultiKey = len(p.keys) > 1
		ch.ChannelInfo.MultiKeySize = len(p.keys)
		// MultiKeyStatusList 保留核心侧按 key 写入的临时禁用状态（欠费/失效单 key
		// 由真实流量错误触发 DisableChannel）：清空会把死 key 放回轮询；key 集
		// 变化时的越界清理由 Channel.Update 内部处理
		if p.orSort {
			po := `{"provider":{"sort":"price"}}`
			ch.ParamOverride = &po
		}
		if p.keyword == "opencode" {
			// OpenCode Zen 要求 x-opencode-session 会话头；管理渠道保持稳定会话 id
			//（只缺时生成，同步不换，让上游按会话做路由亲和）
			ho := map[string]interface{}{}
			if ch.HeaderOverride != nil && *ch.HeaderOverride != "" {
				_ = json.Unmarshal([]byte(*ch.HeaderOverride), &ho)
			}
			if s, _ := ho["x-opencode-session"].(string); strings.TrimSpace(s) == "" {
				ho["x-opencode-session"] = uuid.New().String()
			}
			if b, err := json.Marshal(ho); err == nil {
				s := string(b)
				ch.HeaderOverride = &s
			}
		}
		// 跨渠道同款互通：本渠道 models 额外登记同一条目其他渠道的报价名，并用
		// model_mapping 映射回本渠道自己的上游名。否则 ability 按请求名精确匹配，
		// OR 的带前缀名与 OpenCode 的裸名永远凑不齐跨渠道候选，阶段化 fallback
		// 的后续渠道阶段永远轮不到
		if m := refs[p.keyword]; len(m) == 0 {
			// 该服务商无入选报价（掉出优先级列表/报价全部判死）：清空 models/
			// model_mapping 并停用渠道，死报价不再留在渠道上被路由使用；下轮
			// 有入选报价时恢复启用。不能走下面的通用 ch.Update()：gorm
			// Updates(struct) 跳过零值 string 写不进 models，且 Update 会先从
			// DB 回读旧 models 再重建 abilities，死报价原样留存——这里显式按列
			// 写入并直接删空 abilities（而非留 enabled=false 行：管理端一键启用
			// 经 UpdateAbilityStatus 会把旧行整体救活，死模型立即恢复路由）
			if ch.Id == 0 {
				continue // 渠道还不存在则不必新建
			}
			if model.DB.Model(&model.Channel{}).Where("id = ?", ch.Id).Updates(map[string]interface{}{
				"models": "", "model_mapping": nil, "status": common.ChannelStatusManuallyDisabled,
			}).Error == nil && ch.DeleteAbilities() == nil {
				touched++
			}
			continue
		} else {
			// 有入选报价：确保启用（可能处于上轮无报价时的停用状态，充值/恢复后
			// 由这里自动拉回；Status=1 非零值，gorm Updates 可写入）
			ch.Status = common.ChannelStatusEnabled
			models := append([]string{}, m...)
			seenRef := map[string]bool{}
			for _, r := range m {
				seenRef[r] = true
			}
			mapping := map[string]string{}
			for _, e := range pri.Entries {
				own := ""
				for _, of := range e.Offers {
					if of.Channel == p.keyword {
						own = of.Ref
						break
					}
				}
				if own == "" {
					continue
				}
				seenKind := map[string]bool{}
				for _, of := range e.Offers {
					if of.Channel == p.keyword || seenKind[of.Channel] {
						continue
					}
					seenKind[of.Channel] = true
					if !seenRef[of.Ref] {
						seenRef[of.Ref] = true
						models = append(models, of.Ref)
					}
					if of.Ref != own {
						mapping[of.Ref] = own
					}
				}
			}
			// Harness 显示名（去厂商前缀+（¥价））同样要能路由：每个提供该模型的
			// 管理渠道都收录显示名并映射回本渠道自己的上游名——请求打到哪里都能
			// 换算成该渠道的真实 ref，阶段化 fallback 的后续渠道也因此可用
			for _, e := range pri.Entries {
				if len(e.Offers) == 0 || e.Offers[0].Harness == "" {
					continue
				}
				own := ""
				for _, of := range e.Offers {
					if of.Channel == p.keyword {
						own = of.Ref
						break
					}
				}
				if own == "" {
					continue // 本渠道不提供该模型，无需注册
				}
				h := e.Offers[0].Harness
				if !seenRef[h] {
					seenRef[h] = true
					models = append(models, h)
				}
				mapping[h] = own
			}
			ch.Models = strings.Join(models, ",")
			if b, err := json.Marshal(mapping); err == nil {
				// 空 mapping 也要落 "{}"：gorm Updates 跳过 nil 指针，nil 清不掉旧值
				s := string(b)
				ch.ModelMapping = &s
			}
		}
		if ch.Id == 0 {
			if ch.Insert() == nil { // Insert 会同时建 abilities
				touched++
			}
		} else if ch.Update() == nil { // Update 会重算多 key 大小并刷新 abilities
			touched++
		}
	}
	if touched > 0 {
		model.InitChannelCache() // 内存缓存开启时刷新，否则新 key/新渠道不生效
	}
	// 同 key 冲突：非管理渠道里出现了与保险箱相同的 key
	keySet := map[string]bool{}
	for _, ks := range [][]string{kv.Openrouter, kv.Opencode, kv.Deepseek, kv.Apib, kv.Buzzai, kv.Apikl, kv.Ikun} {
		for _, k := range ks {
			keySet[k] = true
		}
	}
	if len(keySet) > 0 {
		managedNames := []string{pcManagedChanName("openrouter"), pcManagedChanName("opencode"), pcManagedChanName("deepseek"), pcManagedChanName("apib"), pcManagedChanName("buzzai"), pcManagedChanName("apikl"), pcManagedChanName("ikun")}
		var chs []model.Channel
		model.DB.Where("name NOT IN ?", managedNames).Find(&chs)
		for _, c := range chs {
			for _, k := range strings.Split(strings.Trim(c.Key, "\n"), "\n") {
				if keySet[strings.TrimSpace(k)] {
					conflicts = append(conflicts, pcKeyConflict{Id: c.Id, Name: c.Name})
					break
				}
			}
		}
	}
	return conflicts, touched
}

// GetPriceCompareKeys 读渠道 key 保险箱当前值（管理员唯一录入点）。
// pcDetectChannelKeys 从名称含 openrouter/opencode/deepseek 的启用渠道里收集
// 现有密钥（多 key 渠道按行拆分、跨渠道去重），供一键导入保险箱——免去查看渠道
// 密钥原文（该端点带防爆破限流，容易 429）。管理渠道本身跳过（其 key 即保险箱内容）。
func pcDetectChannelKeys() map[string][]string {
	out := map[string][]string{}
	for _, kk := range pcKeyKinds {
		out[kk.Kind] = nil
	}
	if model.DB == nil {
		return out
	}
	var chs []model.Channel
	if err := model.DB.Where("status = ?", common.ChannelStatusEnabled).Find(&chs).Error; err != nil {
		return out
	}
	managed := map[string]bool{}
	for _, kk := range pcKeyKinds {
		managed[pcManagedChanName(kk.Kind)] = true
	}
	seen := map[string]bool{}
	for _, c := range chs {
		if managed[c.Name] {
			continue
		}
		name := strings.ToLower(c.Name)
		for _, kk := range pcKeyKinds {
			if !strings.Contains(name, kk.Kind) {
				continue
			}
			for _, k := range strings.Split(c.Key, "\n") {
				if k = strings.TrimSpace(k); k == "" || seen[kk.Kind+"\x00"+k] {
					continue
				}
				seen[kk.Kind+"\x00"+k] = true
				out[kk.Kind] = append(out[kk.Kind], k)
			}
		}
	}
	return out
}

func GetPriceCompareKeys(c *gin.Context) {
	kv := pcLoadKeys()
	// detected = 现有渠道里尚未导入保险箱的密钥数（前端据此显示一键导入横幅）
	det := pcDetectChannelKeys()
	cand := map[string]int{}
	for _, kk := range pcKeyKinds {
		set := map[string]bool{}
		for _, k := range *kk.Vault(&kv) {
			set[k] = true
		}
		for _, k := range det[kk.Kind] {
			if !set[k] {
				cand[kk.Kind]++
			}
		}
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"openrouter": kv.Openrouter, "opencode": kv.Opencode, "deepseek": kv.Deepseek,
		"apib": kv.Apib, "buzzai": kv.Buzzai, "apikl": kv.Apikl, "ikun": kv.Ikun,
		"detected": cand,
	}})
}

// PostPriceCompareKeysImport 一键把现有同名渠道里的密钥导入保险箱（并集去重）；
// 开启了渠道同步时顺带刷新管理渠道，让导入的 key 立即参与接管。
func PostPriceCompareKeysImport(c *gin.Context) {
	kv := pcLoadKeys()
	det := pcDetectChannelKeys()
	imported := map[string]int{}
	total := 0
	for _, kk := range pcKeyKinds {
		p := kk.Vault(&kv)
		set := map[string]bool{}
		for _, k := range *p {
			set[k] = true
		}
		for _, k := range det[kk.Kind] {
			if !set[k] {
				set[k] = true
				*p = append(*p, k)
				imported[kk.Kind]++
				total++
			}
		}
	}
	if total > 0 {
		pcSaveKeys(kv)
	}
	channels := 0
	conflicts := []pcKeyConflict{}
	if cfg := pcLoadPriority(); cfg.AutoRule.SyncChannels {
		conflicts, channels = pcSyncChannelKeys()
	}
	pcPublishKeyModelRoute() // 渠道/key 集变了 → 重新发布按模型选 key 注册表，防旧下标与新 key 行序错位
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"imported": imported, "total": total, "channels": channels,
		"conflicts": len(conflicts),
	}})
}

// PostPriceCompareKeys 保存渠道 key 保险箱、强制同步管理渠道，并返回同 key 冲突渠道
// 供用户确认删除（非管理渠道被删后即由比价管理渠道完全接管）。
func PostPriceCompareKeys(c *gin.Context) {
	var v pcKeyVault
	if err := c.ShouldBindJSON(&v); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	pcSaveKeys(v)
	conflicts, synced := pcSyncChannelKeys()
	pcPublishKeyModelRoute()  // 渠道/key 集变了 → 重新发布按模型选 key 注册表，防旧下标与新 key 行序错位
	go pcRefreshAccessCache() // 新 key 的可见性未知：后台重探并重发布（探测快照与保险箱不一致时发布会跳过该渠道）
	c.JSON(200, gin.H{"success": true, "data": gin.H{"channels": synced, "conflicts": conflicts}})
}

// PostPriceCompareKeysPurge 删除用户确认的同 key 非管理渠道（只删确实验证通过的）。
func PostPriceCompareKeysPurge(c *gin.Context) {
	var req struct {
		Ids []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	kv := pcLoadKeys()
	keySet := map[string]bool{}
	for _, ks := range [][]string{kv.Openrouter, kv.Opencode, kv.Deepseek} {
		for _, k := range ks {
			keySet[k] = true
		}
	}
	managed := map[string]bool{
		pcManagedChanName("openrouter"): true, pcManagedChanName("opencode"): true, pcManagedChanName("deepseek"): true,
	}
	deleted := 0
	for _, id := range req.Ids {
		ch := model.Channel{}
		if model.DB.First(&ch, "id = ?", id).Error != nil || managed[ch.Name] {
			continue
		}
		same := false
		for _, k := range strings.Split(strings.Trim(ch.Key, "\n"), "\n") {
			if keySet[strings.TrimSpace(k)] {
				same = true
				break
			}
		}
		if !same {
			continue
		}
		if ch.Delete() == nil {
			deleted++
		}
	}
	if deleted > 0 {
		model.InitChannelCache()
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"deleted": deleted}})
}

// ---------------- 余额查询（保险箱密钥 → 官方 API） ----------------
// OpenRouter: GET /api/v1/credits（余额 = total_credits - total_usage，USD）
// DeepSeek:   GET /user/balance（balance_infos 里取 CNY，官方文档 api-docs.deepseek.com）
// OpenCode:   官方没有钱包余额 API（issue #10448 仍 open），退而查 Go 订阅窗口
//             GET /zen/go/v1/usage（rolling/weekly/monthly 用量百分比）。
// 中转站：buzzai/apib（new-api 系）走网关 OpenAI 兼容 billing 端点；
// apikl/ikun（sub2api 系）走 /v1/usage + /v1/sub2api/billing（见 resellers 文件）。

type pcProviderBalance struct {
	Ok       bool               `json:"ok"`
	Balance  *float64           `json:"balance,omitempty"`
	Currency string             `json:"currency,omitempty"`
	Used     *float64           `json:"used,omitempty"`
	Granted  *float64           `json:"granted,omitempty"`
	Windows  map[string]float64 `json:"windows,omitempty"`
	Error    string             `json:"error,omitempty"` // 稳定错误码：no_key/invalid_key/request_failed/bad_response
	Detail   string             `json:"detail,omitempty"`

	BalanceCny *float64       `json:"balance_cny,omitempty"` // USD→CNY 折算（面板同款汇率），OpenRouter 专用
	UsedCny    *float64       `json:"used_cny,omitempty"`
	Keys       []pcKeyBalance `json:"keys,omitempty"` // 逐 key 明细（与保险箱数组同序）
}

// pcKeyBalance 单条 key 的余额/用量；Key 是脱敏掩码，不下发原文
type pcKeyBalance struct {
	Key        string             `json:"key"`
	Ok         bool               `json:"ok"`
	Balance    *float64           `json:"balance,omitempty"`
	BalanceCny *float64           `json:"balance_cny,omitempty"`
	Used       *float64           `json:"used,omitempty"`
	UsedCny    *float64           `json:"used_cny,omitempty"`
	Granted    *float64           `json:"granted,omitempty"`
	Windows    map[string]float64 `json:"windows,omitempty"`
	Error      string             `json:"error,omitempty"`
	Detail     string             `json:"detail,omitempty"`

	// sub2api 附加（apikl/ikun）：key 所属组信息与近期调用模型——同站多 key 常
	// 各限特定模型（如 ikun 一个 key 只能生图、另一个只能 GPT 组），据此可辨
	Plan      string   `json:"plan,omitempty"`       // 组名 / 「钱包余额」
	GroupRate *float64 `json:"group_rate,omitempty"` // 该 key 所属组的倍率
	Models    []string `json:"models,omitempty"`     // 近期调用过的模型（按费用降序，最多 3）

	Currency string `json:"currency,omitempty"` // 网关系（buzzai/apib）per-key 值已折人民币，标 CNY 供前端选符号
}

func pcMaskKey(k string) string {
	switch {
	case len(k) > 15:
		return k[:6] + "******" + k[len(k)-6:]
	case len(k) > 8:
		return k[:4] + "****" + k[len(k)-3:]
	}
	return "******"
}

func pcBalErrCode(status int) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "invalid_key"
	}
	return "request_failed"
}

func pcBalToF64(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func pcBalFetchJSON(url, key string) (map[string]interface{}, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var m map[string]interface{}
	if json.Unmarshal(body, &m) != nil {
		m = map[string]interface{}{}
	}
	return m, resp.StatusCode, nil
}

// pcQueryOpenRouterBalance 逐 key 查询（每个 key 独立钱包），聚合字段=全部成功 key 的合计
func pcQueryOpenRouterBalance(keys []string) pcProviderBalance {
	if len(keys) == 0 {
		return pcProviderBalance{Error: "no_key"}
	}
	total, used, okCount := 0.0, 0.0, 0
	var keysOut []pcKeyBalance
	for _, k := range keys {
		kb := pcKeyBalance{Key: pcMaskKey(k)}
		m, code, err := pcBalFetchJSON("https://openrouter.ai/api/v1/credits", k)
		switch {
		case err != nil:
			kb.Error, kb.Detail = "request_failed", err.Error()
		case code != http.StatusOK:
			kb.Error, kb.Detail = pcBalErrCode(code), fmt.Sprintf("http %d", code)
		default:
			d, _ := m["data"].(map[string]interface{})
			tc, tu := pcBalToF64(d["total_credits"]), pcBalToF64(d["total_usage"])
			bal := tc - tu
			kb.Ok, kb.Balance, kb.Used = true, &bal, &tu
			total, used, okCount = total+bal, used+tu, okCount+1
		}
		keysOut = append(keysOut, kb)
	}
	if okCount == 0 {
		return pcProviderBalance{Error: keysOut[0].Error, Detail: keysOut[0].Detail, Keys: keysOut}
	}
	r := pcProviderBalance{Ok: true, Currency: "USD", Balance: &total, Used: &used, Keys: keysOut}
	if okCount < len(keys) {
		r.Detail = fmt.Sprintf("%d/%d keys failed", len(keys)-okCount, len(keys))
	}
	return r
}

// pcQueryDeepseekBalance 逐 key 查询取 CNY 条目，聚合字段=合计
func pcQueryDeepseekBalance(keys []string) pcProviderBalance {
	if len(keys) == 0 {
		return pcProviderBalance{Error: "no_key"}
	}
	total, granted, okCount := 0.0, 0.0, 0
	var keysOut []pcKeyBalance
	for _, k := range keys {
		kb := pcKeyBalance{Key: pcMaskKey(k)}
		m, code, err := pcBalFetchJSON("https://api.deepseek.com/user/balance", k)
		switch {
		case err != nil:
			kb.Error, kb.Detail = "request_failed", err.Error()
		case code != http.StatusOK:
			kb.Error, kb.Detail = pcBalErrCode(code), fmt.Sprintf("http %d", code)
		default:
			infos, _ := m["balance_infos"].([]interface{})
			pick := func() (float64, float64, bool) {
				for _, inf := range infos {
					im, _ := inf.(map[string]interface{})
					if im == nil || im["currency"] != "CNY" {
						continue
					}
					return pcBalToF64(im["total_balance"]), pcBalToF64(im["granted_balance"]), true
				}
				if len(infos) > 0 {
					// 没有 CNY 条目时退回第一条（如 USD 账户）
					im, _ := infos[0].(map[string]interface{})
					return pcBalToF64(im["total_balance"]), pcBalToF64(im["granted_balance"]), true
				}
				return 0, 0, false
			}
			if t, g, found := pick(); found {
				kb.Ok, kb.Balance, kb.Granted = true, &t, &g
				total, granted, okCount = total+t, granted+g, okCount+1
			} else {
				kb.Error, kb.Detail = "bad_response", "balance_infos missing"
			}
		}
		keysOut = append(keysOut, kb)
	}
	if okCount == 0 {
		return pcProviderBalance{Error: keysOut[0].Error, Detail: keysOut[0].Detail, Keys: keysOut}
	}
	r := pcProviderBalance{Ok: true, Currency: "CNY", Balance: &total, Granted: &granted, Keys: keysOut}
	if okCount < len(keys) {
		r.Detail = fmt.Sprintf("%d/%d keys failed", len(keys)-okCount, len(keys))
	}
	return r
}

// pcParseGoWindows 解析 /zen/go/v1/usage 的窗口字典；官方字段是 percent
// （usagePercent 是 CodexBar 对旧 web 端点的解析，勿混用）
func pcParseGoWindows(usage map[string]interface{}) map[string]float64 {
	windows := map[string]float64{}
	for _, w := range []string{"rolling", "weekly", "monthly"} {
		wm, _ := usage[w].(map[string]interface{})
		if wm == nil {
			continue
		}
		if p, ok := wm["percent"]; ok {
			windows[w] = pcBalToF64(p)
		} else {
			windows[w] = pcBalToF64(wm["usagePercent"])
		}
	}
	return windows
}

// pcQueryOpencodeBalance 钱包余额官方无 API，逐 key 查 Go 订阅窗口用量；
// 聚合 windows=第一个成功 key 的窗口（百分比不可跨账户相加）
func pcQueryOpencodeBalance(keys []string) pcProviderBalance {
	if len(keys) == 0 {
		return pcProviderBalance{Error: "no_key"}
	}
	okCount := 0
	var firstOk map[string]float64
	var keysOut []pcKeyBalance
	for _, k := range keys {
		kb := pcKeyBalance{Key: pcMaskKey(k)}
		m, code, err := pcBalFetchJSON("https://opencode.ai/zen/go/v1/usage", k)
		switch {
		case err != nil:
			kb.Error, kb.Detail = "request_failed", err.Error()
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			kb.Error, kb.Detail = pcBalErrCode(code), fmt.Sprintf("http %d", code)
		case code != http.StatusOK:
			kb.Error, kb.Detail = "bad_response", fmt.Sprintf("http %d", code)
		default:
			usage, _ := m["usage"].(map[string]interface{})
			w := pcParseGoWindows(usage)
			if len(w) == 0 {
				kb.Error, kb.Detail = "bad_response", "windows missing"
			} else {
				kb.Ok, kb.Windows = true, w
				okCount++
				if firstOk == nil {
					firstOk = w
				}
			}
		}
		keysOut = append(keysOut, kb)
	}
	if okCount == 0 {
		return pcProviderBalance{Error: keysOut[0].Error, Detail: keysOut[0].Detail, Keys: keysOut}
	}
	r := pcProviderBalance{Ok: true, Windows: firstOk, Keys: keysOut}
	if okCount < len(keys) {
		r.Detail = fmt.Sprintf("%d/%d keys failed", len(keys)-okCount, len(keys))
	}
	return r
}

var pcBalMu sync.Mutex
var pcBalCache struct {
	at   time.Time
	data map[string]pcProviderBalance
}

// GetPriceCompareBalances 用保险箱密钥并发查询全部渠道余额/用量，60s 内存缓存，
// ?refresh=1 强制刷新；定时刷新任务每轮也预查一份（见 pcRunRefresh 的「余额」），
// 面板打开时多为新鲜缓存直接命中
func GetPriceCompareBalances(c *gin.Context) {
	force := c.Query("refresh") == "1"
	pcBalMu.Lock()
	if !force && pcBalCache.data != nil && time.Since(pcBalCache.at) < time.Minute {
		d := pcBalCache.data
		pcBalMu.Unlock()
		c.JSON(200, gin.H{"success": true, "data": d, "cached": true})
		return
	}
	pcBalMu.Unlock()

	pcFetchBalances()
	pcBalMu.Lock()
	d := pcBalCache.data
	pcBalMu.Unlock()
	c.JSON(200, gin.H{"success": true, "data": d})
}

// pcFetchBalances 并发查询全部保险箱渠道的余额/用量并写入共享缓存（60s 面板
// 缓存 + 定时刷新预查共用）；面板接口与定时任务并发触发时双跑无害（外部免费
// billing 端点，结果一致，末次写入胜出）
func pcFetchBalances() {
	kv := pcLoadKeys()
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]pcProviderBalance{}
	// 余额查询覆盖：openrouter/opencode/deepseek 官方 + 中转站——buzzai/apib 走
	// new-api 系网关的 OpenAI 兼容 billing 端点，apikl/ikun（sub2api 系）走
	// /v1/usage + /v1/sub2api/billing（官方 key 鉴权端点，详见 pcQuerySub2apiBalance）
	balSupported := map[string]func([]string) pcProviderBalance{
		"openrouter": pcQueryOpenRouterBalance,
		"opencode":   pcQueryOpencodeBalance,
		"deepseek":   pcQueryDeepseekBalance,
		"buzzai": func(keys []string) pcProviderBalance {
			return pcQueryGatewayBalance(pcBuzzaiBase, pcFetchBuzzaiExchangeRate, keys)
		},
		"apib": func(keys []string) pcProviderBalance {
			return pcQueryGatewayBalance("https://api.apib.ai", pcExchangeRate, keys)
		},
		"apikl": func(keys []string) pcProviderBalance {
			return pcQuerySub2apiBalance(pcApiklBase, 1, keys) // 充值 1:1（¥1=$1 额度）
		},
		"ikun": func(keys []string) pcProviderBalance {
			return pcQuerySub2apiBalance(pcIkunBase, pcIkunCnyPerCredit, keys) // 充值 1:10
		},
	}
	for _, kk := range pcKeyKinds {
		query, ok := balSupported[kk.Kind]
		if !ok {
			continue
		}
		keys := *kk.Vault(&kv)
		wg.Add(1)
		go func(kind string, keys []string) {
			defer wg.Done()
			r := query(keys)
			mu.Lock()
			out[kind] = r
			mu.Unlock()
		}(kk.Kind, keys)
	}
	wg.Wait()

	// OpenRouter 余额按面板同款实时汇率折算人民币（原始 USD 数值保留，前端 tooltip 展示）
	if r, ok := out["openrouter"]; ok && r.Ok {
		if rate := pcExchangeRate(); rate > 0 {
			if r.Balance != nil {
				bc := *r.Balance * rate
				r.BalanceCny = &bc
			}
			if r.Used != nil {
				uc := *r.Used * rate
				r.UsedCny = &uc
			}
			for i := range r.Keys {
				if r.Keys[i].Balance != nil {
					bc := *r.Keys[i].Balance * rate
					r.Keys[i].BalanceCny = &bc
				}
				if r.Keys[i].Used != nil {
					uc := *r.Keys[i].Used * rate
					r.Keys[i].UsedCny = &uc
				}
			}
			out["openrouter"] = r
		}
	}

	pcBalMu.Lock()
	pcBalCache.at = time.Now()
	pcBalCache.data = out
	pcBalMu.Unlock()
}

func pcSaveCache() {
	pcMu.Lock()
	b, err := json.Marshal(&pcCache)
	pcMu.Unlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(pcCacheFile(), b, 0644)
	pcDbSet(pcDbCacheKey, string(b))
}

func pcLoadCache() {
	b, err := os.ReadFile(pcCacheFile())
	if err != nil {
		// 文件缺失（新机器/换库）时从实例数据库恢复；都没有 → 空缓存走立即刷新兜底
		if s := pcDbGet(pcDbCacheKey); s != "" {
			b = []byte(s)
		}
	}
	if b == nil {
		return
	}
	var c pcCacheData
	if json.Unmarshal(b, &c) != nil {
		return
	}
	if c.Canonical == nil {
		c.Canonical = map[string]string{}
	}
	if c.Prices == nil {
		c.Prices = map[string]pcOrPrice{}
	}
	if c.LbRows == nil {
		c.LbRows = map[string]map[string]*float64{}
	}
	if c.LbCats == nil {
		c.LbCats = map[string][]string{}
	}
	// :batch 兜底清理
	batchPruned := []pcCatalogEntry{}
	for _, m := range c.Models {
		if !strings.HasSuffix(m.Id, ":batch") {
			batchPruned = append(batchPruned, m)
		}
	}
	c.Models = batchPruned
	for k := range c.Canonical {
		if strings.HasSuffix(k, ":batch") {
			delete(c.Canonical, k)
		}
	}
	for k := range c.Prices {
		if strings.HasSuffix(k, ":batch") {
			delete(c.Prices, k)
		}
	}
	// 目录对照清理（官方下架的残留报价，与 pcFetchCatalog 同款）：目录本身过期
	// 误删也无害——条目缺失时补抓会重新拿回来；目录为空时跳过（防异常数据
	// 把 Prices 清光）
	if len(c.Models) > 0 {
		live := map[string]bool{}
		for _, m := range c.Models {
			live[m.Id] = true
		}
		for k := range c.Prices {
			if !live[k] {
				delete(c.Prices, k)
			}
		}
	}
	pcMu.Lock()
	pcCache = c
	pcMu.Unlock()
}

// ---------------- 优先级配置 ----------------

type pcPriorityOffer struct {
	Channel  string `json:"channel"`
	Ref      string `json:"ref"`
	Name     string `json:"name"`
	Period   string `json:"period"`
	Provider string `json:"provider"`
	Quant    string `json:"quant,omitempty"`
	AddedAt  int64  `json:"added_at"`
	// Harness 显示名（去厂商前缀 +（¥实际价），如 gemma-4-31b-it:free（¥0））：
	// 写进 harness 配置的模型名；管理渠道 models 收录并用 model_mapping 映射回 Ref
	Harness string `json:"harness,omitempty"`
}

type pcPriorityEntry struct {
	Name   string            `json:"name"`
	Offers []pcPriorityOffer `json:"offers"`
}

type pcPriorityConfig struct {
	Window          string                     `json:"window"`
	Custom          map[string]float64         `json:"custom"`
	RefreshInterval string                     `json:"refresh_interval"`
	ScoreDim        string                     `json:"score_dim"`
	Channels        map[string]bool            `json:"channels"`
	AutoRule        pcAutoRule                 `json:"auto_rule"`
	Entries         map[string]pcPriorityEntry `json:"entries"`
}

// pcAutoRule 自动更新规则：到达自动抓取间隔（或手动应用）时按规则重建优先级列表。
type pcAutoRule struct {
	// Terms 规则项列表（pareto | fixed | filter），各项入选模型取并集。
	// 旧单模式字段保留用于兼容读取，由 pcRuleTermsFromLegacy 迁移。
	Terms       []pcRuleTerm `json:"terms,omitempty"`
	Mode        string       `json:"mode"`         // 旧单模式字段（兼容）：off | pareto | fixed | filter
	ParetoDim   string       `json:"pareto_dim"`   // pareto 模式：benchmark 维度（"src:field"）
	FixedModels []string     `json:"fixed_models"` // fixed 模式：OpenRouter 正规名（每行一个）
	MaxActual   float64      `json:"max_actual"`   // filter 模式：实际价上限 ¥/M（>0 生效）
	MinScore    float64      `json:"min_score"`    // filter 模式：得分下限（0=不限）
	ScoreDim    string       `json:"score_dim"`    // filter 模式：得分维度（配合 MinScore）
	MaxModels   int          `json:"max_models"`   // filter 模式：最多模型数（0=不限）
	TopN        int          `json:"top_n"`        // 每模型最多 N 条报价（fallback 策略，0=不限）
	// SyncChannels 应用后把选中模型按报价渠道写进 new-api 渠道的 models 字段
	// （渠道按名称包含 openrouter/opencode/deepseek 匹配，只更新不改其它字段）
	SyncChannels bool `json:"sync_channels"`
	// RouteChannels 应用后按每模型报价顺序（实际价升序）构建「模型名 → 渠道ID」
	// 路由表，渠道选择时确定性首选最便宜渠道、故障才顺次降级，避免同优先级
	// 逐请求随机漂移打掉上游前缀缓存。不改渠道的 models 字段。
	RouteChannels bool `json:"route_channels"`
	// Pareto 多维度并集：每一项在一个基准维度上独立算帕累托前沿（带各自的
	// 价格/得分筛选），各项入选模型取并集进入自动更新。为空时退回单维度
	// ParetoDim（兼容旧配置）。
	Pareto []pcParetoTerm `json:"pareto,omitempty"`
	// Harnesses 应用时自动同步本机 harness 配置（claude/codex/opencode/zcode）：
	// 把 127.0.0.1 网关地址 + 托管令牌 + 模型列表写进对应软件的配置文件
	Harnesses []string `json:"harnesses,omitempty"`
}

type pcParetoTerm struct {
	Dim       string  `json:"dim"`        // 基准维度（"来源:字段"）
	MaxActual float64 `json:"max_actual"` // 实际价上限 ¥/M（0=不限）
	MinScore  float64 `json:"min_score"`  // 得分下限（0=不限）
	TopModels int     `json:"top_models"` // 本项最多入选模型数（0=不限，前沿内按实际价升序截断）
}

// pcRuleTerm 一条自动更新规则项：Type = pareto | fixed | filter，各类型只读自己
// 的字段；所有规则项入选模型取并集。
type pcRuleTerm struct {
	Type      string   `json:"type"`
	Dim       string   `json:"dim,omitempty"`        // pareto：基准维度
	MaxActual float64  `json:"max_actual,omitempty"` // pareto/filter：实际价上限
	MinScore  float64  `json:"min_score,omitempty"`  // pareto/filter：得分下限
	TopModels int      `json:"top_models,omitempty"` // pareto：前沿内按实际价截断
	Models    []string `json:"models,omitempty"`     // fixed：OpenRouter 正规名
	ScoreDim  string   `json:"score_dim,omitempty"`  // filter：得分维度
	// IncludeUnscored filter：MinScore>0 时，无基准得分的模型（新模型常还没进任何
	// 榜单）也入选，而不是被排除
	IncludeUnscored bool `json:"include_unscored,omitempty"`
	MaxModels       int  `json:"max_models,omitempty"` // filter：最多模型数
	// TopN 本项选中的模型各保留 N 条报价（fallback，0=不限）。同一模型被多项
	// 选中时取各选中项中最小的正 N——最严格的 fallback 纪律生效；全为 0 才不限。
	// 指针区分「未设置」（nil，回退全局 TopN）与「显式 0=不限」：值类型 +
	// omitempty 会把显式保存的 0 丢掉，重载后静默回退全局 N
	TopN *int `json:"top_n,omitempty"`
}

// pcRuleTermsFromLegacy 归一规则项：新 Terms 字段优先；旧单模式字段迁移为等价
// 规则项，mode=off 迁移为空（不自动更新）。
func pcRuleTermsFromLegacy(r pcAutoRule) []pcRuleTerm {
	if len(r.Terms) > 0 {
		return r.Terms
	}
	tn := r.TopN // 旧全局 TopN 迁移为各项显式值（含 0=不限）
	var terms []pcRuleTerm
	switch r.Mode {
	case "pareto":
		if len(r.Pareto) > 0 {
			for _, t := range r.Pareto {
				terms = append(terms, pcRuleTerm{Type: "pareto", Dim: t.Dim,
					MaxActual: t.MaxActual, MinScore: t.MinScore, TopModels: t.TopModels, TopN: &tn})
			}
		} else if r.ParetoDim != "" {
			terms = append(terms, pcRuleTerm{Type: "pareto", Dim: r.ParetoDim, TopN: &tn})
		}
	case "fixed":
		if len(r.FixedModels) > 0 {
			terms = append(terms, pcRuleTerm{Type: "fixed", Models: r.FixedModels, TopN: &tn})
		}
	case "filter":
		terms = append(terms, pcRuleTerm{Type: "filter", ScoreDim: r.ScoreDim,
			MaxActual: r.MaxActual, MinScore: r.MinScore, MaxModels: r.MaxModels, TopN: &tn})
	}
	return terms
}

func pcDefaultPriority() pcPriorityConfig {
	return pcPriorityConfig{
		Window:          "month",
		Custom:          map[string]float64{"out_in": 10, "cache_hit": 90, "cache_write": 0},
		RefreshInterval: "12h",
		ScoreDim:        "livebench:average",
		Channels:        map[string]bool{"openrouter": true, "opencode": true, "deepseek": true},
		// TopN 默认 3：与前端「每模型保留前 N 条报价」的默认兜底一致；已保存
		// 配置里的 top_n（全局为显式字段、per-term 为指针）由 json.Unmarshal
		// 原样覆盖，用户显式保存的 0=不限不会被默认值吃掉
		AutoRule: pcAutoRule{Mode: "off", TopN: 3},
		Entries:  map[string]pcPriorityEntry{},
	}
}

func pcLoadPriority() pcPriorityConfig {
	load := func(b []byte) pcPriorityConfig {
		cfg := pcDefaultPriority()
		if json.Unmarshal(b, &cfg) != nil {
			return pcDefaultPriority()
		}
		if cfg.Entries == nil {
			cfg.Entries = map[string]pcPriorityEntry{}
		}
		if cfg.Custom == nil {
			cfg.Custom = map[string]float64{"out_in": 10, "cache_hit": 90, "cache_write": 0}
		}
		if cfg.Channels == nil {
			cfg.Channels = map[string]bool{"openrouter": true, "opencode": true, "deepseek": true}
		}
		if cfg.RefreshInterval == "" {
			cfg.RefreshInterval = "12h"
		}
		if cfg.ScoreDim == "" {
			cfg.ScoreDim = "livebench:average"
		}
		return cfg
	}
	b, err := os.ReadFile(pcPriorityFile())
	if err != nil {
		// 文件缺失时从实例数据库恢复
		if s := pcDbGet(pcDbPriorityKey); s != "" {
			return load([]byte(s))
		}
		return pcDefaultPriority()
	}
	return load(b)
}

func pcSavePriority(cfg pcPriorityConfig) error {
	b, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(pcPriorityFile(), b, 0644); err != nil {
		return err
	}
	pcDbSet(pcDbPriorityKey, string(b))
	return nil
}

// ---------------- 自动更新规则 ----------------

// 比价顺序路由表：模型名（含 canon 变体）→ 渠道ID 有序表，挂在 model 层的
// ChannelRoutingHook 上供渠道选择器查表；atomic.Pointer 让请求热路径免锁。
var (
	pcRouteTable atomic.Pointer[map[string][]int]
	pcStageTable atomic.Pointer[map[string][]pcRouteStage]
	pcRouteOnce  sync.Once
)

func init() {
	model.ChannelRoutingHook = pcRoutingLookup
	// 请求发往 OpenRouter 时按优先级列表顺序（阶段化）注入 provider.order
	relaycommon.ProviderOrderHook = pcProviderStageOrder
}

// pcProviderStageOrder 返回当前重试轮次应注入的供应商顺序与是否允许 OR 兜底：
// 混合跨渠道顺序（如 OR→OpenCode→OR）按阶段精确交错——OR 阶段把该阶段整段端点
// 按序注入（列表内部失败由 OpenRouter 按序自行兜底、不出列表），只有整条阶段链
// 末段的 OR 阶段才放开越出列表的兜底，其余阶段失败交给 new-api 重试进下一阶段。
// 选择器（pickRoutedChannelID）按全局 retry 在「候选过滤后的整条阶段链」上取第
// n 个候选阶段，计数包含其他渠道的阶段：链上渠道全在候选内时当前渠道就是
// list[retry]；个别渠道被候选过滤掉导致落点与本渠道错位时，退回本渠道里不早于
// retry 的首段（全局轮次已越过则钳位到本渠道末段）。
// 不在优先级列表里的模型退回全量实际价顺序（允许兜底）。
func pcProviderStageOrder(upstreamModel string, retry int, channelId int) ([]string, bool) {
	if st := pcStageTable.Load(); st != nil {
		list, ok := (*st)[upstreamModel]
		if !ok {
			list, ok = (*st)[pcCanonKey(upstreamModel)]
		}
		if ok && len(list) > 0 {
			if retry < 0 {
				retry = 0
			}
			if retry >= len(list) {
				retry = len(list) - 1
			}
			stageIdx := -1
			if list[retry].Channel == channelId {
				stageIdx = retry // 链上渠道全在候选内：全局轮次即整条链上的位置
			} else {
				last := -1
				for i := range list {
					if list[i].Channel != channelId {
						continue
					}
					last = i
					if i >= retry {
						stageIdx = i
						break
					}
				}
				if stageIdx < 0 {
					stageIdx = last
				}
			}
			if stageIdx < 0 {
				return nil, false // 当前渠道不在该模型的阶段链上
			}
			stage := list[stageIdx]
			if len(stage.Slugs) == 0 {
				return nil, false // 本阶段是非 OR 渠道，请求不经 OpenRouter
			}
			// 只有本阶段同时是整条阶段链的末段才放开兜底；链上后面还有别的渠道
			// 阶段时必须禁掉，防止 OR 用列表外供应商抢答、跳过后续渠道阶段
			return stage.Slugs, stageIdx == len(list)-1
		}
	}
	if o := pcProviderOrderFor(upstreamModel); len(o) > 0 {
		return o, true
	}
	return nil, false
}

func pcRoutingLookup(name string) []int {
	pcRouteOnce.Do(func() { // 重启后首个请求懒恢复（entries 已持久化，可随时重建）
		if pcRouteTable.Load() == nil {
			if cfg := pcLoadPriority(); cfg.AutoRule.RouteChannels && len(cfg.Entries) > 0 {
				if m, st := pcBuildRouteTable(cfg.Entries); len(m) > 0 {
					pcRouteTable.Store(&m)
					pcStageTable.Store(&st)
				}
			}
		}
	})
	if m := pcRouteTable.Load(); m != nil {
		if ids, ok := (*m)[name]; ok {
			return ids
		}
		return (*m)[pcCanonKey(name)]
	}
	return nil
}

// pcRouteStage 优先级列表展开出的一个调用阶段：阶段内指定渠道；OpenRouter 阶段
// 携带整段有序端点列表（Slugs）一次性注入 provider.order，由 OpenRouter 在列表内
// 按序自行兜底；非 OR 阶段 Slugs 为空。跨渠道交错顺序（如 OR→OpenCode→OR）按
// 阶段逐个 fallback：每个阶段失败即由 new-api 重试进入下一阶段。
type pcRouteStage struct {
	Channel int
	Slugs   []string
}

// pcBuildRouteTable 把每模型报价顺序展开成「模型名 → 渠道ID 有序表」与
// 「模型名 → 调用阶段表」：渠道按名称包含报价渠道关键字匹配；OpenRouter 阶段带
// 端点 slug，非 OR 阶段 slug 为空；连续相同的阶段合并，只有单阶段的模型不入表。
func pcBuildRouteTable(entries map[string]pcPriorityEntry) (map[string][]int, map[string][]pcRouteStage) {
	if model.DB == nil || len(entries) == 0 {
		return nil, nil
	}
	type chRow struct {
		Id     int    `gorm:"column:id"`
		Name   string `gorm:"column:name"`
		Models string `gorm:"column:models"`
	}
	var chs []chRow
	if err := model.DB.Table("channels").Select("id, name, models").
		Where("status = ?", common.ChannelStatusEnabled).Order("id").Scan(&chs).Error; err != nil {
		return nil, nil
	}
	route := map[string][]int{}
	stages := map[string][]pcRouteStage{}
	for k, entry := range entries {
		if len(entry.Offers) == 0 {
			continue
		}
		// 先展开成「每报价 × 每同名渠道」并去重，再把连续同渠道阶段合并：同一渠道
		// 连续多条 OR 报价整段按序交给 OpenRouter 内部兜底（一次请求完成），不再
		// 逐条拆成 new-api 重试——省重试轮次，供应商级故障也消化在 OR 内部
		type flatStage struct {
			ch   int
			slug string
		}
		var flat []flatStage
		seenStage := map[flatStage]bool{}
		names := map[string]bool{k: true, pcCanonKey(k): true}
		for _, of := range entry.Offers {
			names[of.Ref] = true
			names[pcCanonKey(of.Ref)] = true
			if of.Harness != "" {
				names[of.Harness] = true // harness 显示名请求同样走阶段化 fallback
			}
			slug := ""
			if of.Channel == "openrouter" {
				slug = orProviderSlug(of.Provider, of.Quant)
				if slug == "" {
					continue
				}
			}
			for _, c := range chs {
				if !strings.Contains(strings.ToLower(c.Name), of.Channel) {
					continue
				}
				st := flatStage{c.Id, slug}
				if seenStage[st] {
					continue
				}
				seenStage[st] = true
				flat = append(flat, st)
			}
		}
		var list []pcRouteStage
		for _, f := range flat {
			if n := len(list); n > 0 && list[n-1].Channel == f.ch {
				if f.slug != "" {
					list[n-1].Slugs = append(list[n-1].Slugs, f.slug)
				}
				continue
			}
			ns := pcRouteStage{Channel: f.ch}
			if f.slug != "" {
				ns.Slugs = []string{f.slug}
			}
			list = append(list, ns)
		}
		if len(list) < 2 {
			continue // 合并后仅单阶段（纯单渠道）交给默认选择逻辑 + OR 全量顺序注入
		}
		ids := make([]int, 0, len(list))
		for _, st := range list {
			ids = append(ids, st.Channel)
		}
		for n := range names {
			if strings.TrimSpace(n) != "" {
				route[n] = ids
				stages[n] = list
			}
		}
	}
	return route, stages
}

// pcApplyAutoRule 按规则重建优先级列表（替换 entries），返回 (模型数, 报价数,
// 未匹配固定模型名, 同步渠道数, 路由模型名数, harness 同步结果)。开启 RouteChannels
// 时同步重建比价顺序路由表；规则关闭或当前无行情数据时不做任何改动并清掉旧路由表。
// manual=true（手动「立即应用」）时连通性实测绕过结果缓存：充值/换 key 后立即
// 恢复，不必等失败缓存过期；自动刷新走 6h 缓存，同一轮不重复花钱。
func pcApplyAutoRule(manual bool) (int, int, []string, int, int, []string, int) {
	cfg := pcLoadPriority()
	terms := pcRuleTermsFromLegacy(cfg.AutoRule)
	if len(terms) == 0 {
		// 规则项为空：优先级列表保持已落盘状态不动，但渠道接管不能被一起
		// 切断——定时刷新（自动抓取间隔）每轮也走到这里，保险箱 key 变化后
		// 仍要同步管理渠道并重发布 key 级路由（无规则 ≠ 不管渠道）
		synced := 0
		if cfg.AutoRule.SyncChannels && model.DB != nil {
			_, synced = pcSyncChannelKeys()
			pcPublishKeyModelRoute()
			go pcRefreshAccessCache()
		}
		pcRouteTable.Store(nil)
		pcStageTable.Store(nil)
		return 0, 0, nil, synced, 0, nil, 0
	}
	rows, _ := pcGetUnified(false)
	if len(rows) == 0 {
		pcRouteTable.Store(nil)
		pcStageTable.Store(nil)
		return 0, 0, nil, 0, 0, nil, 0
	}
	// 实际价：与前端 actualOf 同一公式（窗口倍率 + 汇率 + 渠道倍率）
	ratios := pcReadRatios()
	eff := pcRatio{}
	if cfg.Window == "custom" {
		hit, cwr := cfg.Custom["cache_hit"]/100, cfg.Custom["cache_write"]/100
		eff = pcRatio{Cache: hit, Output: cfg.Custom["out_in"] / 100,
			CacheWrite: cwr, Uncached: math.Max(0, 1-hit-cwr)}
	} else if r, ok := ratios[cfg.Window]; ok && r != nil {
		eff = *r
	} else if r, ok := ratios["month"]; ok && r != nil {
		eff = *r
	}
	rate := pcExchangeRate()
	actual := func(r pcUnifiedRow) float64 {
		cur := 1.0
		if r.Currency != "CNY" {
			cur = rate
		}
		mul := r.Multiplier
		if mul == 0 {
			mul = 1
		}
		cw := eff.CacheWrite * r.Write
		return (eff.Uncached*r.In + eff.Cache*r.Read + cw + eff.Output*r.Out) * cur * mul
	}
	// 渠道开关过滤
	chOk := func(r pcUnifiedRow) bool {
		on, has := cfg.Channels[r.Channel]
		return !has || on
	}
	scoreOf := func(r pcUnifiedRow, dim string) (float64, bool) {
		parts := strings.SplitN(dim, ":", 2)
		if len(parts) != 2 {
			return 0, false
		}
		e := r.Bench[parts[0]]
		if e == nil {
			return 0, false
		}
		v, ok := e.Scores[parts[1]]
		if !ok {
			return 0, false
		}
		if strings.HasSuffix(parts[1], "_rank") {
			v = -v // 排名越小越好，统一成越大越好
		}
		return v, true
	}
	cand := []pcUnifiedRow{}
	for _, r := range rows {
		// 只收能落地成比价管理渠道的服务商（openrouter/opencode/deepseek）：其余
		// 渠道（中转站等）apply 无法为其建渠道，入选只会产出本地永远无法路由的
		// 悬空报价（行情面板仍照常展示全部渠道，仅路由用的优先级列表排除）
		if chOk(r) && pcManagedChanName(r.Channel) != "" {
			cand = append(cand, r)
		}
	}
	// 选中的模型 key 集合：所有规则项（帕累托/固定模型/筛选器）入选模型取并集。
	// 每项带自己的 fallback N；同模型被多项选中时取最小的正 N（0=不限，
	// 只要有任一项给了正 N 就按最严格的执行）
	picked := map[string]int{}
	// 项内 TopN 未设置（nil，如 per-term 改造前保存的旧 terms）时回退旧全局
	// TopN（兼容旧配置的 fallback N）；显式设置（含 0=不限，指针非 nil）原样生效
	termN := func(term pcRuleTerm) int {
		if term.TopN != nil {
			return *term.TopN
		}
		return cfg.AutoRule.TopN
	}
	pick := func(key string, n int) {
		prev, ok := picked[key]
		if !ok {
			picked[key] = n
			return
		}
		if n > 0 && (prev <= 0 || n < prev) {
			picked[key] = n
		}
	}
	missing := []string{}
	type pr struct {
		row   pcUnifiedRow
		price float64
		score float64
	}
	for _, term := range terms {
		switch term.Type {
		case "pareto":
			pts := []pr{}
			for _, r := range cand {
				if term.MaxActual > 0 && actual(r) > term.MaxActual {
					continue
				}
				s, ok := scoreOf(r, term.Dim)
				if !ok || term.Dim == "" {
					continue
				}
				if term.MinScore > 0 && s < term.MinScore {
					continue
				}
				pts = append(pts, pr{r, actual(r), s})
			}
			sort.Slice(pts, func(i, j int) bool {
				return pts[i].price < pts[j].price || pts[i].price == pts[j].price && pts[i].score > pts[j].score
			})
			front := []pr{}
			mx := math.Inf(-1)
			for _, p := range pts { // 帕累托前沿：价格升序 Walk，分数创新高者上前沿
				if p.score > mx {
					mx = p.score
					front = append(front, p)
				}
			}
			if term.TopModels > 0 && len(front) > term.TopModels {
				front = front[:term.TopModels] // 前沿内按实际价升序截断
			}
			for _, p := range front {
				pick(p.row.Key, termN(term))
			}
		case "fixed":
			byName := map[string]string{}
			for _, r := range cand {
				for _, nm := range []string{r.Name, r.FullName, r.Key} {
					for _, v := range pcCanonVariants(nm) {
						if _, ok := byName[v]; !ok {
							byName[v] = r.Key
						}
					}
				}
			}
			for _, name := range term.Models {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				found := ""
				for _, v := range pcCanonVariants(name) {
					if k, ok := byName[v]; ok {
						found = k
						break
					}
				}
				if found == "" {
					missing = append(missing, name)
				} else {
					pick(found, termN(term))
				}
			}
		case "filter":
			pass := map[string]float64{} // key → 最低实际价
			for _, r := range cand {
				if term.MaxActual > 0 && actual(r) > term.MaxActual {
					continue
				}
				if term.MinScore > 0 {
					if s, ok := scoreOf(r, term.ScoreDim); !ok {
						if !term.IncludeUnscored {
							continue // 无基准得分且未开「无分也收」：排除
						}
					} else if s < term.MinScore {
						continue
					}
				}
				k := r.Key
				if a, ok := pass[k]; !ok || actual(r) < a {
					pass[k] = actual(r)
				}
			}
			keys := make([]string, 0, len(pass))
			for k := range pass {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return pass[keys[i]] < pass[keys[j]] })
			if term.MaxModels > 0 && len(keys) > term.MaxModels {
				keys = keys[:term.MaxModels]
			}
			for _, k := range keys {
				pick(k, termN(term))
			}
		}
	}
	// 组装：每个选中模型的全部报价按实际价升序，按该模型的 fallback N 截断
	byKey := map[string][]pcUnifiedRow{}
	for _, r := range cand {
		if _, ok := picked[r.Key]; ok {
			byKey[r.Key] = append(byKey[r.Key], r)
		}
	}
	for k, rs := range byKey {
		sort.Slice(rs, func(i, j int) bool { return actual(rs[i]) < actual(rs[j]) })
		n := picked[k]
		if n > 0 && len(rs) > n {
			byKey[k] = rs[:n]
		}
	}
	// 连通性过滤：候选报价并行实测（max_tokens=1 最小请求，成本可忽略；结果缓存
	// 6h，同一轮自动更新不重复花钱），不可调的不进优先级列表——渠道 models、路由、
	// harness 都不再收录死报价。基础设施性失败（本地网络断等）占比过高时熔断跳过
	// 过滤，防止误清空列表
	cands := []pcTestCand{}
	seenCand := map[string]bool{}
	for _, rs := range byKey {
		for _, r := range rs {
			c := pcTestCand{strings.ToLower(r.Channel), r.Ref, r.Provider, r.Quant}
			if !seenCand[c.key()] {
				seenCand[c.key()] = true
				cands = append(cands, c)
			}
		}
	}
	ttl := 6 * time.Hour
	if manual {
		ttl = 0 // 手动立即应用：绕过结果缓存全量重测（结果仍回写缓存）
	}
	results := pcTestOffersParallel(cands, ttl, map[string]bool{"apikl": true, "ikun": true}, 8)
	tested, infraFailed := 0, 0
	for _, c := range cands {
		r := results[c.key()]
		tested++
		// no_key 是「无法判断」而非基础设施失败，不进熔断分子
		if !r.Ok && r.Error != "no_key" && (r.Status == 0 || r.Error == "request_failed" || strings.Contains(r.Error, "timeout")) {
			infraFailed++
		}
	}
	filterOn := tested < 5 || infraFailed*4 < tested // ≥25% 基础设施失败且 ≥5 例 → 熔断
	entries := map[string]pcPriorityEntry{}
	hprice := map[string]float64{}
	offers := 0
	dropped := 0
	now := time.Now().Unix()
	deadMark := map[string]bool{} // 本轮实测不可调：写回 DeadOffers 供面板标 dead
	revived := map[string]bool{}  // 本轮实测通过：解除历史 dead 标记（充值恢复）
	for k, rs := range byKey {
		if filterOn {
			kept := rs[:0]
			for _, r := range rs {
				c := pcTestCand{strings.ToLower(r.Channel), r.Ref, r.Provider, r.Quant}
				if res, ok := results[c.key()]; ok {
					if res.Ok {
						revived[c.key()] = true
						kept = append(kept, r)
						continue
					}
					if res.Error == "no_key" {
						// 无 key ⇒ 无法判断也先不使用：渠道同步同样不落地无 key
						// 服务商，列表保留只会形成「列表在、无可用渠道」的悬空
						dropped++
						continue
					}
					if pcTestRateLimited(res) {
						kept = append(kept, r) // 限流≠死：本轮突发所致，保留待下轮实测
						continue
					}
					dropped++                // 实测不可调：不进列表
					deadMark[c.key()] = true // 付费报价同样写回 dead 标记（含 402 欠费致死）
					continue
				}
				kept = append(kept, r) // 未测到：保留
			}
			rs = kept
		}
		if len(rs) == 0 {
			continue // 该模型全部报价不可调：整模型不进列表
		}
		entry := pcPriorityEntry{Name: rs[0].Name}
		for _, r := range rs {
			entry.Offers = append(entry.Offers, pcPriorityOffer{
				Channel: r.Channel, Ref: r.Ref, Name: r.FullName, Period: r.Period,
				Provider: r.Provider, Quant: r.Quant, AddedAt: now,
				Harness: pcHarnessDisplayName(r.Ref, actual(r)),
			})
		}
		if len(entry.Offers) > 0 {
			entries[k] = entry
			hprice[k] = actual(rs[0])
			offers += len(entry.Offers)
		}
	}
	if dropped > 0 {
		common.SysLog(fmt.Sprintf("price_compare apply: 连通性过滤剔除 %d/%d 条不可调报价（实测 %d，熔断=%v）",
			dropped, tested, tested, !filterOn))
	}
	// 实测结果写回 DeadOffers：不可调的（含付费/欠费 402 致死，免费档批量实测
	// 覆盖不到的）面板同步标 dead；实测通过的解除标记——充值/换 key 后手动
	// 「立即应用」即可复活。免费键仍由刷新尾部的免费档批量实测整体重建
	if len(deadMark) > 0 || len(revived) > 0 {
		pcMu.Lock()
		if pcCache.DeadOffers == nil {
			pcCache.DeadOffers = map[string]bool{}
		}
		for k := range deadMark {
			pcCache.DeadOffers[k] = true
		}
		for k := range revived {
			delete(pcCache.DeadOffers, k)
		}
		pcMu.Unlock()
		pcSaveCache()
	}
	cfg.Entries = entries
	if err := pcSavePriority(cfg); err != nil {
		return 0, 0, missing, 0, 0, nil, dropped
	}
	// 列表已落盘，供应商顺序随之刷新（必须放在落盘后：内部会重新读列表）
	pcAttachProviderOrder(rows)
	// 同步到 new-api 渠道：pcSyncChannelKeys 内部重新读列表（此时已是刚落盘的
	// 新 entries），管理渠道的 key/models/base_url 全量接管并维护 abilities，
	// 不再需要这里另写一遍 models
	synced := 0
	if cfg.AutoRule.SyncChannels && model.DB != nil {
		_, synced = pcSyncChannelKeys() // key 保险箱 → 渠道（每次自动更新都同步，渠道无需手工维护）
		pcPublishKeyModelRoute()        // 渠道/key 集变了 → 重新发布按模型选 key 注册表
		go pcRefreshAccessCache()       // 探测缓存可能过期：后台重探并重发布，多 key 渠道首轮就按可见性选 key
	}
	routed := 0
	if cfg.AutoRule.RouteChannels {
		if m, st := pcBuildRouteTable(entries); len(m) > 0 {
			pcRouteTable.Store(&m)
			pcStageTable.Store(&st)
			routed = len(m)
		}
	} else {
		pcRouteTable.Store(nil)
		pcStageTable.Store(nil)
	}
	// 本地 harness 配置同步（勾选才写；写的是本机 127.0.0.1 地址 + 托管令牌 + 模型列表）
	var harness []string
	if len(cfg.AutoRule.Harnesses) > 0 {
		harness = pcSyncHarnessConfigs(pcHarnessSortedModels(entries, hprice))
		for _, msg := range harness {
			common.SysLog("harness sync: " + msg)
		}
	}
	return len(entries), offers, missing, synced, routed, harness, dropped
}

// ---------------- 刷新编排 ----------------

func pcRunRefresh(force bool) {
	pcMu.Lock()
	if pcRefresh.Running {
		pcMu.Unlock()
		return
	}
	pcRefresh = pcRefreshState{Running: true, Total: 17, Current: "并行抓取 0/17"}
	pcMu.Unlock()
	go func() {
		// 独立数据源并行抓取（各源写缓存均有 pcMu 保护），OR 目录+供应商价依赖面大殿后串行
		tasks := []struct {
			name string
			fn   func()
		}{
			{"汇率", func() { pcFetchExchange(true) }},
			{"余额", func() { pcFetchBalances() }}, // 余额也按「自动从网络抓取」间隔预查，面板打开即缓存命中
			{"OpenCode Go", func() { pcFetchGoModels(true) }},
			{"DeepSeek 别名", func() { pcFetchDsFlashAlias(true) }},
			{"DeepSeek 官方", func() { pcFetchDeepseek(true) }},
			{"LiveBench", func() { pcFetchLivebench(true) }},
			{"Agent Arena", func() { pcFetchArena(true) }},
			{"OR 用量榜", func() { pcFetchOrRankings(true) }},
			{"AA 指数", func() { pcFetchAA(true) }},
			{"Design Arena", func() { pcFetchDesignArena(true) }},
			{"司南", func() { pcFetchOpenCompass(true) }},
			{"LLM Stats", func() { pcFetchLlmStats(true) }},
			{"AI IQ", func() { pcFetchAiq(true) }},
			{"BuzzAI", func() { pcFetchBuzzaiModels(true) }},
			{"APIKL", func() { pcFetchApiklModels(true) }},
			{"IKUN", func() { pcFetchIkunModels(true) }},
			{"OR 供应商表", func() { pcFetchOrProviderSlugs() }},
			// APIB（apib.ai）定价走站内 Next.js RSC 私有通道，对服务器端请求 307
			// 门禁（须浏览器会话 cookie），暂无稳定公开接口，未接入定价行情
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		done := 0
		for _, t := range tasks {
			wg.Add(1)
			go func(name string, fn func()) {
				defer wg.Done()
				fn()
				mu.Lock()
				done++
				d := done
				mu.Unlock()
				pcRefreshSet(func(st *pcRefreshState) {
					st.Done = d
					st.Current = fmt.Sprintf("并行抓取 %d/%d（%s 完成）", d, len(tasks), name)
				})
			}(t.name, t.fn)
		}
		wg.Wait()
		pcRefreshSet(func(st *pcRefreshState) { st.Done = len(tasks); st.Current = "OpenRouter 供应商价" })
		pcFetchCatalog(force)
		if force {
			pcPrefetchOr(true, func(_ string, done, total int) {
				pcRefreshSet(func(st *pcRefreshState) {
					st.Total = len(tasks) + total
					st.Done = len(tasks) + done
				})
			})
		} else {
			// 只补缺失/过期的
			pcMu.Lock()
			staleIds := []string{}
			for _, m := range pcCache.Models {
				if strings.HasPrefix(m.Id, "~") || strings.HasPrefix(m.Id, "openrouter/") {
					continue
				}
				p, ok := pcCache.Prices[m.Id]
				if !ok || len(p.Providers) == 0 || time.Now().Unix()-p.FetchedAt > pcPricesTTL {
					staleIds = append(staleIds, m.Id)
				}
			}
			pcMu.Unlock()
			pcRefreshSet(func(st *pcRefreshState) { st.Total = len(tasks) + len(staleIds) })
			for _, id := range staleIds {
				pcRefreshSet(func(st *pcRefreshState) { st.Current = id })
				pcFetchOrPrice(id, false)
				pcRefreshSet(func(st *pcRefreshState) { st.Done++ })
			}
		}
		pcSaveCache()
		pcGetUnified(true)
		// 零价（免费档）报价批量实测：OR 的 :free 档已全线收回但目录仍挂着，
		// 按实测而非后缀判定（供应商免费档是否可用只有真实调用说了算）。
		// max_tokens=1 打免费/报错请求成本≈0；结果进连通性测试缓存（30 分钟，
		// 与 apply 的过滤共用存储）。基础设施性失败（本地断网等）≥25% 且 ≥5 例
		// 时熔断不标记，防止把全场报价误杀
		pcRefreshSet(func(st *pcRefreshState) { st.Current = "免费档报价实测" })
		rows, _ := pcGetUnified(false)
		cands := []pcTestCand{}
		seen := map[string]bool{}
		for _, r := range rows {
			if r.In > 0 {
				continue
			}
			c := pcTestCand{strings.ToLower(r.Channel), r.Ref, r.Provider, r.Quant}
			if !seen[c.key()] {
				seen[c.key()] = true
				cands = append(cands, c)
			}
		}
		if len(cands) > 0 {
			// 并发压到 4：免费档集中在同一 OR 账号，突发太猛会触发上游限流，
			// 也可能挤占同一把 key 上生产流量的每分钟配额
			results := pcTestOffersParallel(cands, 30*time.Minute, nil, 4)
			dead := map[string]bool{}
			tested, infra, rl := 0, 0, 0
			for _, c := range cands {
				res := results[c.key()]
				tested++
				if pcTestRateLimited(res) {
					rl++ // 限流=不定：不判死（否则自家突发会造成假阴性）
					continue
				}
				// no_key 是「无法判断」而非基础设施失败，不进熔断分子
				if !res.Ok && res.Error != "no_key" && (res.Status == 0 || res.Error == "request_failed" || strings.Contains(res.Error, "timeout")) {
					infra++
				}
				if !res.Ok && res.Error != "no_key" {
					dead[c.key()] = true
				}
			}
			// 限流/网络故障占比过高 = 结果不可信（我们自己太快或本地断网），熔断不标记
			if tested >= 5 && (infra+rl)*4 >= tested {
				dead = map[string]bool{}
			}
			pcMu.Lock()
			// 只重建免费档键：apply 连通性过滤写回的付费 dead（免费档批量实测
			// 覆盖不到的）原样保留，否则下一次刷新会把它们整体抹掉
			merged := map[string]bool{}
			for k, v := range pcCache.DeadOffers {
				if !seen[k] {
					merged[k] = v
				}
			}
			for k := range dead {
				merged[k] = true
			}
			pcCache.DeadOffers = merged
			pcMu.Unlock()
			pcSaveCache()
			pcGetUnified(true) // 带 dead 标记重建行情
		}
		// 自动更新规则：抓取完成后按规则重建优先级列表（规则关闭时内部直接跳过）
		pcApplyAutoRule(false)
		pcRefreshSet(func(st *pcRefreshState) {
			st.Running = false
			st.Finished = true
			st.Current = ""
		})
	}()
}

func pcStartAutoRefresh() {
	pcAutoOnce.Do(func() {
		go func() {
			for {
				time.Sleep(60 * time.Second)
				cfg := pcLoadPriority()
				if cfg.RefreshInterval == "never" {
					continue
				}
				secs := map[string]float64{
					"10m": 600, "30m": 1800, "1h": 3600, "6h": 21600, "12h": 43200,
					"1d": 86400, "1w": 604800, "1M": 2592000,
				}
				interval, ok := secs[cfg.RefreshInterval]
				if !ok {
					continue
				}
				pcMu.Lock()
				running := pcRefresh.Running
				last := pcCache.ArenaAt
				for _, ts := range []int64{pcCache.LbAt, pcCache.OrAt, pcCache.AaAt, pcCache.DaAt, pcCache.OcAt, pcCache.LsAt, pcCache.AiqAt, pcCache.BzAt, pcCache.AkAt, pcCache.IkAt} {
					if ts > 0 && (last == 0 || ts < last) {
						last = ts
					}
				}
				pcMu.Unlock()
				if running {
					continue
				}
				if last == 0 || time.Now().Unix()-last >= int64(interval) {
					pcRunRefresh(false)
				}
			}
		}()
	})
}

// ---------------- HTTP 处理器 ----------------

func GetPriceCompareOverview(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	pcFetchExchange(false)
	pcMu.Lock()
	ex := gin.H{"rate": pcCache.ExchangeRate, "live": pcCache.ExchangeLive, "at": pcCache.ExchangeAt}
	pcMu.Unlock()
	peak := pcIsPeak(time.Now())
	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"exchange": ex,
			"ratios":   pcReadRatios(),
			"peak_now": peak,
		},
	})
}

var pcLoadOnce sync.Once

func pcLoadCacheOnce() {
	pcLoadOnce.Do(func() {
		pcLoadCache()
		// 汇率缺失或已过期（曾在线成功后离线超 6h 重启）都先落负缓存标记：
		// 任何 HTTP 路径都不再同步等汇率抓取（离线/被墙时两个源各 15s 超时
		// 会拖死首开），先沿用缓存/兜底汇率出图，真实汇率后台补抓、定时刷新
		// 任务会正常更新
		now := time.Now().Unix()
		pcMu.Lock()
		live := pcCache.ExchangeLive
		stale := live && now-pcCache.ExchangeAt >= pcExchangeTTL
		if !live || stale {
			pcCache.ExchangeAt = now
		}
		pcMu.Unlock()
		if !live || stale {
			go pcFetchExchange(true)
		}
		// OR 供应商 slug 对照表（provider.order/连通性测试的钉扎依赖）冷启动即取，
		// 避免首轮 apply 用小写推导的错 slug 误判报价
		pcFetchOrProviderSlugs()
		// 上次构建好的行情直接载入：冷启动免十几秒全量重建，面板秒开；
		// 过期后由 pcGetUnified 的 stale-while-rebuild 路径后台刷新
		pcLoadUnified()
		pcMu.Lock()
		built := len(pcUnifiedRows) > 0
		pcMu.Unlock()
		if !built {
			// 无持久化行情（首次/文件丢失）：同步用缓存建一次，首开即有图
			pcGetUnified(true)
		}
		go func() {
			// 后台完整刷新：抓源+补 OR 价+应用规则，完成后行情自动更新
			pcRunRefresh(false)
		}()
	})
}

func GetPriceCompareUnified(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	rows, at := pcGetUnified(false)
	// 带上后台任务状态与 LLM Stats 基准元数据（id→展示名，前端动态维度用）
	pcMu.Lock()
	st := pcRefresh
	benchMeta := map[string]string{}
	for _, b := range pcCache.LsBoards {
		benchMeta[b.ID] = b.Name
	}
	for f, name := range pcCache.AiqMeta {
		benchMeta[f] = name // AI IQ 字段展示名（前端动态发现维度用）
	}
	pcMu.Unlock()
	data := gin.H{"success": true, "data": gin.H{
		"rows": rows, "at": at, "peak_now": pcIsPeak(time.Now()),
		"rate": pcExchangeRate(), "refresh": st, "bench_meta": benchMeta,
	}}
	// 手动 marshal：失败时（如残留 +Inf/NaN）兜底返回空行而非让 gin 写完
	// 状态头后 panic-abort 产出 200+空体
	b, err := json.Marshal(data)
	if err != nil {
		common.SysError("price_compare unified marshal: " + err.Error())
		c.JSON(200, gin.H{"success": true, "data": gin.H{
			"rows": []pcUnifiedRow{}, "at": at, "marshal_error": err.Error(),
		}})
		return
	}
	c.Data(200, "application/json; charset=utf-8", b)
}

func GetPriceComparePriority(c *gin.Context) {
	pcStartAutoRefresh()
	c.JSON(200, gin.H{"success": true, "data": pcLoadPriority()})
}

func PostPriceComparePriority(c *gin.Context) {
	// 从默认值起绑（Decoder.Decode 只覆盖载荷里出现的字段）：缺省 top_n 落
	// 默认 3 而非零值——否则「未设置」会以 0=不限落库，与用户显式设 0 无法
	// 区分，重载后项内 nil TopN 的 fallback N 静默变成不限
	cfg := pcDefaultPriority()
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	if cfg.Entries == nil {
		cfg.Entries = map[string]pcPriorityEntry{}
	}
	if err := pcSavePriority(cfg); err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	// 手工调整立即生效：重建供应商顺序（provider.order）与比价路由表
	if rows, _ := pcGetUnified(false); len(rows) > 0 {
		pcAttachProviderOrder(rows)
	}
	if cfg.AutoRule.RouteChannels {
		if m, st := pcBuildRouteTable(cfg.Entries); len(m) > 0 {
			pcRouteTable.Store(&m)
			pcStageTable.Store(&st)
		} else {
			pcRouteTable.Store(nil)
			pcStageTable.Store(nil)
		}
	} else {
		// 开关关掉立即停用旧路由表，不等下次自动应用
		pcRouteTable.Store(nil)
		pcStageTable.Store(nil)
	}
	c.JSON(200, gin.H{"success": true, "data": cfg})
}

func PostPriceCompareRefresh(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	pcMu.Lock()
	running := pcRefresh.Running
	pcMu.Unlock()
	if running {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"running": true}})
		return
	}
	pcRunRefresh(true)
	c.JSON(200, gin.H{"success": true, "data": gin.H{"running": true}})
}

// PostPriceCompareAutoRuleApply 手动立即应用自动更新规则（不触发抓取，用当前行情）
func PostPriceCompareAutoRuleApply(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	models, offers, missing, channels, routes, harness, dropped := pcApplyAutoRule(true)
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"models": models, "offers": offers, "missing": missing,
		"channels": channels, "routes": routes, "harness": harness,
		"conn_dropped": dropped,
	}})
}

func GetPriceCompareProviders(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	mid := c.Query("model")
	if mid == "" {
		c.JSON(200, gin.H{"success": false, "message": "missing model"})
		return
	}
	if strings.HasSuffix(mid, ":batch") {
		c.JSON(200, gin.H{"success": false, "message": ":batch models are excluded"})
		return
	}
	pcMu.Lock()
	p, ok := pcCache.Prices[mid]
	pcMu.Unlock()
	if !ok || (p.Error == "" && len(p.Providers) == 0) {
		pcFetchOrPrice(mid, false) // 可能阻塞数秒
		pcMu.Lock()
		p, ok = pcCache.Prices[mid]
		pcMu.Unlock()
	}
	if !ok || (p.Error != "" && len(p.Providers) == 0) {
		c.JSON(200, gin.H{"success": false, "message": "no provider data"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": p})
}

// GetPriceCompareAliases 读同款识别规则（内置映射单独返回，仅作只读展示）。
func GetPriceCompareAliases(c *gin.Context) {
	al := pcLoadAliases()
	pcMu.Lock()
	models := append([]pcCatalogEntry{}, pcCache.Models...)
	pcMu.Unlock()
	exact := map[string]string{}
	for src, dst := range al.Exact {
		if _, builtin := al.Builtin[src]; builtin {
			continue // 内置映射不是用户规则，不返回
		}
		exact[src] = dst
	}
	catalog := make([]string, 0, len(models))
	for _, m := range models {
		catalog = append(catalog, m.Id[strings.LastIndex(m.Id, "/")+1:])
	}
	sort.Strings(catalog)
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"exact": exact, "bench": al.Bench, "catalog": catalog,
	}})
}

// PostPriceCompareAliases 保存同款识别规则并立即重建统一模型库。
func PostPriceCompareAliases(c *gin.Context) {
	var body struct {
		Exact map[string]string   `json:"exact"`
		Bench map[string]string   `json:"bench"`
		Regex []map[string]string `json:"regex"`
		Split []string            `json:"split"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "invalid payload"})
		return
	}
	rules := []map[string]string{}
	for _, r := range body.Regex {
		if r["pattern"] == "" {
			continue
		}
		if _, err := regexp.Compile(r["pattern"]); err != nil {
			c.JSON(200, gin.H{"success": false, "message": "非法正则: " + r["pattern"]})
			return
		}
		rules = append(rules, map[string]string{"pattern": r["pattern"], "target": r["target"]})
	}
	// 未携带的段落保留原值：UI 各区块分别保存（如渠道修正只带 exact），
	// 不能把 bench/regex/split 意外清空
	prev := pcLoadAliases()
	if body.Exact == nil {
		body.Exact = map[string]string{}
		for src, dst := range prev.Exact {
			if _, isB := prev.Builtin[src]; !isB {
				body.Exact[src] = dst
			}
		}
	}
	if body.Bench == nil {
		body.Bench = prev.Bench
	}
	if body.Regex == nil {
		body.Regex = []map[string]string{}
		for _, r := range prev.Regex {
			body.Regex = append(body.Regex, map[string]string{"pattern": r.Pattern.String(), "target": r.Target})
		}
	}
	if body.Split == nil {
		body.Split = []string{}
		for src := range prev.Split {
			body.Split = append(body.Split, src)
		}
	}
	bench := map[string]string{}
	for k, dst := range body.Bench {
		if src, name, ok := strings.Cut(k, "|"); ok && name != "" && dst != "" {
			bench[src+"|"+name] = dst
		}
	}
	out := map[string]interface{}{"exact": body.Exact, "bench": bench, "regex": rules, "split": body.Split}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := os.WriteFile(filepath.Join(pcDataDir(), "aliases.json"), b, 0644); err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	pcDbSet(pcDbAliasesKey, string(b)) // 双写实例数据库，文件丢失时可恢复
	rows, at := pcGetUnified(true)     // 别名变了 → 立即重建归并
	c.JSON(200, gin.H{"success": true, "data": gin.H{"rows": rows != nil, "count": len(rows), "at": at}})
}

func GetPriceCompareMapping(c *gin.Context) {
	pcStartAutoRefresh()
	pcLoadCacheOnce()
	ix := pcBuildResolveIdx()
	pcMu.Lock()
	goModels := append([]pcGoModel{}, pcCache.GoModels...)
	dsModels := append([]pcDsModel{}, pcCache.DsModels...)
	pcMu.Unlock()
	type mappingRow struct {
		Channel   string `json:"channel"`
		Ref       string `json:"ref"`
		Name      string `json:"name"`
		Target    string `json:"target"`
		TargetKey string `json:"target_key"`
		Source    string `json:"source"`
		Matched   bool   `json:"matched"`
	}
	rows := []mappingRow{}
	seen := map[string]bool{}
	for _, m := range goModels {
		base := strings.TrimSpace(pcGoPeriodRe.ReplaceAllString(m.Name, ""))
		id := m.ModelId
		if seen["opencode|"+id+"|"+base] {
			continue
		}
		seen["opencode|"+id+"|"+base] = true
		k, nm, src, ok := ix.resolve(id, base)
		rows = append(rows, mappingRow{Channel: "opencode", Ref: id, Name: base,
			Target: nm, TargetKey: k, Source: src, Matched: ok})
	}
	for _, dm := range dsModels {
		k, nm, src, ok := ix.resolve(dm.Id, dm.Name)
		rows = append(rows, mappingRow{Channel: "deepseek", Ref: dm.Id, Name: dm.Id,
			Target: nm, TargetKey: k, Source: src, Matched: ok})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Matched != rows[j].Matched {
			return !rows[i].Matched // 未匹配在前
		}
		if rows[i].Channel != rows[j].Channel {
			return rows[i].Channel < rows[j].Channel
		}
		return rows[i].Name < rows[j].Name
	})
	unmatched := 0
	for _, r := range rows {
		if !r.Matched {
			unmatched++
		}
	}
	// Benchmark 榜单行 → OR 正名 的实际生效映射（自动+钉住），并给出每源未匹配行，
	// 让用户能审计模糊匹配的结果。行情未预热时跳过（此时全部判未匹配会误导）。
	type benchMapRow struct {
		Source    string  `json:"source"`
		BenchName string  `json:"bench_name"`
		Target    string  `json:"target"`
		TargetKey string  `json:"target_key"`
		Sim       float64 `json:"sim"`
		Pinned    bool    `json:"pinned"`
		Matched   bool    `json:"matched"`
	}
	var benchOut []benchMapRow
	benchUnmatched := 0
	urows, _ := pcGetUnified(false)
	if len(urows) > 0 {
		pcMu.Lock()
		lbKeys := make([]string, 0, len(pcCache.LbRows))
		for k := range pcCache.LbRows {
			lbKeys = append(lbKeys, k)
		}
		arenaRows := []map[string]interface{}{}
		arenaCanon := map[string]string{}
		for _, rs := range pcCache.ArenaBoards {
			for _, r := range rs {
				arenaRows = append(arenaRows, r)
				if nm, ok2 := r["name"].(string); ok2 {
					cn := pcCanonKey(nm)
					if _, dup := arenaCanon[cn]; !dup {
						arenaCanon[cn] = nm
					}
				}
			}
		}
		pcMu.Unlock()
		pinCanon := map[string]bool{}
		for k := range pcLoadAliases().Bench {
			if src, name, ok2 := strings.Cut(k, "|"); ok2 {
				pinCanon[src+"|"+pcCanonKey(name)] = true
			}
		}
		benchRows := []benchMapRow{}
		matched := map[string]bool{}
		seenPair := map[string]bool{}
		addBench := func(src, bname, target, tkey string, sim float64) {
			cn := pcCanonKey(bname)
			if cn == "" || seenPair[src+"|"+cn] {
				return
			}
			seenPair[src+"|"+cn] = true
			matched[src+"|"+cn] = true
			benchRows = append(benchRows, benchMapRow{src, bname, target, tkey,
				sim, pinCanon[src+"|"+cn], true})
		}
		seenKey := map[string]bool{}
		for _, r := range urows {
			if seenKey[r.Key] {
				continue // 同 canonical 的多渠道报价行只列一次
			}
			seenKey[r.Key] = true
			for src, e := range r.Bench {
				if e == nil {
					continue
				}
				if e.Name != "" {
					addBench(src, e.Name, r.Name, r.Key, e.Sim)
				} else if src == "arena" {
					// 仅 elo 榜命中时 Bench 不带行名：按 eloOf 同款两步法补齐
					var nm string
					var sim float64
					var ok2 bool
					if n, dup := arenaCanon[pcCanonKey(r.Name)]; dup {
						nm, sim, ok2 = n, 1.0, true
					} else if n, s, ok3 := pcMatchRows(arenaRows, r.Name, r.Ref); ok3 {
						nm, sim, ok2 = n, s, true
					}
					if ok2 {
						addBench("arena", nm, r.Name, r.Key, sim)
					}
				}
			}
		}
		for _, nm := range lbKeys {
			if !matched["livebench|"+pcCanonKey(nm)] {
				benchRows = append(benchRows, benchMapRow{Source: "livebench", BenchName: nm})
			}
		}
		for cn, nm := range arenaCanon {
			if !matched["arena|"+cn] {
				benchRows = append(benchRows, benchMapRow{Source: "arena", BenchName: nm})
			}
		}
		for src, named := range pcExtSrcLists() {
			for _, n := range named {
				if n.Name != "" && !matched[src+"|"+pcCanonKey(n.Name)] {
					benchRows = append(benchRows, benchMapRow{Source: src, BenchName: n.Name})
				}
			}
		}
		srcRank := map[string]int{"livebench": 0, "arena": 1, "orr": 2, "aa": 3, "da": 4, "oc": 5, "ls": 6, "aiq": 7}
		sort.Slice(benchRows, func(i, j int) bool {
			if benchRows[i].Matched != benchRows[j].Matched {
				return !benchRows[i].Matched
			}
			if benchRows[i].Source != benchRows[j].Source {
				return srcRank[benchRows[i].Source] < srcRank[benchRows[j].Source]
			}
			return benchRows[i].BenchName < benchRows[j].BenchName
		})
		for _, br := range benchRows {
			if !br.Matched {
				benchUnmatched++
			}
		}
		benchOut = benchRows
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"rows": rows, "total": len(rows), "unmatched": unmatched,
		"bench": gin.H{"rows": benchOut, "total": len(benchOut), "unmatched": benchUnmatched},
	}})
}

func GetPriceCompareRefreshStatus(c *gin.Context) {
	pcMu.Lock()
	st := pcRefresh
	pcMu.Unlock()
	c.JSON(200, gin.H{"success": true, "data": st})
}
