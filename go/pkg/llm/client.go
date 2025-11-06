package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/shared"
)

const (
	maxResponseLogLen = 200
	maxPromptLogLen   = 100
)

// LLMClient defines the supported client behaviours.
type LLMClient interface {
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamResponse, error)
	ChatStructured(ctx context.Context, req *ChatRequest, target interface{}) (*ChatResponse, error)
	GetConfig() *Config
	Close() error
}

// Client interacts with ZenMux-exposed LLMs via the OpenAI SDK.
type Client struct {
	config       *Config
	openaiClient *openai.Client
	logger       Logger
	retryHandler *RetryHandler
	httpClient   *http.Client
	// defaultRouting is applied when using zenmux/auto with no explicit Routing provided
	defaultRouting *RoutingConfig
}

// ClientOption configures optional client behaviour.
type ClientOption func(*clientOptions)

type clientOptions struct {
	logger       Logger
	retry        *RetryHandler
	httpClient   *http.Client
	openaiClient *openai.Client
}

// WithLogger injects a custom logger implementation.
func WithLogger(logger Logger) ClientOption {
	return func(opts *clientOptions) {
		opts.logger = logger
	}
}

// WithRetryHandler injects a custom retry handler.
func WithRetryHandler(handler *RetryHandler) ClientOption {
	return func(opts *clientOptions) {
		opts.retry = handler
	}
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(opts *clientOptions) {
		opts.httpClient = client
	}
}

// WithOpenAIClient injects a pre-configured OpenAI client (primarily for testing).
func WithOpenAIClient(client *openai.Client) ClientOption {
	return func(opts *clientOptions) {
		opts.openaiClient = client
	}
}

// NewClient constructs a new LLM client using the provided configuration.
func NewClient(cfg *Config, opts ...ClientOption) (*Client, error) {
	if cfg == nil {
		return nil, errors.New("llm: config cannot be nil")
	}

	clientCfg := cfg.Clone()
	if clientCfg == nil {
		return nil, errors.New("llm: failed to copy config")
	}
	if err := clientCfg.Validate(); err != nil {
		return nil, err
	}

	optState := clientOptions{}
	for _, opt := range opts {
		opt(&optState)
	}

	logger := optState.logger
	if logger == nil {
		logger = NewLogger(clientCfg.LogLevel)
	}

	var retryHandler *RetryHandler
	if optState.retry != nil {
		retryHandler = optState.retry
	} else {
		retryHandler = NewRetryHandler(RetryConfig{
			MaxRetries: clientCfg.MaxRetries,
		})
	}

	var oaClient *openai.Client
	if optState.openaiClient != nil {
		oaClient = optState.openaiClient
	} else {
		oaOpts := []option.RequestOption{
			option.WithAPIKey(clientCfg.APIKey),
			option.WithBaseURL(clientCfg.BaseURL),
		}
		if clientCfg.Timeout > 0 {
			oaOpts = append(oaOpts, option.WithRequestTimeout(clientCfg.Timeout))
		}
		if optState.httpClient != nil {
			oaOpts = append(oaOpts, option.WithHTTPClient(optState.httpClient))
		}
		clientVal := openai.NewClient(oaOpts...)
		oaClient = &clientVal
	}

	c := &Client{
		config:       clientCfg,
		openaiClient: oaClient,
		logger:       logger,
		retryHandler: retryHandler,
		httpClient:   optState.httpClient,
	}

	// NOTE: zenmux/auto routing is currently unstable (returns HTTP 500).
	// This code is retained for future use when the API is fixed.
	// For now, test mode uses a fixed low-cost model instead.
	if strings.EqualFold(clientCfg.DefaultModel, "zenmux/auto") {
		if clientCfg.RoutingDefaults != nil && len(clientCfg.RoutingDefaults.AvailableModels) > 0 {
			c.defaultRouting = clientCfg.RoutingDefaults
		} else {
			cutoff := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
			if time.Now().UTC().Before(cutoff) {
				c.defaultRouting = &RoutingConfig{
					AvailableModels: []string{
						"kuaishou/kat-coder-pro-v1",
						"minimax/minimax-m2",
					},
					Preference: "balanced",
				}
			} else {
				c.defaultRouting = &RoutingConfig{
					AvailableModels: []string{
						"openai/gpt-5-nano",
						"google/gemini-2.5-flash-lite",
						"x-ai/grok-4-fast",
						"qwen/qwen3-235b-a22b-2507",
						"deepseek/deepseek-chat-v3.1",
					},
					Preference: "balanced",
				}
			}
		}
	}

	return c, nil
}

