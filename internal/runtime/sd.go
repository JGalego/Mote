package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jgalego/mote/registry"
)

// ImageRequest asks an image model for one picture.
type ImageRequest struct {
	Prompt        string
	Width, Height int
	Seed          int64
	Out           string // PNG path to write
}

// Drawer is implemented by backends that generate images. Callers
// type-assert for it, as they do for Embedder.
type Drawer interface {
	Draw(ctx context.Context, m *registry.Model, files map[string]string, req ImageRequest) (Stats, error)
}

// SD runs image models with stable-diffusion.cpp's sd-cli, one process per
// picture: generation takes minutes, so loading the weights each time costs
// little, and nothing stays resident between requests.
type SD struct {
	Dir     string // directory containing sd-cli
	Threads int
	LogDir  string
}

func (s *SD) bin() string { return filepath.Join(s.Dir, exe("sd-cli")) }

// Open fails: sd.cpp models make pictures, not replies.
func (s *SD) Open(_ context.Context, m *registry.Model, _ map[string]string) (Session, error) {
	return nil, fmt.Errorf("%w: %s generates images and cannot chat", ErrUnsupported, m.ID)
}

// Speak fails for the same reason.
func (s *SD) Speak(_ context.Context, m *registry.Model, _ map[string]string, _, _ string) (Stats, error) {
	return Stats{}, fmt.Errorf("%w: %s generates images and cannot speak", ErrUnsupported, m.ID)
}

// DrawArgs is the sd-cli command line for a request. The model's registry
// arguments (its step count and guidance) come before the request's own.
func (s *SD) DrawArgs(m *registry.Model, files map[string]string, req ImageRequest) []string {
	args := []string{
		"--diffusion-model", files["model"],
		"--vae", files["vae"],
		"--llm", files["llm"],
	}
	args = append(args, m.Args...)
	args = append(args,
		"-p", req.Prompt,
		"-W", strconv.Itoa(req.Width), "-H", strconv.Itoa(req.Height),
		"--seed", strconv.FormatInt(req.Seed, 10),
		// Mapped weights are file-backed pages the kernel can drop and
		// read back under memory pressure; copied ones get the process
		// killed on a laptop with other programs open.
		"--mmap",
		"--diffusion-fa",
		"-o", req.Out,
	)
	// Tiled decoding saves memory but was eight times slower at 256x256,
	// so it is kept for the sizes that need it.
	if req.Width*req.Height > 512*512 {
		args = append(args, "--vae-tiling")
	}
	if s.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(s.Threads))
	}
	return args
}

// Draw generates one image and writes it to req.Out.
func (s *SD) Draw(ctx context.Context, m *registry.Model, files map[string]string, req ImageRequest) (Stats, error) {
	if _, err := os.Stat(s.bin()); err != nil {
		return Stats{}, fmt.Errorf("sd-cli not found in %s", s.Dir)
	}
	if dir := filepath.Dir(req.Out); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Stats{}, err
		}
	}
	cmd := exec.CommandContext(ctx, s.bin(), s.DrawArgs(m, files, req)...)
	logf, logPath := logFile(s.LogDir, "sd-cli")
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	start := time.Now()
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Stats{}, ctx.Err()
		}
		return Stats{}, fmt.Errorf("sd-cli: %w\n%s", err, tail(logPath, 15))
	}
	if st, err := os.Stat(req.Out); err != nil || st.Size() == 0 {
		return Stats{}, fmt.Errorf("sd-cli finished without writing %s\n%s", req.Out, tail(logPath, 15))
	}
	return Stats{StartupMS: float64(time.Since(start).Milliseconds()), PeakRSSMB: exitRSSMB(cmd.ProcessState)}, nil
}
