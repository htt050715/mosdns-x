package coremain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	"github.com/prometheus/client_golang/prometheus"
)

func testPanel(capacity int) *webPanel {
	return newWebPanel(&Config{API: APIConfig{AuditCapacity: capacity}}, prometheus.NewRegistry())
}

func panelRequest(p *webPanel, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	p.protect(http.HandlerFunc(p.serveAPI)).ServeHTTP(w, r)
	return w
}

func TestPanelRetentionPauseAndConcurrentStats(t *testing.T) {
	p := testPanel(3)
	for i := 0; i < 5; i++ {
		p.record(panelLog{TraceID: string(rune('0' + i)), DurationMS: 2, Answers: []panelAnswer{}})
	}
	logs := p.snapshot()
	if len(logs) != 3 || logs[0].TraceID != "4" || logs[2].TraceID != "2" {
		t.Fatalf("ring ordering: %+v", logs)
	}
	panelRequest(p, "POST", "/api/v1/audit/stop", "{}")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				p.record(panelLog{DurationMS: 2})
				p.snapshot()
			}
		}()
	}
	wg.Wait()
	if p.total != 1005 || p.duration != 2010 || len(p.snapshot()) != 3 {
		t.Fatal("paused logging must continue lifetime stats")
	}
	panelRequest(p, "POST", "/api/v1/audit/clear", "{}")
	if len(p.snapshot()) != 0 || p.total != 1005 {
		t.Fatal("clear must only clear retained records")
	}
	panelRequest(p, "POST", "/api/v1/audit/start", "{}")
	p.record(panelLog{TraceID: "new"})
	if p.snapshot()[0].TraceID != "new" {
		t.Fatal("resume failed")
	}
}

