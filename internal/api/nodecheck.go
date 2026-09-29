package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"openpass/internal/model"
	"openpass/internal/singbox"
)

// Each URL check starts an isolated sing-box process, so cap concurrent checks
// on small routers. Waiting consumes the request timeout, not another process.
var nodeProbeSlots = make(chan struct{}, 2)

func (s *Server) testNode(w http.ResponseWriter, r *http.Request, id string) error {
	var body struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	var n model.Node
	for _, candidate := range s.Store.Nodes() {
		if candidate.ID == id {
			n = candidate
			break
		}
	}
	if n.ID == "" {
		return &httpError{http.StatusNotFound, "node not found"}
	}
	if body.Type == "" {
		body.Type = "url"
	}
	if body.Type == "tcping" {
		body.Type = "tcp"
	}
	if body.Type != "ping" && body.Type != "tcp" && body.Type != "url" {
		return fmt.Errorf("unknown test type %q", body.Type)
	}
	if n.Address == "" || n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("node server address/port is invalid")
	}
	settings := s.Store.Settings()
	if body.URL == "" {
		body.URL = settings.URLTestAddress
	}
	if body.Type == "url" {
		if err := validateProbeURL(body.URL); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	start := time.Now()
	result := map[string]any{"node_id": id, "type": body.Type, "ok": false, "scope": "server"}
	var err error
	switch body.Type {
	case "url":
		result["scope"], result["proxy"], result["url"] = "proxy", true, body.URL
		select {
		case nodeProbeSlots <- struct{}{}:
			defer func() { <-nodeProbeSlots }()
		case <-ctx.Done():
			err = fmt.Errorf("等待节点检测超时，请稍后重试")
		}
		if err == nil {
			var status int
			status, err = checkNodeURL(ctx, s.SingBoxPath, n, body.URL, settings.DefaultDNS, s.Store.DNS())
			if status > 0 {
				result["status"] = status
			}
		}
	case "ping", "tcp":
		// Resolve via pinned DoH first. A plaintext lookup would be blocked
		// by OpenPass's DNS leak protection, even when the node is healthy.
		var ips []net.IP
		ips, err = resolveProbeHost(ctx, n.Address, s.Store.DNS())
		if err == nil && body.Type == "ping" {
			result["note"] = "Ping 只检测服务器 ICMP；服务器禁 Ping 不代表代理不可用，请以 URL 检测为准。"
			pingPath, lookupErr := exec.LookPath("ping")
			if lookupErr != nil {
				err = fmt.Errorf("系统没有 ping 命令；请使用 TCP 或 URL 检测")
			} else {
				pingCtx, stop := context.WithTimeout(ctx, 4*time.Second)
				defer stop()
				if runErr := exec.CommandContext(pingCtx, pingPath, "-c", "1", "-W", "2", ips[0].String()).Run(); runErr != nil {
					err = fmt.Errorf("服务器未响应 ICMP（可能禁 Ping），请使用 URL 检测")
				}
			}
		} else if err == nil {
			result["note"] = "TCP 只检测服务器端口；代理认证和实际访问请使用 URL 检测。"
			d := net.Dialer{Timeout: 5 * time.Second}
			for _, ip := range ips {
				var conn net.Conn
				conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(n.Port)))
				if err == nil {
					conn.Close()
					break
				}
			}
		}
	}
	result["ok"] = err == nil
	if err != nil {
		result["error"] = redactNodeError(err.Error(), n)
	}
	result["latency_ms"] = time.Since(start).Milliseconds()
	writeJSON(w, result)
	return nil
}

func validateProbeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("URL 检测地址必须是完整的 http:// 或 https:// 地址，且不能包含用户名密码")
	}
	return nil
}

// lockedProbeLog keeps only a bounded amount of child stderr. Credentials are
// redacted before any of it can be returned to the browser.
type lockedProbeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedProbeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if remaining := 8192 - l.b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = l.b.Write(p)
	}
	return n, nil
}

func (l *lockedProbeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(l.b.String())
}

func checkNodeURL(ctx context.Context, executable string, n model.Node, target, dnsID string, profiles []model.DNS) (int, error) {
	if executable == "" {
		executable = "sing-box"
	}
	binaryPath, err := exec.LookPath(executable)
	if err != nil {
		return 0, fmt.Errorf("找不到 sing-box 内核，无法进行真实代理 URL 检测")
	}
	// The temporary listener never binds to LAN/WAN. No TUN inbound or route
	// changes are needed, including while global protection is disabled.
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	config, err := singbox.ProbeConfig(n, port, dnsID, profiles)
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "openpass-probe-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	configPath := filepath.Join(dir, "sing-box.json")
	if err = os.WriteFile(configPath, config, 0600); err != nil {
		return 0, err
	}
	log := &lockedProbeLog{}
	cmd := exec.CommandContext(ctx, binaryPath, "run", "-c", configPath)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动节点检测内核失败: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { _ = cmd.Process.Kill(); <-done }()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	startup := time.NewTimer(5 * time.Second)
	defer startup.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("节点检测超时: %w", ctx.Err())
		case <-done:
			return 0, fmt.Errorf("节点检测内核退出: %s", log.String())
		case <-startup.C:
			return 0, fmt.Errorf("节点检测内核未能启动: %s", log.String())
		case <-ticker.C:
			conn, dialErr := net.DialTimeout("tcp4", address, 100*time.Millisecond)
			if dialErr == nil {
				conn.Close()
				break ready
			}
		}
	}
	proxyURL := &url.URL{Scheme: "http", Host: address}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), ForceAttemptHTTP2: false, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("URL 重定向超过 5 次")
		}
		return validateProbeURL(req.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "OpenPass-NodeCheck/1.0")
	response, err := client.Do(req)
	if err != nil {
		if details := log.String(); details != "" {
			return 0, fmt.Errorf("代理 URL 请求失败: %w; sing-box: %s", err, details)
		}
		return 0, fmt.Errorf("代理 URL 请求失败: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return response.StatusCode, fmt.Errorf("测试站点通过代理返回 HTTP %d；可更换国内/海外测试地址后重试", response.StatusCode)
	}
	return response.StatusCode, nil
}

