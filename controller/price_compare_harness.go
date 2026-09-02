package controller

// 本地 harness 配置自动同步：自动更新规则里勾选了 Claude Code / Codex / OpenCode /
// ZCode 时，应用规则（定时到点或手动触发）把本机 New API 地址（http://127.0.0.1:端口）
// 与托管令牌写进对应 harness 的配置文件，使其模型列表跟随优先级列表自动更新，
// 免去每次手动改各软件的模型设置。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

// pcHarnessTokenName harness 同步专用令牌名（面板令牌列表可见，删了下次同步自动重建）
const pcHarnessTokenName = "harness-auto"

// pcHarnessMu 串行化整次 harness 同步：定时刷新链与手动 apply 端点可能并发到达，
// 不加锁的话 pcEnsureHarnessToken 的查-建竞态会造出重复的 harness-auto 无限额令牌，
// 同一批配置文件也会被并发读改写。注意不能用 pcMu——pcSyncZcodeRules 内部还要拿它。
var pcHarnessMu sync.Mutex

var pcHarnessList = []struct{ Key, Label string }{
	{"claude", "Claude Code"},
	{"codex", "Codex"},
	{"opencode", "OpenCode"},
	{"zcode", "ZCode"},
}

// pcSyncHarnessConfigs 把地址+令牌+模型列表写入勾选的 harness 配置，返回每个
// harness 的结果行（"Claude Code ✓" / "Codex: 原因"）。models 为空（列表还没建）时跳过。
func pcSyncHarnessConfigs(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	pcHarnessMu.Lock()
	defer pcHarnessMu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"harness: " + err.Error()}
	}
	token, err := pcEnsureHarnessToken()
	if err != nil {
		return []string{"harness token: " + err.Error()}
	}
	// 监听端口与 main.go 同规则：优先 PORT 环境变量，其次 --port flag
	port := common.ListenPort()
	base := "http://127.0.0.1:" + port
	enabled := map[string]bool{}
	for _, h := range pcLoadPriority().AutoRule.Harnesses {
		enabled[h] = true
	}
	var out []string
	for _, h := range pcHarnessList {
		if !enabled[h.Key] {
			continue
		}
		var err error
		switch h.Key {
		case "claude":
			err = pcSyncClaude(filepath.Join(home, ".claude", "settings.json"), base, token, models)
		case "codex":
			err = pcSyncCodex(filepath.Join(home, ".codex"), base, token, models)
		case "opencode":
			err = pcSyncOpencode(home, base, token, models)
		case "zcode":
			err = pcSyncZcode(home, base, token, models)
		}
		if err != nil {
			out = append(out, h.Label+": "+err.Error())
		} else {
			out = append(out, h.Label+" ✓")
		}
	}
	return out
}

// pcHarnessDisplayName harness 模型显示名：去掉斜杠前的厂商前缀，尾部附人民币
// 实际价（全角括号，如 google/gemma-4-31b-it:free → gemma-4-31b-it:free（¥0））。
// 价格四舍五入到 3 位小数并去尾零。显示名同时注册进管理渠道 models + model_mapping
// （映射回真实 ref），保证按显示名发起的请求可路由。
func pcHarnessDisplayName(ref string, price float64) string {
	name := ref
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	p := math.Round(price*1000) / 1000
	if p == 0 {
		p = 0 // 清掉 -0，避免格式化出 "-0"
	}
	return fmt.Sprintf("%s（¥%s）", name, strconv.FormatFloat(p, 'f', -1, 64))
}

// pcHarnessSortedModels 优先级列表的模型名列表：名字取每条目跨刷新稳定的报价
// ref（见 pcStableOfferRef），按最低实际价升序，与面板展示顺序一致
func pcHarnessSortedModels(entries map[string]pcPriorityEntry, hprice map[string]float64) []string {
	type mi struct {
		name  string
		price float64
	}
	var list []mi
	for k, e := range entries {
		if len(e.Offers) == 0 {
			continue
		}
		list = append(list, mi{pcStableOfferRef(e.Offers), hprice[k]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].price < list[j].price })
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, m := range list {
		if seen[m.name] {
			continue // 不同模型撞名的罕见情况：保留更便宜的
		}
		seen[m.name] = true
		out = append(out, m.name)
	}
	return out
}

