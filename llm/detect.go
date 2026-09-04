package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/retrofilter/rf/models"
)

func (c *Chat) ollamaContextWindow(cfg Config) int {
	if cfg.Provider != models.ProviderOllama {
		return 0
	}
	key := cfg.BaseURL + " " + cfg.Model
	if w, ok := c.ollamaCtx[key]; ok {
		return w
	}
	w := probeOllamaContext(cfg.BaseURL, cfg.Model)
	if c.ollamaCtx == nil {
		c.ollamaCtx = make(map[string]int)
	}
	c.ollamaCtx[key] = w
	return w
}

func probeOllamaContext(baseURL, model string) int {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var show struct {
		Parameters string         `json:"parameters"`
		ModelInfo  map[string]any `json:"model_info"`
	}
	if json.NewDecoder(resp.Body).Decode(&show) != nil {
		return 0
	}
	for line := range strings.SplitSeq(show.Parameters, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "num_ctx" {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	for k, v := range show.ModelInfo {
		if n, ok := v.(float64); ok && n > 0 && strings.HasSuffix(k, ".context_length") {
			return int(n)
		}
	}
	return 0
}
