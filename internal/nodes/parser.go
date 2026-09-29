package nodes

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"openpass/internal/model"
)

// Parse accepts common sing-box compatible share links. It deliberately keeps
// URI on the node so unknown transport parameters remain available to users.
func Parse(raw string) (model.Node, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.Node{}, fmt.Errorf("empty node URI")
	}
	if strings.HasPrefix(raw, "vmess://") {
		return parseVMess(raw)
	}
	if strings.HasPrefix(raw, "ss://") {
		return parseShadowsocks(raw)
	}
	// A number of router clients export SOCKS5 as host:port:user:password
	// instead of a URI. Accept that form so it can be pasted directly.
	if !strings.Contains(raw, "://") {
		parts := strings.Split(raw, ":")
		if len(parts) == 4 {
			if port, e := strconv.Atoi(parts[1]); e == nil && port > 0 && port < 65536 {
				return model.Node{URI: "socks5://" + url.QueryEscape(parts[2]) + ":" + url.QueryEscape(parts[3]) + "@" + parts[0] + ":" + parts[1], Type: "socks5", Address: parts[0], Port: port, UUID: parts[2], Password: parts[3], Name: parts[0], Enabled: true}, nil
			}
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return model.Node{}, err
	}
	n := model.Node{URI: raw, Type: strings.ToLower(u.Scheme), Address: u.Hostname(), Name: u.Fragment, Enabled: true}
	if n.Name == "" {
		n.Name = n.Address
	}
	if p := u.Port(); p != "" {
		n.Port, _ = strconv.Atoi(p)
	}
	q := u.Query()
	n.SNI = q.Get("sni")
	if n.SNI == "" {
		n.SNI = q.Get("serverName")
	}
	n.Host = q.Get("host")
	n.Path = q.Get("path")
	n.Flow = q.Get("flow")
	n.Network = q.Get("type")
	if strings.EqualFold(n.Network, "grpc") && q.Get("serviceName") != "" {
		n.Path = q.Get("serviceName")
	}
	n.RealityPublicKey = q.Get("pbk")
	n.RealityShortID = q.Get("sid")
	n.Fingerprint = q.Get("fp")
	n.TLS = strings.EqualFold(q.Get("security"), "tls") || strings.EqualFold(q.Get("security"), "reality") || strings.EqualFold(q.Get("tls"), "tls") || strings.EqualFold(q.Get("type"), "tls") || n.RealityPublicKey != ""
	if u.User != nil {
		n.UUID = u.User.Username()
		n.Password, _ = u.User.Password()
		if n.Type == "trojan" {
			// Trojan uses the URI user component as its password, unlike
			// VLESS/VMess which use it as a UUID.
			if n.Password == "" {
				n.Password = n.UUID
			}
			n.UUID = ""
		}
	}
	if n.Type == "socks5" || n.Type == "socks" {
		n.Type = "socks5"
	}
	if n.Type != "vless" && n.Type != "vmess" && n.Type != "trojan" && n.Type != "socks5" && n.Type != "http" && n.Type != "shadowsocks" {
		return model.Node{}, fmt.Errorf("unsupported URI scheme %q", u.Scheme)
	}
	return n, nil
}

func parseShadowsocks(raw string) (model.Node, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return model.Node{}, err
	}
	enc := strings.TrimPrefix(raw, "ss://")
	if i := strings.IndexByte(enc, '#'); i >= 0 {
		enc = enc[:i]
	}
	if u.Host == "" || u.User == nil {
		var b []byte
		if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(enc, "=")); err != nil {
			b, err = base64.StdEncoding.DecodeString(enc)
		}
		if err != nil {
			return model.Node{}, err
		}
		u, err = url.Parse("ss://" + string(b))
		if err != nil {
			return model.Node{}, err
		}
	}
	n := model.Node{Type: "shadowsocks", URI: raw, Address: u.Hostname(), Name: u.Fragment, Enabled: true}
	if n.Name == "" {
		n.Name = n.Address
	}
	if p := u.Port(); p != "" {
		n.Port, _ = strconv.Atoi(p)
	}
	if u.User != nil {
		x := u.User.Username()
		if i := strings.IndexByte(x, ':'); i >= 0 {
			n.Method = x[:i]
			n.Password = x[i+1:]
		} else {
			n.Method = x
		}
		if p, ok := u.User.Password(); ok && n.Password == "" {
			n.Password = p
		}
	}
	if n.Method == "" {
		return model.Node{}, fmt.Errorf("invalid shadowsocks URI")
	}
	return n, nil
}

func parseVMess(raw string) (model.Node, error) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(raw, "vmess://"))
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "vmess://"))
	}
	if err != nil {
		return model.Node{}, err
	}
	var v struct {
		V    interface{} `json:"v"`
		Ps   string      `json:"ps"`
		Add  string      `json:"add"`
		Port interface{} `json:"port"`
		ID   string      `json:"id"`
		Aid  interface{} `json:"aid"`
		Net  string      `json:"net"`
		Host string      `json:"host"`
		Path string      `json:"path"`
		TLS  string      `json:"tls"`
		SNI  string      `json:"sni"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return model.Node{}, err
	}
	port := 0
	switch x := v.Port.(type) {
	case float64:
		port = int(x)
	case string:
		port, _ = strconv.Atoi(x)
	}
	return model.Node{ID: "", Name: v.Ps, Type: "vmess", Address: v.Add, Port: port, UUID: v.ID, Network: v.Net, Host: v.Host, Path: v.Path, TLS: strings.EqualFold(v.TLS, "tls"), SNI: v.SNI, URI: raw, Enabled: true}, nil
}

// ParseMany imports newline separated links, a base64 encoded list, or a
// single URI. Invalid lines are returned to the caller rather than discarded.
func ParseMany(input string) ([]model.Node, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty import")
	}
	lines := strings.Split(input, "\n")
	if len(lines) == 1 && !strings.Contains(input, "://") {
		if b, e := base64.StdEncoding.DecodeString(input); e == nil && strings.Contains(string(b), "://") {
			lines = strings.Split(string(b), "\n")
		}
	}
	var out []model.Node
	var errs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, e := Parse(line)
		if e != nil {
			errs = append(errs, e.Error())
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid nodes: %s", strings.Join(errs, "; "))
	}
	return out, nil
}
