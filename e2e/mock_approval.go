// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// MockApprovalResponder automatically responds to approval requests for testing.
// It can be configured to auto-approve, auto-deny, or respond based on patterns.
//
// Usage:
//
//	mock := NewMockApprovalResponder(repo)
//	mock.AutoApprove() // Approve all requests
//	mock.DenyAll("Testing denial flow")
//	mock.OnTitle("Delete files", ApprovalResponse{Approved: false, Message: "Too dangerous"})
type MockApprovalResponder struct {
	mu sync.RWMutex

	repo db.Repository

	// AutoApproveAll approves all requests immediately when true
	autoApprove bool

	// AutoDenyAll denies all requests immediately when true
	autoDeny       bool
	autoDenyReason string

	// Responses maps patterns to specific responses
	responses []mockApprovalPattern

	// Calls records all approval requests handled
	Calls []MockApprovalCall

	// DefaultResponse for unmatched patterns (only used if not auto-approve/deny)
	DefaultResponse *ApprovalResponse

	// Polling configuration
	pollInterval time.Duration
	running      bool
	stopChan     chan struct{}

	// Watched chat IDs for polling
	watchedChats map[string]bool
}

// mockApprovalPattern holds a pattern and its response
type mockApprovalPattern struct {
	titlePattern *regexp.Regexp
	titleGlob    string
	chatID       string // If set, only matches this chat
	response     ApprovalResponse
}

// ApprovalResponse represents how to respond to an approval request
type ApprovalResponse struct {
	Approved bool
	Message  string        // Optional message/reason
	Delay    time.Duration // Simulate user think time before responding
	Data     map[string]interface{}
}

// MockApprovalCall records an approval request that was handled
type MockApprovalCall struct {
	ApprovalID string
	ChatID     string
	Title      string
	Status     string // "approved", "denied"
	Timestamp  time.Time
	MatchedBy  string // Which pattern matched
}

// NewMockApprovalResponder creates a new mock approval responder
func NewMockApprovalResponder(repo db.Repository) *MockApprovalResponder {
	return &MockApprovalResponder{
		repo:         repo,
		Calls:        make([]MockApprovalCall, 0),
		watchedChats: make(map[string]bool),
		pollInterval: 100 * time.Millisecond,
		DefaultResponse: &ApprovalResponse{
			Approved: true, // Default to approve
		},
	}
}

// AutoApprove sets the responder to automatically approve all requests
func (m *MockApprovalResponder) AutoApprove() *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.autoApprove = true
	m.autoDeny = false
	return m
}

// DenyAll sets the responder to automatically deny all requests
func (m *MockApprovalResponder) DenyAll(reason string) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.autoDeny = true
	m.autoApprove = false
	m.autoDenyReason = reason
	return m
}

// OnTitle registers a response for approvals matching a title pattern (glob-style)
func (m *MockApprovalResponder) OnTitle(titlePattern string, response ApprovalResponse) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockApprovalPattern{
		titleGlob: titlePattern,
		response:  response,
	})
	return m
}

// OnTitleRegex registers a response for approvals matching a title regex
func (m *MockApprovalResponder) OnTitleRegex(pattern string, response ApprovalResponse) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	re := regexp.MustCompile(pattern)
	m.responses = append(m.responses, mockApprovalPattern{
		titlePattern: re,
		response:     response,
	})
	return m
}

// OnChat registers a response for approvals from a specific chat
func (m *MockApprovalResponder) OnChat(chatID string, response ApprovalResponse) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = append(m.responses, mockApprovalPattern{
		chatID:   chatID,
		response: response,
	})
	return m
}

// SetDefault sets the default response for unmatched approvals
func (m *MockApprovalResponder) SetDefault(response ApprovalResponse) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.DefaultResponse = &response
	return m
}

// SetPollInterval sets the polling interval for checking pending approvals
func (m *MockApprovalResponder) SetPollInterval(interval time.Duration) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pollInterval = interval
	return m
}

// WatchChat adds a chat ID to the watch list for approval polling
func (m *MockApprovalResponder) WatchChat(chatID string) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.watchedChats[chatID] = true
	return m
}

// UnwatchChat removes a chat ID from the watch list
func (m *MockApprovalResponder) UnwatchChat(chatID string) *MockApprovalResponder {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.watchedChats, chatID)
	return m
}

// Start begins the approval responder background polling
func (m *MockApprovalResponder) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.stopChan = make(chan struct{})
	m.mu.Unlock()

	go m.pollLoop(ctx)
}

// Stop stops the background polling
func (m *MockApprovalResponder) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}
	m.running = false
	close(m.stopChan)
}

// pollLoop continuously checks for pending approvals and responds to them
func (m *MockApprovalResponder) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.checkAndRespondToApprovals(ctx)
		}
	}
}

// checkAndRespondToApprovals finds pending approvals and responds to them
func (m *MockApprovalResponder) checkAndRespondToApprovals(ctx context.Context) {
	// Get all chats with pending approvals by checking all watched chat IDs
	m.mu.RLock()
	chatIDs := make([]string, 0, len(m.watchedChats))
	for chatID := range m.watchedChats {
		chatIDs = append(chatIDs, chatID)
	}
	m.mu.RUnlock()

	for _, chatID := range chatIDs {
		approvals, err := m.repo.ListPendingApprovalsByChat(ctx, chatID)
		if err != nil {
			continue
		}

		for _, approval := range approvals {
			response := m.getResponse(approval)
			if response == nil {
				continue
			}

			// Apply delay if configured
			if response.Delay > 0 {
				time.Sleep(response.Delay)
			}

			// Respond to the approval
			m.respondToApproval(ctx, approval, response)
		}
	}
}

