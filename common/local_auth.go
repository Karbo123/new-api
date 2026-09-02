package common

import (
	"net/http"
	"strings"
)

// LocalAuthBypass 本机（回环地址）请求免登录，默认以管理员身份放行。
// 生效条件（同时满足）：
//  1. 请求来源 IP 为 127.0.0.1 / ::1（用 RemoteAddr 真实对端地址，不受 X-Forwarded-For 伪造影响）；
//  2. Host 头为本机名（127.0.0.1 / localhost / ::1）——防 DNS Rebinding：
//     恶意网页把自有域名解析到 127.0.0.1 后借浏览器调用本机接口时，Host 是攻击域名，会被拒绝。
//  3. 未经代理转发：携带 Forwarded / X-Forwarded-For / X-Real-IP 且其中任一跳来源
//     非回环时视为远程请求（同机反向代理会把远程请求变成回环来源，见 IsLoopbackRequest）。
//
// 远程（非回环）请求完全不受影响，仍需正常登录。
// 注意：若同机反向代理未透传任何转发头（如 nginx 未配置 proxy_set_header
// X-Forwarded-For），仅凭对端地址无法区分远程请求，此类部署应设置
// LOCAL_AUTH_BYPASS=false 关闭免密。
// 设置环境变量 LOCAL_AUTH_BYPASS=false 可关闭。
var LocalAuthBypass = true

func init() {
	LocalAuthBypass = GetEnvOrDefaultBool("LOCAL_AUTH_BYPASS", true)
}

// IsLoopbackRequest 判定请求是否来自本机回环地址且 Host 为本机名。
// 同机反向代理（nginx/Apache）转发远程请求时，对端地址同样是 127.0.0.1，
// 且 nginx 默认把 Host 改写为 proxy_pass 主机名（Apache ProxyPreserveHost 默认
// Off 同理），仅凭 RemoteAddr+Host 会把经代理的全部远程请求误判为本机回环。
// 因此携带代理转发头时逐跳校验：任一跳来源非回环即视为远程请求。
func IsLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if hasNonLoopbackForwardedHop(r) {
		return false
	}
	if !isLoopbackIP(hostOnly(r.RemoteAddr)) {
		return false
	}
	switch hostOnly(strings.ToLower(r.Host)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// hasNonLoopbackForwardedHop 检查代理转发头（RFC 7239 Forwarded 的 for=、
// X-Forwarded-For 各跳、X-Real-IP）中是否出现非回环来源。这些头可被伪造，
// 但伪造只会让请求更难享受免密（fail-safe），不会放大权限；逐跳全查可挡住
// 「攻击者自带 X-Forwarded-For: 127.0.0.1 前缀 + 代理追加真实 IP」的绕过。
func hasNonLoopbackForwardedHop(r *http.Request) bool {
	if forwarded := r.Header.Get("Forwarded"); forwarded != "" {
		for _, field := range strings.FieldsFunc(forwarded, func(c rune) bool { return c == ',' || c == ';' }) {
			field = strings.TrimSpace(field)
			if len(field) <= 4 || !strings.EqualFold(field[:4], "for=") {
				continue
			}
			// for= 值可能带引号（IPv6/混淆形式）；无法识别的值按非回环处理。
			if hop := forwardedHopIP(field[4:]); hop == "" || !isLoopbackIP(hop) {
				return true
			}
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, hop := range strings.Split(xff, ",") {
			hop = forwardedHopIP(hop)
			if hop == "" || !isLoopbackIP(hop) {
				return true
			}
		}
	}
	if realIP := forwardedHopIP(r.Header.Get("X-Real-IP")); realIP != "" && !isLoopbackIP(realIP) {
		return true
	}
	return false
}

// forwardedHopIP 规范化转发头里的来源值：去空白与引号、去方括号；仅当值里
// 恰有一个冒号（"IPv4:port" 形式）才剥端口，避免把裸 IPv6（如 "::1"）切坏。
func forwardedHopIP(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if strings.HasPrefix(value, "[") {
		if i := strings.Index(value, "]"); i >= 0 {
			return value[1:i]
		}
		return value
	}
	if strings.Count(value, ":") == 1 {
		return value[:strings.Index(value, ":")]
	}
	return value
}

func isLoopbackIP(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

// hostOnly 从 "host:port" / "[::1]:port" 中取出主机部分。
func hostOnly(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if strings.HasPrefix(hostport, "[") {
		if i := strings.Index(hostport, "]"); i >= 0 {
			return hostport[1:i]
		}
		return hostport
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i]
	}
	return hostport
}
