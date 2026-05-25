package main

// animation.go — Loads PNG frames from disk and drives a GC9A01 display.
//
// Each Animator runs in its own goroutine and reads the shared AnimState to
// decide which frame sequence to play.  State transitions cross-fade by
// immediately resetting the frame index.

import (
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// ────────────────────────────────────────────────────────────────────────────
// AnimState
// ────────────────────────────────────────────────────────────────────────────

// AnimState is an animation state identifier stored atomically so goroutines
// can read it without locks.
type AnimState int32

const (
	StateNeutral  AnimState = 0
	StateSpeaking AnimState = 1
	StateAsleep   AnimState = 2
)

func (s AnimState) String() string {
	switch s {
	case StateNeutral:
		return "neutral"
	case StateSpeaking:
		return "speaking"
	case StateAsleep:
		return "asleep"
	default:
		return "unknown"
	}
}

// dirName maps a state to its sub-directory name.
func (s AnimState) dirName() string { return s.String() }

// ────────────────────────────────────────────────────────────────────────────
// Frame timing
// ────────────────────────────────────────────────────────────────────────────

// framePeriod returns the inter-frame delay for each state.
// Slower for asleep to look drowsy; faster for speaking to feel animated.
func framePeriod(s AnimState) time.Duration {
	switch s {
	case StateNeutral:
		return 100 * time.Millisecond // ~10 fps, blink cycle ≈ 1.6 s
	case StateSpeaking:
		return 80 * time.Millisecond // ~12 fps, lip-sync feel
	case StateAsleep:
		return 160 * time.Millisecond // ~6 fps, sluggish
	default:
		return 100 * time.Millisecond
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Frame cache
// ────────────────────────────────────────────────────────────────────────────

// stateFrames holds the decoded PNG frames for all three states.
type stateFrames map[AnimState][]image.Image

// loadFrames reads PNG files from baseDir/{neutral,speaking,asleep}/*.png
// (sorted) and returns a populated stateFrames map.
func loadFrames(baseDir string) (stateFrames, error) {
	sf := make(stateFrames)
	for _, s := range []AnimState{StateNeutral, StateSpeaking, StateAsleep} {
		dir := filepath.Join(baseDir, s.dirName())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("loadFrames: read %q: %w", dir, err)
		}
		// Sort ensures deterministic frame order.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		var frames []image.Image
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			img, err := loadPNG(p)
			if err != nil {
				return nil, fmt.Errorf("loadFrames: %w", err)
			}
			frames = append(frames, img)
		}
		if len(frames) == 0 {
			return nil, fmt.Errorf("loadFrames: no PNG frames in %q", dir)
		}
		sf[s] = frames
		log.Printf("  loaded %2d frames for %-9s from %s", len(frames), s, dir)
	}
	return sf, nil
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	return img, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Animator
// ────────────────────────────────────────────────────────────────────────────

// sharedState is an *int32 that both animators read.
type sharedState = *atomic.Int32

// Animator drives one GC9A01 display with the correct frame set for the
// current animation state.
type Animator struct {
	display *GC9A01
	frames  stateFrames
	state   sharedState // pointer shared across both eyes
	quit    chan struct{}
	name    string // "left" or "right"
}

// NewAnimator returns a new Animator.  state is shared between left and right
// animators so both eyes always show the same expression.
func NewAnimator(name string, display *GC9A01, frames stateFrames, state sharedState) *Animator {
	return &Animator{
		display: display,
		frames:  frames,
		state:   state,
		quit:    make(chan struct{}),
		name:    name,
	}
}

// Run loops forever, displaying frames for the current state.  Call in a
// separate goroutine.  Close quit to stop.
func (a *Animator) Run() {
	var lastState AnimState = -1
	frameIdx := 0

	for {
		select {
		case <-a.quit:
			return
		default:
		}

		curState := AnimState(a.state.Load())
		frames := a.frames[curState]

		// Reset frame index on state change.
		if curState != lastState {
			frameIdx = 0
			lastState = curState
		}

		img := frames[frameIdx%len(frames)]
		if err := a.display.DisplayImage(img); err != nil {
			log.Printf("[%s] display error: %v", a.name, err)
		}

		frameIdx++
		if frameIdx >= len(frames) {
			frameIdx = 0
		}

		select {
		case <-a.quit:
			return
		case <-time.After(framePeriod(curState)):
		}
	}
}

// Stop signals the animator goroutine to exit.
func (a *Animator) Stop() { close(a.quit) }
