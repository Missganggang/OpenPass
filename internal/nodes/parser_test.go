package nodes

import (
	"encoding/base64"
	"testing"

	"openpass/internal/model"
)

func TestVMessPreservesTransport(t *testing.T) {
	uri := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"add":"node.example","port":"443","id":"uuid","net":"grpc","path":"service","tls":"none"}`))
	n, err := Parse(uri)
	if err != nil || n.Network != "grpc" || n.Path != "service" || n.TLS {
		t.Fatalf("transport lost: %#v %v", n, err)
	}
}

func TestURIExportPreservesImportedLinkAndBuildsStructuredSocks(t *testing.T) {
	original := "vless://uuid@example.com:443?security=tls&sni=example.com#home"
	n, err := Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := URI(n)
	if err != nil || got != original {
		t.Fatalf("URI=%q err=%v", got, err)
	}
	got, err = URI(model.Node{ID: "n", Type: "socks5", Address: "127.0.0.1", Port: 1080, UUID: "u", Password: "p", Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(got)
	if err != nil || parsed.Type != "socks5" || parsed.UUID != "u" || parsed.Password != "p" {
		t.Fatalf("round trip URI=%q parsed=%+v err=%v", got, parsed, err)
	}
}

func TestVLESSGRPCServiceName(t *testing.T) {
	n, err := Parse("vless://uuid@node.example:443?type=grpc&serviceName=my-service&security=tls")
	if err != nil || n.Path != "my-service" || n.Network != "grpc" || !n.TLS {
		t.Fatalf("invalid gRPC import: %#v %v", n, err)
	}
}