// pcStableOfferRef 选一条跨刷新稳定的报价名作 harness 模型 ID：优先 OpenRouter
// 报价（目录名 vendor/model，最规整且 ZCode 智能配置可识别），否则取字典序最小
// 的 ref（与 Offers 内部排序无关，价格变动不改名）。绝不能用带（¥价）的显示名：
// 它随每轮刷新变价改名，正在使用的会话下一请求就「模型找不到」。
func pcStableOfferRef(offers []pcPriorityOffer) string {
	pick := ""
	for _, of := range offers {
		if of.Channel == "openrouter" {
			return of.Ref
		}
		if pick == "" || of.Ref < pick {
			pick = of.Ref
		}
	}
	return pick
}

// pcEnsureHarnessToken 返回（必要时创建）harness 同步专用的本机令牌（sk- 前缀全量 key）
func pcEnsureHarnessToken() (string, error) {
	var ids []int
	// fork 的 DB 层会把 gorm 内部状态包装成非 nil error，以 ids 为准
	_ = model.DB.Table("users").
		Where("role >= ? AND status = ?", common.RoleAdminUser, common.UserStatusEnabled).
		Order("role DESC, id ASC").Limit(1).Pluck("id", &ids)
	if len(ids) == 0 {
		return "", fmt.Errorf("no enabled admin user")
	}
	uid := ids[0]
	var toks []*model.Token
	_ = model.DB.Where("user_id = ? AND name = ?", uid, pcHarnessTokenName).Limit(1).Find(&toks)
	if len(toks) > 0 {
		return "sk-" + toks[0].Key, nil
	}
	k, err := common.GenerateKey()
	if err != nil {
		return "", err
	}
	t := model.Token{
		UserId:         uid,
		Name:           pcHarnessTokenName,
		Key:            k,
		CreatedTime:    common.GetTimestamp(),
		AccessedTime:   common.GetTimestamp(),
		ExpiredTime:    -1,
		UnlimitedQuota: true,
		Status:         common.TokenStatusEnabled,
		Group:          "default",
	}
	if err := t.Insert(); err != nil {
		return "", err
	}
	return "sk-" + k, nil
}

// pcHarnessBackup 首次改写前把原文件备份为 <path>.pc-bak；备份已存在则保留最早那份
func pcHarnessBackup(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	bak := path + ".pc-bak"
	if _, err := os.Stat(bak); err == nil {
		return
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(bak, b, 0644)
	}
}

// pcWriteFileAtomic 同目录临时文件 + rename，避免半截配置；目录缺失时自动创建
// （如 OpenCode 装了但从未生成过配置目录）
func pcWriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pc-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// pcReadJSONMap 读 JSON 配置为通用 map（保留未知字段）；文件不存在按空对象处理
func pcReadJSONMap(path string) (map[string]interface{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, nil
		}
		return nil, err
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	return m, nil
}

func pcWriteJSONMap(path string, m map[string]interface{}) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return pcWriteFileAtomic(path, append(b, '\n'))
}

// ---- Claude Code：~/.claude/settings.json 的 env（BASE_URL/AUTH_TOKEN/模型）----

func pcSyncClaude(path, base, token string, models []string) error {
	pcHarnessBackup(path)
	m, err := pcReadJSONMap(path)
	if err != nil {
		return err
	}
	env, _ := m["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
	}
	env["ANTHROPIC_BASE_URL"] = base
	env["ANTHROPIC_AUTH_TOKEN"] = token
	env["ANTHROPIC_MODEL"] = models[0]
	env["ANTHROPIC_SMALL_FAST_MODEL"] = models[0]
	m["env"] = env
	m["model"] = models[0]
	return pcWriteJSONMap(path, m)
}

