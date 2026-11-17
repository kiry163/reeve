package cli

import (
	"slices"
	"strings"

	"github.com/kiry163/reeve/internal/exec"
)

const (
	// maxArgvPrompt 是提示词走位置参数时的长度上限（omp 的提示词在 argv 里）。
	maxArgvPrompt      = 24_000
	promptSystemLimit  = 4500
	promptHistoryLimit = 5200
	historyOmitted     = "（更早的对话已省略）"
)

// buildPrompt 拼出外部 CLI 的整段提示：system（Instructions）+ 先前上下文 + 当前任务。
// 上下文的预算固定，超长时丢最旧的对话，保证当前任务永远是完整的。
func buildPrompt(req exec.Request) string {
	var b strings.Builder
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		b.WriteString(truncate(instructions, promptSystemLimit))
		b.WriteString("\n\n")
	}
	if history := renderHistory(req.History); history != "" {
		b.WriteString("先前上下文：\n")
		b.WriteString(history)
		b.WriteString("\n\n")
	}
	b.WriteString("当前任务：\n")
	b.WriteString(req.Input)
	return b.String()
}

// renderHistory 从最新往回取，超预算就丢掉最旧的部分并标注省略。
func renderHistory(history []string) string {
	kept := make([]string, 0, len(history))
	total := 0
	for i := len(history) - 1; i >= 0; i-- {
		line := strings.TrimSpace(history[i])
		if line == "" {
			continue
		}
		if total+len(line)+1 > promptHistoryLimit {
			if len(kept) == 0 {
				kept = append(kept, truncate(line, promptHistoryLimit))
			} else {
				kept = append(kept, historyOmitted)
			}
			break
		}
		kept = append(kept, line)
		total += len(line) + 1
	}
	slices.Reverse(kept)
	return strings.Join(kept, "\n")
}
