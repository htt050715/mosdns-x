package data_provider

import (
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeRemoteDomainFormats(t *testing.T) {
	b, err := NormalizeRemoteDomains([]byte("# comment\npayload:\n - '+.Example.COM'\n - 'DOMAIN,api.example.net'\nDOMAIN-KEYWORD,video\n||ads.example.org^\nregexp:^foo\\.\nexample.com\n"))
	want := "domain:example.com\nfull:api.example.net\nkeyword:video\ndomain:ads.example.org\nregexp:^foo\\.\n"
	if err != nil || string(b) != want {
		t.Fatalf("%q, %v", b, err)
	}
	for _, input := range []string{"", "IP-CIDR,192.0.2.0/24", "DOMAIN,example.com,DIRECT", "@@||example.com^", "regexp:[", "<html>", "*.example.com"} {
		if _, err := NormalizeRemoteDomains([]byte(input)); err == nil {
			t.Errorf("accepted unsupported subscription %q", input)
		}
	}
}

type updateProbe struct{ values chan string }

func (p *updateProbe) Update(b []byte) error { p.values <- string(b); return nil }

func TestRemoteFailurePreservesLastGoodAndLiveMatcher(t *testing.T) {
	var state atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch state.Load() {
		case 0:
			w.Write([]byte("example.com\n"))
		case 1:
			http.Error(w, "unavailable", 503)
		default:
			w.Write([]byte("IP-CIDR,192.0.2.0/24"))
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "rules.txt")
	dp, err := NewDataProvider(zap.NewNop(), DataProviderConfig{File: path, URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer dp.Close()
	p := &updateProbe{values: make(chan string, 10)}
	if err = dp.LoadAndAddListener(p); err != nil {
		t.Fatal(err)
	}
	<-p.values
	for _, stateValue := range []int32{1, 2} {
		state.Store(stateValue)
		if err = dp.RefreshRemote(); err == nil {
			t.Fatal("bad download accepted")
		}
		b, _ := os.ReadFile(path)
		if string(b) != "domain:example.com\n" {
			t.Fatalf("last good cache lost: %q", b)
		}
		select {
		case <-p.values:
			t.Fatal("failed update reached live matcher")
		default:
		}
		if dp.RemoteStatus()["last_error"] == "" {
			t.Fatal("missing refresh error")
		}
	}
}

func TestLocalWatcherSurvivesAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.txt")
	os.WriteFile(path, []byte("old.example\n"), 0600)
	dp, err := NewDataProvider(zap.NewNop(), DataProviderConfig{File: path, AutoReload: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dp.Close()
	p := &updateProbe{values: make(chan string, 10)}
	dp.LoadAndAddListener(p)
	<-p.values
	for _, name := range []string{"first.example", "second.example"} {
		if err = AtomicWrite(path, []byte(name+"\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case b := <-p.values:
			if !strings.Contains(b, name) {
				t.Fatal(b)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("directory watcher lost replacement")
		}
	}
}
