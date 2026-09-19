package provider

import (
	"context"
	"encoding/json"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function Func   `json:"function"`
}

type Func struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolSpec struct {
	Type     string   `json:"type"`
	Function FuncSpec `json:"function"`
}

type FuncSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ToolCallDelta struct {
	Index int
	ID    string
	Name  string
	Args  string
}

type Delta struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCallDelta
	Usage        *Usage
	FinishReason string
}

type Stream interface {
	Recv() (Delta, error)
	Close() error
}

type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (Stream, error)
}

// New returns a provider for any OpenAI-compatible chat completions endpoint.
func New(name, baseURL, apiKey string) Provider {
	return NewOpenAI(name, baseURL, apiKey)
}
