package usage

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        Usage
		wantOK      bool
	}{
		{
			name:        "openai chat completion",
			contentType: "application/json",
			body: `{"id":"chatcmpl-1","model":"gpt-4o-mini-2024-07-18",
				"usage":{"prompt_tokens":10,"completion_tokens":14,"total_tokens":24}}`,
			want:   Usage{Model: "gpt-4o-mini-2024-07-18", InputTokens: 10, OutputTokens: 14},
			wantOK: true,
		},
		{
			name:        "openai responses api",
			contentType: "application/json",
			body:        `{"object":"response","model":"gpt-4.1","usage":{"input_tokens":7,"output_tokens":3}}`,
			want:        Usage{Model: "gpt-4.1", InputTokens: 7, OutputTokens: 3},
			wantOK:      true,
		},
		{
			name:        "anthropic message",
			contentType: "application/json",
			body:        `{"type":"message","model":"claude-haiku-5-5","usage":{"input_tokens":12,"output_tokens":30}}`,
			want:        Usage{Model: "claude-haiku-5-5", InputTokens: 12, OutputTokens: 30},
			wantOK:      true,
		},
		{
			name:        "openai chat stream with include_usage",
			contentType: "text/event-stream; charset=utf-8",
			body: "data: {\"model\":\"gpt-4o-mini\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}],\"usage\":null}\n\n" +
				"data: {\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n" +
				"data: [DONE]\n\n",
			want:   Usage{Model: "gpt-4o-mini", InputTokens: 5, OutputTokens: 2},
			wantOK: true,
		},
		{
			name:        "anthropic stream",
			contentType: "text/event-stream",
			body: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":25,\"output_tokens\":1}}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
				"event: message_delta\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\n\n",
			want:   Usage{Model: "claude-opus-5-5", InputTokens: 25, OutputTokens: 15},
			wantOK: true,
		},
		{
			name:        "openai responses stream",
			contentType: "text/event-stream",
			body: "event: response.created\n" +
				"data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-4.1\",\"usage\":null}}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-4.1\",\"usage\":{\"input_tokens\":9,\"output_tokens\":4}}}\n\n",
			want:   Usage{Model: "gpt-4.1", InputTokens: 9, OutputTokens: 4},
			wantOK: true,
		},
		{
			name:        "error response",
			contentType: "application/json",
			body:        `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
		},
		{
			name:        "not json",
			contentType: "text/plain",
			body:        "not json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.contentType, []byte(tt.body))
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("Parse() = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
