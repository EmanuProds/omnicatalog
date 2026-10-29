package omniroute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"omnicatalog/internal/config"
)

// Client is the OmniRoute API client
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Provider represents an OmniRoute provider
type Provider struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Status      string                 `json:"status"`
	FreeTier    bool                   `json:"free_tier"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
	LastChecked *time.Time             `json:"last_checked,omitempty"`
}

// Model represents an OmniRoute model (matches API response)
type Model struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Created           int64             `json:"created"`
	OwnedBy           string            `json:"owned_by"`
	ContextLength     int               `json:"context_length"`
	MaxInputTokens    int               `json:"max_input_tokens"`
	MaxOutputTokens   int               `json:"max_output_tokens"`
	Pricing           *ModelPricing     `json:"pricing,omitempty"`
	Capabilities      ModelCapabilities `json:"capabilities,omitempty"`
	Description       string            `json:"description,omitempty"`
	InputModalities   []string          `json:"input_modalities,omitempty"`
	OutputModalities  []string          `json:"output_modalities,omitempty"`
}

// SupportsTools returns true if model supports tool calling
func (m *Model) SupportsTools() bool {
	return m.Capabilities.ToolCalling
}

// SupportsVision returns true if model supports vision
func (m *Model) SupportsVision() bool {
	return m.Capabilities.Vision
}

// SupportsReasoning returns true if model supports reasoning
func (m *Model) SupportsReasoning() bool {
	return m.Capabilities.Reasoning
}

// ContextWindow returns the context window (alias for ContextLength)
func (m *Model) ContextWindow() int {
	return m.ContextLength
}

// ModelPricing represents model pricing
type ModelPricing struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// ModelCapabilities represents model capabilities
type ModelCapabilities struct {
	Vision       bool `json:"vision"`
	ToolCalling  bool `json:"tool_calling"`
	Reasoning    bool `json:"reasoning"`
	Thinking     bool `json:"thinking"`
	Attachment   bool `json:"attachment"`
	Temperature  bool `json:"temperature"`
}

// ModelsResponse represents the models list response
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// ModelsQueryRequest represents a models query request
type ModelsQueryRequest struct {
	Capability string `json:"capability,omitempty"`
	MinContext int    `json:"min_context,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Query      string `json:"q,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	After      string `json:"after,omitempty"`
}

// ModelsQueryResponse represents a models query response
type ModelsQueryResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Combo represents an OmniRoute routing combo
type Combo struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	Strategy             string   `json:"strategy"`
	Targets              []ComboTarget `json:"targets"`
	Enabled              bool     `json:"enabled"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}

// ComboTarget represents a target model in a combo
type ComboTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
}

// ComboRequest represents a combo create/update request
type ComboRequest struct {
	Name        string        `json:"name"`
	Strategy    string        `json:"strategy"`
	Targets     []ComboTarget `json:"targets"`
	Enabled     bool          `json:"enabled"`
	Description string        `json:"description,omitempty"`
}

// CatalogModel represents a model in the full catalog
type CatalogModel struct {
	ID                string         `json:"id"`
	Provider          string         `json:"provider"`
	Name              string         `json:"name"`
	ContextWindow     int            `json:"context_window"`
	MaxOutputTokens   int            `json:"max_output_tokens"`
	SupportsTools     bool           `json:"supports_tools"`
	SupportsVision    bool           `json:"supports_vision"`
	SupportsReasoning bool           `json:"supports_reasoning"`
	Pricing           *ModelPricing  `json:"pricing,omitempty"`
	Capabilities      ModelCapabilities `json:"capabilities,omitempty"`
	Free              bool           `json:"free"`
	Description       string         `json:"description,omitempty"`
}

// CatalogResponse represents the full catalog response
type CatalogResponse struct {
	Object string         `json:"object"`
	Data   []CatalogModel `json:"data"`
}

// NewClient creates a new OmniRoute client
func NewClient(cfg *config.Config) *Client {
	return &Client{
		baseURL: cfg.OmniRoute.BaseURL,
		apiKey:  cfg.OmniRoute.APIKey,
		httpClient: &http.Client{
			Timeout: cfg.OmniRoute.Timeout,
		},
	}
}

// doRequest executes an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// providerConnection is a single OmniRoute provider connection
type providerConnection struct {
	ID                 string         `json:"id"`
	Provider           string         `json:"provider"`
	Name               string         `json:"name"`
	IsActive           bool           `json:"isActive"`
	TestStatus         string         `json:"testStatus"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
}

