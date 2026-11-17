//go:build !windows

package bot

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/exec/cli"
)

// 安装轮询走 ?probe=0：只刷新安装进度，不起 `--version` 子进程。
func TestConnectionsProbeOptOutSkipsVersionProbe(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("REEVE_PROBE_CALLS", calls)
	writeFakeCLI(t, bin, "pi", "#!/bin/sh\necho called >> \"$REEVE_PROBE_CALLS\"\necho 'pi 0.12.3'\n")

	b := newBotWith(t, t.TempDir(), nil, nil)
	t.Cleanup(b.Close)
	h := b.Handler("")
	get := func(url string) connectionsView {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", url, w.Code, w.Body)
		}
		var v connectionsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return v
	}
	piOf := func(v connectionsView) cli.Status {
		t.Helper()
		for _, st := range v.CLIs {
			if st.ID == "pi" {
				return st
			}
		}
		t.Fatal("检测结果里缺少 pi")
		return cli.Status{}
	}
	countCalls := func() int {
		t.Helper()
		raw, err := os.ReadFile(calls)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "called")
	}

	if pi := piOf(get("/api/settings/connections?probe=0")); !pi.Installed || pi.Version != "" || pi.Available || pi.Probed {
		t.Fatalf("probe=0 不该探测，且 Probed 必须为 false：%+v", pi)
	}
	if n := countCalls(); n != 0 {
		t.Fatalf("probe=0 起了 %d 次 --version", n)
	}
	if pi := piOf(get("/api/settings/connections")); pi.Version != "0.12.3" || !pi.Available || !pi.Probed {
		t.Fatalf("默认应探测出版本：%+v", pi)
	}
	if n := countCalls(); n != 1 {
		t.Fatalf("默认请求起了 %d 次 --version", n)
	}
	// 轮询再次 probe=0：版本取缓存，不新增子进程。
	if pi := piOf(get("/api/settings/connections?probe=0")); pi.Version != "0.12.3" || !pi.Probed || !pi.Available {
		t.Fatalf("probe=0 应返回缓存版本（Probed=true）：%+v", pi)
	}
	if n := countCalls(); n != 1 {
		t.Fatalf("probe=0 起了 %d 次 --version", n)
	}
}
