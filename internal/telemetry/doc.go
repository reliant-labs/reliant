// Copyright (c) 2025 Reliant Labs

// Package telemetry is what a server tells forge's observability stack beyond
// its ordinary logs, spans and metrics: whose request a record belongs to
// (BindUser), and the bug reports agents file (FileBugReport). Errors need
// nothing from it: every ERROR record reaches ClickStack through the log
// pipeline, where the control plane's issues worker groups it.
package telemetry
