package compat

import "sync"

// PreferenceStore tracks preferred envelope per server/tool for current process lifetime.
type PreferenceStore struct {
	mu   sync.RWMutex
	data map[string]EnvelopeName
}

func NewPreferenceStore() *PreferenceStore {
	return &PreferenceStore{data: make(map[string]EnvelopeName)}
}

func preferenceKey(serverName, toolName string) string {
	return serverName + "::" + toolName
}

func (s *PreferenceStore) Get(serverName, toolName string) EnvelopeName {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[preferenceKey(serverName, toolName)]
}

func (s *PreferenceStore) Set(serverName, toolName string, envelope EnvelopeName) {
	if serverName == "" || toolName == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := preferenceKey(serverName, toolName)
	if envelope == EnvelopeDirect {
		delete(s.data, key)
		return
	}
	s.data[key] = envelope
}