// ---- Codex：~/.codex/config.toml 顶层键 + [model_providers.newapi] 段 + auth.json ----
// TOML 做行级外科手术（顶层键原位替换、整段替换/追加），不重排用户文件的其它内容

func pcSyncCodex(dir, base, token string, models []string) error {
	cfgPath := filepath.Join(dir, "config.toml")
	pcHarnessBackup(cfgPath)
	var raw []byte
	if b, err := os.ReadFile(cfgPath); err == nil {
		raw = b
	} else if !os.IsNotExist(err) {
		return err
	}
	out := pcTomlTopLevel(raw, map[string]string{
		"model_provider": "newapi",
		"model":          models[0],
	})
	section := fmt.Sprintf("[model_providers.newapi]\nname = \"New API\"\nbase_url = \"%s/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n", base)
	out = pcTomlReplaceSection(out, "[model_providers.newapi]", section)
	if err := pcWriteFileAtomic(cfgPath, out); err != nil {
		return err
	}
	authPath := filepath.Join(dir, "auth.json")
	pcHarnessBackup(authPath)
	am, err := pcReadJSONMap(authPath)
	if err != nil {
		return err
	}
	am["OPENAI_API_KEY"] = token
	return pcWriteJSONMap(authPath, am)
}

// pcTomlTopLevel 在 TOML 顶层区（首个 [表头] 之前）设置若干 key = "value"：
// 已有该键则原位替换，缺失则插入到顶层区末尾
func pcTomlTopLevel(src []byte, kv map[string]string) []byte {
	lines := strings.Split(string(src), "\n")
	topEnd := len(lines)
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "[") {
			topEnd = i
			break
		}
	}
	done := map[string]bool{}
	for i := 0; i < topEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if _, want := kv[key]; want {
			lines[i] = key + " = " + strconv.Quote(kv[key])
			done[key] = true
		}
	}
	var missing []string
	for _, k := range []string{"model_provider", "model"} {
		if !done[k] {
			missing = append(missing, k+" = "+strconv.Quote(kv[k]))
		}
	}
	if len(missing) > 0 {
		var out []string
		out = append(out, lines[:topEnd]...)
		out = append(out, strings.Join(missing, "\n"), "")
		out = append(out, lines[topEnd:]...)
		lines = out
	}
	return []byte(strings.Join(lines, "\n"))
}

// pcTomlReplaceSection 替换指定表头的整段（到下一个表头或文件尾）；不存在则追加文件尾
func pcTomlReplaceSection(src []byte, header, section string) []byte {
	lines := strings.Split(string(src), "\n")
	start, end := -1, len(lines)
	for i, ln := range lines {
		if strings.TrimSpace(ln) == header {
			start = i
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
					end = j
					break
				}
			}
			break
		}
	}
	sec := strings.Split(strings.TrimRight(section, "\n"), "\n")
	var out []string
	if start < 0 {
		out = append(out, lines...)
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", "")
		out = append(out, sec...)
		out = append(out, "")
	} else {
		out = append(out, lines[:start]...)
		out = append(out, sec...)
		out = append(out, "")
		out = append(out, lines[end:]...)
	}
	return []byte(strings.Join(out, "\n"))
}

// ---- OpenCode：~/.config/opencode/opencode(.jsonc) 的 provider.newapi + auth.json ----

func pcSyncOpencode(home, base, token string, models []string) error {
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(cfgPath); err != nil {
		if _, e2 := os.Stat(cfgPath + "c"); e2 == nil {
			cfgPath += "c" // 沿用既有的 .jsonc 文件名（内容本就是纯 JSON）
		}
	}
	pcHarnessBackup(cfgPath)
	m, err := pcReadJSONMap(cfgPath)
	if err != nil {
		return err
	}
	prov, _ := m["provider"].(map[string]interface{})
	if prov == nil {
		prov = map[string]interface{}{}
	}
	prov["newapi"] = map[string]interface{}{
		"name":    "New API",
		"npm":     "@ai-sdk/openai-compatible",
		"options": map[string]interface{}{"baseURL": base + "/v1"},
		"models":  pcHarnessModelMap(models),
	}
	m["provider"] = prov
	if err := pcWriteJSONMap(cfgPath, m); err != nil {
		return err
	}
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	pcHarnessBackup(authPath)
	am, err := pcReadJSONMap(authPath)
	if err != nil {
		return err
	}
	am["newapi"] = map[string]interface{}{"type": "api", "key": token}
	return pcWriteJSONMap(authPath, am)
}