// GetProviders lists configured providers, aggregated by provider slug
func (c *Client) GetProviders(ctx context.Context) ([]Provider, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/providers", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get providers: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Object      string               `json:"object"`
		Data        []Provider           `json:"data"`
		Connections []providerConnection `json:"connections"`
		Total       int                  `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode providers response: %w", err)
	}

	// Direct (legacy) shape
	if len(result.Connections) == 0 {
		return result.Data, nil
	}

	// Aggregate connections by provider slug
	by := make(map[string]*Provider)
	order := make([]string, 0)
	for _, conn := range result.Connections {
		slug := conn.Provider
		if slug == "" {
			slug = conn.ID
		}
		if slug == "" {
			continue
		}
		p, ok := by[slug]
		if !ok {
			p = &Provider{
				ID:       slug,
				Name:     slug,
				Status:   "disconnected",
				FreeTier: true,
				Metadata: map[string]any{"connections": []string{}},
			}
			by[slug] = p
			order = append(order, slug)
		}
		if conn.IsActive {
			p.Status = "connected"
		}
		// connections that do not exclusively import free models make provider not guaranteed free
		if v, ok := conn.ProviderSpecificData["importFreeModelsOnly"].(bool); ok && !v {
			p.FreeTier = false
		}
		conns := p.Metadata["connections"].([]string)
		conns = append(conns, conn.ID)
		p.Metadata["connections"] = conns
	}

	providers := make([]Provider, 0, len(order))
	for _, slug := range order {
		providers = append(providers, *by[slug])
	}
	return providers, nil
}

// GetModels lists all available models
func (c *Client) GetModels(ctx context.Context) ([]Model, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get models: %s - %s", resp.Status, string(body))
	}

	var result ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode models response: %w", err)
	}

	return result.Data, nil
}

// QueryModels filters models by capability
func (c *Client) QueryModels(ctx context.Context, req ModelsQueryRequest) ([]Model, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/models/query", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to query models: %s - %s", resp.Status, string(body))
	}

	var result ModelsQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode query response: %w", err)
	}

	return result.Data, nil
}

// GetCombos lists existing combos
func (c *Client) GetCombos(ctx context.Context) ([]Combo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/combos", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get combos: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Object string  `json:"object"`
		Data   []Combo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode combos response: %w", err)
	}

	return result.Data, nil
}

// GetCombo gets a specific combo by ID
func (c *Client) GetCombo(ctx context.Context, id string) (*Combo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/combos/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get combo: %s - %s", resp.Status, string(body))
	}

	var combo Combo
	if err := json.NewDecoder(resp.Body).Decode(&combo); err != nil {
		return nil, fmt.Errorf("failed to decode combo response: %w", err)
	}

	return &combo, nil
}

// CreateCombo creates a new combo
func (c *Client) CreateCombo(ctx context.Context, req ComboRequest) (*Combo, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/combos", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create combo: %s - %s", resp.Status, string(body))
	}

	var combo Combo
	if err := json.NewDecoder(resp.Body).Decode(&combo); err != nil {
		return nil, fmt.Errorf("failed to decode combo response: %w", err)
	}

	return &combo, nil
}

// UpdateCombo updates an existing combo
func (c *Client) UpdateCombo(ctx context.Context, id string, req ComboRequest) (*Combo, error) {
	resp, err := c.doRequest(ctx, http.MethodPut, "/api/combos/"+id, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to update combo: %s - %s", resp.Status, string(body))
	}

	var combo Combo
	if err := json.NewDecoder(resp.Body).Decode(&combo); err != nil {
		return nil, fmt.Errorf("failed to decode combo response: %w", err)
	}

	return &combo, nil
}

// DeleteCombo deletes a combo
func (c *Client) DeleteCombo(ctx context.Context, id string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/api/combos/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete combo: %s - %s", resp.Status, string(body))
	}

	return nil
}

// GetCatalog gets the full model catalog
func (c *Client) GetCatalog(ctx context.Context) ([]CatalogModel, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/models/catalog", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get catalog: %s - %s", resp.Status, string(body))
	}

	var result CatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode catalog response: %w", err)
	}

	return result.Data, nil
}

// TestModel tests a model with a simple request
func (c *Client) TestModel(ctx context.Context, modelID string) error {
	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": "Hello"},
		},
		"max_tokens": 10,
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/chat/completions", reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("model test failed: %s - %s", resp.Status, string(body))
	}

	return nil
}

// TestModelToolCalling tests a model's tool calling capability
func (c *Client) TestModelToolCalling(ctx context.Context, modelID string) (bool, error) {
	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": "Use a tool to list files in current directory"},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "list_files",
					"description": "List files in a directory",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "Directory path",
							},
						},
						"required": []string{"path"},
					},
				},
			},
		},
		"tool_choice": "auto",
		"max_tokens":  100,
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/chat/completions", reqBody)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("tool calling test failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("failed to decode tool calling response: %w", err)
	}

	// Check if model made a tool call
	for _, choice := range result.Choices {
		for _, tc := range choice.Message.ToolCalls {
			if tc.Function.Name == "list_files" {
				return true, nil
			}
		}
	}

	return false, nil
}

// MeasureTTFT measures Time To First Token for a streaming request
func (c *Client) MeasureTTFT(ctx context.Context, body map[string]any) (int, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Read the first chunk of the streaming response
	reader := bufio.NewReader(resp.Body)
	_, err = reader.ReadBytes('\n')
	if err != nil {
		return 0, fmt.Errorf("failed to read first chunk: %w", err)
	}

	return int(time.Since(start).Milliseconds()), nil
}

// MeasureThroughput measures tokens per second for a streaming request
func (c *Client) MeasureThroughput(ctx context.Context, body map[string]any, expectedTokens int) (float64, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Count tokens in the streaming response
	reader := bufio.NewReader(resp.Body)
	tokenCount := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		// Parse SSE data for token count
		if bytes.HasPrefix(line, []byte("data: ")) {
			data := bytes.TrimPrefix(line, []byte("data: "))
			data = bytes.TrimSpace(data)
			if string(data) == "[DONE]" {
				break
			}
			var chunk map[string]any
			if json.Unmarshal(data, &chunk) == nil {
				if choices, ok := chunk["choices"].([]any); ok {
					for _, choice := range choices {
						if choiceMap, ok := choice.(map[string]any); ok {
							if delta, ok := choiceMap["delta"].(map[string]any); ok {
								if content, ok := delta["content"].(string); ok && content != "" {
									tokenCount += len(content) / 4 // rough estimate: 4 chars per token
								}
							}
						}
					}
				}
			}
		}
	}

	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, fmt.Errorf("elapsed time too small")
	}

	return float64(tokenCount) / elapsed, nil
}

// MeasureToolLatency measures tool call latency
func (c *Client) MeasureToolLatency(ctx context.Context, body map[string]any) (int, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check if tool was called
	for _, choice := range result.Choices {
		for _, tc := range choice.Message.ToolCalls {
			if tc.Function.Name == "list_files" {
				return int(time.Since(start).Milliseconds()), nil
			}
		}
	}

	return int(time.Since(start).Milliseconds()), fmt.Errorf("tool not called")
}
func (c *Client) TestModelWithResponse(ctx context.Context, body map[string]any) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}