func redactNodeError(message string, n model.Node) string {
	for _, secret := range []string{n.URI, n.Password, n.UUID, n.RealityPublicKey, n.RealityShortID} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func resolveProbeHost(ctx context.Context, host string, profiles []model.DNS) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	// Resolve the node endpoint itself through the reachable bootstrap DoH
	// resolver.  The selected overseas resolver is used by the proxy after
	// the node is connected; using it here would make every hostname-based
	// node fail on networks that block 1.1.1.1/8.8.8.8 before the proxy can
	// start.
	ip, tlsName, path := singbox.BootstrapDoHEndpoint(profiles)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: tlsName, MinVersion: tls.VersionTLS12},
		DialContext: func(c context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(c, network, net.JoinHostPort(ip, "443"))
		},
		TLSHandshakeTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, typ := range []uint16{1, 28} {
		query, err := probeDNSQuery(host, typ)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+tlsName+path, bytes.NewReader(query))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/dns-message")
		req.Header.Set("Accept", "application/dns-message")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("节点引导 DoH 解析失败（阿里）: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || len(body) > 65536 {
			return nil, fmt.Errorf("节点域名 DoH 解析失败: HTTP %d", resp.StatusCode)
		}
		answers, err := probeDNSAnswers(query, body)
		if err != nil {
			return nil, fmt.Errorf("节点域名 DoH 解析失败: %w", err)
		}
		if len(answers) > 0 {
			return answers, nil
		}
	}
	return nil, fmt.Errorf("节点域名没有可用的 A/AAAA 地址")
}

func probeDNSQuery(host string, typ uint16) ([]byte, error) {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return nil, fmt.Errorf("invalid node hostname")
	}
	packet := make([]byte, 12)
	if _, err := rand.Read(packet[:2]); err != nil {
		return nil, err
	}
	packet[2], packet[5] = 1, 1 // recursion desired, one question
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS label")
		}
		packet = append(packet, byte(len(label)))
		packet = append(packet, label...)
	}
	packet = append(packet, 0, byte(typ>>8), byte(typ), 0, 1)
	return packet, nil
}

func skipProbeDNSName(packet []byte, offset int) (int, error) {
	for offset < len(packet) {
		size := int(packet[offset])
		offset++
		if size == 0 {
			return offset, nil
		}
		if size&0xc0 == 0xc0 {
			if offset >= len(packet) || ((size&0x3f)<<8|int(packet[offset])) >= len(packet) {
				return 0, fmt.Errorf("invalid DNS pointer")
			}
			return offset + 1, nil
		}
		if size > 63 || offset+size > len(packet) {
			return 0, fmt.Errorf("invalid DNS name")
		}
		offset += size
	}
	return 0, fmt.Errorf("truncated DNS name")
}

func probeDNSAnswers(query, packet []byte) ([]net.IP, error) {
	if len(packet) < 12 || !bytes.Equal(query[:2], packet[:2]) || packet[2]&0x80 == 0 || packet[2]&2 != 0 {
		return nil, fmt.Errorf("invalid DNS response")
	}
	if code := packet[3] & 15; code != 0 {
		return nil, fmt.Errorf("DNS response code %d", code)
	}
	offset := 12
	for i := 0; i < int(binary.BigEndian.Uint16(packet[4:6])); i++ {
		var err error
		offset, err = skipProbeDNSName(packet, offset)
		if err != nil || offset+4 > len(packet) {
			return nil, fmt.Errorf("invalid DNS question")
		}
		offset += 4
	}
	var out []net.IP
	for i := 0; i < int(binary.BigEndian.Uint16(packet[6:8])); i++ {
		var err error
		offset, err = skipProbeDNSName(packet, offset)
		if err != nil || offset+10 > len(packet) {
			return nil, fmt.Errorf("invalid DNS answer")
		}
		typ, class := binary.BigEndian.Uint16(packet[offset:]), binary.BigEndian.Uint16(packet[offset+2:])
		size := int(binary.BigEndian.Uint16(packet[offset+8:]))
		offset += 10
		if offset+size > len(packet) {
			return nil, fmt.Errorf("truncated DNS answer")
		}
		if class == 1 && ((typ == 1 && size == 4) || (typ == 28 && size == 16)) {
			out = append(out, append(net.IP(nil), packet[offset:offset+size]...))
		}
		offset += size
	}
	return out, nil
}
