package gateway

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestQualityModelHasCipherOnlyReasoning(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"grok-4.20-multi-agent-0309", "grok-4.20-multi-agent-xhigh", "Console/grok-4.20-multi-agent-0309"} {
		if !qualityModelHasCipherOnlyReasoning("", model) {
			t.Fatalf("%q must be cipher-only", model)
		}
	}
	for _, model := range []string{"grok-4.6", "grok-4.20-0309-reasoning", ""} {
		if qualityModelHasCipherOnlyReasoning(model) {
			t.Fatalf("%q must not be cipher-only", model)
		}
	}
}

func TestClassifyQualityHoldCipherOnlyReasoning(t *testing.T) {
	t.Parallel()
	// Shape captured from a healthy grok-4.20-multi-agent Console stream:
	// 57 KB encrypted_content, then the whole answer inside one second.
	fastFlush := QualityStreamSignals{HasThinking: true, VisibleTokens: 40, EncryptedBytes: 57129, EncryptedFloor: 256, FirstVisible: true, VisibleFlushMS: 0}
	tests := []struct {
		name string
		sig  QualityStreamSignals
		want QualityVerdict
	}{
		{name: "other models keep fake-enc withhold", sig: fastFlush, want: QualityWithhold},
		{name: "multi-agent ciphertext then fast flush delivers", sig: withCipherOnly(fastFlush), want: QualityDeliver},
		{
			name: "multi-agent without ciphertext still withholds",
			sig:  QualityStreamSignals{VisibleTokens: 50, Terminal: true, CipherOnlyReasoning: true},
			want: QualityWithhold,
		},
		{
			name: "multi-agent stub below floor still withholds",
			sig:  QualityStreamSignals{VisibleTokens: 50, ReasoningTokens: 100, EncryptedBytes: 20, EncryptedFloor: 400, FirstVisible: true, VisibleFlushMS: 10, Terminal: true, CipherOnlyReasoning: true},
			want: QualityWithhold,
		},
	}
	for _, tt := range tests {
		if got := ClassifyQualityHold(tt.sig, 8); got != tt.want {
			t.Fatalf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func withCipherOnly(sig QualityStreamSignals) QualityStreamSignals {
	sig.CipherOnlyReasoning = true
	return sig
}

func TestPeekQualityStreamMultiAgentChatDelivers(t *testing.T) {
	t.Parallel()
	stream := sse(
		`data: {"id":"c1","model":"grok-4.20-multi-agent-0309","choices":[{"delta":{"role":"assistant"},"index":0}]}`,
	) + ": grok2api-reasoning-evidence 57129\n\n" + sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"太阳光由多种颜色的光组成，"},"index":0}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"蓝光波长较短，被大气分子散射得更强烈，"},"index":0}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"所以天空看起来是蓝色的。"},"index":0}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":2689,"completion_tokens":13875,"completion_tokens_details":{"reasoning_tokens":10573}}}`,
		`data: [DONE]`,
	)
	for _, tt := range []struct {
		cipherOnly bool
		want       QualityVerdict
	}{{cipherOnly: false, want: QualityWithhold}, {cipherOnly: true, want: QualityDeliver}} {
		cfg := QualityRetryRuntime{MinOutputTokens: 8, HoldTimeout: 2 * time.Second, CipherOnlyReasoning: tt.cipherOnly}
		replay, verdict, _, _, err := peekQualityStream(context.Background(), io.NopCloser(strings.NewReader(stream)), qualityProtocolChat, cfg)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(replay)
		_ = replay.Close()
		if verdict != tt.want {
			t.Fatalf("cipherOnly=%v verdict = %s, want %s", tt.cipherOnly, verdict, tt.want)
		}
		if tt.want == QualityDeliver && string(body) != stream {
			t.Fatalf("delivered replay must be byte-identical")
		}
	}
}
