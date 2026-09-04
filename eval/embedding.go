package eval

import (
	"github.com/retrofilter/rf/core"
)

func syncEmbeddings(env *Environment, gs *core.GraphStore) {
	if gs == nil {
		return
	}
	cfg := core.EmbeddingConfig{}
	if v, err := env.Lookup("graph-embeddings"); err == nil {
		if b, ok := v.(bool); ok {
			cfg.Enabled = b
		}
	}
	model, err := currentModel(env)
	if err != nil {
		cfg.Enabled = false
	}
	cfg.Model = string(model)
	cfg.Encoder = func() (core.EmbeddingEncoder, error) {
		enc, err := getEncoder(env)
		if err != nil {
			return nil, err
		}
		return enc, nil
	}
	gs.Embeddings().Configure(cfg)
}
