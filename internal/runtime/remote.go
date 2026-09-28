package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jgalego/mote/registry"
)

// Remote runs models in a resident mote server (mote serve), which keeps
// them loaded between commands. Speech still runs locally: llama-tts is a
// one-shot program with nothing to keep loaded.
type Remote struct {
	URL   string
	Local *Llama
	// Fallback, when set, is called when the server cannot be reached;
	// Open then loads the model locally, as if there were no server.
	Fallback func(err error)
}

// ErrUnreachable reports a resident server that did not answer.
var ErrUnreachable = errors.New("mote serve is not reachable")

func (r *Remote) client() *http.Client { return &http.Client{Transport: &http.Transport{Proxy: nil}} }

// Open asks the server to load the model, waiting while it does, and
// returns a session whose requests go through the server.
func (r *Remote) Open(ctx context.Context, m *registry.Model, files map[string]string) (Session, error) {
	payload, _ := json.Marshal(map[string]string{"model": m.ID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL+"/mote/load", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if r.Fallback != nil && r.Local != nil {
			r.Fallback(fmt.Errorf("%w: %v", ErrUnreachable, err))
			return r.Local.Open(ctx, m, files)
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return nil, errors.New(e.Error.Message)
		}
		return nil, fmt.Errorf("mote serve: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var loaded struct {
		StartupMS float64 `json:"startup_ms"`
	}
	json.Unmarshal(raw, &loaded)
	return &server{name: m.ID, model: m, base: r.URL, client: r.client(),
		stats: Stats{StartupMS: loaded.StartupMS}}, nil
}

// Speak synthesizes speech locally.
func (r *Remote) Speak(ctx context.Context, m *registry.Model, files map[string]string, text, out string) (Stats, error) {
	if r.Local == nil {
		return Stats{}, ErrUnsupported
	}
	return r.Local.Speak(ctx, m, files, text, out)
}
