package query_context

import (
	"context"
	"sync"
	"testing"
)

func TestAuditCloseConcurrentBackgroundWork(t *testing.T) {
	ctx, trace := WithAudit(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				RecordAudit(ctx, "step", "test", "")
			}
		}()
	}
	wg.Wait()
	events := trace.Finish()
	if len(events) != 80 {
		t.Fatal(len(events))
	}
	RecordAudit(ctx, "upstream", "", "late")
	if len(trace.Finish()) != len(events) {
		t.Fatal("late event changed final trace")
	}
}
