package bot

import "slices"

// 同机多 Bot 注册表：Bot 列表、按 ID 取运行时。所有 Bot 共享同一个数据目录和 API 渠道。

func (bs *Bots) localProfiles() []BotProfile {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return slices.Clone(bs.profiles)
}

func (bs *Bots) acquireBot(id string) (*Bot, func()) {
	b, _, release := bs.acquireBotEndpoint(id)
	return b, release
}

func (bs *Bots) acquireBotEndpoint(id string) (*Bot, *botEndpoint, func()) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	endpoint := bs.handlers[id]
	if bs.closed || endpoint == nil {
		return nil, nil, func() {}
	}
	endpoint.requests.Add(1)
	return bs.runtimes[id], endpoint, endpoint.requests.Done
}
