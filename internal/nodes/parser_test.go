package nodes

import (
	"encoding/base64"
	"testing"
)

func TestVMessPreservesTransport(t *testing.T) {
	uri := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"add":"node.example","port":"443","id":"uuid","net":"grpc","path":"service","tls":"none"}`))
	n, err := Parse(uri)
	if err != nil || n.Network != "grpc" || n.Path != "service" || n.TLS {
		t.Fatalf("transport lost: %#v %v", n, err)
	}
}

func TestVLESSGRPCServiceName(t *testing.T) {
	n, err := Parse("vless://uuid@node.example:443?type=grpc&serviceName=my-service&security=tls")
	if err != nil || n.Path != "my-service" || n.Network != "grpc" || !n.TLS {
		t.Fatalf("invalid gRPC import: %#v %v", n, err)
	}
}
