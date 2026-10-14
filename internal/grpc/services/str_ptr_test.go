// Copyright (c) 2025 Reliant Labs
package services

// strPtr is the inbox and trigger tests' string-pointer helper. It was
// declared in a chat test until ChatService moved to internal/handlers/chat,
// whose tests use ptr.Of; it goes with this package once its last service
// moves.
func strPtr(s string) *string { return &s }
