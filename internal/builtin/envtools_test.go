package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSetCall struct{ name, value, source string }
type fakeUnsetCall struct{ name, source string }

type fakeEnvStore struct {
	env      map[string]string
	viewErr  error
	setCalls []fakeSetCall
	unset    []fakeUnsetCall
	views    int
}

func (f *fakeEnvStore) ListEnv() (map[string]string, error) {
	f.views++
	if f.viewErr != nil {
		return nil, f.viewErr
	}
	return f.env, nil
}

func (f *fakeEnvStore) SetInstanceEnv(name, value, source string) error {
	f.setCalls = append(f.setCalls, fakeSetCall{name, value, source})
	return nil
}

func (f *fakeEnvStore) UnsetInstanceEnv(name, source string) error {
	f.unset = append(f.unset, fakeUnsetCall{name, source})
	return nil
}

func TestEnvToolsRiskLevels(t *testing.T) {
	cases := []struct {
		tool Tool
		want Risk
		args string
	}{
		{listEnvTool, RiskRead, `{}`},
		{setEnvTool, RiskWrite, `{"name":"A","value":"1"}`},
		{unsetEnvTool, RiskWrite, `{"name":"A"}`},
	}
	for _, c := range cases {
		if got := c.tool.Risk(json.RawMessage(c.args)); got != c.want {
			t.Errorf("%s 风险等级应为 %v，得到 %v", c.tool.Name(), c.want, got)
		}
	}
}

func TestEnvToolsRegisteredByDefault(t *testing.T) {
	r := NewRegistry(DefaultTools()...)
	for _, name := range []string{"list_env", "set_env", "unset_env"} {
		if r.Get(name) == nil {
			t.Errorf("DefaultTools 未注册 %s", name)
		}
	}
}

func TestEnvToolsRejectInvalidArgsWithoutTouchingStore(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args string
	}{
		{"变量名非法", setEnvTool, `{"name":"1BAD","value":"x"}`},
		{"变量名含空格", unsetEnvTool, `{"name":"BAD NAME"}`},
		{"值含换行", setEnvTool, `{"name":"OK","value":"a\nb"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &fakeEnvStore{env: map[string]string{}}
			_, err := c.tool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(c.args))
			if err == nil {
				t.Fatal("非法参数应报错")
			}
			if len(st.setCalls) != 0 || len(st.unset) != 0 {
				t.Fatalf("校验失败时不应调用 EnvStore：set=%v unset=%v", st.setCalls, st.unset)
			}
		})
	}
}

func TestEnvToolsWithoutStore(t *testing.T) {
	cases := []struct {
		tool Tool
		args string
	}{
		{listEnvTool, `{}`},
		{setEnvTool, `{"name":"A","value":"1"}`},
		{unsetEnvTool, `{"name":"A"}`},
	}
	for _, c := range cases {
		_, err := c.tool.Execute(context.Background(), ToolEnv{}, json.RawMessage(c.args))
		if err == nil || !strings.Contains(err.Error(), "不支持") {
			t.Errorf("%s 在 EnvStore 为 nil 时应返回明确错误，得到 %v", c.tool.Name(), err)
		}
	}
}

func TestSetEnvPersistsAsBuiltin(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{}}
	out, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{"name":"HTTPS_PROXY","value":"http://proxy:8080"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已设置 HTTPS_PROXY") || !strings.Contains(out, "下一个任务生效") {
		t.Fatalf("成功提示不完整：%q", out)
	}
	if len(st.setCalls) != 1 {
		t.Fatalf("应写入一次，得到 %v", st.setCalls)
	}
	if got := st.setCalls[0]; got.name != "HTTPS_PROXY" || got.value != "http://proxy:8080" || got.source != "builtin" {
		t.Fatalf("写入参数不对：%+v", got)
	}
}

func TestSetEnvRejectsSecretRefs(t *testing.T) {
	if !handlesSecrets(setEnvTool) {
		t.Fatal("set_env 必须声明 PassSecrets，避免 Guard 把 vault 明文替换进参数")
	}
	for _, v := range []string{
		"{{secret:PG_PASS}}",
		"postgres://user:{{secret:PG_PASS}}@db:5432/app",
		"{{secret:BAD}}",
	} {
		st := &fakeEnvStore{env: map[string]string{}}
		_, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st},
			json.RawMessage(`{"name":"DATABASE_URL","value":`+mustJSON(t, v)+`}`))
		if err == nil {
			t.Fatalf("%q 应被拒绝", v)
		}
		if !strings.Contains(err.Error(), "密钥永不进入 env 存储") || !strings.Contains(err.Error(), "shell") {
			t.Errorf("错误文案应说明密钥不落 env 及正确用法，得到 %q", err)
		}
		if len(st.setCalls) != 0 {
			t.Errorf("%q 被拒绝时不应写 EnvStore：%v", v, st.setCalls)
		}
	}
}

func TestSetEnvHighEntropyLiteralOnlyWarns(t *testing.T) {
	const literal = "9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c"
	st := &fakeEnvStore{env: map[string]string{}}
	out, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st},
		json.RawMessage(`{"name":"LITERAL","value":"`+literal+`"}`))
	if err != nil {
		t.Fatalf("高熵字面值不应被拒绝：%v", err)
	}
	if !strings.Contains(out, "已设置 LITERAL") || !strings.Contains(out, "这个值看起来像密钥") {
		t.Fatalf("应成功并告警：%q", out)
	}
	if len(st.setCalls) != 1 || st.setCalls[0].value != literal {
		t.Fatalf("应原样写入：%v", st.setCalls)
	}
}

func TestSetEnvEmptyValueClears(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{"A": "1"}}
	out, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{"name":"A","value":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已清空 A") || !strings.Contains(out, "unset_env") {
		t.Fatalf("空值应提示已清空，而不是已设置：%q", out)
	}
	if len(st.setCalls) != 1 || st.setCalls[0].value != "" {
		t.Fatalf("应以空值写入：%v", st.setCalls)
	}
}

func TestSetEnvMissingValueRejected(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{}}
	for _, args := range []string{`{"name":"A"}`, `{"name":"A","value":null}`} {
		_, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "需要 value") || !strings.Contains(err.Error(), "unset_env") {
			t.Errorf("%s 缺 value 时应报错并指向 unset_env，得到 %v", args, err)
		}
	}
	if len(st.setCalls) != 0 {
		t.Fatalf("缺 value 时不应写 EnvStore：%v", st.setCalls)
	}
}

func TestUnsetEnvListErrorBlocksDelete(t *testing.T) {
	st := &fakeEnvStore{viewErr: errors.New("实例级 env 第 3 行不合法")}
	_, err := unsetEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{"name":"A"}`))
	if err == nil || !strings.Contains(err.Error(), "实例级 env") {
		t.Fatalf("读取失败时应报错，得到 %v", err)
	}
	if len(st.unset) != 0 {
		t.Fatalf("读取失败时不应盲目删除：%v", st.unset)
	}
}

