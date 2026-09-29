package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"openpass/internal/model"
)

// The subprocess stands in for sing-box's loopback mixed inbound. The target
// hostname deliberately cannot resolve locally: success proves the request is
// sent to the proxy and never to the router's default/direct HTTP client.
func TestMain(m *testing.M) {
	if os.Getenv("OPENPASS_PROBE_TEST_HELPER") == "1" {
		var cfg struct {
			Inbounds []struct {
				Listen string `json:"listen"`
				Port   int    `json:"listen_port"`
			} `json:"inbounds"`
		}
		b, err := os.ReadFile(os.Args[len(os.Args)-1])
		if err != nil || json.Unmarshal(b, &cfg) != nil || len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Listen != "127.0.0.1" {
			os.Exit(2)
		}
		server := &http.Server{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Inbounds[0].Port)), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "target.invalid" || r.URL.Path != "/probe" || r.Method != http.MethodGet {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})}
		_ = server.ListenAndServe()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestURLProbeAlwaysUsesIsolatedProxy(t *testing.T) {
	t.Setenv("OPENPASS_PROBE_TEST_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := model.Node{Type: "vless", Address: "node.invalid", Port: 443, UUID: "sample"}
	status, err := checkNodeURL(ctx, executable, n, "http://target.invalid/probe", "aliyun", nil)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("expected proxy success, got status=%d err=%v", status, err)
	}
}

func TestProbeURLRejectsUnsupportedSchemesAndCredentials(t *testing.T) {
	for _, target := range []string{"", "target.invalid", "file:///etc/passwd", "ftp://target.invalid", "https://user:secret@target.invalid"} {
		if validateProbeURL(target) == nil {
			t.Errorf("accepted invalid URL %q", target)
		}
	}
	if err := validateProbeURL("https://target.invalid/generate_204"); err != nil {
		t.Fatal(err)
	}
}

func TestProbeDNSCompressedAnswersAndMalformedPackets(t *testing.T) {
	query, err := probeDNSQuery("node.example", 1)
	if err != nil {
		t.Fatal(err)
	}
	answer := append([]byte(nil), query...)
	answer[2], answer[3], answer[7] = 0x81, 0x80, 1
	answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7)
	ips, err := probeDNSAnswers(query, answer)
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.7" {
		t.Fatalf("unexpected answer: %v %v", ips, err)
	}
	for length := 0; length < len(answer); length++ {
		if _, err := probeDNSAnswers(query, answer[:length]); err == nil {
			t.Fatalf("accepted truncated packet length %d", length)
		}
	}
	answer[0] ^= 1
	if _, err := probeDNSAnswers(query, answer); err == nil {
		t.Fatal("accepted wrong transaction ID")
	}
}

func TestNodeErrorsDoNotRevealCredentials(t *testing.T) {
	n := model.Node{URI: "vless://full-secret", Password: "p@ssword", UUID: "sample-uuid", RealityPublicKey: "key-value", RealityShortID: "short-id"}
	message := "vless://full-secret p@ssword sample-uuid key-value short-id"
	if got := redactNodeError(message, n); got != "[redacted] [redacted] [redacted] [redacted] [redacted]" {
		t.Fatal(got)
	}
}
