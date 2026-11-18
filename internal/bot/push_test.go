package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

type ntfyHit struct {
	auth    string
	payload map[string]any
}

// newFakeNtfy 记录每次 publish 的 Authorization 和 JSON body。
func newFakeNtfy(t *testing.T) (*httptest.Server, <-chan ntfyHit) {
	t.Helper()
	hits := make(chan ntfyHit, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		hits <- ntfyHit{auth: r.Header.Get("Authorization"), payload: payload}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func waitHit(t *testing.T, hits <-chan ntfyHit) ntfyHit {
	t.Helper()
	select {
	case h := <-hits:
		return h
	case <-time.After(3 * time.Second):
		t.Fatal("没有收到 ntfy 请求")
		return ntfyHit{}
	}
}

func noHit(t *testing.T, hits <-chan ntfyHit, d time.Duration) {
	t.Helper()
	select {
	case h := <-hits:
		t.Fatalf("不应该收到通知，却收到了：%+v", h.payload)
	case <-time.After(d):
	}
}

func enablePush(t *testing.T, bs *Bots, rawURL, token string) {
	t.Helper()
	draft := pushDraft{
		Enabled: true, URL: rawURL, Topic: "reeve-test", PublicHost: "home.example.com:18800",
		Approval: true, Task: true, Reply: true,
	}
	if token != "" {
		draft.Token = &token
	}
	if _, err := bs.push.update(draft); err != nil {
		t.Fatalf("保存推送配置失败：%v", err)
	}
}

func TestPushConfigAPI(t *testing.T) {
	bs, h := newEnvServer(t)

	w, body := envRequest(t, h, http.MethodGet, "/api/push", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/push = %d: %s", w.Code, body)
	}
	var view pushView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if view.Enabled || !view.Approval || !view.Task || !view.Reply || view.HasToken {
		t.Fatalf("默认配置不对：%+v", view)
	}

	token := "tk_secret"
	draft := pushDraft{
		Enabled: true, URL: "https://ntfy.example.com", Topic: "reeve-test",
		PublicHost: "https://home.example.com:18800", Approval: true, Task: false, Reply: true, Token: &token,
	}
	raw, _ := json.Marshal(draft)
	w, body = envRequest(t, h, http.MethodPut, "/api/push", string(raw))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/push = %d: %s", w.Code, body)
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || !view.HasToken || view.PublicHost != "home.example.com:18800" || view.Task {
		t.Fatalf("保存后的配置不对：%+v", view)
	}
	if strings.Contains(body, token) {
		t.Fatal("GET/PUT 响应泄露了 token")
	}
	file := filepath.Join(bs.main.cfg.DataDir, "push.json")
	rawFile, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawFile), token) || !strings.Contains(string(rawFile), "sealed_token") {
		t.Fatalf("push.json 不应包含明文 token：%s", rawFile)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("push.json 权限应为 0600：%v %v", st, err)
	}

	// 省略 token = 保留
	draft.Token = nil
	raw, _ = json.Marshal(draft)
	w, body = envRequest(t, h, http.MethodPut, "/api/push", string(raw))
	if w.Code != http.StatusOK {
		t.Fatalf("保留 token 失败：%d %s", w.Code, body)
	}
	_ = json.Unmarshal([]byte(body), &view)
	if !view.HasToken {
		t.Fatal("省略 token 时应该保留旧 token")
	}

	// 空串 = 清除
	empty := ""
	draft.Token = &empty
	raw, _ = json.Marshal(draft)
	w, body = envRequest(t, h, http.MethodPut, "/api/push", string(raw))
	if w.Code != http.StatusOK {
		t.Fatalf("清除 token 失败：%d %s", w.Code, body)
	}
	_ = json.Unmarshal([]byte(body), &view)
	if view.HasToken {
		t.Fatal("空串 token 应该清除")
	}

	// 非法 URL / topic
	for _, bad := range []string{
		`{"enabled":true,"url":"https://ntfy.example.com/path","topic":"reeve-test","approval":true,"task":true,"reply":true}`,
		`{"enabled":true,"url":"https://ntfy.example.com","topic":"with space","approval":true,"task":true,"reply":true}`,
		`{"enabled":true,"url":"","topic":"reeve-test","approval":true,"task":true,"reply":true}`,
	} {
		w, body = envRequest(t, h, http.MethodPut, "/api/push", bad)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法配置应返回 400，得到 %d：%s", w.Code, body)
		}
	}
}

func TestPushTestEndpoint(t *testing.T) {
	bs, h := newEnvServer(t)
	srv, hits := newFakeNtfy(t)
	enablePush(t, bs, srv.URL, "tk_test")

	w, body := envRequest(t, h, http.MethodPost, "/api/push/test", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/push/test = %d: %s", w.Code, body)
	}
	hit := waitHit(t, hits)
	if hit.auth != "Bearer tk_test" {
		t.Fatalf("Authorization = %q", hit.auth)
	}
	if hit.payload["topic"] != "reeve-test" || hit.payload["message"] != "reeve 推送测试" {
		t.Fatalf("测试通知内容不对：%+v", hit.payload)
	}
	if click, _ := hit.payload["click"].(string); !strings.HasPrefix(click, "reeve://open") {
		t.Fatalf("测试通知 click 不对：%q", click)
	}
}

