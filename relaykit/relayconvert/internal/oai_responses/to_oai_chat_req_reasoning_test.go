package oairesponses

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func responsesRequestFromInput(t *testing.T, input string) *dto.OpenAIResponsesRequest {
	t.Helper()
	req := &dto.OpenAIResponsesRequest{}
	if err := json.Unmarshal([]byte(`{"model":"deepseek-v4-flash","input":`+input+`}`), req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

func mustConvertToChat(t *testing.T, req *dto.OpenAIResponsesRequest) *dto.GeneralOpenAIRequest {
	t.Helper()
	out, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("convert request: %v", err)
	}
	return out
}

func TestReasoningItemWithSummaryAttachedToFollowingAssistant(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"let me think"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]`)
	out := mustConvertToChat(t, req)
	if len(out.Messages) != 3 {
		t.Fatalf("expected 3 messages (reasoning item must not become a message), got %d: %+v", len(out.Messages), out.Messages)
	}
	assistant := out.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("expected assistant role, got %q", assistant.Role)
	}
	if got := assistant.GetReasoningContent(); got != "let me think" {
		t.Fatalf("expected reasoning_content %q on assistant, got %q", "let me think", got)
	}
	if out.Messages[2].GetReasoningContent() != "" {
		t.Fatalf("reasoning must not leak into the following user message")
	}
}

func TestReasoningItemWithContentPartsAttached(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"raw thinking"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
	]`)
	out := mustConvertToChat(t, req)
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out.Messages))
	}
	if got := out.Messages[0].GetReasoningContent(); got != "raw thinking" {
		t.Fatalf("expected reasoning_content %q, got %q", "raw thinking", got)
	}
}

func TestReasoningItemWithDetailsAttached(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1","reasoning_details":[{"type":"reasoning.text","text":"detail thinking","format":"unknown","index":0}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
	]`)
	out := mustConvertToChat(t, req)
	if got := out.Messages[0].GetReasoningContent(); got != "detail thinking" {
		t.Fatalf("expected reasoning_content %q, got %q", "detail thinking", got)
	}
}

func TestReasoningItemDuplicateTextNotDoubled(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1",
		 "summary":[{"type":"summary_text","text":"same text"}],
		 "content":[{"type":"reasoning_text","text":"same text"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
	]`)
	out := mustConvertToChat(t, req)
	if got := out.Messages[0].GetReasoningContent(); got != "same text" {
		t.Fatalf("expected deduplicated reasoning_content %q, got %q", "same text", got)
	}
}

func TestAssistantItemInlineReasoningContent(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}],"reasoning_content":"inline thinking"}
	]`)
	out := mustConvertToChat(t, req)
	if got := out.Messages[0].GetReasoningContent(); got != "inline thinking" {
		t.Fatalf("expected reasoning_content %q, got %q", "inline thinking", got)
	}
}

func TestAssistantMessageContentNeverUsedAsReasoning(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"just the answer"}]}
	]`)
	out := mustConvertToChat(t, req)
	if got := out.Messages[0].GetReasoningContent(); got != "" {
		t.Fatalf("assistant body text must not become reasoning_content, got %q", got)
	}
}

func TestReasoningNotAttachedToUserMessage(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"orphan thinking"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next question"}]}
	]`)
	out := mustConvertToChat(t, req)
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "user" || out.Messages[0].GetReasoningContent() != "" {
		t.Fatalf("orphan reasoning must not attach to a user message")
	}
}

func TestReasoningAttachedToAssistantCreatedForFunctionCall(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"need a tool"}]},
		{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"x\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"sunny"}
	]`)
	out := mustConvertToChat(t, req)
	if len(out.Messages) != 2 {
		t.Fatalf("expected 2 messages (assistant with tool call + tool), got %d", len(out.Messages))
	}
	if got := out.Messages[0].GetReasoningContent(); got != "need a tool" {
		t.Fatalf("expected reasoning_content on tool-call assistant message, got %q", got)
	}
}

func TestConsecutiveReasoningItemsJoined(t *testing.T) {
	req := responsesRequestFromInput(t, `[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"part one"}]},
		{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"part two"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
	]`)
	out := mustConvertToChat(t, req)
	if got := out.Messages[0].GetReasoningContent(); got != "part one\n\npart two" {
		t.Fatalf("expected joined reasoning_content, got %q", got)
	}
}

func TestAssistantReasoningContentSerializedUpstream(t *testing.T) {
	msg := dto.Message{Role: "assistant", Content: "answer", ReasoningContent: kitutilGetPointer("thinking")}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var check map[string]any
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if check["reasoning_content"] != "thinking" {
		t.Fatalf("expected reasoning_content field on serialized message, got %s", string(raw))
	}
}

func kitutilGetPointer(s string) *string { return &s }
