/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package coremain

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"context"
	"github.com/pmkol/mosdns-x/mlog"
	"github.com/pmkol/mosdns-x/pkg/data_provider"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/safe_close"
)

type Mosdns struct {
	panel  *webPanel
	logger *zap.Logger

	// Data
	dataManager *data_provider.DataManager

	// Plugins
	execs    map[string]executable_seq.Executable
	matchers map[string]executable_seq.Matcher

	httpAPIMux    *http.ServeMux
	httpAPIServer *http.Server

	metricsReg *prometheus.Registry

	sc *safe_close.SafeClose
}

func RunMosdns(cfg *Config) error {
	lg, err := mlog.NewLogger(&cfg.Log)
	if err != nil {
		return fmt.Errorf("failed to init logger: %w", err)
	}

	m := &Mosdns{
		logger:      lg,
		dataManager: data_provider.NewDataManager(),
		execs:       make(map[string]executable_seq.Executable),
		matchers:    make(map[string]executable_seq.Matcher),
		httpAPIMux:  http.NewServeMux(),
		metricsReg:  newMetricsReg(),
		sc:          safe_close.NewSafeClose(),
	}
	defer func() { m.sc.Done(); m.sc.CloseWait() }()

	m.httpAPIMux.Handle("/metrics", promhttp.HandlerFor(m.metricsReg, promhttp.HandlerOpts{}))
	m.httpAPIMux.HandleFunc("/debug/pprof/", pprof.Index)
	m.httpAPIMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	m.httpAPIMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	m.httpAPIMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	m.httpAPIMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	if cfg.API.WebUI {
		if cfg.API.HTTP == "" {
			return errors.New("api.webui requires api.http")
		}
		m.panel = newWebPanel(cfg, m.metricsReg)
		m.panel.manager = m.dataManager
		m.panel.register(m.httpAPIMux)
	}

	// Init data manager
	dupTag := make(map[string]struct{})
	for _, dpc := range cfg.DataProviders {
		if len(dpc.Tag) == 0 {
			continue
		}
		if _, ok := dupTag[dpc.Tag]; ok {
			return fmt.Errorf("duplicated provider tag %s", dpc.Tag)
		}
		dupTag[dpc.Tag] = struct{}{}
		// Validation may fetch a new subscription, but must not create or modify
		// the live rule cache before the configuration has been accepted.
		if cfg.checkOnly && dpc.URL != "" {
			if _, err := os.Stat(dpc.File); os.IsNotExist(err) {
				f, err := os.CreateTemp("", "mosdns-rule-check-*")
				if err != nil {
					return err
				}
				f.Close()
				os.Remove(f.Name())
				dpc.File = f.Name()
				defer os.Remove(dpc.File)
			}
		}

		dp, err := data_provider.NewDataProvider(lg, dpc)
		if err != nil {
			return fmt.Errorf("failed to init data provider %s, %w", dpc.Tag, err)
		}
		m.dataManager.AddDataProvider(dpc.Tag, dp)
		m.sc.Attach(func(done func(), signal <-chan struct{}) { defer done(); <-signal; dp.Close() })
		if !cfg.checkOnly {
			dp.StartRemoteUpdates()
		}
	}

	// Init preset plugins
	for tag, f := range LoadNewPersetPluginFuncs() {
		p, err := f(NewBP(tag, "preset", m.logger, m))
		if err != nil {
			return fmt.Errorf("failed to init preset plugin %s, %w", tag, err)
		}
		m.addPlugin(p)
	}

	// Init plugins
	dupTag = make(map[string]struct{})
	for i, pc := range cfg.Plugins {
		if len(pc.Type) == 0 || len(pc.Tag) == 0 {
			continue
		}
		if _, dup := dupTag[pc.Tag]; dup {
			return fmt.Errorf("duplicated plugin tag %s", pc.Tag)
		}
		dupTag[pc.Tag] = struct{}{}

		m.logger.Info("loading plugin", zap.String("tag", pc.Tag), zap.String("type", pc.Type))
		p, err := NewPlugin(&pc, m.logger, m)
		if err != nil {
			return fmt.Errorf("failed to init plugin #%d, %w", i, err)
		}

		m.addPlugin(p)
		// Also add it to api mux if plugin implements http.Handler.
		if h, ok := p.(http.Handler); ok {
			m.httpAPIMux.Handle(fmt.Sprintf("/plugins/%s/", p.Tag()), h)
		}
	}

	if len(cfg.Servers) == 0 {
		return errors.New("no server is configured")
	}
	if cfg.checkOnly {
		for _, s := range cfg.Servers {
			if m.execs[s.Exec] == nil {
				return fmt.Errorf("unknown server entry %s", s.Exec)
			}
		}
		return validatePanelConfig(cfg)
	}
	for i, sc := range cfg.Servers {
		if err := m.startServers(&sc); err != nil {
			return fmt.Errorf("failed to start server #%d, %w", i, err)
		}
	}

	// Start http api server
	if httpAddr := cfg.API.HTTP; len(httpAddr) > 0 {
		httpServer := &http.Server{
			Addr:    httpAddr,
			Handler: m.httpAPIMux,
		}
		m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
			defer done()
			errChan := make(chan error, 1)
			go func() {
				m.logger.Info("starting api http server", zap.String("addr", httpAddr))
				errChan <- httpServer.ListenAndServe()
			}()
			select {
			case err := <-errChan:
				m.sc.SendCloseSignal(err)
			case <-closeSignal:
				httpServer.Close()
			}
		})
	}

	time.AfterFunc(time.Second*1, func() {
		runtime.GC()
		debug.FreeOSMemory()
	})
	<-m.sc.ReceiveCloseSignal()
	m.sc.Done()
	m.sc.CloseWait()
	return m.sc.Err()
}

