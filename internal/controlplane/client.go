package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL       = ""
	defaultTimeout       = 10 * time.Second
	userTokenServicePath = "/controlplane.v1.UserModelTokenService"
	llmAccessServicePath = "/controlplane.v1.LLMAccessService"
)

var ErrNotConfigured = fmt.Errorf("control-plane client is not configured")

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Config struct {
	BaseURL string
	Timeout time.Duration
}

type RPCError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return fmt.Sprintf("control-plane request failed with status %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("control-plane %s (%d): %s", e.Code, e.StatusCode, e.Message)
}

func IsCode(err error, code string) bool {
	rpcErr, ok := err.(*RPCError)
	return ok && rpcErr.Code == code
}

func LoadConfigFromEnv() Config {
	cfg := Config{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("RELIANT_CONTROLPLANE_URL")), "/"),
		Timeout: defaultTimeout,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if raw := strings.TrimSpace(os.Getenv("RELIANT_CONTROLPLANE_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.Timeout = d
		} else if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			cfg.Timeout = time.Duration(secs) * time.Second
		}
	}
	return cfg
}

func NewClientFromEnv() *Client {
	return NewClient(LoadConfigFromEnv())
}

func NewClient(cfg Config) *Client {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

type CreateUserTokenRequest struct {
	Name             string `json:"name,omitempty"`
	DaemonID         string `json:"daemonId,omitempty"`
	Ephemeral        bool   `json:"ephemeral"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
}

type DaemonToken struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TokenPrefix string `json:"tokenPrefix"`
	Ephemeral   bool   `json:"ephemeral"`
	CreatedAt   string `json:"createdAt"`
	LastUsedAt  string `json:"lastUsedAt"`
	ExpiresAt   string `json:"expiresAt"`
	RevokedAt   string `json:"revokedAt"`
}

type CreateUserTokenResponse struct {
	Token       string      `json:"token"`
	DaemonToken DaemonToken `json:"daemonToken"`
}

type ListUserTokensResponse struct {
	DaemonTokens []DaemonToken `json:"daemonTokens"`
	Tokens       []DaemonToken `json:"tokens"`
}

type RevokeUserTokenRequest struct {
	TokenID string `json:"tokenId"`
}

type GetCurrentLLMAccessRequest struct {
	AllowAnonymous bool `json:"allowAnonymous"`
}

type GetCurrentLLMAccessResponse struct {
	Key                 string   `json:"key"`
	KeyAlias            string   `json:"keyAlias"`
	UserID              string   `json:"userId"`
	OwnerID             string   `json:"ownerId"`
	OwnerType           string   `json:"ownerType"`
	PlanID              string   `json:"planId"`
	PlanCode            string   `json:"planCode"`
	AllowedModels       []string `json:"allowedModels"`
	RequestTags         []string `json:"requestTags"`
	Spend               float64  `json:"spend"`
	HardBudgetUSD       float64  `json:"hardBudgetUsd"`
	BudgetDuration      string   `json:"budgetDuration"`
	RPMLimit            int64    `json:"rpmLimit"`
	TPMLimit            int64    `json:"tpmLimit"`
	MaxParallelRequests int64    `json:"maxParallelRequests"`
	KeyDuration         string   `json:"keyDuration"`
}

type connectErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

func (c *Client) CreateUserToken(ctx context.Context, userJWT string, req CreateUserTokenRequest) (*CreateUserTokenResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var resp CreateUserTokenResponse
	if err := c.postJSON(ctx, userTokenServicePath+"/CreateUserToken", userJWT, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ListUserTokens(ctx context.Context, userJWT string) ([]DaemonToken, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var resp ListUserTokensResponse
	if err := c.postJSON(ctx, userTokenServicePath+"/ListUserTokens", userJWT, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	if len(resp.DaemonTokens) > 0 {
		return resp.DaemonTokens, nil
	}
	return resp.Tokens, nil
}

func (c *Client) RevokeUserToken(ctx context.Context, userJWT string, tokenID string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	return c.postJSON(ctx, userTokenServicePath+"/RevokeUserToken", userJWT, RevokeUserTokenRequest{TokenID: tokenID}, nil)
}

func (c *Client) GetCurrentLLMAccess(ctx context.Context, token string) (*GetCurrentLLMAccessResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var resp GetCurrentLLMAccessResponse
	if err := c.postJSON(ctx, llmAccessServicePath+"/GetCurrentLLMAccess", token, GetCurrentLLMAccessRequest{AllowAnonymous: false}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) postJSON(ctx context.Context, path string, bearerToken string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var bodyErr connectErrorBody
		_ = json.Unmarshal(respBody, &bodyErr)
		msg := strings.TrimSpace(bodyErr.Message)
		if msg == "" {
			msg = strings.TrimSpace(bodyErr.Error)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}
		return &RPCError{StatusCode: resp.StatusCode, Code: strings.TrimSpace(bodyErr.Code), Message: msg}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
