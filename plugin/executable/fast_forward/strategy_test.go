package fastforward

import (
	"context"
	"errors"
	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/bundled_upstream"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"go.uber.org/zap"
	"testing"
)

type stubUpstream struct {
	addr  string
	fail  bool
	calls int
}

func (s *stubUpstream) Address() string { return s.addr }
func (s *stubUpstream) Trusted() bool   { return true }
func (s *stubUpstream) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	s.calls++
	q.Compress = true
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.fail {
		return nil, errors.New("offline")
	}
	r := new(dns.Msg)
	r.SetReply(q)
	return r, nil
}
func TestGroupStrategiesAndSelectedUpstream(t *testing.T) {
	for _, strategy := range []string{"fallback", "round_robin"} {
		a := &stubUpstream{addr: "first", fail: strategy == "fallback"}
		b := &stubUpstream{addr: "second"}
		f := &fastForward{BP: coremain.NewBP("group", PluginType, zap.NewNop(), nil), args: &Args{Strategy: strategy}, upstreamWrappers: []bundled_upstream.Upstream{a, b}}
		for i := 0; i < 2; i++ {
			q := new(dns.Msg)
			q.SetQuestion("example.com.", dns.TypeA)
			qc := query_context.NewContext(q, nil)
			ctx, trace := query_context.WithAudit(context.Background())
			if err := f.exec(ctx, qc); err != nil {
				t.Fatal(err)
			}
			events := trace.Finish()
			want := "second"
			if strategy == "round_robin" && i == 0 {
				want = "first"
			}
			if len(events) != 2 || events[0].Tag != "group" || events[1].Detail != want {
				t.Fatalf("%s: %+v", strategy, events)
			}
			if q.Compress {
				t.Fatal("upstream mutated original query")
			}
		}
		if strategy == "fallback" && (a.calls != 2 || b.calls != 2) {
			t.Fatal("fallback order")
		}
		if strategy == "round_robin" && (a.calls != 1 || b.calls != 1) {
			t.Fatal("rotation")
		}
	}
}
func TestGroupRespectsParentCancellation(t *testing.T) {
	s := &stubUpstream{addr: "first"}
	f := &fastForward{BP: coremain.NewBP("group", PluginType, zap.NewNop(), nil), args: &Args{Strategy: "fallback"}, upstreamWrappers: []bundled_upstream.Upstream{s}}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.exec(ctx, query_context.NewContext(q, nil)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