func (m *Mosdns) addPlugin(p Plugin) {
	t := p.Tag()
	if p, ok := p.(ExecutablePlugin); ok {
		if m.panel != nil {
			m.execs[t] = &panelExecutable{Executable: p, tag: t}
		} else {
			m.execs[t] = p
		}
	}
	if p, ok := p.(MatcherPlugin); ok {
		if m.panel != nil {
			m.matchers[p.Tag()] = &panelMatcher{Matcher: p, tag: t}
		} else {
			m.matchers[p.Tag()] = p
		}
	}
}

type panelExecutable struct {
	executable_seq.Executable
	tag string
}

func (p *panelExecutable) Exec(ctx context.Context, q *query_context.Context, n executable_seq.ExecutableChainNode) error {
	query_context.RecordAudit(ctx, "step", p.tag, "")
	return p.Executable.Exec(ctx, q, n)
}

type panelMatcher struct {
	executable_seq.Matcher
	tag string
}

func (p *panelMatcher) Match(ctx context.Context, q *query_context.Context) (bool, error) {
	ok, err := p.Matcher.Match(ctx, q)
	if ok {
		query_context.RecordAudit(ctx, "match", p.tag, "")
	}
	return ok, err
}

func (m *Mosdns) GetDataManager() *data_provider.DataManager {
	return m.dataManager
}

func (m *Mosdns) GetSafeClose() *safe_close.SafeClose {
	return m.sc
}

func (m *Mosdns) GetExecutables() map[string]executable_seq.Executable {
	return m.execs
}

func (m *Mosdns) GetMatchers() map[string]executable_seq.Matcher {
	return m.matchers
}

// GetMetricsReg returns a prometheus.Registerer with a prefix of "mosdns_"
func (m *Mosdns) GetMetricsReg() prometheus.Registerer {
	return prometheus.WrapRegistererWithPrefix("mosdns_", m.metricsReg)
}

// GetHTTPAPIMux returns the api http.ServeMux.
// The pattern "/plugins/plugin_tag/" has been registered if
// Plugin implements http.Handler interface.
// Plugin caller should register path that has "/plugins/plugin_tag/"
// prefix only.
func (m *Mosdns) GetHTTPAPIMux() *http.ServeMux {
	return m.httpAPIMux
}

func newMetricsReg() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())
	return reg
}