// getResponse determines the response for an approval
func (m *MockApprovalResponder) getResponse(approval *db.Approval) *ApprovalResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check auto-approve first
	if m.autoApprove {
		return &ApprovalResponse{Approved: true}
	}

	// Check auto-deny
	if m.autoDeny {
		return &ApprovalResponse{Approved: false, Message: m.autoDenyReason}
	}

	// Check patterns
	for _, pattern := range m.responses {
		if m.matchesPattern(&pattern, approval) {
			return &pattern.response
		}
	}

	// Return default
	return m.DefaultResponse
}

// matchesPattern checks if an approval matches a pattern
func (m *MockApprovalResponder) matchesPattern(pattern *mockApprovalPattern, approval *db.Approval) bool {
	// Check chat ID
	if pattern.chatID != "" && pattern.chatID != approval.ChatID {
		return false
	}

	// Check title regex
	if pattern.titlePattern != nil {
		if !pattern.titlePattern.MatchString(approval.Title) {
			return false
		}
	}

	// Check title glob
	if pattern.titleGlob != "" {
		if !globMatch(pattern.titleGlob, approval.Title) {
			return false
		}
	}

	return true
}

// respondToApproval updates the approval in the database
func (m *MockApprovalResponder) respondToApproval(ctx context.Context, approval *db.Approval, response *ApprovalResponse) {
	status := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED)
	var reason *string
	if !response.Approved {
		status = int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED)
		if response.Message != "" {
			reason = &response.Message
		}
	}

	// Update the approval in the database
	// Pass nil for actionTaken and metadata
	err := m.repo.UpdateApprovalStatus(ctx, approval.ID, status, reason, nil, nil)
	if err != nil {
		return
	}

	// Record the call
	m.mu.Lock()
	m.Calls = append(m.Calls, MockApprovalCall{
		ApprovalID: approval.ID,
		ChatID:     approval.ChatID,
		Title:      approval.Title,
		Status:     approvalStatusToString(status),
		Timestamp:  time.Now(),
	})
	m.mu.Unlock()
}

// Reset clears all recorded calls and responses
func (m *MockApprovalResponder) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = make([]MockApprovalCall, 0)
	m.responses = nil
	m.autoApprove = false
	m.autoDeny = false
	m.autoDenyReason = ""
}

// CallCount returns the number of approvals handled
func (m *MockApprovalResponder) CallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Calls)
}

// GetCalls returns a copy of all recorded calls
func (m *MockApprovalResponder) GetCalls() []MockApprovalCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]MockApprovalCall, len(m.Calls))
	copy(result, m.Calls)
	return result
}

// WasApproved checks if any approval with the given title was approved
func (m *MockApprovalResponder) WasApproved(titlePattern string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, call := range m.Calls {
		if globMatch(titlePattern, call.Title) && call.Status == "approved" {
			return true
		}
	}
	return false
}

// WasDenied checks if any approval with the given title was denied
func (m *MockApprovalResponder) WasDenied(titlePattern string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, call := range m.Calls {
		if globMatch(titlePattern, call.Title) && call.Status == "denied" {
			return true
		}
	}
	return false
}

// ApprovalCountFor returns the count of approvals handled for a title pattern
func (m *MockApprovalResponder) ApprovalCountFor(titlePattern string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, call := range m.Calls {
		if globMatch(titlePattern, call.Title) {
			count++
		}
	}
	return count
}

// RespondManually allows manually responding to a specific approval
// This is useful when you want fine-grained control in tests
func (m *MockApprovalResponder) RespondManually(ctx context.Context, approvalID string, approved bool, message string) error {
	status := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED)
	var reason *string
	if !approved {
		status = int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED)
		if message != "" {
			reason = &message
		}
	}

	// Pass nil for actionTaken and metadata
	err := m.repo.UpdateApprovalStatus(ctx, approvalID, status, reason, nil, nil)
	if err != nil {
		return err
	}

	// Get approval details for recording
	approval, err := m.repo.GetApproval(ctx, approvalID)
	if err != nil {
		return nil // Still succeeded, just can't record
	}

	// Record the call
	m.mu.Lock()
	m.Calls = append(m.Calls, MockApprovalCall{
		ApprovalID: approval.ID,
		ChatID:     approval.ChatID,
		Title:      approval.Title,
		Status:     approvalStatusToString(status),
		Timestamp:  time.Now(),
		MatchedBy:  "manual",
	})
	m.mu.Unlock()

	return nil
}

// AssertApprovalHandled is a test helper that fails if no approval with the title was handled
func (m *MockApprovalResponder) AssertApprovalHandled(t interface {
	Helper()
	Fatalf(string, ...interface{})
}, titlePattern string) {
	t.Helper()
	if m.ApprovalCountFor(titlePattern) == 0 {
		t.Fatalf("expected approval with title matching %q to be handled, but none were. Handled approvals: %v", titlePattern, m.getHandledTitles())
	}
}

// approvalStatusToString converts an int32 approval status to a display string.
func approvalStatusToString(status int32) string {
	switch reliantv1.ApprovalStatus(status) {
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED:
		return "approved"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED:
		return "denied"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING:
		return "pending"
	default:
		return "unknown"
	}
}

// getHandledTitles returns a list of handled approval titles
func (m *MockApprovalResponder) getHandledTitles() []string {
	var titles []string
	for _, call := range m.Calls {
		titles = append(titles, call.Title)
	}
	return titles
}
