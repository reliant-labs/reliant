// Copyright (c) 2025 Reliant Labs
package codex

// JWT Claims for extracting account ID
type CodexJWTClaims struct {
	Audience []string         `json:"aud"`
	Subject  string           `json:"sub"`
	Exp      int64            `json:"exp"`
	Iat      int64            `json:"iat"`
	Auth     *CodexAuthClaims `json:"https://api.openai.com/auth,omitempty"`
}

// CodexAuthClaims contains Codex-specific auth claims
type CodexAuthClaims struct {
	ChatGPTAccountID     string `json:"chatgpt_account_id"`
	ChatGPTAccountUserID string `json:"chatgpt_account_user_id"`
	ChatGPTUserID        string `json:"chatgpt_user_id"`
	ChatGPTPlanType      string `json:"chatgpt_plan_type"`
}
