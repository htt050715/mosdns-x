// Management adapter for the Vue dashboard from jasonxtt/mosdns (GPL-3.0).
package coremain

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/constant"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	"github.com/pmkol/mosdns-x/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"
)

//go:embed www
var panelAssets embed.FS

type panelAnswer struct {
	Type string `json:"type"`
	TTL  uint32 `json:"ttl"`
	Data string `json:"data"`
}

type panelLog struct {
	TraceID          string        `json:"trace_id"`
	QueryTime        time.Time     `json:"query_time"`
	QueryName        string        `json:"query_name"`
	QueryType        string        `json:"query_type"`
	ClientIP         string        `json:"client_ip"`
	Protocol         string        `json:"protocol"`
	Entry            string        `json:"entry"`
	DurationMS       float64       `json:"duration_ms"`
	ResponseCode     string        `json:"response_code"`
	Answers          []panelAnswer `json:"answers"`
	AnswersTruncated bool          `json:"answers_truncated,omitempty"`
	Error            string        `json:"error,omitempty"`
}

type webPanel struct {
	mu          sync.Mutex
	logs        []panelLog // Fixed-capacity ring, oldest is at next when full.
	next, count int
	capturing   bool
	total       uint64
	duration    float64
	started     time.Time
	sequence    atomic.Uint64
	cfg         *Config
	reg         *prometheus.Registry
	configMu    sync.Mutex
}

func newWebPanel(cfg *Config, reg *prometheus.Registry) *webPanel {
	capacity := cfg.API.AuditCapacity
	if capacity <= 0 {
		capacity = 1000
	}
	if capacity > 50000 {
		capacity = 50000
	}
	return &webPanel{logs: make([]panelLog, capacity), capturing: true, started: time.Now(), cfg: cfg, reg: reg}
}

type auditedHandler struct {
	next  D.Handler
	panel *webPanel
	entry string
}

func (h *auditedHandler) ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	log := panelLog{QueryTime: time.Now(), Entry: h.entry, TraceID: strconv.FormatUint(h.panel.sequence.Add(1), 10), Answers: []panelAnswer{}}
	if len(req.Question) > 0 {
		q := req.Question[0]
		log.QueryName = strings.TrimSuffix(q.Name, ".")
		log.QueryType = dns.Type(q.Qtype).String()
	}
	if meta != nil {
		if meta.GetClientAddr().IsValid() {
			log.ClientIP = meta.GetClientAddr().String()
		}
		log.Protocol = meta.GetProtocol()
	}
	resp, err := h.next.ServeDNS(ctx, req, meta)
	log.DurationMS = float64(time.Since(log.QueryTime)) / float64(time.Millisecond)
	if err != nil {
		log.Error = err.Error()
	}
	if resp != nil {
		log.ResponseCode = dns.RcodeToString[resp.Rcode]
		answerBytes := 0
		for _, rr := range resp.Answer {
			data := rr.String()
			if len(data) > 2048 {
				data = data[:2048] + "..."
				log.AnswersTruncated = true
			}
			if answerBytes+len(data) > 8192 || len(log.Answers) >= 64 {
				log.AnswersTruncated = true
				break
			}
			answerBytes += len(data)
			log.Answers = append(log.Answers, panelAnswer{Type: dns.Type(rr.Header().Rrtype).String(), TTL: rr.Header().Ttl, Data: data})
		}
	}
	h.panel.record(log)
	return resp, err
}

func (p *webPanel) record(log panelLog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total++
	p.duration += log.DurationMS
	if !p.capturing {
		return
	}
	p.logs[p.next] = log
	p.next = (p.next + 1) % len(p.logs)
	if p.count < len(p.logs) {
		p.count++
	}
}

func (p *webPanel) snapshot() []panelLog {
	p.mu.Lock()
	defer p.mu.Unlock()
	logs := make([]panelLog, 0, p.count)
	for i := 0; i < p.count; i++ {
		logs = append(logs, p.logs[(p.next-1-i+len(p.logs))%len(p.logs)])
	}
	return logs
}

func panelJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func panelError(w http.ResponseWriter, status int, err any) {
	panelJSON(w, status, map[string]any{"error": fmt.Sprint(err)})
}