// ---- ZCode：v2/config.json 的 provider.newapi + v2/provider_config.json 的规则表 ----
// （后者才是桌面版 UI 模型列表的真正来源：personalModelIds/modelOrder）

func pcSyncZcode(home, base, token string, models []string) error {
	path := filepath.Join(home, ".zcode", "v2", "config.json")
	pcHarnessBackup(path)
	m, err := pcReadJSONMap(path)
	if err != nil {
		return err
	}
	prov, _ := m["provider"].(map[string]interface{})
	if prov == nil {
		prov = map[string]interface{}{}
	}
	prov["newapi"] = map[string]interface{}{
		"name":    "New API",
		"kind":    "anthropic",
		"options": map[string]interface{}{"apiKey": token, "baseURL": base},
		"enabled": true,
		"source":  "custom",
		"models":  pcHarnessModelMap(models),
	}
	m["provider"] = prov
	if err := pcWriteJSONMap(path, m); err != nil {
		return err
	}
	// ZCode 桌面版的供应器模型列表真正来源是 provider_config.json 的
	// personalModelIds/modelOrder（config.json 不驱动 UI 列表），必须一并更新
	return pcSyncZcodeRules(filepath.Join(home, ".zcode", "v2", "provider_config.json"), base, token, models)
}

// pcSyncZcodeRules 更新 provider_config.json 里「同名或同网关端口」供应器规则的
// 模型列表；没有匹配规则时按 ZCode 自身 schema 新建一条
func pcSyncZcodeRules(rulesPath, base, token string, models []string) error {
	pcHarnessBackup(rulesPath)
	rm, err := pcReadJSONMap(rulesPath)
	if err != nil {
		return err
	}
	cfg, _ := rm["config"].(map[string]interface{})
	if cfg == nil {
		// 文件不存在/无 config 段时从零构建（裸 home 首次同步场景），否则对 nil map 赋值 panic
		cfg = map[string]interface{}{}
		rm["config"] = cfg
	}
	pcr, _ := cfg["providerConfigRules"].(map[string]interface{})
	if pcr == nil {
		pcr = map[string]interface{}{}
		cfg["providerConfigRules"] = pcr
	}
	rules, _ := pcr["providerRules"].([]interface{})
	// 地址匹配用主机+端口（127.0.0.1:端口）：只比端口会把恰好同端口的远程无关规则
	// 误认领并遭到破坏性改写
	hostPort := base[strings.LastIndex(base, "/")+1:]
	hit := -1
	for i, r := range rules {
		rr, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := rr["providerName"].(string)
		cc, _ := rr["config"].(map[string]interface{})
		api, _ := cc["api"].(map[string]interface{})
		bURL, _ := api["baseUrl"].(string)
		// 名字比较忽略大小写与空白（自建规则写作 "New API"）：名字臂不依赖端口，
		// 实例端口变更后仍能命中旧规则改写其地址，而不是另建重复规则
		if strings.EqualFold(strings.Join(strings.Fields(name), ""), "newapi") ||
			(bURL != "" && strings.Contains(bURL, hostPort)) {
			hit = i
			break
		}
	}
	var pid string
	if hit >= 0 {
		rr, _ := rules[hit].(map[string]interface{})
		pid, _ = rr["providerId"].(string)
		cc, _ := rr["config"].(map[string]interface{})
		if cc == nil {
			cc = map[string]interface{}{}
			rr["config"] = cc
		}
		cc["personalModelIds"] = models
		cc["modelOrder"] = models
		if en, ok := rr["enabled"].(bool); !ok || !en {
			rr["enabled"] = true
		}
		// 凭据与地址跟随本网关刷新：config.json 每轮无条件写最新令牌/地址，这里不刷
		// 新的话令牌轮换（删除重建 harness-auto）或端口变更后两个文件同轮写出分叉；
		// api 段按新建分支模板整体重写，顺带清掉 strict schema 不认的 apiKey
		// （凭据只放 access.apiKey）
		cc["access"] = map[string]interface{}{"type": "api-key", "apiKey": token}
		cc["api"] = map[string]interface{}{"type": "openai-chat-completions", "baseUrl": base + "/v1"}
		rules[hit] = rr
	} else {
		id := uuid.NewString()
		pid = id
		rules = append(rules, map[string]interface{}{
			"providerId":   id,
			"providerName": "New API",
			"enabled":      true,
			"config": map[string]interface{}{
				"group":            "standard-personal",
				"access":           map[string]interface{}{"type": "api-key", "apiKey": token},
				"api":              map[string]interface{}{"type": "openai-chat-completions", "baseUrl": base + "/v1"},
				"personalModelIds": models,
				"modelOrder":       models,
			},
		})
	}
	pcr["providerRules"] = rules
	// 模型要出现在 ZCode 选择器里，除 personalModelIds 外还必须每模型有一条
	// modelConfigRules（enabled 显式 true，否则 resolver 判不可执行 → 整个供应器
	// 因 0 个可执行模型被跳过，选择器里根本找不到）；顺带清掉该供应器已下线的旧条目
	mcr, _ := cfg["modelConfigRules"].(map[string]interface{})
	if mcr == nil {
		mcr = map[string]interface{}{}
		cfg["modelConfigRules"] = mcr
	}
	pmr, _ := mcr["providerModelRules"].([]interface{})
	inList := map[string]bool{}
	for _, mid := range models {
		inList[mid] = true
	}
	kept := pmr[:0]
	seen := map[string]bool{}
	for _, rule := range pmr {
		r0, ok := rule.(map[string]interface{})
		if !ok {
			kept = append(kept, rule)
			continue
		}
		mid, _ := r0["modelId"].(string)
		rpid, _ := r0["providerId"].(string)
		if rpid == pid && !inList[mid] {
			continue // 已下线模型：连规则一起清掉
		}
		if rpid == pid {
			seen[mid] = true
			// 只保证存在且启用；properties/optionSpecs 原样保留（ZCode 智能配置
			// 维护的推荐值与用户手动管理的字段都不被冲掉）
			cc, _ := r0["config"].(map[string]interface{})
			if cc == nil {
				cc = map[string]interface{}{}
				r0["config"] = cc
			}
			cc["enabled"] = true
		}
		kept = append(kept, rule)
	}
	for _, mid := range models {
		if seen[mid] {
			continue
		}
		kept = append(kept, map[string]interface{}{
			"modelId":    mid,
			"providerId": pid,
			"config":     map[string]interface{}{"enabled": true},
		})
	}
	mcr["providerModelRules"] = kept
	return pcWriteJSONMap(rulesPath, rm)
}

// 模型参数（properties/optionSpecs：上下文窗口、输出上限、模态、推理档位）一律
// 不写、不覆盖：ZCode 的「智能配置」按模型 ID 持续维护推荐值并自动同步，网关
// 侧写死既会冲掉用户手改（手改在 ZCode 里转手动管理），也曾因目录数据保守
// （兜底 128k 上下文/32k 输出）导致窗口过短。同步只保证条目存在且 enabled，
// 参数全部交给 ZCode 智能配置。

func pcHarnessModelMap(models []string) map[string]interface{} {
	m := map[string]interface{}{}
	for _, id := range models {
		m[id] = map[string]interface{}{}
	}
	return m
}
