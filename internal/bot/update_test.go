package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, current, want string
		code                int
	}{
		{"newer release", "v1.9.9", "available", 200},
		{"same release", "v1.10.0", "current", 200},
		{"current is newer", "v2.0.0", "current", 200},
		{"local prerelease", "v1.10.0-beta.1", "available", 200},
		{"local build", "开发版", "unversioned", 200},
		{"no published release", "v1.0.0", "no_release", 404},
		{"rate limited", "v1.0.0", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.10.0"}`))}, nil
			})}
			status, err := checkUpdate(context.Background(), client, latestReleaseAPI, tc.current)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected a check error, not an up-to-date result")
				}
			} else if err != nil || status.State != tc.want {
				t.Fatalf("checkUpdate = %+v, %v", status, err)
			}
		})
	}
}

// 更新检查用的 release API 与页面展示的 releases 地址必须指向同一个仓库，
// 否则版本比较会拿到另一个项目的 tag。
func TestUpdateEndpointsShareRepo(t *testing.T) {
	repo := strings.TrimSuffix(strings.TrimPrefix(releasesURL, "https://github.com/"), "/releases")
	var requested string
	client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		requested = r.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.10.0"}`))}, nil
	})}
	status, err := checkUpdate(context.Background(), client, latestReleaseAPI, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.github.com/repos/" + repo + "/releases/latest"
	if latestReleaseAPI != want {
		t.Fatalf("latestReleaseAPI = %q，想要 %q", latestReleaseAPI, want)
	}
	if requested != want {
		t.Fatalf("checkUpdate 请求了 %q，想要 %q", requested, want)
	}
	if status.ReleaseURL != releasesURL+"/tag/v1.10.0" {
		t.Fatalf("ReleaseURL = %q", status.ReleaseURL)
	}
}
