package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromReader(t *testing.T) {
	t.Setenv(envAPIKey, "override-key")
	t.Setenv(envTimeout, "45s")
	t.Setenv(envMaxRetries, "5")

	data := `
base_url: "https://example.com"
api_key: "${ZENMUX_API_KEY}"
default_model: "gpt-5"
timeout: "30s"
max_retries: 2
log_level: "debug"

models:
  gpt-5:
    provider: "openai"
    model_name: "openai/gpt-5"
    temperature: 0.5
    max_completion_tokens: 1024
`

	cfg, err := LoadConfigFromReader(strings.NewReader(data))
	require.NoError(t, err)

	require.Equal(t, "https://example.com", cfg.BaseURL)
	require.Equal(t, "override-key", cfg.APIKey)
	require.Equal(t, "gpt-5", cfg.DefaultModel)
	require.Equal(t, 5, cfg.MaxRetries)
	require.Equal(t, 45*time.Second, cfg.Timeout)

	model, ok := cfg.Model("gpt-5")
	require.True(t, ok)
	require.Equal(t, "openai", model.Provider)
	require.Equal(t, "openai/gpt-5", model.ModelName)
	require.NotNil(t, model.Temperature)
	require.InDelta(t, 0.5, *model.Temperature, 0.0001)
	require.NotNil(t, model.MaxCompletionTokens)
	require.Equal(t, 1024, *model.MaxCompletionTokens)
}

func TestClientChat(t *testing.T) {
	var (
		mu        sync.Mutex
		lastBody  []byte
		lastPath  string
		callCount int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		lastPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		lastBody = body

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1730366400,
			"model":"openai/gpt-5",
			"choices":[
				{
					"index":0,
					"finish_reason":"stop",
					"logprobs":null,
					"message":{
						"role":"assistant",
						"content":"Hello from test",
						"tool_calls":[]
					}
				}
			],
			"usage":{
				"prompt_tokens":10,
				"completion_tokens":12,
				"total_tokens":22
			}
		}`))
	}))
	defer server.Close()

	cfg := &Config{
		BaseURL:      server.URL,
		APIKey:       "test-key",
		DefaultModel: "gpt-5",
		Timeout:      5 * time.Second,
		MaxRetries:   1,
		LogLevel:     "error",
		Models: map[string]ModelConfig{
			"gpt-5": {
				Provider:  "openai",
				ModelName: "openai/gpt-5",
			},
		},
	}

	client, err := NewClient(cfg, WithHTTPClient(server.Client()))
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	limit := 64
	resp, err := client.Chat(ctx, &ChatRequest{
		Model: "gpt-5",
		Messages: []Message{
			{Role: "user", Content: "hi"},
		},
		MaxCompletionTokens: &limit,
	})
	require.NoError(t, err)

	require.Equal(t, "openai/gpt-5", resp.Model)
	require.Len(t, resp.Choices, 1)
	require.Equal(t, "Hello from test", resp.Choices[0].Message.Content)
	require.Equal(t, 22, resp.Usage.TotalTokens)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "/chat/completions", lastPath)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(lastBody, &payload))
	require.Equal(t, "openai/gpt-5", payload["model"])
	require.EqualValues(t, limit, payload["max_completion_tokens"])
	_, hasDeprecated := payload["max_tokens"]
	require.False(t, hasDeprecated, "max_tokens should not be sent")
	require.Equal(t, 1, callCount)
}

func TestClientChatStructured(t *testing.T) {
	var captured map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-structured",
			"object":"chat.completion",
			"created":1730366400,
			"model":"openai/gpt-5",
			"choices":[
				{
					"index":0,
					"finish_reason":"stop",
					"logprobs":null,
					"message":{
						"role":"assistant",
						"content":"{\"action\":\"BUY\",\"symbol\":\"BTC\",\"confidence\":0.92,\"reasoning\":\"Momentum bullish\"}",
						"tool_calls":[]
					}
				}
			],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":20,
				"total_tokens":32
			}
		}`))
	}))
	defer server.Close()

	cfg := &Config{
		BaseURL:      server.URL,
		APIKey:       "test-key",
		DefaultModel: "gpt-5",
		Timeout:      5 * time.Second,
		MaxRetries:   1,
		LogLevel:     "error",
		Models: map[string]ModelConfig{
			"gpt-5": {
				Provider:  "openai",
				ModelName: "openai/gpt-5",
			},
		},
	}

	client, err := NewClient(cfg, WithHTTPClient(server.Client()))
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	type Decision struct {
		Action     string  `json:"action"`
		Symbol     string  `json:"symbol"`
		Confidence float64 `json:"confidence"`
		Reasoning  string  `json:"reasoning"`
	}

	var decision Decision
	_, err = client.ChatStructured(ctx, &ChatRequest{
		Model: "gpt-5",
		Messages: []Message{
			{Role: "system", Content: "You are a trading assistant."},
			{Role: "user", Content: "Suggest BTC action."},
		},
	}, &decision)
	require.NoError(t, err)

	require.Equal(t, "BUY", decision.Action)
	require.Equal(t, "BTC", decision.Symbol)
	require.InDelta(t, 0.92, decision.Confidence, 0.0001)

	responseFormat, ok := captured["response_format"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, responseFormat, "json_schema")
}

