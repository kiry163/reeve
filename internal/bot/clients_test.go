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
	tok, _, err := auth.EnsureToken(b.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	// LoopbackFree 关掉，这样 401 边界仍然会被真实校验（httptest 的来源是回环）。
	guard := &auth.Guard{Token: tok, LoopbackFree: false, SessionTTL: time.Hour}
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
	native := &http.Client{Timeout: 30 * time.Second}
	request(native, srv.URL, "GET", "/api/health", "", "", 200).Body.Close()
	request(native, srv.URL, "GET", "/api/capabilities", "", "", 401).Body.Close()
	clients := []*http.Client{nil, nil, native}
	for i := range 2 {
		jar, _ := cookiejar.New(nil)
		clients[i] = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	}
	cookie := ""
	for i, client := range clients {
		resp := request(client, srv.URL, "POST", "/api/auth/token", `{"token":"`+tok+`"}`, "", 200)
		if i == 2 {
			cookie = cookieFrom(resp)
		}
		resp.Body.Close()
	}
	if cookie == "" {
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

	// token 轮换后：旧 token 换不到会话，旧 Cookie 失效，新 token 正常工作。
	next, err := auth.RotateToken(b.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	guard2 := &auth.Guard{Token: next, LoopbackFree: false, SessionTTL: time.Hour}
	srv2 := httptest.NewServer(guard2.Middleware(bs.Handler(nil)))
	defer srv2.Close()
	request(native, srv2.URL, "POST", "/api/auth/token", `{"token":"`+tok+`"}`, "", 401).Body.Close()
	request(native, srv2.URL, "GET", base+"/info", "", cookie, 401).Body.Close()
	resp = request(native, srv2.URL, "POST", "/api/auth/token", `{"token":"`+next+`"}`, "", 200)
	fresh := cookieFrom(resp)
	resp.Body.Close()
	if fresh == "" {
		t.Fatal("轮换后的 token 应能换到新会话")
	}
	request(native, srv2.URL, "GET", base+"/info", "", fresh, 200).Body.Close()
}