func TestPanelLogFiltersAndPagination(t *testing.T) {
	p := testPanel(5)
	for _, name := range []string{"example.com", "sub.example.com", "other.test"} {
		p.record(panelLog{QueryName: name, ClientIP: "127.0.0.1"})
	}
	w := panelRequest(p, "GET", "/api/v2/audit/logs?q=EXAMPLE.COM&exact=true", "")
	var result struct {
		Logs       []panelLog `json:"logs"`
		Pagination struct {
			Total int `json:"total_items"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 1 || result.Logs[0].QueryName != "example.com" {
		t.Fatal(w.Body.String())
	}
	w = panelRequest(p, "GET", "/api/v2/audit/logs?page=2&limit=1&q=example.com", "")
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Pagination.Total != 2 || result.Logs[0].QueryName != "example.com" {
		t.Fatal(w.Body.String())
	}
	w = panelRequest(p, "GET", "/api/v2/audit/logs?client_ip=10.0.0.1&page=999999999", "")
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Logs) != 0 {
		t.Fatal(w.Body.String())
	}
}

type panelTestEntry struct{}

func (panelTestEntry) Exec(_ context.Context, q *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	r := new(dns.Msg)
	r.SetReply(q.Q())
	rr, _ := dns.NewRR("example.com. 60 IN A 192.0.2.1")
	r.Answer = []dns.RR{rr}
	q.SetResponse(r)
	return nil
}

func TestPanelCapturesFinalResponseAndMalformedQuery(t *testing.T) {
	p := testPanel(4)
	entry, err := D.NewEntryHandler(D.EntryHandlerOpts{Entry: panelTestEntry{}, RecursionAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &auditedHandler{next: entry, panel: p, entry: "main"}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	r, err := h.ServeDNS(context.Background(), q, query_context.NewRequestMeta(netip.MustParseAddr("192.0.2.2")))
	if err != nil || r.Id != q.Id || r.Rcode != dns.RcodeSuccess {
		t.Fatal("DNS behavior changed")
	}
	log := p.snapshot()[0]
	if log.QueryName != "example.com" || log.ClientIP != "192.0.2.2" || log.Entry != "main" || len(log.Answers) != 1 || !strings.Contains(log.Answers[0].Data, "192.0.2.1") {
		t.Fatalf("incomplete log: %+v", log)
	}
	r, _ = h.ServeDNS(context.Background(), new(dns.Msg), nil)
	if r.Rcode != dns.RcodeFormatError || p.snapshot()[0].ResponseCode != "FORMERR" {
		t.Fatal("must capture actual malformed response")
	}
}

func TestPanelConfigValidationBackupConflictAndDisabled(t *testing.T) {
	tag := "panel_test_entry"
	RegNewPersetPluginFunc(tag, func(*BP) (Plugin, error) { return nil, nil })
	defer func() { presetPluginFuncReg.Lock(); delete(presetPluginFuncReg.m, tag); presetPluginFuncReg.Unlock() }()
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "servers:\n  - exec: panel_test_entry\n    listeners:\n      - addr: 127.0.0.1:15353\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	p := testPanel(5)
	p.cfg.configFile = path
	if w := panelRequest(p, "GET", "/api/v1/config", ""); w.Code != 403 {
		t.Fatal("editor must default to disabled")
	}
	p.cfg.API.AllowConfigWrite = true
	send := func(text, hash string, validate bool) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"text": text, "sha256": hash, "validate_only": validate})
		return panelRequest(p, "POST", "/api/v1/config", string(b))
	}
	hash := configDigest([]byte(original))
	if w := send("servers: [", hash, false); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	if w := send(strings.ReplaceAll(original, tag, "unknown"), hash, false); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	updated := original + "api:\n  http: 127.0.0.1:9099\n  webui: true\n"
	if w := send(updated, hash, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b, _ := os.ReadFile(path)
	if string(b) != original {
		t.Fatal("validation changed config")
	}
	if w := send(updated, hash, false); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b, _ = os.ReadFile(path)
	if string(b) != updated {
		t.Fatal("save failed")
	}
	backups, _ := filepath.Glob(path + ".panel-backup-*")
	if len(backups) != 1 {
		t.Fatal("missing backup")
	}
	b, _ = os.ReadFile(backups[0])
	if string(b) != original {
		t.Fatal("incorrect backup")
	}
	if w := send(original, hash, false); w.Code != 409 {
		t.Fatal("stale hash must not overwrite newer config")
	}
	if w := panelRequest(p, "POST", "/api/v1/config", `{"text":"x","sha256":"x"} {}`); w.Code != 400 {
		t.Fatal("trailing body accepted")
	}
}

func TestPanelHTTPAssetsAndOriginProtection(t *testing.T) {
	p := testPanel(2)
	mux := http.NewServeMux()
	p.register(mux)
	for _, path := range []string{"/", "/api/v2/audit/stats"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/audit/clear", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://unrelated.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	r.Header.Del("Origin")
	r.Header.Del("Content-Type")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("form mutation accepted")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 {
		t.Fatal("unknown routes must not return the dashboard")
	}
	value := redactPanelValue(map[string]any{"s5_password": "private", "addr": "https://user:pass@dns.example/query", "nested": []any{map[string]any{"access_key_secret": "private"}}}, "")
	b, _ := json.Marshal(value)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "pass@") {
		t.Fatal("runtime config leaked credentials")
	}
	p.record(panelLog{QueryTime: time.Now(), DurationMS: 10})
	w = panelRequest(p, "GET", "/api/v2/audit/stats/windows", "")
	if !strings.Contains(w.Body.String(), `"complete":false`) {
		t.Fatal("bounded history must not claim complete historical statistics")
	}
}

func TestPanelBoundsAnswerDisplayWithoutChangingDNS(t *testing.T) {
	p := testPanel(2)
	r := new(dns.Msg)
	for i := 0; i < 10; i++ {
		r.Answer = append(r.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60}, Txt: []string{strings.Repeat("x", 3000)}})
	}
	h := &auditedHandler{next: &D.DummyServerHandler{WantMsg: r}, panel: p}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeTXT)
	resp, err := h.ServeDNS(context.Background(), q, nil)
	if err != nil || len(resp.Answer) != 10 || len(resp.Answer[0].(*dns.TXT).Txt[0]) != 3000 {
		t.Fatal("display cap changed DNS response")
	}
	log := p.snapshot()[0]
	size := 0
	for _, a := range log.Answers {
		size += len(a.Data)
	}
	if !log.AnswersTruncated || size > 8192 {
		t.Fatalf("log not bounded: %d", size)
	}
}
