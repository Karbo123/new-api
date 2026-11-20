package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// 回归测试：池子 404 重试的前缀扰动曾只改原始 body 的 instructions 字段——
// relay 各 Helper 用解析后的 info.Request 结构体组装上游请求，从不重读 body，
// 扰动对全部格式都是无效操作（粘死耗尽账号的 404 永远不变）。后补齐了
// ClaudeRequest / GeneralOpenAIRequest / OpenAIResponsesRequest 三个结构体分支，
// 此测试锁住 responses（ZCode 主流量）与 chat completions 分支的扰动行为：
// 尾部追加 n 个零宽字符，n≥2 时头部也加；Instructions 缺失时置为扰动串
func TestPcPerturbResponsesInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	t.Run("string instructions appended", func(t *testing.T) {
		req := &dto.OpenAIResponsesRequest{Instructions: []byte(`"system prompt"`)}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 1)
		var s string
		if err := json.Unmarshal(req.Instructions, &s); err != nil {
			t.Fatalf("instructions not a JSON string: %v (%s)", err, string(req.Instructions))
		}
		if s != "system prompt\u200b" {
			t.Fatalf("got %q", s)
		}
	})

	t.Run("n>=2 pads head too", func(t *testing.T) {
		req := &dto.OpenAIResponsesRequest{Instructions: []byte(`"sys"`)}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 3)
		var s string
		_ = json.Unmarshal(req.Instructions, &s)
		if !strings.HasPrefix(s, "\u200b\u200b") || !strings.HasSuffix(s, "\u200b\u200b\u200b") {
			t.Fatalf("want head+tail padding, got %q", s)
		}
	})

	t.Run("missing instructions set to pad", func(t *testing.T) {
		req := &dto.OpenAIResponsesRequest{}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 1)
		var s string
		if err := json.Unmarshal(req.Instructions, &s); err != nil || s != "\u200b" {
			t.Fatalf("got %s err=%v", string(req.Instructions), err)
		}
	})

	t.Run("chat completions first message appended", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "system", Content: "be nice"}}}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 2)
		if s, ok := req.Messages[0].Content.(string); !ok || s != "\u200bbe nice\u200b\u200b" {
			t.Fatalf("got %v", req.Messages[0].Content)
		}
	})
}
