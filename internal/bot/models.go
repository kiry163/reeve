package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

// APISettings describes the connection shared by chat and background tasks.
// APIKey is write-only: omitted keeps the key for the same URL; empty clears it.
type APISettings struct {
	BaseURL           string       `json:"base_url"`
	Model             string       `json:"model"`
	APIKey            *string      `json:"api_key,omitempty"`
	HasAPIKey         bool         `json:"has_api_key"`
	Providers         []APIChannel `json:"providers,omitempty"`
	DefaultProviderID string       `json:"default_provider_id,omitempty"`
	AgentProviderID   string       `json:"agent_provider_id,omitempty"`
	AgentModel        string       `json:"agent_model,omitempty"`
	ProviderID        string       `json:"provider_id,omitempty"`
	// EnvOverride 表示模型连接由环境变量（BASE_URL / API_KEY / CHAT_MODEL / AGENT_MODEL）提供，
	// 优先于网页里保存的渠道；网页上的修改要等去掉环境变量后才生效。
	EnvOverride   bool   `json:"env_override,omitempty"`
	EnvModel      string `json:"env_model,omitempty"`
	EnvAgentModel string `json:"env_agent_model,omitempty"`
}

type modelConnection struct {
	baseURL, model, key string
	provider            builtin.Provider
}