// Chat performs a single synchronous completion request.
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, errors.New("llm: request cannot be nil")
	}
	params, modelID, err := c.buildChatParams(req)
	if err != nil {
		return nil, err
	}

	c.logger.Info(ctx, "llm chat request", Fields{
		"model":    modelID,
		"messages": len(req.Messages),
		"prompt":   summarizeMessages(req.Messages),
	})

	// If using Zenmux auto-routing, fall back to raw JSON call to support
	// `model_routing_config` which is not modeled in the OpenAI SDK types.
	if strings.EqualFold(modelID, "zenmux/auto") || req.Routing != nil {
		// Ensure routing provided
		reqCopy := *req
		if reqCopy.Routing == nil && c.defaultRouting != nil {
			reqCopy.Routing = c.defaultRouting
		}
		return c.chatRaw(ctx, &reqCopy, modelID)
	}

	start := time.Now()
	c.logger.Info(ctx, "llm chat request", Fields{
		"model":    modelID,
		"messages": len(req.Messages),
	})

	var completion *openai.ChatCompletion
	err = c.retryHandler.Do(ctx, func() error {
		resp, callErr := c.openaiClient.Chat.Completions.New(ctx, params)
		if callErr != nil {
			c.logger.Error(ctx, fmt.Errorf("chat completion failed: %w", callErr), Fields{
				"model": modelID,
			})
			return callErr
		}
		completion = resp
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := convertCompletion(completion)
	respText := ""
	if len(result.Choices) > 0 {
		respText = strings.TrimSpace(result.Choices[0].Message.Content)
		// Limit response text length in logs unless verbose logging explicitly enabled
		if len(respText) > maxResponseLogLen && !isVerboseLoggingEnabled() {
			respText = respText[:maxResponseLogLen] + "..."
		}
	}
	c.logger.Info(ctx, "llm chat success", Fields{
		"model":             modelID,
		"duration_ms":       time.Since(start).Milliseconds(),
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
		"response":          respText,
	})

	return result, nil
}

// chatRaw posts a raw JSON body to support Zenmux auto-routing extensions.
func (c *Client) chatRaw(ctx context.Context, req *ChatRequest, modelID string) (*ChatResponse, error) {
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: c.config.Timeout}
	}
	// Build messages payload preserving optional fields (name, tool_call_id)
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		}
		item := map[string]any{"role": role}
		if m.Content != "" {
			item["content"] = m.Content
		}
		switch role {
		case "function":
			if m.Name != "" {
				item["name"] = m.Name
			}
		case "tool":
			if m.ToolCallID != "" {
				item["tool_call_id"] = m.ToolCallID
			}
		default:
			if m.Name != "" {
				item["name"] = m.Name
			}
		}
		msgs = append(msgs, item)
	}

	body := map[string]any{
		"model":    modelID,
		"messages": msgs,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxCompletionTokens != nil {
		body["max_completion_tokens"] = *req.MaxCompletionTokens
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.Routing != nil {
		body["model_routing_config"] = req.Routing
	}
	if rf := req.ResponseFormat; rf != nil {
		t := strings.ToLower(strings.TrimSpace(rf.Type))
		switch t {
		case "json_schema":
			rfBody := map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   ifEmptyString(rf.Name, "schema"),
					"schema": rf.Schema,
				},
			}
			if rf.Strict != nil {
				rfBody["json_schema"].(map[string]any)["strict"] = *rf.Strict
			}
			if rf.Description != "" {
				rfBody["json_schema"].(map[string]any)["description"] = rf.Description
			}
			body["response_format"] = rfBody
		case "json_object":
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}

	// POST to <base>/chat/completions with retry/backoff
	url := strings.TrimRight(c.config.BaseURL, "/") + "/chat/completions"
	data, _ := json.Marshal(body)

	var completion *openai.ChatCompletion
	if err := c.retryHandler.Do(ctx, func() error {
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, callErr := c.httpClient.Do(httpReq)
		if callErr != nil {
			return callErr
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Wrap as openai.Error so retry policy recognizes retriable status codes
			return &openai.Error{StatusCode: resp.StatusCode}
		}
		b, _ := io.ReadAll(resp.Body)
		var parsed openai.ChatCompletion
		if err := json.Unmarshal(b, &parsed); err != nil {
			return fmt.Errorf("llm: decode completion: %w", err)
		}
		completion = &parsed
		return nil
	}); err != nil {
		// Avoid leaking openai.Error with nil Request/Response, which can panic on Error()
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("llm: http %d", apiErr.StatusCode)
		}
		return nil, err
	}
	return convertCompletion(completion), nil
}