func panelBody(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func (p *webPanel) register(mux *http.ServeMux) {
	assets, _ := fs.Sub(panelAssets, "www")
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	mux.Handle("/api/", p.protect(http.HandlerFunc(p.serveAPI)))
}

// Reject browser cross-origin access; writes require JSON so forms cannot invoke them.
// For remote access use an authenticated reverse proxy; this shares the existing API listener.
func (p *webPanel) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				panelError(w, 403, "cross-origin access denied")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			panelError(w, 403, "cross-site access denied")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			panelError(w, 405, "method not allowed")
			return
		}
		if r.Method == http.MethodPost {
			media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if media != "application/json" {
				panelError(w, 415, "application/json required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (p *webPanel) serveAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/v1/config" {
		p.serveConfig(w, r)
		return
	}
	if r.Method == http.MethodPost {
		p.mu.Lock()
		switch path {
		case "/api/v1/audit/start":
			p.capturing = true
		case "/api/v1/audit/stop":
			p.capturing = false
		case "/api/v1/audit/clear":
			clear(p.logs)
			p.next, p.count = 0, 0
		default:
			p.mu.Unlock()
			panelError(w, 404, "unknown API")
			return
		}
		p.mu.Unlock()
		panelJSON(w, 200, map[string]any{"ok": true})
		return
	}
	switch path {
	case "/api/v1/audit/status":
		p.mu.Lock()
		capturing := p.capturing
		p.mu.Unlock()
		panelJSON(w, 200, map[string]any{"capturing": capturing})
	case "/api/v1/audit/capacity":
		panelJSON(w, 200, map[string]any{"capacity": len(p.logs)})
	case "/api/v2/audit/stats":
		p.mu.Lock()
		total, duration := p.total, p.duration
		p.mu.Unlock()
		average := 0.0
		if total > 0 {
			average = duration / float64(total)
		}
		panelJSON(w, 200, map[string]any{"total_queries": total, "total_duration_ms": duration, "average_duration_ms": average})
	case "/api/v2/audit/stats/windows":
		logs := p.snapshot()
		now := time.Now()
		items := []map[string]any{}
		for _, hours := range []int{1, 6, 24, 72, 168} {
			start := now.Add(-time.Duration(hours) * time.Hour)
			count, duration := 0, 0.0
			for _, l := range logs {
				if !l.QueryTime.Before(start) {
					count++
					duration += l.DurationMS
				}
			}
			average := 0.0
			if count > 0 {
				average = duration / float64(count)
			}
			coverage := now
			if len(logs) > 0 {
				coverage = logs[len(logs)-1].QueryTime
			}
			items = append(items, map[string]any{"key": fmt.Sprintf("%dh", hours), "label": fmt.Sprintf("%d小时（保留日志）", hours), "window_seconds": hours * 3600, "request_count": count, "average_duration_ms": average, "complete": false, "coverage_start": coverage.Format(time.RFC3339)})
		}
		panelJSON(w, 200, map[string]any{"generated_at": now.Format(time.RFC3339), "items": items})
	case "/api/v2/audit/logs":
		p.serveLogs(w, r)
	case "/api/v1/system/info":
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		panelJSON(w, 200, map[string]any{"version": constant.Version, "build_time": constant.BuildTime, "go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "uptime_seconds": time.Since(p.started).Seconds(), "memory_bytes": mem.Alloc, "goroutines": runtime.NumGoroutine(), "config_write": p.cfg.API.AllowConfigWrite && p.cfg.configFile != "", "config_file": filepath.Base(p.cfg.configFile)})
	case "/api/v1/runtime/config":
		b, err := yaml.Marshal(p.cfg)
		if err != nil {
			panelError(w, 500, err)
			return
		}
		var value any
		if err := yaml.Unmarshal(b, &value); err != nil {
			panelError(w, 500, err)
			return
		}
		panelJSON(w, 200, redactPanelValue(value, ""))
	case "/api/v1/cache/stats":
		families, err := p.reg.Gather()
		if err != nil {
			panelError(w, 500, err)
			return
		}
		stats := map[string]map[string]float64{}
		for _, pc := range append([]PluginConfig{{Tag: "_default_cache", Type: "cache"}}, p.cfg.Plugins...) {
			if pc.Type != "cache" {
				continue
			}
			entry := map[string]float64{}
			for _, family := range families {
				for _, key := range []string{"query_total", "hit_total", "lazy_hit_total", "cache_size"} {
					if family.GetName() != "mosdns_plugin_"+pc.Tag+"_"+key {
						continue
					}
					for _, m := range family.Metric {
						entry[key] += m.GetCounter().GetValue() + m.GetGauge().GetValue()
					}
				}
			}
			stats[pc.Tag] = entry
		}
		panelJSON(w, 200, stats)
	default:
		panelError(w, 404, "unknown API")
	}
}

func panelInt(r *http.Request, key string, fallback, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v < 1 {
		return fallback
	}
	if v > max {
		return max
	}
	return v
}

func (p *webPanel) serveLogs(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	exact := r.URL.Query().Get("exact") == "true"
	clients := r.URL.Query()["client_ip"]
	filtered := []panelLog{}
	for _, log := range p.snapshot() {
		if len(clients) > 0 {
			found := false
			for _, ip := range clients {
				if log.ClientIP == ip {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if q != "" {
			fields := []string{log.QueryName, log.QueryType, log.ClientIP, log.TraceID, log.ResponseCode, log.Entry}
			for _, a := range log.Answers {
				fields = append(fields, a.Data)
			}
			found := false
			for _, field := range fields {
				field = strings.ToLower(field)
				if (exact && field == q) || (!exact && strings.Contains(field, q)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		filtered = append(filtered, log)
	}
	page, limit := panelInt(r, "page", 1, 1000000), panelInt(r, "limit", 50, 500)
	total := len(filtered)
	pages := (total + limit - 1) / limit
	start := min((page-1)*limit, total)
	end := min(start+limit, total)
	panelJSON(w, 200, map[string]any{"logs": filtered[start:end], "pagination": map[string]int{"total_items": total, "total_pages": pages, "current_page": page, "items_per_page": limit}})
}

func redactPanelValue(value any, key string) any {
	key = strings.ToLower(key)
	for _, word := range []string{"password", "secret", "token", "username", "access_key", "private_key"} {
		if strings.Contains(key, word) {
			return "***"
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			v[k] = redactPanelValue(item, k)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = redactPanelValue(item, key)
		}
		return v
	case string:
		if u, err := url.Parse(v); err == nil && u.User != nil {
			u.User = url.User("***")
			return u.String()
		}
	}
	return value
}

func configDigest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func (p *webPanel) serveConfig(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.API.AllowConfigWrite || p.cfg.configFile == "" {
		panelError(w, 403, "configuration editor disabled; set api.allow_config_write: true and restart")
		return
	}
	p.configMu.Lock()
	defer p.configMu.Unlock()
	path := p.cfg.configFile
	b, err := os.ReadFile(path)
	if err != nil {
		panelError(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		panelJSON(w, 200, map[string]any{"text": string(b), "sha256": configDigest(b), "file": filepath.Base(path)})
		return
	}
	var body struct {
		Text         string `json:"text"`
		SHA256       string `json:"sha256"`
		ValidateOnly bool   `json:"validate_only"`
	}
	if err := panelBody(w, r, &body); err != nil {
		panelError(w, 400, err)
		return
	}
	if body.SHA256 != configDigest(b) {
		panelError(w, 409, "configuration changed on disk; reload before saving")
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		panelError(w, 500, err)
		return
	}
	if !info.Mode().IsRegular() {
		panelError(w, 400, "configuration must be a regular file")
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".panel-*"+filepath.Ext(path))
	if err != nil {
		panelError(w, 500, err)
		return
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.WriteString(body.Text); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		panelError(w, 500, err)
		return
	}
	cfg, _, err := loadConfig(temp)
	if err == nil {
		err = mergeInclude(cfg, 0, []string{path})
	}
	if err == nil {
		err = validatePanelConfig(cfg)
	}
	if err != nil {
		panelError(w, 400, err)
		return
	}
	if body.ValidateOnly {
		panelJSON(w, 200, map[string]any{"valid": true, "message": "Syntax, plugin arguments and server references validated; restart is required to verify runtime resources."})
		return
	}
	// Recheck after validation: an external editor may have changed the original.
	current, err := os.ReadFile(path)
	if err != nil {
		panelError(w, 500, err)
		return
	}
	if configDigest(current) != body.SHA256 {
		panelError(w, 409, "configuration changed during validation")
		return
	}
	backup := path + ".panel-backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.WriteFile(backup, b, 0600); err != nil {
		panelError(w, 500, err)
		return
	}
	if err := os.Chmod(temp, info.Mode().Perm()); err != nil {
		panelError(w, 500, err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		panelError(w, 500, err)
		return
	}
	panelJSON(w, 200, map[string]any{"saved": true, "restart_required": true, "sha256": configDigest([]byte(body.Text)), "backup": filepath.Base(backup)})
}

func validatePanelConfig(cfg *Config) error {
	if cfg.API.WebUI && cfg.API.HTTP == "" {
		return fmt.Errorf("api.webui requires api.http")
	}
	tags := map[string]bool{}
	for tag := range LoadNewPersetPluginFuncs() {
		tags[tag] = true
	}
	seen := map[string]bool{}
	for _, pc := range cfg.Plugins {
		if pc.Tag == "" || pc.Type == "" {
			continue
		} // Match startup behavior.
		if seen[pc.Tag] {
			return fmt.Errorf("duplicate plugin tag %s", pc.Tag)
		}
		seen[pc.Tag], tags[pc.Tag] = true, true
		info, ok := GetPluginType(pc.Type)
		if !ok {
			return fmt.Errorf("unknown plugin type %s", pc.Type)
		}
		if info.NewArgs != nil && pc.Args != nil {
			if args, ok := pc.Args.(map[string]interface{}); ok {
				if err := utils.WeakDecode(args, info.NewArgs()); err != nil {
					return fmt.Errorf("plugin %s: %w", pc.Tag, err)
				}
			}
		}
	}
	if len(cfg.Servers) == 0 {
		return fmt.Errorf("no server configured")
	}
	for _, server := range cfg.Servers {
		if !tags[server.Exec] {
			return fmt.Errorf("unknown server entry %s", server.Exec)
		}
		if len(server.Listeners) == 0 {
			return fmt.Errorf("no server listener configured")
		}
		for _, l := range server.Listeners {
			if l == nil || l.Addr == "" {
				return fmt.Errorf("empty listener")
			}
			switch l.Protocol {
			case "", "udp", "tcp", "dot", "tls", "doh", "https", "http", "doq", "quic", "doh3", "h3":
			default:
				return fmt.Errorf("unknown protocol %s", l.Protocol)
			}
		}
	}
	return nil
}
