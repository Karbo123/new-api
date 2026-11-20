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

// 回归测试：池子 404 重试的亲和扰动必须在位置 0 前置——上游池按上下文「前缀」
// 做账号亲和（多轮对话尾部不断追加新消息，稳定的头部才是会话身份），尾部追加
// 不改变前缀、绑定关系原封不动（2026-11-21 用户指出）。头前置对全部可行方案
// 有效：全量哈希（任何变更）、固定窗口前缀哈希（位置 0 必在窗口内）、最长公共
// 前缀路由（匹配长度归零）。此测试锁住 responses（ZCode 主流量）与 chat
// completions 分支的扰动行为：头部前置 n 个零宽字符；Instructions 缺失时置为
// 扰动串
func TestPcPerturbResponsesInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	t.Run("string instructions prepended at head", func(t *testing.T) {
		req := &dto.OpenAIResponsesRequest{Instructions: []byte(`"system prompt"`)}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 1)
		var s string
		if err := json.Unmarshal(req.Instructions, &s); err != nil {
			t.Fatalf("instructions not a JSON string: %v (%s)", err, string(req.Instructions))
		}
		if s != "\u200bsystem prompt" {
			t.Fatalf("got %q", s)
		}
	})

	t.Run("n chars all at head", func(t *testing.T) {
		req := &dto.OpenAIResponsesRequest{Instructions: []byte(`"sys"`)}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 3)
		var s string
		_ = json.Unmarshal(req.Instructions, &s)
		if !strings.HasPrefix(s, "\u200b\u200b\u200b") || strings.HasSuffix(s, "\u200b\u200b\u200bsys") == false && s != "\u200b\u200b\u200bsys" {
			t.Fatalf("want head-only padding, got %q", s)
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

	t.Run("chat completions first message prepended", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "system", Content: "be nice"}}}
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 2)
		if s, ok := req.Messages[0].Content.(string); !ok || s != "\u200b\u200bbe nice" {
			t.Fatalf("got %v", req.Messages[0].Content)
		}
	})

	t.Run("claude system string prepended", func(t *testing.T) {
		req := &dto.ClaudeRequest{}
		req.SetStringSystem("you are claude")
		info := &common.RelayInfo{Request: req}
		pcPerturbPrefixForStickyPool(c, info, 1)
		if s := req.GetStringSystem(); s != "\u200byou are claude" {
			t.Fatalf("got %q", s)
		}
	})
}
