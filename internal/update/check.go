// Package update checks published OpenPass releases without starting services.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestEndpoint = "https://api.github.com/repos/Missganggang/OpenPass/releases/latest"
	repositoryURL  = "https://github.com/Missganggang/OpenPass"
	maxResponse    = 1024 * 1024
)

// Result contains only public release metadata. Checking never installs an update.
type Result struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	PublishedAt     string `json:"published_at"`
}

type release struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type checker struct {
	client   *http.Client
	endpoint string
	arch     string
}

// Check uses the official GitHub latest-release endpoint and a bounded HTTPS
// request. The caller does not need a state file, service, or sing-box process.
func Check(current string) (Result, error) {
	return (checker{
		client: &http.Client{
			Timeout: 15 * time.Second,
			// The official endpoint is fixed; do not follow unexpected redirects.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		endpoint: latestEndpoint,
		arch:     runtime.GOARCH,
	}).check(current)
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func parseVersion(value string) ([3]uint64, error) {
	var parts [3]uint64
	if !versionPattern.MatchString(value) {
		return parts, fmt.Errorf("版本号必须为 MAJOR.MINOR.PATCH")
	}
	for i, part := range strings.Split(value, ".") {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parts, fmt.Errorf("版本号数字超出范围")
		}
		parts[i] = n
	}
	return parts, nil
}

func isNewer(latest, current [3]uint64) bool {
	for i := range latest {
		if latest[i] != current[i] {
			return latest[i] > current[i]
		}
	}
	return false
}

func networkError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("检查更新超时，请检查路由器网络后重试")
	}
	return fmt.Errorf("无法连接 GitHub 检查更新：%w", err)
}

func (c checker) check(current string) (Result, error) {
	current = strings.TrimPrefix(current, "v")
	currentParts, err := parseVersion(current)
	if err != nil {
		return Result{}, fmt.Errorf("当前版本 %q 无法检查更新：%w", current, err)
	}
	if c.arch != "amd64" && c.arch != "386" {
		return Result{}, fmt.Errorf("不支持当前架构 %q 的更新检查", c.arch)
	}
	req, err := http.NewRequest(http.MethodGet, c.endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OpenPass/"+current)
	resp, err := c.client.Do(req)
	if err != nil {
		return Result{}, networkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusForbidden, http.StatusTooManyRequests:
			return Result{}, fmt.Errorf("GitHub 拒绝更新检查或请求过于频繁（HTTP %d），请稍后重试", resp.StatusCode)
		case http.StatusNotFound:
			return Result{}, fmt.Errorf("GitHub 暂无可用的正式发布版本（HTTP 404）")
		default:
			return Result{}, fmt.Errorf("GitHub 更新检查失败（HTTP %d）", resp.StatusCode)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return Result{}, networkError(err)
	}
	if len(body) > maxResponse {
		return Result{}, fmt.Errorf("GitHub 更新响应超过大小限制")
	}
	var latest release
	if err := json.Unmarshal(body, &latest); err != nil {
		return Result{}, fmt.Errorf("GitHub 更新响应不是有效 JSON：%w", err)
	}
	if latest.Draft || latest.Prerelease {
		return Result{}, fmt.Errorf("最新发布为草稿或预发布版本，暂不可更新")
	}
	if !strings.HasPrefix(latest.TagName, "v") {
		return Result{}, fmt.Errorf("GitHub 发布标签 %q 无效，需要 vMAJOR.MINOR.PATCH", latest.TagName)
	}
	latestVersion := strings.TrimPrefix(latest.TagName, "v")
	latestParts, err := parseVersion(latestVersion)
	if err != nil {
		return Result{}, fmt.Errorf("GitHub 发布标签 %q 无效：%w", latest.TagName, err)
	}
	expectedReleaseURL := repositoryURL + "/releases/tag/" + latest.TagName
	if latest.HTMLURL != expectedReleaseURL {
		return Result{}, fmt.Errorf("GitHub 发布地址不是 OpenPass 官方仓库地址")
	}
	if _, err := time.Parse(time.RFC3339, latest.PublishedAt); err != nil {
		return Result{}, fmt.Errorf("GitHub 发布日期无效")
	}
	archive := "openpass-linux-" + c.arch + ".tar.gz"
	for _, name := range []string{archive, "SHA256SUMS"} {
		found := false
		for _, asset := range latest.Assets {
			if asset.Name != name {
				continue
			}
			if found {
				return Result{}, fmt.Errorf("GitHub 发布资产 %s 重复", name)
			}
			if asset.URL != repositoryURL+"/releases/download/"+latest.TagName+"/"+name {
				return Result{}, fmt.Errorf("GitHub 发布资产 %s 不是 OpenPass 官方下载地址", name)
			}
			found = true
		}
		if !found {
			return Result{}, fmt.Errorf("最新版本缺少 %s，暂不可更新", name)
		}
	}
	return Result{
		CurrentVersion: current, LatestVersion: latestVersion,
		UpdateAvailable: isNewer(latestParts, currentParts),
		ReleaseURL:      latest.HTMLURL, PublishedAt: latest.PublishedAt,
	}, nil
}
