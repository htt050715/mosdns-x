package data_provider

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const MaxRuleBytes = 16 << 20

func ValidateRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("规则地址必须为不含登录凭据的 HTTP/HTTPS URL")
	}
	return nil
}

// Accept plain domains, mosdns patterns and Clash domain payloads, never silently
// discard an unsupported rule (which could change a user's routing policy).
func NormalizeRemoteDomains(b []byte) ([]byte, error) {
	var out []string
	seen := map[string]bool{}
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 4096), 65536)
	line := 0
	for scan.Scan() {
		line++
		s := strings.TrimSpace(strings.TrimPrefix(scan.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "!") || s == "payload:" {
			continue
		}
		s = strings.TrimSpace(strings.TrimPrefix(s, "- "))
		s = strings.Trim(s, "\"'")
		if i := strings.Index(s, " #"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if strings.Contains(s, ",") {
			parts := strings.Split(s, ",")
			if len(parts) != 2 {
				return nil, fmt.Errorf("第 %d 行含不支持的规则动作", line)
			}
			prefix := map[string]string{"DOMAIN": "full:", "DOMAIN-SUFFIX": "domain:", "DOMAIN-KEYWORD": "keyword:"}[strings.ToUpper(strings.TrimSpace(parts[0]))]
			if prefix == "" {
				return nil, fmt.Errorf("第 %d 行不是域名规则", line)
			}
			s = prefix + strings.TrimSpace(parts[1])
		} else if strings.HasPrefix(s, "+.") {
			s = "domain:" + s[2:]
		} else if strings.HasPrefix(s, "||") && strings.HasSuffix(s, "^") {
			s = "domain:" + s[2:len(s)-1]
		}
		kind, value, has := strings.Cut(s, ":")
		if !has {
			kind, value = "domain", s
		}
		switch kind {
		case "domain", "full":
			value = strings.ToLower(strings.TrimSuffix(value, "."))
			if value == "" || strings.ContainsAny(value, " /\t*<>[]@") {
				return nil, fmt.Errorf("第 %d 行域名无效", line)
			}
			if _, ok := dns.IsDomainName(value + "."); !ok {
				return nil, fmt.Errorf("第 %d 行域名无效", line)
			}
		case "keyword":
			if value == "" || strings.ContainsAny(value, " \t") {
				return nil, fmt.Errorf("第 %d 行关键词无效", line)
			}
		case "regexp":
			if _, err := regexp.Compile(value); err != nil {
				return nil, fmt.Errorf("第 %d 行正则无效: %w", line, err)
			}
		default:
			return nil, fmt.Errorf("第 %d 行不支持的规则类型 %s", line, kind)
		}
		s = kind + ":" + value
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("远程规则集为空或没有有效域名")
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

func FetchRemoteDomains(ctx context.Context, raw string) ([]byte, error) {
	if err := ValidateRemoteURL(raw); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向过多")
		}
		return ValidateRemoteURL(r.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxRuleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxRuleBytes {
		return nil, fmt.Errorf("规则集超过 16 MiB")
	}
	return NormalizeRemoteDomains(b)
}

func (ds *DataProvider) RefreshRemote() (err error) {
	ds.updateMu.Lock()
	defer ds.updateMu.Unlock()
	defer func() {
		ds.statusMu.Lock()
		defer ds.statusMu.Unlock()
		if err != nil {
			ds.lastError = err.Error()
		} else {
			ds.lastUpdate = time.Now()
			ds.lastError = ""
		}
	}()
	if ds.remote.URL == "" {
		return fmt.Errorf("该规则集没有远程地址")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := FetchRemoteDomains(ctx, ds.remote.URL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ds.file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(ds.file), ".rule-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), ds.file); err != nil {
		return err
	}
	ds.pushData(b)
	return nil
}
func (ds *DataProvider) RemoteStatus() map[string]any {
	ds.statusMu.Lock()
	defer ds.statusMu.Unlock()
	return map[string]any{"last_update": ds.lastUpdate, "last_error": ds.lastError}
}
func (ds *DataProvider) StartRemoteUpdates() {
	if ds.remote.URL == "" {
		return
	}
	minutes := ds.remote.Interval
	if minutes < 5 {
		minutes = 60
	}
	ds.sc.Attach(func(done func(), stop <-chan struct{}) {
		defer done()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				_ = ds.RefreshRemote()
				timer.Reset(time.Duration(minutes) * time.Minute)
			}
		}
	})
}
func (ds *DataProvider) ReplaceData(b []byte) error {
	ds.updateMu.Lock()
	defer ds.updateMu.Unlock()
	if err := AtomicWrite(ds.file, b); err != nil {
		return err
	}
	ds.pushData(b)
	return nil
}

// AtomicWrite keeps the previous file intact if staging fails. Directory watchers
// continue observing subsequent replacements of the same path.
func AtomicWrite(path string, b []byte) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rule-edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
