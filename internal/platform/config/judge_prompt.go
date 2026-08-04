package config

const maxJudgePromptBytes = 32 * 1024

// LoadPrompt 在评测进程启动时加载并缓存 Judge 指令。
func (c JudgeModelConfig) LoadPrompt() (string, error) {
	return loadPromptFile("models.judge", "prompt", c.PromptFile, maxJudgePromptBytes)
}
