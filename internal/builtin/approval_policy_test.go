package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestValidPolicyRejectsAuto(t *testing.T) {
	if ValidPolicy(Policy("auto")) {
		t.Fatal("auto 已经不是合法策略")
	}
	for _, p := range []Policy{PolicyUntrusted, PolicyOnRequest, PolicyNever} {
		if !ValidPolicy(p) {
			t.Fatalf("%q 应该是合法策略", p)
		}
	}
}

type stubTool struct{ risk Risk }

func (s stubTool) Name() string              { return "stub" }
func (s stubTool) Description() string       { return "" }
func (s stubTool) Schema() json.RawMessage   { return json.RawMessage(`{}`) }
func (s stubTool) Risk(json.RawMessage) Risk { return s.risk }
func (s stubTool) Execute(context.Context, ToolEnv, json.RawMessage) (string, error) {
	return "", nil
}

func TestUntrustedAsksForWrite(t *testing.T) {
	g, err := NewGuard(Rules{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, risk := g.Classify(Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyUntrusted}, stubTool{RiskWrite}, json.RawMessage(`{"path":"a.txt"}`))
	if v.Decision != AskUser {
		t.Fatalf("untrusted 下写操作应询问，得到 %v", v.Decision)
	}
	if risk != RiskWrite {
		t.Fatalf("风险等级应为 write，得到 %v", risk)
	}
}

func TestNeverDeniesDanger(t *testing.T) {
	g, err := NewGuard(Rules{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := g.Classify(Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyNever}, stubTool{RiskDanger}, json.RawMessage(`{"command":"rm -rf /"}`))
	if v.Decision != Deny {
		t.Fatalf("never 下危险操作应拒绝，得到 %v", v.Decision)
	}
}

// never 是"全自动但从不问用户"：除只读外一律拒绝，不弹卡。
func TestNeverDeniesEveryNonRead(t *testing.T) {
	g, err := NewGuard(Rules{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, risk := range []Risk{RiskWrite, RiskExec, RiskDanger} {
		if v, _ := g.Classify(Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyNever}, stubTool{risk}, json.RawMessage(`{"path":"a.txt"}`)); v.Decision != Deny {
			t.Errorf("risk %v under never: want deny, got %v", risk, v.Decision)
		}
	}
	if v, _ := g.Classify(Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyNever}, stubTool{RiskRead}, json.RawMessage(`{"path":"a.txt"}`)); v.Decision != Allow {
		t.Errorf("read-only under never: want allow, got %v", v.Decision)
	}
}

func TestEmptyPolicyDefaultsToUntrusted(t *testing.T) {
	g, err := NewGuard(Rules{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := g.Classify(Task{ID: "t", Workspace: t.TempDir()}, stubTool{RiskWrite}, json.RawMessage(`{"path":"a.txt"}`))
	if v.Decision != AskUser {
		t.Fatalf("空策略应按 untrusted 询问，得到 %v", v.Decision)
	}
	if v, _ := g.Classify(Task{ID: "t", Workspace: t.TempDir()}, stubTool{RiskRead}, json.RawMessage(`{"path":"a.txt"}`)); v.Decision != Allow {
		t.Fatalf("只读操作应放行，得到 %v", v.Decision)
	}
}

// recordingApprover 记下审批请求并按拒绝处理。
type recordingApprover struct{ reqs []ApprovalRequest }

func (r *recordingApprover) RequestApproval(_ context.Context, req ApprovalRequest) (UserDecision, error) {
	r.reqs = append(r.reqs, req)
	return UserDecision{Approve: false}, nil
}

// 没填 Policy 的任务必须按默认的 untrusted 走审批：写操作先问用户，批准前不落盘。
func TestRunDefaultsToUntrusted(t *testing.T) {
	ws := t.TempDir()
	fp := &fakeProvider{replies: []Message{
		call("write_file", map[string]string{"path": "a.txt", "content": "x"}),
		{Role: "assistant", Content: "没有写"},
	}}
	rec := &recordingApprover{}
	a := &Agent{Provider: fp, Guard: newGuard(t, rec)}
	if _, err := a.Run(context.Background(), Task{ID: "t", Input: "写文件", Workspace: ws}); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || rec.reqs[0].ToolName != "write_file" {
		t.Fatalf("空策略下写操作应发出审批请求，得到 %+v", rec.reqs)
	}
	if _, err := os.Stat(filepath.Join(ws, "a.txt")); err == nil {
		t.Fatal("未获批准前不应写文件")
	}
}

type memApprovalStore struct {
	mu   sync.Mutex
	recs map[string]ApprovalRecord
}

func newMemApprovalStore() *memApprovalStore {
	return &memApprovalStore{recs: map[string]ApprovalRecord{}}
}

func (m *memApprovalStore) SaveApproval(_ context.Context, r ApprovalRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[r.ID] = r
	return nil
}

func (m *memApprovalStore) all() []ApprovalRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ApprovalRecord, 0, len(m.recs))
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out
}

// 「批准一次」用过之后必须落盘；重启并 Restore 后同一次调用不能再次放行。
func TestOnceApprovalUsePersistsAcrossRestart(t *testing.T) {
	st := newMemApprovalStore()
	g := newGuard(t, nil)
	g.Store = st
	args := raw(map[string]string{"command": "make deploy"})
	h := ArgsHash(args)
	g.Grant(ApprovalRecord{ID: "r1", TaskID: "t", ToolName: "shell", ArgsHash: h, Decision: Allow, Scope: ScopeOnce})
	rec, ok := g.Consume("t", "shell", h)
	if !ok || !rec.Used {
		t.Fatalf("first use should match and be marked used: %+v %v", rec, ok)
	}
	if got := st.all(); len(got) != 1 || !got[0].Used {
		t.Fatalf("used flag was not persisted: %+v", got)
	}
	// 重启：新 Guard + 从持久记录恢复
	g2 := newGuard(t, nil)
	g2.Store = st
	g2.Restore(st.all())
	if _, ok := g2.Consume("t", "shell", h); ok {
		t.Fatal("a used once-approval must not authorize again after restart")
	}
}

// 实例级白名单通过 SetAllowShell 热更新，编译失败时旧规则必须保留。
func TestAllowShellHotUpdate(t *testing.T) {
	g, err := NewGuard(Rules{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyUntrusted}
	args := raw(map[string]string{"command": "go test ./..."})
	if v, _ := g.Classify(task, ShellTool, args); v.Decision != AskUser {
		t.Fatalf("没有白名单时应询问，得到 %v", v.Decision)
	}
	if err := g.SetAllowShell([]string{`^go test\b`}); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Classify(task, ShellTool, args); v.Decision != Allow {
		t.Fatalf("命中白名单后应放行，得到 %v", v.Decision)
	}
	if err := g.SetAllowShell([]string{`(`}); err == nil {
		t.Fatal("非法正则应该报错")
	}
	if v, _ := g.Classify(task, ShellTool, args); v.Decision != Allow {
		t.Fatalf("热更新失败后应保留旧规则，得到 %v", v.Decision)
	}
}

// 允许名单只放行单条简单命令；复合命令必须继续走审批，避免 `go test; rm -rf /` 被前缀匹配放行。
func TestAllowShellRejectsCompoundCommands(t *testing.T) {
	g, err := NewGuard(Rules{AllowShell: []string{`^go test.*$`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "t", Workspace: ".", Policy: PolicyUntrusted}
	if v, _ := g.Classify(task, ShellTool, raw(map[string]string{"command": "go test ./..."})); v.Decision != Allow {
		t.Fatalf("简单命令应放行，得到 %v", v.Decision)
	}
	for _, cmd := range []string{
		"go test ./...; touch x",
		"go test ./... && echo ok",
		"go test ./... || true",
		"go test ./... | head",
		"go test $(pwd)",
		"go test `pwd`",
		"go test ./... > out",
		"go test ./...\necho x",
	} {
		if v, _ := g.Classify(task, ShellTool, raw(map[string]string{"command": cmd})); v.Decision != AskUser {
			t.Errorf("%q 应继续询问，得到 %v", cmd, v.Decision)
		}
	}
	// 非空 env 可以改 PATH/BASH_ENV/LD_PRELOAD，不能借白名单自动执行。
	withEnv := raw(map[string]any{"command": "go test ./...", "env": map[string]string{"PATH": "/tmp/evil"}})
	if v, _ := g.Classify(task, ShellTool, withEnv); v.Decision != AskUser {
		t.Errorf("带 env 的命令应继续询问，得到 %v", v.Decision)
	}
	emptyEnv := raw(map[string]any{"command": "go test ./...", "env": map[string]string{}})
	if v, _ := g.Classify(task, ShellTool, emptyEnv); v.Decision != Allow {
		t.Errorf("空 env 不应影响白名单，得到 %v", v.Decision)
	}
}
