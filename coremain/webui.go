// Management adapter for the Vue dashboard from jasonxtt/mosdns (GPL-3.0).
package coremain

import (
	"bytes"
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/constant"
	"github.com/pmkol/mosdns-x/pkg/data_provider"
	"github.com/pmkol/mosdns-x/pkg/hosts"
	"github.com/pmkol/mosdns-x/pkg/matcher/domain"
	"github.com/pmkol/mosdns-x/pkg/matcher/netlist"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	"github.com/pmkol/mosdns-x/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"
)

//go:embed www
var panelAssets embed.FS

type panelAnswer struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	Data  string `json:"data"`
}

type panelLog struct {
	TraceID          string                     `json:"trace_id"`
	QueryTime        time.Time                  `json:"query_time"`
	QueryName        string                     `json:"query_name"`
	QueryType        string                     `json:"query_type"`
	ClientIP         string                     `json:"client_ip"`
	Protocol         string                     `json:"protocol"`
	Entry            string                     `json:"entry"`
	DurationMS       float64                    `json:"duration_ms"`
	ResponseCode     string                     `json:"response_code"`
	Answers          []panelAnswer              `json:"answers"`
	AnswersTruncated bool                       `json:"answers_truncated,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Trace            []query_context.AuditEvent `json:"trace"`
	Upstream         string                     `json:"upstream,omitempty"`
	Group            string                     `json:"group,omitempty"`
	Cache            string                     `json:"cache,omitempty"`
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
	manager     *data_provider.DataManager
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
	ctx, trace := query_context.WithAudit(ctx)
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
	log.Trace = trace.Finish()
	for i := range log.Trace {
		e := &log.Trace[i]
		e.Detail = fmt.Sprint(redactPanelValue(e.Detail, ""))
		switch e.Kind {
		case "upstream":
			log.Upstream = e.Detail
		case "group":
			log.Group = e.Tag
		case "cache":
			log.Cache = e.Tag + " / " + e.Detail
		case "error":
			log.Error = e.Detail
		}
	}
	log.DurationMS = float64(time.Since(log.QueryTime)) / float64(time.Millisecond)
	if err != nil {
		log.Error = fmt.Sprint(redactPanelValue(err.Error(), ""))
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
			parts := strings.SplitN(data, "\t", 5)
			value := data
			if len(parts) == 5 {
				value = parts[4]
			}
			if answerBytes+len(data)+len(value)+len(rr.Header().Name) > 16384 {
				log.AnswersTruncated = true
				break
			}
			answerBytes += len(data) + len(value) + len(rr.Header().Name)
			log.Answers = append(log.Answers, panelAnswer{Name: rr.Header().Name, Value: value, Type: dns.Type(rr.Header().Rrtype).String(), TTL: rr.Header().Ttl, Data: data})
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
	limit := int64(2 << 20)
	if r.URL.Path == "/api/v1/rule-file" {
		limit = 4 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	if path == "/api/v1/rule-file" || path == "/api/v1/rule-refresh" || path == "/api/v1/rule-preview" {
		p.serveRuleFile(w, r)
		return
	}
	if path == "/api/v1/config/format" {
		if r.Method != "POST" {
			panelError(w, 405, "POST required")
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := panelBody(w, r, &body); err != nil {
			panelError(w, 400, err)
			return
		}
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(body.Text), &doc); err != nil {
			panelError(w, 400, err)
			return
		}
		var out bytes.Buffer
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			panelError(w, 400, err)
			return
		}
		_ = enc.Close()
		panelJSON(w, 200, map[string]any{"text": out.String()})
		return
	}
	if path == "/api/v1/config/apply-status" && r.Method == "GET" {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(p.cfg.configFile), ".panel-apply-status.json"))
		if err != nil {
			panelJSON(w, 200, map[string]any{"state": "idle"})
			return
		}
		var v any
		if json.Unmarshal(b, &v) != nil {
			panelError(w, 500, "invalid apply status")
			return
		}
		panelJSON(w, 200, v)
		return
	}
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
		panelJSON(w, 200, map[string]any{"version": constant.Version, "build_time": constant.BuildTime, "go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "uptime_seconds": time.Since(p.started).Seconds(), "memory_bytes": mem.Alloc, "goroutines": runtime.NumGoroutine(), "config_write": p.cfg.API.AllowConfigWrite && p.cfg.configFile != "", "config_file": filepath.Base(p.cfg.configFile), "can_apply": len(p.cfg.API.ApplyCommand) > 0, "panel_revision": "management-v2"})
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
			fields := []string{log.QueryName, log.QueryType, log.ClientIP, log.TraceID, log.ResponseCode, log.Entry, log.Upstream, log.Group, log.Cache, log.Error}
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
		Apply        bool   `json:"apply"`
	}
	if err := panelBody(w, r, &body); err != nil {
		panelError(w, 400, err)
		return
	}
	if body.ValidateOnly {
		body.Apply = false
	}
	if !body.ValidateOnly {
		status, _ := os.ReadFile(filepath.Join(filepath.Dir(path), ".panel-apply-status.json"))
		var state struct {
			State   string    `json:"state"`
			Started time.Time `json:"started"`
		}
		if json.Unmarshal(status, &state) == nil && state.State == "applying" && time.Since(state.Started) < 2*time.Minute {
			panelError(w, 409, "已有配置正在应用，请等待完成后再保存")
			return
		}
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
	if err == nil && len(p.cfg.API.ApplyCommand) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		binary, e := os.Executable()
		if e != nil {
			err = e
		} else {
			cmd := exec.CommandContext(ctx, binary, "check", "-c", temp, "-d", filepath.Dir(path))
			out, e := cmd.CombinedOutput()
			if e != nil {
				err = fmt.Errorf("运行校验失败: %s", string(out[max(0, len(out)-4000):]))
			}
		}
	}
	if body.Apply && err == nil {
		if len(p.cfg.API.ApplyCommand) == 0 {
			err = fmt.Errorf("未配置服务应用命令")
		}
		if cfg.API.HTTP != p.cfg.API.HTTP || !cfg.API.WebUI || !cfg.API.AllowConfigWrite {
			err = fmt.Errorf("应用时请保留当前面板监听地址和配置编辑开关")
		}
		if cfg.API.HTTP != "" && cfg.API.HTTP == p.cfg.API.HTTP {
			status, _ := os.ReadFile(filepath.Join(filepath.Dir(path), ".panel-apply-status.json"))
			var state struct {
				State   string    `json:"state"`
				Started time.Time `json:"started"`
			}
			if json.Unmarshal(status, &state) == nil && state.State == "applying" && time.Since(state.Started) < 2*time.Minute {
				err = fmt.Errorf("已有配置正在应用，请等待完成")
			}
		}
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
	if body.Apply {
		status := filepath.Join(filepath.Dir(path), ".panel-apply-status.json")
		state, _ := json.Marshal(map[string]any{"state": "applying", "started": time.Now().UTC(), "sha256": configDigest([]byte(body.Text))})
		if err := os.WriteFile(status, state, 0600); err != nil {
			_ = os.WriteFile(path, b, info.Mode().Perm())
			panelError(w, 500, err)
			return
		}
		args := append(append([]string{}, p.cfg.API.ApplyCommand[1:]...), path, backup, status)
		cmd := exec.Command(p.cfg.API.ApplyCommand[0], args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.WriteFile(path, b, info.Mode().Perm())
			_ = os.Remove(status)
			panelError(w, 500, fmt.Errorf("无法启动应用任务: %s", out))
			return
		}
	}
	panelJSON(w, 200, map[string]any{"saved": true, "restart_required": !body.Apply, "applying": body.Apply, "sha256": configDigest([]byte(body.Text)), "backup": filepath.Base(backup)})
}

func (p *webPanel) serveRuleFile(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.API.AllowConfigWrite {
		panelError(w, 403, "规则编辑未开启")
		return
	}
	if r.URL.Path == "/api/v1/rule-preview" {
		if r.Method != "POST" {
			panelError(w, 405, "POST required")
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := panelBody(w, r, &body); err != nil {
			panelError(w, 400, err)
			return
		}
		b, err := data_provider.FetchRemoteDomains(r.Context(), body.URL)
		if err != nil {
			panelError(w, 400, err)
			return
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		panelJSON(w, 200, map[string]any{"count": len(lines), "preview": strings.Join(lines[:min(20, len(lines))], "\n")})
		return
	}
	tag := r.URL.Query().Get("tag")
	var body struct {
		Tag    string `json:"tag"`
		Text   string `json:"text"`
		SHA256 string `json:"sha256"`
	}
	if r.Method == "POST" {
		if err := panelBody(w, r, &body); err != nil {
			panelError(w, 400, err)
			return
		}
		tag = body.Tag
	}
	var provider *data_provider.DataProviderConfig
	for i := range p.cfg.DataProviders {
		if p.cfg.DataProviders[i].Tag == tag {
			provider = &p.cfg.DataProviders[i]
			break
		}
	}
	if provider == nil {
		panelError(w, 404, "未找到运行中的规则集；新规则请先保存并应用配置")
		return
	}
	if r.URL.Path == "/api/v1/rule-refresh" {
		if r.Method != "POST" {
			panelError(w, 405, "POST required")
			return
		}
		if p.manager == nil || p.manager.GetDataProvider(tag) == nil {
			panelError(w, 400, "规则管理器不可用")
			return
		}
		if err := p.manager.GetDataProvider(tag).RefreshRemote(); err != nil {
			panelError(w, 400, err)
			return
		}
		panelJSON(w, 200, map[string]any{"updated": true})
		return
	}
	p.configMu.Lock()
	defer p.configMu.Unlock()
	info, err := os.Lstat(provider.File)
	if err != nil {
		panelError(w, 400, err)
		return
	}
	if !info.Mode().IsRegular() || info.Size() > data_provider.MaxRuleBytes {
		panelError(w, 400, "仅支持 16 MiB 以内的文本规则文件")
		return
	}
	b, err := os.ReadFile(provider.File)
	if err != nil {
		panelError(w, 400, err)
		return
	}
	kind := "domain"
	for _, pc := range p.cfg.Plugins {
		args, _ := pc.Args.(map[string]interface{})
		for k, v := range args {
			if !strings.Contains(fmt.Sprint(v), "provider:"+tag) {
				continue
			}
			if k == "ip" || k == "ecs" || k == "client_ip" {
				kind = "ip"
			}
			if k == "hosts" {
				kind = "hosts"
			}
		}
	}
	if r.Method == "GET" {
		value := string(b[:min(len(b), 2<<20)])
		var status any
		if p.manager != nil {
			if dp := p.manager.GetDataProvider(tag); dp != nil {
				status = dp.RemoteStatus()
			}
		}
		count := 0
		for _, line := range strings.Split(string(b), "\n") {
			s := strings.TrimSpace(line)
			if s != "" && !strings.HasPrefix(s, "#") {
				count++
			}
		}
		panelJSON(w, 200, map[string]any{"text": value, "sha256": configDigest(b), "tag": tag, "kind": kind, "count": count, "read_only": len(b) > 2<<20 || provider.URL != "", "status": status})
		return
	}
	if provider.URL != "" {
		panelError(w, 400, "远程规则由订阅维护；可修改 URL 或创建本地域名集")
		return
	}
	if body.SHA256 != configDigest(b) {
		panelError(w, 409, "规则文件已变化，请重新读取")
		return
	}
	if kind == "domain" {
		_, err = domain.ParseTextDomainFile([]byte(body.Text))
	} else if kind == "ip" {
		err = netlist.LoadFromText(netlist.NewList(), body.Text)
	} else {
		m := domain.NewMixMatcher[*hosts.IPs]()
		m.SetDefaultMatcher(domain.MatcherFull)
		err = domain.LoadFromTextReader[*hosts.IPs](m, strings.NewReader(body.Text), hosts.ParseIPs)
	}
	if err != nil {
		panelError(w, 400, err)
		return
	}
	backup := provider.File + ".panel-backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err = os.WriteFile(backup, b, 0600); err != nil {
		panelError(w, 500, err)
		return
	}
	if p.manager != nil && p.manager.GetDataProvider(tag) != nil {
		err = p.manager.GetDataProvider(tag).ReplaceData([]byte(body.Text))
	} else {
		err = data_provider.AtomicWrite(provider.File, []byte(body.Text))
	}
	if err != nil {
		panelError(w, 500, err)
		return
	}
	panelJSON(w, 200, map[string]any{"saved": true, "sha256": configDigest([]byte(body.Text)), "backup": filepath.Base(backup)})
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
