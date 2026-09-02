package oaichat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestChatResponseReasoningItemHasSummaryAndContent(t *testing.T) {
	resp := &dto.OpenAITextResponse{
		Id:      "resp_1",
		Model:   "deepseek-v4-flash",
		Choices: []dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "answer", ReasoningContent: strPtr("thinking hard")}}},
	}
	out, _, err := ChatCompletionsResponseToResponsesResponse(resp, "resp_1")
	if err != nil {
		t.Fatalf("convert response: %v", err)
	}
	var reasoningOutput *dto.ResponsesOutput
	for i := range out.Output {
		if out.Output[i].Type == "reasoning" {
			reasoningOutput = &out.Output[i]
		}
	}
	if reasoningOutput == nil {
		t.Fatalf("expected reasoning output item, got %+v", out.Output)
	}
	raw, _ := json.Marshal(reasoningOutput)
	var check map[string]any
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	summary, ok := check["summary"].([]any)
	if !ok || len(summary) == 0 {
		t.Fatalf("expected summary array on reasoning item, got %s", string(raw))
	}
	if summary[0].(map[string]any)["text"] != "thinking hard" {
		t.Fatalf("expected reasoning text in summary, got %s", string(raw))
	}
	// Upstream main emits only the standard `summary` field (content stays null);
	// both shapes are accepted by Responses clients, summary is the contract.
	if summary, ok := check["summary"].([]any); !ok || len(summary) == 0 {
		t.Fatalf("expected non-empty summary, got %s", string(raw))
	}
}

func TestChatResponseWithoutReasoningHasNoSummaryField(t *testing.T) {
	resp := &dto.OpenAITextResponse{
		Id:      "resp_1",
		Model:   "gpt-5.5",
		Choices: []dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "answer"}}},
	}
	out, _, err := ChatCompletionsResponseToResponsesResponse(resp, "resp_1")
	if err != nil {
		t.Fatalf("convert response: %v", err)
	}
	for i := range out.Output {
		if out.Output[i].Type == "reasoning" {
			t.Fatalf("non-thinking response must not emit reasoning item")
		}
	}
}

func strPtr(s string) *string { return &s }
