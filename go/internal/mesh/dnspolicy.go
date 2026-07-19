package mesh

// dnspolicy.go — SNI 域名策略服务:三端统一的「DNS 服务自持 OSS 配置」组件。
//
// 设计(2026-07-19 统一):拦截策略的权威来源是 OSS domainlist(intercept apex + upstream),
// **由 DNS 服务自己周期拉取、自己热重载**,不再由 agent 拉取后经 arm 下发。三端跑同一份:
//
//   - Linux:agent(systemd root)进程内自持,系统解析经 resolv.conf 全局指到 127.0.0.1:53
//   - macOS:root sni-helper 进程内常驻自持(启动即拉,不等 arm),/etc/resolver scoped 指向
//   - Windows:agent(提权任务)进程内自持,网卡 DNS 指到 127.0.0.1 + 次级上游 fail-open
//
// 信任点=OSS 本身(public-read,仅 agent 持 AK 可写,HTTPS 防篡改);文档只做合法性 sanity
// (filterAllowedHosts)与上游规范化(normalizeDNSUpstreams),不判厂商。拉取失败保留上一份
// 已知好配置;首拉失败用编译期兜底清单,保证 DNS 服务永远起得来。

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sniDomainListURL = "https://ccfly-domainlist.oss-cn-hongkong.aliyuncs.com/domainlist.json"

// 编译期兜底:首拉 OSS 失败时的最小可用策略(与 cloud 原下发默认一致)。
var (
	fallbackInterceptDomains = []string{"anthropic.com", "claude.ai", "claude.com", "statsig.com"}
)

// domainListClient 直连拉 OSS。① Proxy:nil——**不读环境 HTTP(S)_PROXY**(代理会 MITM/篡改;同
// internal/cloudhttp)。② DisableCompression——**不带 Accept-Encoding: gzip**:OSS 对 gzip 传输会剥掉
// ETag 响应头(Vary: Accept-Encoding),而我们靠 ETag 当版本号;清单才 1KB,不压缩无所谓。TLS 用系统 CA。
var domainListClient = &http.Client{
	Timeout:   8 * time.Second,
	Transport: &http.Transport{Proxy: nil, DisableCompression: true},
}

// dnsPolicyService 持一份 SNI 域名策略(拦截 apex + 上游 + 版本 ETag)并以其驱动一个内嵌
// CoreDNS 实例:策略变化时重新渲染 Corefile 并热重载(旧实例 Stop → 新实例 Start,秒级空窗)。
type dnsPolicyService struct {
	listenIP string
	port     int

	mu        sync.RWMutex
	domains   []string // 拦截 apex,已 sanity 过滤
	upstreams []string // 上游,规范化 IP:port
	etag      string   // 当前生效策略的 OSS 版本(内容 MD5);编译期兜底=空

	dns *coreDNSService

	fetchURL string        // 生产=sniDomainListURL;var 便于测试注入 httptest
	poll     time.Duration // 轮询周期;var 便于测试加速
	stopCh   chan struct{}
	stopped  chan struct{}
	onChange func([]string) // 热重载成功后回调(新 domains);darwin helper 据此重写 /etc/resolver
}

func newDNSPolicyService(listenIP string, port int) *dnsPolicyService {
	return &dnsPolicyService{
		listenIP:  listenIP,
		port:      port,
		domains:   append([]string(nil), fallbackInterceptDomains...),
		upstreams: append([]string(nil), defaultSNIUpstreams...),
		fetchURL:  sniDomainListURL,
		poll:      15 * time.Second,
	}
}

// Domains 返回当前生效的拦截 apex 清单(darwin helper 写 /etc/resolver 用)。
func (s *dnsPolicyService) Domains() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.domains...)
}

// Version 返回当前生效策略的 OSS ETag;编译期兜底/从未拉成功=空。
func (s *dnsPolicyService) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.etag
}

// Running 报告内嵌 CoreDNS 是否在跑(arm 的前置检查;绑 :53 失败时服务仍可降级存在)。
func (s *dnsPolicyService) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dns != nil
}

