package update

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func published(tag string) release {
	r := release{
		TagName: tag, HTMLURL: repositoryURL + "/releases/tag/" + tag,
		PublishedAt: "2026-10-17T12:00:00Z",
	}
	for _, name := range []string{"openpass-linux-amd64.tar.gz", "openpass-linux-386.tar.gz", "SHA256SUMS"} {
		r.Assets = append(r.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{Name: name, URL: repositoryURL + "/releases/download/" + tag + "/" + name})
	}
	return r
}

func serveRelease(t *testing.T, value release) checker {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "OpenPass/") {
			t.Error("missing GitHub request headers")
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(server.Close)
	return checker{client: &http.Client{Timeout: time.Second}, endpoint: server.URL, arch: "amd64"}
}

func TestCheckNumericVersions(t *testing.T) {
	for _, tc := range []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.1.9", "v0.1.10", true},
		{"0.1.10", "v0.1.9", false},
		{"v0.1.12", "v0.1.12", false},
		{"0.9.99", "v0.10.0", true},
		{"0.99.99", "v1.0.0", true},
		{"2.0.0", "v1.99.99", false},
		{"0.1.0", "v0.0.99", false},
	} {
		t.Run(tc.current+"/"+tc.latest, func(t *testing.T) {
			c := serveRelease(t, published(tc.latest))
			got, err := c.check(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if got.UpdateAvailable != tc.want || got.CurrentVersion != strings.TrimPrefix(tc.current, "v") || got.LatestVersion != strings.TrimPrefix(tc.latest, "v") {
				t.Fatalf("unexpected result: %+v", got)
			}
			if got.ReleaseURL != repositoryURL+"/releases/tag/"+tc.latest || got.PublishedAt != "2026-10-17T12:00:00Z" {
				t.Fatalf("incorrect release metadata: %+v", got)
			}
		})
	}
}

func TestCheckInvalidCurrentVersionBeforeNetwork(t *testing.T) {
	c := checker{client: nil, endpoint: "unused", arch: "amd64"}
	for _, current := range []string{"local", "", "0.1", "1.2.3-dev", "01.2.3", " 1.2.3", "1.2.3 ", "1.2.3.4", "18446744073709551616.0.0"} {
		if _, err := c.check(current); err == nil || !strings.Contains(err.Error(), "当前版本") {
			t.Errorf("current %q: got %v", current, err)
		}
	}
}

func TestCheckRejectsInvalidRelease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*release)
	}{
		{"draft", func(r *release) { r.Draft = true }},
		{"prerelease", func(r *release) { r.Prerelease = true }},
		{"missing tag prefix", func(r *release) { r.TagName = "0.1.12" }},
		{"prerelease tag", func(r *release) { r.TagName = "v0.1.12-beta" }},
		{"tag leading zero", func(r *release) { r.TagName = "v0.01.12" }},
		{"tag overflowing number", func(r *release) { r.TagName = "v18446744073709551616.0.0" }},
		{"foreign release", func(r *release) { r.HTMLURL = "https://example.org/releases/tag/v0.1.12" }},
		{"wrong release tag", func(r *release) { r.HTMLURL = repositoryURL + "/releases/tag/v0.1.11" }},
		{"invalid date", func(r *release) { r.PublishedAt = "yesterday" }},
		{"missing archive", func(r *release) { r.Assets = r.Assets[1:] }},
		{"missing checksum", func(r *release) { r.Assets = r.Assets[:2] }},
		{"duplicate archive", func(r *release) { r.Assets = append(r.Assets, r.Assets[0]) }},
		{"foreign archive", func(r *release) { r.Assets[0].URL = "https://example.org/openpass-linux-amd64.tar.gz" }},
		{"foreign checksum", func(r *release) { r.Assets[2].URL = "https://example.org/SHA256SUMS" }},
		{"insecure archive", func(r *release) { r.Assets[0].URL = strings.Replace(r.Assets[0].URL, "https:", "http:", 1) }},
		{"archive URL query", func(r *release) { r.Assets[0].URL += "?redirect=example.org" }},
		{"archive wrong tag", func(r *release) { r.Assets[0].URL = strings.Replace(r.Assets[0].URL, "v0.1.12", "v0.1.11", 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := published("v0.1.12")
			tc.mutate(&r)
			if _, err := serveRelease(t, r).check("0.1.11"); err == nil {
				t.Fatal("expected invalid release to be rejected")
			}
		})
	}
}

func TestCheckArchitectures(t *testing.T) {
	c := serveRelease(t, published("v0.1.12"))
	c.arch = "386"
	if _, err := c.check("0.1.11"); err != nil {
		t.Fatal(err)
	}
	c.arch = "arm64"
	if _, err := c.check("0.1.11"); err == nil || !strings.Contains(err.Error(), "架构") {
		t.Fatalf("unexpected architecture error: %v", err)
	}
}

func TestCheckHTTPErrors(t *testing.T) {
	for _, status := range []int{301, 403, 404, 429, 500, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		c := checker{client: server.Client(), endpoint: server.URL, arch: "amd64"}
		_, err := c.check("0.1.11")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "HTTP") {
			t.Errorf("HTTP %d: got %v", status, err)
		}
	}
}

func TestCheckResponseLimits(t *testing.T) {
	for _, tc := range []struct{ name, body, errorText string }{
		{"oversized", strings.Repeat(" ", maxResponse+1), "大小限制"},
		{"invalid JSON", "<html>Unavailable</html>", "JSON"},
		{"two objects", "{} {}", "JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			c := checker{client: server.Client(), endpoint: server.URL, arch: "amd64"}
			if _, err := c.check("0.1.11"); err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCheckTimeout(t *testing.T) {
	for _, flush := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if flush {
				_, _ = io.WriteString(w, "{")
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}))
		c := checker{client: &http.Client{Timeout: 50 * time.Millisecond}, endpoint: server.URL, arch: "amd64"}
		_, err := c.check("0.1.11")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("flush %v: unexpected error: %v", flush, err)
		}
	}
}

func TestCheckRejectsUntrustedTLS(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(published("v0.1.12"))
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	c := checker{client: &http.Client{Timeout: time.Second}, endpoint: server.URL, arch: "amd64"}
	if _, err := c.check("0.1.11"); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
}
