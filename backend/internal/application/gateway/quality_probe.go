package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

const qualityProbeMaxStreamBytes = 4 << 20

type qualityProbeChatEvent struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens            int64 `json:"prompt_tokens"`
		CompletionTokens        int64 `json:"completion_tokens"`
		TotalTokens             int64 `json:"total_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (s *Service) ProbeEgressQuality(ctx context.Context, nodeID uint64, input egressapp.QualityProbeInput) (egressapp.QualityProbeResult, error) {
	key, err := s.clientKeys.Get(ctx, input.ClientKeyID)
	if err != nil {
		return egressapp.QualityProbeResult{}, fmt.Errorf("读取质量探测 Client Key: %w", err)
	}
	if !key.IsAvailable(time.Now().UTC()) {
		return egressapp.QualityProbeResult{}, fmt.Errorf("质量探测 Client Key 已禁用或过期")
	}
	requestIDPart, err := security.NewOpaqueToken(12)
	if err != nil {
		return egressapp.QualityProbeResult{}, err
	}
	requestID := "quality_" + requestIDPart
	body, err := json.Marshal(map[string]any{
		"model":          input.Model,
		"messages":       []map[string]string{{"role": "user", "content": input.Prompt}},
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"max_tokens":     input.MaxOutputTokens,
	})
	if err != nil {
		return egressapp.QualityProbeResult{}, err
	}

	startedAt := time.Now()
	publicModel, ok := qualityProbeBuildPublicModel(input.Model)
	if !ok {
		return egressapp.QualityProbeResult{}, fmt.Errorf("%w: 质量探测模型必须属于 Grok Build", egressapp.ErrInvalidInput)
	}
	probeCtx := infraegress.WithQualityProbe(ctx)
	result, err := s.CreateChatCompletion(probeCtx, Input{
		RequestID: requestID, ClientKey: key, PublicModel: publicModel, Body: body,
		Streaming: true, Operation: audit.OperationChat, ForcedEgressNodeID: nodeID,
		ForcedAccountID: input.AccountID,
	})
	if err != nil {
		return egressapp.QualityProbeResult{}, normalizeQualityProbeRequestError(err)
	}
	defer result.Body.Close()

	usage := Usage{}
	responseID := ""
	errorCode := ""
	defer func() { result.Finalize(usage, responseID, errorCode) }()
	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		errorCode = "quality_probe_upstream_error"
		body, _ := io.ReadAll(io.LimitReader(result.Body, 32<<10))
		return egressapp.QualityProbeResult{}, fmt.Errorf("质量探测上游返回 %d: %s", result.StatusCode, strings.TrimSpace(string(body)))
	}

	var firstGeneratedAt time.Time
	var visible strings.Builder
	chunkCount := 0
	totalBytes := 0
	terminal := false
	scanner := bufio.NewScanner(result.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		totalBytes += len(line) + 1
		if totalBytes > qualityProbeMaxStreamBytes {
			errorCode = "quality_probe_response_too_large"
			return egressapp.QualityProbeResult{}, fmt.Errorf("质量探测响应超过 %d MiB", qualityProbeMaxStreamBytes>>20)
		}
		line = []byte(strings.TrimSpace(string(line)))
		if !strings.HasPrefix(string(line), "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(string(line), "data:"))
		if payload == "[DONE]" {
			terminal = true
			break
		}
		var event qualityProbeChatEvent
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		if responseID == "" {
			responseID = event.ID
		}
		if event.Usage != nil {
			usage.InputTokens = event.Usage.PromptTokens
			usage.OutputTokens = event.Usage.CompletionTokens
			usage.ReasoningTokens = event.Usage.CompletionTokensDetails.ReasoningTokens
			usage.TotalTokens = event.Usage.TotalTokens
			usage.ResponseModel = event.Model
		}
		for _, choice := range event.Choices {
			delta := choice.Delta
			generated := delta.Content != "" || delta.Reasoning != "" || delta.ReasoningContent != ""
			if generated && firstGeneratedAt.IsZero() {
				firstGeneratedAt = time.Now()
				if result.MarkFirstToken != nil {
					result.MarkFirstToken()
				}
			}
			if delta.Content != "" {
				visible.WriteString(delta.Content)
				chunkCount++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		errorCode = "quality_probe_stream_interrupted"
		return egressapp.QualityProbeResult{}, fmt.Errorf("读取质量探测流: %w", err)
	}
	if !terminal {
		errorCode = "quality_probe_stream_incomplete"
		return egressapp.QualityProbeResult{}, errors.New("质量探测流未正常结束")
	}

	completedAt := time.Now()
	text := visible.String()
	visibleCharacters := utf8.RuneCountInString(text)
	visibleTokens := usage.OutputTokens - usage.ReasoningTokens
	if visibleTokens <= 0 && visibleCharacters > 0 {
		visibleTokens = int64((visibleCharacters + 3) / 4)
	}
	var firstTokenMS int64
	if !firstGeneratedAt.IsZero() {
		firstTokenMS = firstGeneratedAt.Sub(startedAt).Milliseconds()
	}
	durationMS := completedAt.Sub(startedAt).Milliseconds()
	var generationMS int64
	if !firstGeneratedAt.IsZero() {
		generationMS = durationMS - firstTokenMS
		if generationMS < 1 {
			generationMS = 1
		}
	}
	var outputTokensPerSecond, visibleTokensPerSecond float64
	if !firstGeneratedAt.IsZero() {
		outputTokensPerSecond = qualityProbeTokensPerSecond(usage.OutputTokens, durationMS)
		visibleTokensPerSecond = qualityProbeTokensPerSecond(visibleTokens, generationMS)
	}
	digest := sha256.Sum256([]byte(text))
	return egressapp.QualityProbeResult{
		RequestID: requestID, NodeID: nodeID, AccountID: result.AccountID, Model: input.Model, StatusCode: result.StatusCode,
		FirstTokenMS: firstTokenMS, DurationMS: durationMS, GenerationMS: generationMS,
		ChunkCount: chunkCount, OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens,
		VisibleTokens: visibleTokens, VisibleCharacters: visibleCharacters,
		OutputTokensPerSecond: outputTokensPerSecond, VisibleTokensPerSecond: visibleTokensPerSecond,
		ExpectedMatched: strings.Contains(text, input.Expected), ResponseSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func (s *Service) QuarantineQualityAccount(ctx context.Context, accountID uint64, model string, cooldown time.Duration) error {
	publicModel, ok := qualityProbeBuildPublicModel(model)
	if !ok || accountID == 0 {
		return fmt.Errorf("%w: 质量账号或模型无效", egressapp.ErrInvalidInput)
	}
	route, err := s.models.GetByPublicID(ctx, publicModel)
	if err != nil {
		return fmt.Errorf("读取质量探测模型: %w", err)
	}
	if route.Provider != accountdomain.ProviderBuild {
		return fmt.Errorf("%w: 质量账号冷却仅支持 Grok Build", egressapp.ErrInvalidInput)
	}
	credential, err := s.selector.accounts.Get(ctx, accountID)
	if err != nil {
		return fmt.Errorf("读取质量探测账号: %w", err)
	}
	if credential.Provider != accountdomain.ProviderBuild {
		return fmt.Errorf("%w: 质量账号不属于 Grok Build", egressapp.ErrInvalidInput)
	}
	return s.selector.MarkModelQualityDegraded(ctx, credential, route.UpstreamModel, cooldown)
}

func (s *Service) SetQualityNodeSuspended(nodeID uint64, suspended bool) {
	s.selector.SetQualityNodeSuspended(nodeID, suspended)
}

func qualityProbeBuildPublicModel(value string) (string, bool) {
	return modeldomain.NormalizePublicID(accountdomain.ProviderBuild, value)
}

func normalizeQualityProbeRequestError(err error) error {
	if errors.Is(err, ErrNoAvailableAccount) {
		return egressapp.ErrQualityProbeNoAccount
	}
	return err
}

func qualityProbeTokensPerSecond(tokens, durationMS int64) float64 {
	if tokens <= 0 || durationMS <= 0 {
		return 0
	}
	return float64(tokens) * 1000 / float64(durationMS)
}