func ifEmptyString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func summarizeMessages(msgs []Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var parts []string
	for i, m := range msgs {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(m.Content)
		if len(content) > maxPromptLogLen && !isVerboseLoggingEnabled() {
			content = content[:maxPromptLogLen] + "..."
		}
		parts = append(parts, fmt.Sprintf("[%d] role=%s content=%s", i, role, content))
	}
	return strings.Join(parts, " | ")
}

// ChatStream initiates a streaming completion call. The returned channel closes once the stream is exhausted.
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamResponse, error) {
	if req == nil {
		return nil, errors.New("llm: request cannot be nil")
	}
	streamReq := *req
	streamReq.Stream = true
	params, modelID, err := c.buildChatParams(&streamReq)
	if err != nil {
		return nil, err
	}

	stream := c.openaiClient.Chat.Completions.NewStreaming(ctx, params)
	if stream == nil {
		return nil, errors.New("llm: streaming not supported")
	}

	out := make(chan StreamResponse)
	go func(s *ssestream.Stream[openai.ChatCompletionChunk]) {
		defer close(out)
		defer s.Close()
		for s.Next() {
			chunk := s.Current()
			out <- convertChunk(chunk)
		}
		if err := s.Err(); err != nil {
			c.logger.Error(ctx, fmt.Errorf("stream failed: %w", err), Fields{"model": modelID})
		}
	}(stream)

	return out, nil
}

// ChatStructured enforces structured output using JSON schema and decodes the result into target.
func (c *Client) ChatStructured(ctx context.Context, req *ChatRequest, target interface{}) (*ChatResponse, error) {
	if target == nil {
		return nil, errors.New("llm: structured target cannot be nil")
	}

	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil, errors.New("llm: structured target must be a pointer")
	}

	schema, err := GenerateSchema(target)
	if err != nil {
		return nil, err
	}

	var strict bool = true
	format := &ResponseFormat{
		Type:        "json_schema",
		Name:        deriveSchemaName(value),
		Schema:      schema,
		Description: "Structured response",
		Strict:      &strict,
	}

	structuredReq := *req
	structuredReq.ResponseFormat = format
	resp, err := c.Chat(ctx, &structuredReq)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("llm: empty structured response")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if err := ParseStructured(content, target); err != nil {
		c.logger.Error(ctx, fmt.Errorf("parse structured response: %w", err), Fields{
			"model": resp.Model,
		})
		return nil, err
	}
	return resp, nil
}

// GetConfig returns an immutable copy of the client configuration.
func (c *Client) GetConfig() *Config {
	return c.config.Clone()
}

// Close releases resources associated with the client.
func (c *Client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
		if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	return nil
}

func (c *Client) buildChatParams(req *ChatRequest) (openai.ChatCompletionNewParams, string, error) {
	if len(req.Messages) == 0 {
		return openai.ChatCompletionNewParams{}, "", errors.New("llm: request requires at least one message")
	}

	modelAlias := strings.TrimSpace(req.Model)
	if modelAlias == "" {
		modelAlias = c.config.DefaultModel
	}

	modelCfg, ok := c.config.Model(modelAlias)
	if !ok {
		// fallback to direct model
		modelCfg = ModelConfig{ModelName: modelAlias}
	}
	modelID := ResolveModelID(modelAlias, modelCfg)

	messageParams, err := buildMessageParams(req.Messages)
	if err != nil {
		return openai.ChatCompletionNewParams{}, "", err
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(modelID),
		Messages: messageParams,
	}

	if rf, ok, err := toResponseFormatParam(req.ResponseFormat); err != nil {
		return openai.ChatCompletionNewParams{}, "", err
	} else if ok {
		params.ResponseFormat = rf
	}

	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	} else if modelCfg.Temperature != nil {
		params.Temperature = openai.Float(*modelCfg.Temperature)
	}

	if req.MaxCompletionTokens != nil {
		params.MaxCompletionTokens = openai.Int(int64(*req.MaxCompletionTokens))
	} else if modelCfg.MaxCompletionTokens != nil {
		params.MaxCompletionTokens = openai.Int(int64(*modelCfg.MaxCompletionTokens))
	}

	if req.TopP != nil {
		params.TopP = openai.Float(*req.TopP)
	} else if modelCfg.TopP != nil {
		params.TopP = openai.Float(*modelCfg.TopP)
	}

	return params, modelID, nil
}