func TestUnsetEnvMissingDoesNotWrite(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{}}
	out, err := unsetEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{"name":"NOPE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "本来就没有 NOPE") {
		t.Fatalf("提示不对：%q", out)
	}
	if len(st.unset) != 0 {
		t.Fatalf("不存在时不应写事件：%v", st.unset)
	}
}

func TestUnsetEnvRemovesVariable(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{"A": "1"}}
	out, err := unsetEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{"name":"A"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已删除 A") {
		t.Fatalf("成功提示不对：%q", out)
	}
	if len(st.unset) != 1 || st.unset[0].name != "A" || st.unset[0].source != "builtin" {
		t.Fatalf("删除调用不对：%v", st.unset)
	}
}

func TestSetEnvSecretWarning(t *testing.T) {
	secretish := []string{
		"sk-live-abcdef123456",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"xoxb-123456789012-abcdef",
		"AKIAIOSFODNN7EXAMPLE",
		"9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c",
	}
	ordinary := []string{
		"hello",
		"http://proxy:8080",
		"/usr/local/bin:/usr/bin:/bin",
		"abcabcabcabcabcabcabcabcabcabcabc",
	}
	for _, v := range secretish {
		if !LooksLikeSecret(v) {
			t.Errorf("%q 应被判定为疑似密钥", v)
		}
		st := &fakeEnvStore{env: map[string]string{}}
		out, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st},
			json.RawMessage(`{"name":"TOKEN","value":`+mustJSON(t, v)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "这个值看起来像密钥") || !strings.Contains(out, "让用户在聊天里保存") {
			t.Errorf("%q 的成功文本应带密钥告警：%q", v, out)
		}
	}
	for _, v := range ordinary {
		if LooksLikeSecret(v) {
			t.Errorf("%q 不应被判定为疑似密钥", v)
		}
		st := &fakeEnvStore{env: map[string]string{}}
		out, err := setEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st},
			json.RawMessage(`{"name":"NOTE","value":`+mustJSON(t, v)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "看起来像密钥") {
			t.Errorf("%q 的普通值不应告警：%q", v, out)
		}
	}
}

func TestListEnvShowsInstanceOnly(t *testing.T) {
	st := &fakeEnvStore{env: map[string]string{"INSTANCE_VAR": "iv"}}
	out, err := listEnvTool.Execute(context.Background(),
		ToolEnv{EnvStore: st, Env: map[string]string{"CALL_VAR": "cv"}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"实例级", "INSTANCE_VAR=iv", "{{secret:NAME}}"} {
		if !strings.Contains(out, want) {
			t.Errorf("list_env 输出缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "bot 级") {
		t.Errorf("list_env 不应再输出 bot 级：\n%s", out)
	}
	if strings.Contains(out, "CALL_VAR") {
		t.Errorf("list_env 不应打印调用级变量：\n%s", out)
	}
}

func TestListEnvPropagatesViewError(t *testing.T) {
	st := &fakeEnvStore{viewErr: errors.New("磁盘坏了")}
	_, err := listEnvTool.Execute(context.Background(), ToolEnv{EnvStore: st}, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "磁盘坏了") {
		t.Fatalf("应返回读取错误，得到 %v", err)
	}
}

func mustJSON(t *testing.T, v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