// API 渠道存在设备共用的 data/api-channels.json（API Key 用默认 Bot 的主密钥加密），每个 Bot 的选择存在
// 自己的 channels.json。环境变量可以临时覆盖，但不写盘。不做旧版本配置迁移（spec 明确 M1 不迁移）。
func (b *Bot) loadSettings() error {
	if b.cfg.InitialBaseURL != "" {
		model := b.cfg.ChatModel
		if model == "" {
			model = b.cfg.AgentModel
		}
		if model != "" {
			agent := b.cfg.AgentModel
			if agent == "" {
				agent = model
			}
			b.envModel = newConnection(b.cfg.InitialBaseURL, model, b.cfg.InitialKey)
			b.envAgent = newConnection(b.cfg.InitialBaseURL, agent, b.cfg.InitialKey)
		}
	}
	if _, err := os.Stat(b.channelPath()); err == nil {
		return b.loadChannels()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil // 全新安装：等用户在设置里选渠道
}

func newConnection(baseURL, model, key string) modelConnection {
	if normalized, err := normalizeBaseURL(baseURL); err == nil {
		baseURL = normalized
	}
	return modelConnection{baseURL: baseURL, model: model, key: key, provider: newProvider(baseURL, key)}
}

func newProvider(baseURL, key string) builtin.Provider {
	p := builtin.NewOpenAI(baseURL, key)
	p.Client.CheckRedirect = noRedirect
	return p
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("请输入有效的 HTTP 或 HTTPS 接口地址，地址中不要包含密钥、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	for _, endpoint := range []string{"/chat/completions", "/models"} {
		if strings.HasSuffix(u.Path, endpoint) {
			u.Path = strings.TrimSuffix(u.Path, endpoint)
			break
		}
	}
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// resolveModel 返回聊天层（chat=true）或后台 Agent 当前要用的模型：环境变量 > 本 Bot 在共用渠道里的选择 >
// 启动配置。选择的渠道被停用或删除时返回 nil（界面上会提示重新选择）。
func (b *Bot) resolveModel(chat bool) (builtin.Provider, string) {
	b.modelMu.RLock()
	if b.envModel.provider != nil {
		defer b.modelMu.RUnlock()
		if chat {
			return b.envModel.provider, b.envModel.model
		}
		return b.envAgent.provider, b.envAgent.model
	}
	has, id, model := b.hasRoutes, b.agentChannel, b.agentModelName
	if chat {
		id, model = b.defaultChannel, b.chatModel
	}
	b.modelMu.RUnlock()
	if !has {
		if chat {
			return b.cfg.ChatProvider, b.cfg.ChatModel
		}
		return b.cfg.AgentProvider, b.cfg.AgentModel
	}
	c, ok := b.pool.find(id)
	if !ok || !c.Enabled || model == "" {
		return nil, ""
	}
	return c.provider, model
}

// GetSettings 返回本 Bot 的模型选择，以及设备上全部 API 渠道（供选择，密钥不回显）。
func (b *Bot) GetSettings() APISettings {
	b.modelMu.RLock()
	s := APISettings{DefaultProviderID: b.defaultChannel, Model: b.chatModel, AgentProviderID: b.agentChannel, AgentModel: b.agentModelName}
	if s.EnvOverride = b.envModel.provider != nil; s.EnvOverride {
		s.EnvModel, s.EnvAgentModel = b.envModel.model, b.envAgent.model
	}
	b.modelMu.RUnlock()
	s.Providers = b.pool.public()
	if c, ok := b.pool.find(s.DefaultProviderID); ok {
		s.BaseURL, s.HasAPIKey = c.BaseURL, c.key != ""
	}
	return s
}

// SaveSettings 是旧的一体化接口：带 providers 时同时保存设备的渠道池和本 Bot 的选择；只带
// base_url / model / api_key 时更新本 Bot 正在用的渠道（没有时新建「主力 API」）。新网页分开调用
// /api/channels（渠道）和 /api/settings/routes（本 Bot 的选择）。
func (b *Bot) SaveSettings(draft APISettings) (APISettings, error) {
	if draft.Providers != nil {
		return b.saveChannels(draft)
	}
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return APISettings{}, err
	}
	model := strings.TrimSpace(draft.Model)
	if model == "" {
		return APISettings{}, errors.New("请输入模型 ID")
	}
	key := b.catalogKey(draft, baseURL)
	providers := b.pool.public()
	b.modelMu.RLock()
	id, agentID, agentModel := b.defaultChannel, b.agentChannel, b.agentModelName
	b.modelMu.RUnlock()
	idx := slices.IndexFunc(providers, func(p APIChannel) bool { return p.ID == id })
	if idx < 0 {
		id = "primary"
		if idx = slices.IndexFunc(providers, func(p APIChannel) bool { return p.ID == id }); idx < 0 {
			providers = append(providers, APIChannel{ID: id, Name: "主力 API"})
			idx = len(providers) - 1
		}
	}
	p := &providers[idx]
	p.BaseURL, p.Enabled, p.AuthRequired, p.APIKey = baseURL, true, key != "", &key
	if !slices.Contains(p.Models, model) {
		p.Models = append(slices.Clone(p.Models), model)
	}
	p.HiddenModels = slices.DeleteFunc(slices.Clone(p.HiddenModels), func(m string) bool { return m == model })
	if !validRoute(providers, agentID, agentModel) {
		agentID, agentModel = id, model
	}
	return b.saveChannels(APISettings{Providers: providers, DefaultProviderID: id, Model: model, AgentProviderID: agentID, AgentModel: agentModel})
}

var modelCatalogClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}

func (b *Bot) FetchRemoteModels(ctx context.Context, draft APISettings) ([]string, error) {
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return nil, err
	}
	key := b.catalogKey(draft, baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("无法创建模型列表请求")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := modelCatalogClient.Do(req)
	if err != nil {
		return nil, errors.New("无法连接模型服务，请检查接口地址和网络")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("模型服务拒绝访问（HTTP %d），请检查 API Key", resp.StatusCode)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, errors.New("此地址未提供模型列表，请检查接口地址或手动填写模型 ID")
	default:
		return nil, fmt.Errorf("读取模型列表失败（HTTP %d）", resp.StatusCode)
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil || data.Data == nil {
		return nil, errors.New("模型列表格式不兼容，请手动填写模型 ID")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range data.Data {
		if id := strings.TrimSpace(m.ID); id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	slices.Sort(ids)
	return ids, nil
}
