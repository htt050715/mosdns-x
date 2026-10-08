package query_context

import (
	"context"
	"sync"
)

type AuditEvent struct {
	Kind   string `json:"kind"`
	Tag    string `json:"tag,omitempty"`
	Detail string `json:"detail,omitempty"`
}
type auditKey struct{}
type AuditTrace struct {
	mu     sync.Mutex
	events []AuditEvent
	closed bool
}

func WithAudit(ctx context.Context) (context.Context, *AuditTrace) {
	t := new(AuditTrace)
	return context.WithValue(ctx, auditKey{}, t), t
}
func RecordAudit(ctx context.Context, kind, tag, detail string) {
	t, _ := ctx.Value(auditKey{}).(*AuditTrace)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed && len(t.events) < 80 {
		if len(detail) > 2048 {
			detail = detail[:2048] + "…"
		}
		t.events = append(t.events, AuditEvent{kind, tag, detail})
	}
}
func (t *AuditTrace) Finish() []AuditEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return append([]AuditEvent{}, t.events...)
}