// start 首拉(best-effort)→ 起 CoreDNS → 起轮询循环。CoreDNS bind 失败返回错误(调用方
// 决定 fail-open);OSS 首拉失败不视为错误(用兜底清单照跑)。
func (s *dnsPolicyService) start() error {
	s.refresh() // best-effort;失败保留兜底
	domains, upstreams := s.Domains(), s.currentUpstreams()
	dns, err := startCoreDNS(s.listenIP, s.port, domains, upstreams)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.dns = dns
	s.stopCh = make(chan struct{})
	s.stopped = make(chan struct{})
	s.mu.Unlock()
	go s.loop()
	return nil
}

// Stop 停轮询与 CoreDNS。幂等。
func (s *dnsPolicyService) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	dns := s.dns
	s.dns = nil
	stopped := s.stopped
	s.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
	if dns != nil {
		if err := dns.Stop(); err != nil {
			log.Printf("ccfly: stop CoreDNS: %v", err)
		}
	}
}

func (s *dnsPolicyService) loop() {
	s.mu.RLock()
	tick := time.NewTicker(s.poll)
	stopCh := s.stopCh
	stopped := s.stopped
	s.mu.RUnlock()
	defer func() {
		tick.Stop()
		close(stopped)
	}()
	for {
		select {
		case <-stopCh:
			return
		case <-tick.C:
			if s.refresh() {
				s.reload()
			}
		}
	}
}

// reload 用当前策略重渲染 Corefile 并热重载 CoreDNS;失败保留旧实例(下次轮询再试)。
func (s *dnsPolicyService) reload() {
	domains, upstreams := s.Domains(), s.currentUpstreams()
	dns, err := startCoreDNS(s.listenIP, s.port, domains, upstreams)
	if err != nil {
		log.Printf("ccfly: CoreDNS reload on policy change failed (keep old): %v", err)
		return
	}
	s.mu.Lock()
	old := s.dns
	s.dns = dns
	onChange := s.onChange
	s.mu.Unlock()
	if old != nil {
		_ = old.Stop()
	}
	log.Printf("ccfly: SNI DNS policy reloaded (%d domains, version=%.8s)", len(domains), s.Version())
	if onChange != nil {
		onChange(domains)
	}
}

// refresh 拉一次 OSS;文档合法且与现策略不同才发布,返回是否有变化。任一步失败保留旧策略。
func (s *dnsPolicyService) refresh() (changed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.fetchURL, nil)
	if err != nil {
		return false
	}
	resp, err := domainListClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false
	}
	return s.publish(body, strings.Trim(resp.Header.Get("ETag"), `"`))
}

// publish 校验一份 OSS 文档并原子发布。非法/不完整文档不动现策略(防坏拉取把 DNS 打空)。
func (s *dnsPolicyService) publish(body []byte, etag string) (changed bool) {
	var parsed struct {
		Intercept []string `json:"intercept"`
		Upstream  []string `json:"upstream"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	intercept := filterAllowedHosts(parsed.Intercept)
	upstream := normalizeDNSUpstreams(parsed.Upstream)
	if len(intercept) == 0 || len(upstream) == 0 {
		return false
	}
	s.mu.Lock()
	changed = strings.Join(s.domains, ",") != strings.Join(intercept, ",") ||
		strings.Join(s.upstreams, ",") != strings.Join(upstream, ",")
	s.domains, s.upstreams, s.etag = intercept, upstream, etag
	s.mu.Unlock()
	return changed
}

func (s *dnsPolicyService) currentUpstreams() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.upstreams...)
}

// observeDomainListVersion 轻量取一次 OSS 当前 ETag(HEAD),仅作版本观测——darwin agent 用:
// 配置权威在 root helper 自持的 dnsPolicyService,agent 不拿配置、只为状态上报对齐版本。
func observeDomainListVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, sniDomainListURL, nil)
	if err != nil {
		return ""
	}
	resp, err := domainListClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`)
}
