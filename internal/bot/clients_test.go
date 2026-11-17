package bot

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/auth"
)

// Test real authenticated HTTP using browser/WebView cookie jars and the native
// client's explicit Cookie header. No model or client UI is involved.
func TestClientsShareAuthenticatedMessagesAndResumeEvents(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	bs := &Bots{main: b, runtimes: map[string]*Bot{defaultBotID: b},
		handlers: map[string]*botEndpoint{defaultBotID: {Handler: b.Handler("")}}}
	if err := auth.SetPassword(b.cfg.DataDir, "test-password"); err != nil {
		t.Fatal(err)
	}
	guard := &auth.Guard{DataDir: b.cfg.DataDir, SessionTTL: time.Hour}
	srv := httptest.NewServer(guard.Middleware(bs.Handler(nil)))
	defer srv.Close()

	request := func(client *http.Client, base, method, path, body, cookie string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		} else {
			req.Header.Set("Origin", base)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, resp.StatusCode, want)
		}
		return resp
	}
	cookieFrom := func(resp *http.Response) string {
		for _, c := range resp.Cookies() {
			if c.Name == auth.CookieName {
				return c.Name + "=" + c.Value
			}
		}
		return ""
	}
	login := func(client *http.Client, password string, want int) (string, string) {
		t.Helper()
		resp := request(client, srv.URL, "POST", "/api/auth/login", `{"password":"`+password+`"}`, "", want)
		if want != 200 {
			return "", ""
		}
		var out struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
			t.Fatalf("login response: %+v %v", out, err)
		}
		return cookieFrom(resp), out.Token
	}
	native := &http.Client{Timeout: 30 * time.Second}
	request(native, srv.URL, "GET", "/api/health", "", "", 200).Body.Close()
	request(native, srv.URL, "GET", "/api/capabilities", "", "", 401).Body.Close()
	clients := []*http.Client{nil, nil, native}
	for i := range 2 {
		jar, _ := cookiejar.New(nil)
		clients[i] = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	}
	cookie, token := "", ""
	for i, client := range clients {
		c, tok := login(client, "test-password", 200)
		if i == 2 {
			cookie, token = c, tok
		}
	}
	if cookie == "" || token == "" {
		t.Fatal("native login did not return a session")
	}
	resp := request(native, srv.URL, "GET", "/api/capabilities", "", cookie, 200)
	var caps struct {
		Version int      `json:"api_version"`
		Clients []string `json:"clients"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil || caps.Version != 1 || strings.Join(caps.Clients, ",") != "web,android" {
		t.Fatalf("invalid capabilities: %+v, %v", caps, err)
	}
	resp.Body.Close()
	// Bearer 会话令牌与 Cookie 是同一种凭据。
	if req, _ := http.NewRequest("GET", srv.URL+"/api/capabilities", nil); req != nil {
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := native.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("Bearer 会话令牌应 200，得到 %d", resp.StatusCode)
		}
	}

	base := "/api/bots/main"
	for i, client := range clients {
		credential := ""
		if i == 2 {
			credential = cookie
		}
		request(client, srv.URL, "POST", base+"/messages", `{"text":"client-`+strconv.Itoa(i)+`"}`, credential, 200).Body.Close()
	}
	if len(b.store.Messages()) != 3 {
		t.Fatal("clients did not write into the same conversation")
	}

	replay := func(client *http.Client, cookie, cursor string, header bool) []Event {
		t.Helper()
		path := srv.URL + base + "/stream"
		if cursor != "" && !header {
			path += "?after=" + cursor
		}
		req, _ := http.NewRequest("GET", path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if header {
			req.Header.Set("Last-Event-ID", cursor)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("stream: %d", resp.StatusCode)
		}
		var events []Event
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Kind == "replay.done" {
				return events
			}
			events = append(events, event)
		}
		t.Fatalf("stream ended before replay.done: %v", scanner.Err())
		return nil
	}
	events := replay(clients[1], "", "", false)
	if len(events) != 3 {
		t.Fatalf("WebView replay: got %d events", len(events))
	}
	cursor := strconv.FormatInt(events[len(events)-1].Seq, 10)
	request(native, srv.URL, "POST", base+"/messages", `{"text":"after-disconnect"}`, cookie, 200).Body.Close()
	for _, header := range []bool{false, true} {
		resumed := replay(native, cookie, cursor, header)
		if len(resumed) != 1 || !strings.Contains(string(resumed[0].Data), "after-disconnect") {
			t.Fatalf("resume header=%t: %+v", header, resumed)
		}
	}

	// 改密后：旧 Cookie / 旧 Bearer 立即失效，新密码可登录，新会话可用。
	resp = request(native, srv.URL, "POST", "/api/auth/password",
		`{"current_password":"test-password","new_password":"next-password"}`, cookie, 200)
	freshCookie := cookieFrom(resp)
	var fresh struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fresh); err != nil || fresh.Token == "" {
		t.Fatalf("改密响应不对：%v", err)
	}
	resp.Body.Close()
	if freshCookie == "" {
		t.Fatal("改密应刷新会话 Cookie")
	}
	request(native, srv.URL, "GET", base+"/info", "", cookie, 401).Body.Close()
	introspect := func(headers map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+base+"/info", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Origin", srv.URL)
		r, err := native.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if got := introspect(map[string]string{"Authorization": "Bearer " + token}); got != 401 {
		t.Fatalf("改密后旧 Bearer 应 401，得到 %d", got)
	}
	request(native, srv.URL, "GET", base+"/info", "", freshCookie, 200).Body.Close()
	if got := introspect(map[string]string{"Authorization": "Bearer " + fresh.Token}); got != 200 {
		t.Fatalf("改密后的新 Bearer 应 200，得到 %d", got)
	}
	login(native, "test-password", 401)
	login(native, "next-password", 200)
}