func TestPushEventNotices(t *testing.T) {
	bs, _ := newEnvServer(t)
	srv, hits := newFakeNtfy(t)
	enablePush(t, bs, srv.URL, "")
	bs.push.replyDelay = 20 * time.Millisecond

	// 任务终态：标题来自 TaskRec
	bs.main.store.Append("task.created", TaskRec{ID: "task_1", Title: "world", Status: "running"}, false)
	bs.main.store.Append("agent", builtin.Event{
		TaskID: "task_1", Type: builtin.EvTaskCompleted,
		Data: builtin.Result{TaskID: "task_1", Status: builtin.StatusCompleted},
	}, false)
	hit := waitHit(t, hits)
	if hit.payload["message"] != "任务「world」完成了" {
		t.Fatalf("任务通知 = %+v", hit.payload)
	}
	if click, _ := hit.payload["click"].(string); !strings.Contains(click, "bot=main") || !strings.Contains(click, "host=home.example.com%3A18800") {
		t.Fatalf("任务通知 click = %q", click)
	}
	if hit.payload["priority"].(float64) != 3 {
		t.Fatalf("任务优先级 = %v", hit.payload["priority"])
	}

	// 同一任务的 task.updated 终态不应重复推
	bs.main.store.Append("task.updated", TaskRec{ID: "task_1", Status: "completed"}, false)
	noHit(t, hits, 150*time.Millisecond)

	// 审批：立即推，高优先级；chat_ 任务也推
	bs.main.store.Append("agent", builtin.Event{
		TaskID: "chat_1", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "ap_1", TaskID: "chat_1", ToolName: "shell"},
	}, false)
	hit = waitHit(t, hits)
	if hit.payload["message"] != "有新的审批请求：shell" || hit.payload["priority"].(float64) != 4 {
		t.Fatalf("审批通知 = %+v", hit.payload)
	}

	// 聊天层内部任务的终态没有 TaskRec，不推
	bs.main.store.Append("agent", builtin.Event{
		TaskID: "chat_1", Type: builtin.EvTaskCompleted,
		Data: builtin.Result{TaskID: "chat_1", Status: builtin.StatusCompleted},
	}, false)
	noHit(t, hits, 150*time.Millisecond)

	// 回复：防抖，两条只推一条
	bs.main.store.Append("message", Message{ID: "msg_1", Role: "assistant", Text: "第一段", CreatedAt: time.Now()}, false)
	bs.main.store.Append("message", Message{ID: "msg_2", Role: "assistant", Text: "第二段", CreatedAt: time.Now()}, false)
	hit = waitHit(t, hits)
	if hit.payload["message"] != "助手有新回复" || hit.payload["priority"].(float64) != 2 {
		t.Fatalf("回复通知 = %+v", hit.payload)
	}
	noHit(t, hits, 150*time.Millisecond)

	// 已隐藏的消息不再推
	bs.main.store.Append("message", Message{ID: "msg_3", Role: "assistant", Text: "会被替换", CreatedAt: time.Now()}, false)
	bs.main.store.Append("message.hidden", []string{"msg_3"}, false)
	noHit(t, hits, 150*time.Millisecond)

	// internal 事件不推
	bs.main.store.Append("agent", builtin.Event{
		TaskID: "chat_2", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "ap_2", TaskID: "chat_2", ToolName: "write_file"},
	}, true)
	noHit(t, hits, 150*time.Millisecond)
}

func TestPushStartupSkipsHistory(t *testing.T) {
	bs, _ := newEnvServer(t)
	// 第一条 manager 是默认配置（关闭），不会发。
	bs.main.store.Append("agent", builtin.Event{
		TaskID: "chat_old", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "ap_old", TaskID: "chat_old", ToolName: "shell"},
	}, false)

	srv, hits := newFakeNtfy(t)
	m := newPushManager(bs.main.cfg.DataDir, bs.main.vault)
	t.Cleanup(m.close)
	if _, err := m.update(pushDraft{
		Enabled: true, URL: srv.URL, Topic: "reeve-test", Approval: true, Task: true, Reply: false,
	}); err != nil {
		t.Fatal(err)
	}
	m.attach(bs.main)
	noHit(t, hits, 150*time.Millisecond)

	bs.main.store.Append("agent", builtin.Event{
		TaskID: "chat_new", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "ap_new", TaskID: "chat_new", ToolName: "shell"},
	}, false)
	if hit := waitHit(t, hits); hit.payload["message"] != "有新的审批请求：shell" {
		t.Fatalf("新事件通知 = %+v", hit.payload)
	}
}

func TestNormalizePushDraft(t *testing.T) {
	enabled := pushDraft{Enabled: true, URL: "https://ntfy.example.com/", Topic: "reeve-test",
		PublicHost: "https://home.example.com:18800", Approval: true, Task: true, Reply: true}
	cfg, err := normalizePushDraft(enabled)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://ntfy.example.com" || cfg.PublicHost != "home.example.com:18800" {
		t.Fatalf("归一化结果 = %+v", cfg)
	}
	if _, err := normalizePushDraft(pushDraft{Enabled: true, Topic: "x"}); err == nil {
		t.Fatal("启用但缺 URL 应报错")
	}
	if _, err := normalizePushDraft(pushDraft{URL: "ftp://ntfy.example.com"}); err == nil {
		t.Fatal("非 http(s) 应报错")
	}
	if _, err := normalizePushDraft(pushDraft{URL: "https://ntfy.example.com", Topic: "bad topic"}); err == nil {
		t.Fatal("非法 topic 应报错")
	}
}
