package task

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/runtime"
)

// Image sizes are clamped to what a CPU can finish in reasonable time and
// rounded to a multiple every sd.cpp model family accepts.
const (
	minSide  = 128
	maxSide  = 1024
	sideStep = 64
)

// parseSize reads "512" as 512x512 and "768x512" as width x height.
func parseSize(s string) (int, int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	ws, hs, both := strings.Cut(s, "x")
	w, err := strconv.Atoi(strings.TrimSpace(ws))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: size %q is not a number or WIDTHxHEIGHT", ErrUsage, s)
	}
	h := w
	if both {
		if h, err = strconv.Atoi(strings.TrimSpace(hs)); err != nil {
			return 0, 0, fmt.Errorf("%w: size %q is not WIDTHxHEIGHT", ErrUsage, s)
		}
	}
	for _, v := range []int{w, h} {
		if v < minSide || v > maxSide {
			return 0, 0, fmt.Errorf("%w: each side must be %d-%d pixels, got %d", ErrUsage, minSide, maxSide, v)
		}
	}
	round := func(v int) int { return (v + sideStep/2) / sideStep * sideStep }
	return round(w), round(h), nil
}

// newSeed picks a random seed, printed so a picture can be made again.
func newSeed() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 33) // small enough to type
}

func opDraw(r *run, s Step) (Value, error) {
	prompt := strings.TrimSpace(r.expand(s.Prompt))
	if prompt == "" {
		return Value{}, errors.New("nothing to draw")
	}
	w, h, err := parseSize(r.ref(s.Width))
	if err != nil {
		return Value{}, err
	}
	seed := newSeed()
	if v := strings.TrimSpace(r.vars[s.Seed].Text); s.Seed != "" && v != "" {
		if seed, err = strconv.ParseInt(v, 10, 64); err != nil || seed < 0 {
			return Value{}, fmt.Errorf("%w: seed %q is not a whole number", ErrUsage, v)
		}
	}
	m, files, err := r.env.Resolve(r.ctx, s.Cap)
	if err != nil {
		return Value{}, err
	}
	b, err := r.env.Backend(m)
	if err != nil {
		return Value{}, err
	}
	d, ok := b.(runtime.Drawer)
	if !ok {
		return Value{}, fmt.Errorf("%s cannot generate images", m.ID)
	}
	out := r.outputPath()
	if out == "" {
		out = "image.png"
	}
	done := r.status(fmt.Sprintf("drawing %dx%d with %s (minutes on a CPU)", w, h, m.ID))
	_, err = d.Draw(r.ctx, m, files, runtime.ImageRequest{Prompt: prompt, Width: w, Height: h, Seed: seed, Out: out})
	done(err == nil)
	if err != nil {
		return Value{}, err
	}
	r.logf("seed %d; pass it as SEED to draw the same picture again", seed)
	return Value{Text: out, Files: []string{out}}, nil
}
