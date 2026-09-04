package eval

const framePoolCap = 256

type framePool struct {
	free []*Environment
}

func (env *Environment) markCaptured() {
	for e := env; e != nil && !e.captured; e = e.parent {
		e.captured = true
	}
}

func (e *Evaluator) acquireFrame(parent *Environment, params []Symbol, n int) *Environment {
	if p := e.frames; p != nil {
		if last := len(p.free) - 1; last >= 0 {
			f := p.free[last]
			p.free = p.free[:last]
			f.parent = parent
			f.paramNames = params
			if cap(f.vals) >= n {
				f.vals = f.vals[:n]
			} else {
				f.vals = make([]Value, n)
			}
			return f
		}
	}
	return &Environment{parent: parent, paramNames: params, vals: make([]Value, n)}
}

func (e *Evaluator) releaseOwned(f *Environment) {
	if f != nil {
		e.releaseFrame(f)
	}
}

func (e *Evaluator) releaseFrame(f *Environment) {
	p := e.frames
	if f == nil || f.captured || p == nil || len(p.free) >= framePoolCap {
		return
	}
	clear(f.vals)
	f.vals = f.vals[:0]
	f.paramNames = nil
	f.parent = nil
	f.shadow = nil
	f.fresh = false
	if len(f.bindings) > 0 {
		clear(f.bindings)
	}
	p.free = append(p.free, f)
}