func TestClientChatBudget(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-budget",
			"object":"chat.completion",
			"created":1730366400,
			"model":"openai/gpt-5",
			"choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"Hello","tool_calls":[]}}],
			"usage":{"prompt_tokens":10,"completion_tokens":12,"total_tokens":22}
		}`))
	}))
	defer server.Close()

	cfg := &Config{
		BaseURL:      server.URL,
		APIKey:       "test-key",
		DefaultModel: "gpt-5",
		Timeout:      5 * time.Second,
		MaxRetries:   0,
		LogLevel:     "error",
		Models: map[string]ModelConfig{
			"gpt-5": {Provider: "openai", ModelName: "openai/gpt-5"},
		},
		Budget: &BudgetConfig{
			DailyTokenLimit:   30,
			AlertThresholdPct: 50,
			StrictEnforcement: true,
		},
	}

	client, err := NewClient(cfg, WithHTTPClient(server.Client()))
	require.NoError(t, err)
	defer client.Close()

	req := &ChatRequest{
		Model: "gpt-5",
		Messages: []Message{
			{Role: "user", Content: "hi"},
		},
	}

	ctx := context.Background()
	_, err = client.Chat(ctx, req)
	require.NoError(t, err)

	_, err = client.Chat(ctx, req)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBudgetExceeded))

	mu.Lock()
	recordedCalls := callCount
	mu.Unlock()
	require.Equal(t, 2, recordedCalls)

	_, err = client.Chat(ctx, req)
	require.ErrorIs(t, err, ErrBudgetExceeded)

	mu.Lock()
	recordedCalls = callCount
	mu.Unlock()
	require.Equal(t, 2, recordedCalls, "third call should be blocked before HTTP request")
}

func TestClientOptions(t *testing.T) {
	t.Run("WithLogger", func(t *testing.T) {
		cfg := &Config{
			BaseURL:      "https://api.example.com",
			APIKey:       "test-key",
			DefaultModel: "gpt-4",
			Timeout:      5 * time.Second,
			MaxRetries:   1,
		}

		customLogger := NewLogger("debug")
		client, err := NewClient(cfg, WithLogger(customLogger))
		require.NoError(t, err)
		defer client.Close()

		require.NotNil(t, client.logger)
		require.Equal(t, customLogger, client.logger)
	})

	t.Run("WithRetryHandler", func(t *testing.T) {
		cfg := &Config{
			BaseURL:      "https://api.example.com",
			APIKey:       "test-key",
			DefaultModel: "gpt-4",
			Timeout:      5 * time.Second,
			MaxRetries:   1,
		}

		customRetry := NewRetryHandler(RetryConfig{MaxRetries: 5})
		client, err := NewClient(cfg, WithRetryHandler(customRetry))
		require.NoError(t, err)
		defer client.Close()

		require.NotNil(t, client.retryHandler)
		require.Equal(t, customRetry, client.retryHandler)
	})

	t.Run("WithHTTPClient", func(t *testing.T) {
		cfg := &Config{
			BaseURL:      "https://api.example.com",
			APIKey:       "test-key",
			DefaultModel: "gpt-4",
			Timeout:      5 * time.Second,
			MaxRetries:   1,
		}

		customHTTPClient := &http.Client{Timeout: 10 * time.Second}
		client, err := NewClient(cfg, WithHTTPClient(customHTTPClient))
		require.NoError(t, err)
		defer client.Close()

		require.NotNil(t, client.httpClient)
	})

	t.Run("multiple options", func(t *testing.T) {
		cfg := &Config{
			BaseURL:      "https://api.example.com",
			APIKey:       "test-key",
			DefaultModel: "gpt-4",
			Timeout:      5 * time.Second,
			MaxRetries:   1,
		}

		customLogger := NewLogger("debug")
		customRetry := NewRetryHandler(RetryConfig{MaxRetries: 5})
		customHTTPClient := &http.Client{Timeout: 10 * time.Second}

		client, err := NewClient(cfg,
			WithLogger(customLogger),
			WithRetryHandler(customRetry),
			WithHTTPClient(customHTTPClient),
		)
		require.NoError(t, err)
		defer client.Close()

		require.Equal(t, customLogger, client.logger)
		require.Equal(t, customRetry, client.retryHandler)
	})
}

func TestGetConfig(t *testing.T) {
	cfg := &Config{
		BaseURL:      "https://api.example.com",
		APIKey:       "test-key",
		DefaultModel: "gpt-4",
		Timeout:      5 * time.Second,
		MaxRetries:   3,
		LogLevel:     "info",
	}

	client, err := NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	returnedCfg := client.GetConfig()
	require.NotNil(t, returnedCfg)
	require.Equal(t, cfg.BaseURL, returnedCfg.BaseURL)
	require.Equal(t, cfg.APIKey, returnedCfg.APIKey)
	require.Equal(t, cfg.DefaultModel, returnedCfg.DefaultModel)
	require.Equal(t, cfg.Timeout, returnedCfg.Timeout)

	// Verify it's a clone, not the original
	require.NotSame(t, cfg, returnedCfg)
}

func TestConvertToolCalls(t *testing.T) {
	t.Run("empty tool calls", func(t *testing.T) {
		result := convertToolCalls(nil)
		require.Nil(t, result)

		result = convertToolCalls([]openai.ChatCompletionMessageToolCall{})
		require.Nil(t, result)
	})

	t.Run("single tool call", func(t *testing.T) {
		calls := []openai.ChatCompletionMessageToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: openai.ChatCompletionMessageToolCallFunction{
					Name:      "get_weather",
					Arguments: `{"location":"Tokyo"}`,
				},
			},
		}

		result := convertToolCalls(calls)
		require.Len(t, result, 1)
		require.Equal(t, "call_123", result[0].ID)
		require.Equal(t, "function", result[0].Type)
		require.Equal(t, "get_weather", result[0].Function.Name)
		require.Equal(t, `{"location":"Tokyo"}`, result[0].Function.Arguments)
	})

	t.Run("multiple tool calls", func(t *testing.T) {
		calls := []openai.ChatCompletionMessageToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: openai.ChatCompletionMessageToolCallFunction{
					Name:      "func1",
					Arguments: `{"arg1":"val1"}`,
				},
			},
			{
				ID:   "call_2",
				Type: "function",
				Function: openai.ChatCompletionMessageToolCallFunction{
					Name:      "func2",
					Arguments: `{"arg2":"val2"}`,
				},
			},
		}

		result := convertToolCalls(calls)
		require.Len(t, result, 2)
		require.Equal(t, "call_1", result[0].ID)
		require.Equal(t, "call_2", result[1].ID)
	})
}
