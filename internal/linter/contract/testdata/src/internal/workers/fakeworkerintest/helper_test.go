package fakeworkerintest

import "context"

type fakeWorker struct{}

func (w *fakeWorker) Name() string { return "fake" }

func (w *fakeWorker) Start(ctx context.Context) error { return nil }