func buildMessageParams(msgs []Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system":
			param := openai.SystemMessage(m.Content)
			if m.Name != "" && param.OfSystem != nil {
				param.OfSystem.Name = openai.String(m.Name)
			}
			result = append(result, param)
		case "developer":
			param := openai.DeveloperMessage(m.Content)
			result = append(result, param)
		case "assistant":
			param := openai.ChatCompletionMessageParamOfAssistant(m.Content)
			result = append(result, param)
		case "tool":
			param := openai.ToolMessage(m.Content, m.ToolCallID)
			result = append(result, param)
		case "function":
			param := openai.ChatCompletionMessageParamOfFunction(m.Content, m.Name)
			result = append(result, param)
		default:
			param := openai.UserMessage(m.Content)
			if m.Name != "" && param.OfUser != nil {
				param.OfUser.Name = openai.String(m.Name)
			}
			result = append(result, param)
		}
	}
	return result, nil
}

func toResponseFormatParam(format *ResponseFormat) (openai.ChatCompletionNewParamsResponseFormatUnion, bool, error) {
	var empty openai.ChatCompletionNewParamsResponseFormatUnion
	if format == nil || strings.EqualFold(format.Type, "text") || format.Type == "" {
		return empty, false, nil
	}

	switch strings.ToLower(format.Type) {
	case "json_object":
		val := shared.NewResponseFormatJSONObjectParam()
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &val,
		}, true, nil
	case "json_schema":
		schema, ok := format.Schema.(map[string]interface{})
		if !ok {
			return empty, false, fmt.Errorf("llm: json_schema requires map schema")
		}
		name := format.Name
		if name == "" {
			name = "structured_output"
		}
		jsonSchema := shared.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:   name,
			Schema: schema,
		}
		if format.Strict != nil {
			jsonSchema.Strict = openai.Bool(*format.Strict)
		}
		if desc := strings.TrimSpace(format.Description); desc != "" {
			jsonSchema.Description = openai.String(desc)
		}
		val := shared.ResponseFormatJSONSchemaParam{
			JSONSchema: jsonSchema,
		}
		val.Type = val.Type.Default()
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &val,
		}, true, nil
	default:
		return empty, false, fmt.Errorf("llm: unsupported response format %q", format.Type)
	}
}

func convertCompletion(resp *openai.ChatCompletion) *ChatResponse {
	if resp == nil {
		return nil
	}

	result := &ChatResponse{
		ID:          resp.ID,
		Model:       resp.Model,
		Created:     resp.Created,
		RawJSON:     resp.RawJSON(),
		Fingerprint: resp.SystemFingerprint,
		Usage: Usage{
			PromptTokens:     int(resp.Usage.PromptTokens),
			CompletionTokens: int(resp.Usage.CompletionTokens),
			TotalTokens:      int(resp.Usage.TotalTokens),
		},
	}

	if resp.ServiceTier != "" {
		result.Tier = string(resp.ServiceTier)
	}

	for _, choice := range resp.Choices {
		result.Choices = append(result.Choices, Choice{
			Index:        int(choice.Index),
			Message:      convertMessage(choice.Message),
			FinishReason: choice.FinishReason,
			ToolCalls:    convertToolCalls(choice.Message.ToolCalls),
		})
	}
	return result
}

func convertChunk(chunk openai.ChatCompletionChunk) StreamResponse {
	resp := StreamResponse{
		ID:      chunk.ID,
		Model:   chunk.Model,
		Created: chunk.Created,
	}
	if chunk.Usage.TotalTokens > 0 {
		resp.Usage = &Usage{
			PromptTokens:     int(chunk.Usage.PromptTokens),
			CompletionTokens: int(chunk.Usage.CompletionTokens),
			TotalTokens:      int(chunk.Usage.TotalTokens),
		}
	}
	for _, choice := range chunk.Choices {
		var toolCalls []ToolCall
		for _, call := range choice.Delta.ToolCalls {
			toolCalls = append(toolCalls, ToolCall{
				ID:   call.ID,
				Type: string(call.Type),
				Function: FunctionCall{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		resp.Choices = append(resp.Choices, StreamChoice{
			Index: int(choice.Index),
			Delta: Delta{
				Role:      choice.Delta.Role,
				Content:   choice.Delta.Content,
				ToolCalls: toolCalls,
			},
			FinishReason: choice.FinishReason,
		})
	}
	return resp
}

func convertMessage(msg openai.ChatCompletionMessage) Message {
	result := Message{
		Role:    string(msg.Role),
		Content: msg.Content,
	}
	if msg.FunctionCall.Name != "" || msg.FunctionCall.Arguments != "" {
		result.ToolCallID = msg.FunctionCall.Name
	}
	return result
}

func convertToolCalls(calls []openai.ChatCompletionMessageToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		result = append(result, ToolCall{
			ID:   call.ID,
			Type: string(call.Type),
			Function: FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return result
}

func deriveSchemaName(val reflect.Value) string {
	t := val.Type()
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return strings.ToLower(t.Name())
}
