package glyphengine

import (
	"math"
	"math/rand"

	"github.com/derekmwright/glyphengine/renderer"
)

// SpawnMode controls how particles are emitted.
type SpawnMode uint8

const (
	SpawnContinuous SpawnMode = iota
	SpawnBurst
)

// MoveMode controls particle movement behavior.
type MoveMode uint8

const (
	MoveDrift   MoveMode = iota // random walk with turbulence
	MoveGravity                 // upward burst, falls back down
	MoveRadial                  // expand outward from center
)

// FadeMode controls how particle alpha changes over lifetime.
type FadeMode uint8

const (
	FadePulse FadeMode = iota // sinusoidal pulsing with fade in/out
	FadePlain                 // linear fade in and fade out
	FadeFlash                 // instant bright, rapid quadratic decay
)

// EmitterConfig defines all behavior parameters for a particle emitter.
type EmitterConfig struct {
	// Spawning
	Spawn        SpawnMode
	SpawnRate    float32 // particles per second (continuous mode)
	BurstCount   int     // number of particles (burst mode)
	MaxParticles int

	// Lifetime
	LifeMin, LifeMax float32

	// Size
	SizeMin, SizeMax float32

	// Color ranges (random per particle at spawn)
	RMin, RMax float32
	GMin, GMax float32
	BMin, BMax float32

	// Movement
	Move           MoveMode
	InitialSpeedXZ float32 // half-range for random initial XZ velocity
	InitialSpeedY  float32 // half-range for random initial Y velocity
	MaxSpeed       float32
	TurbulenceXZ   float32 // per-second random velocity perturbation in XZ
	TurbulenceY    float32 // per-second random velocity perturbation in Y
	Gravity        float32 // downward acceleration (positive = down)
	RadialSpeed    float32 // outward speed for radial mode
	BounceAtBounds bool

	// Visuals
	Fade        FadeMode
	FadeInTime  float32
	FadeOutTime float32
	PulseSpeed  float32
	AlphaMax    float32

	// Conditions
	NightOnly bool
	NightMin  float32 // minimum nightFactor to be active

	// Lifecycle
	Loop bool // continuous emitters loop; one-shot burst emitters set Done when empty
}

// Particle is a single simulated particle.
type Particle struct {
	X, Y, Z    float32
	VX, VY, VZ float32
	Phase      float32 // random offset for pulsing
	Life       float32 // remaining life in seconds
	MaxLife    float32 // total life in seconds
	Size       float32
	R, G, B    float32
}

// ParticleEmitter spawns and manages particles within a bounding region.
type ParticleEmitter struct {
	Config *EmitterConfig

	MinX, MaxX float32
	MinZ, MaxZ float32
	BaseY      float32
	HeightMin  float32
	HeightMax  float32

	Particles  []Particle
	elapsed    float32
	spawnAccum float32
	burstFired bool
	Done       bool
}

// NewParticleEmitter creates an emitter with the given config and bounding region.
func NewParticleEmitter(cfg *EmitterConfig, minX, maxX, minZ, maxZ, baseY, heightMin, heightMax float32) *ParticleEmitter {
	return &ParticleEmitter{
		Config:    cfg,
		MinX:      minX,
		MaxX:      maxX,
		MinZ:      minZ,
		MaxZ:      maxZ,
		BaseY:     baseY,
		HeightMin: heightMin,
		HeightMax: heightMax,
	}
}

// SetPosition repositions the emitter centered on (cx, baseY, cz), preserving its extent.
func (em *ParticleEmitter) SetPosition(cx, baseY, cz float32) {
	hw := (em.MaxX - em.MinX) / 2
	hd := (em.MaxZ - em.MinZ) / 2
	em.MinX = cx - hw
	em.MaxX = cx + hw
	em.MinZ = cz - hd
	em.MaxZ = cz + hd
	em.BaseY = baseY
}

// Reset clears all particles and resets burst state for reuse.
func (em *ParticleEmitter) Reset() {
	em.Particles = em.Particles[:0]
	em.spawnAccum = 0
	em.burstFired = false
	em.Done = false
	em.elapsed = 0
}

// spawnParticle creates a new particle using the emitter config.
func (em *ParticleEmitter) spawnParticle() Particle {
	cfg := em.Config
	life := cfg.LifeMin + rand.Float32()*(cfg.LifeMax-cfg.LifeMin)

	p := Particle{
		X:       em.MinX + rand.Float32()*(em.MaxX-em.MinX),
		Y:       em.BaseY + em.HeightMin + rand.Float32()*(em.HeightMax-em.HeightMin),
		Z:       em.MinZ + rand.Float32()*(em.MaxZ-em.MinZ),
		Phase:   rand.Float32() * 2 * math.Pi,
		Life:    life,
		MaxLife: life,
		Size:    cfg.SizeMin + rand.Float32()*(cfg.SizeMax-cfg.SizeMin),
		R:       cfg.RMin + rand.Float32()*(cfg.RMax-cfg.RMin),
		G:       cfg.GMin + rand.Float32()*(cfg.GMax-cfg.GMin),
		B:       cfg.BMin + rand.Float32()*(cfg.BMax-cfg.BMin),
	}

	switch cfg.Move {
	case MoveDrift:
		p.VX = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedXZ
		p.VY = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedY
		p.VZ = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedXZ
	case MoveGravity:
		p.VX = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedXZ
		p.VY = cfg.InitialSpeedY + rand.Float32()*cfg.InitialSpeedY*0.5
		p.VZ = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedXZ
	case MoveRadial:
		cx := (em.MinX + em.MaxX) / 2
		cz := (em.MinZ + em.MaxZ) / 2
		dx := p.X - cx
		dz := p.Z - cz
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if dist > 0.001 {
			p.VX = dx / dist * cfg.RadialSpeed
			p.VZ = dz / dist * cfg.RadialSpeed
		}
		p.VY = (rand.Float32() - 0.5) * 2 * cfg.InitialSpeedY
	}

	return p
}

// Tick advances the emitter by dt seconds. nightFactor is 0 (day) to 1 (night).
func (em *ParticleEmitter) Tick(dt, nightFactor float32) {
	cfg := em.Config

	// Night gating.
	if cfg.NightOnly && nightFactor < cfg.NightMin {
		for i := range em.Particles {
			em.Particles[i].Life = 0
		}
		em.Particles = em.Particles[:0]
		return
	}

	em.elapsed += dt

	// Update existing particles.
	n := 0
	for i := range em.Particles {
		p := &em.Particles[i]
		p.Life -= dt
		if p.Life <= 0 {
			continue
		}

		// Movement.
		switch cfg.Move {
		case MoveDrift:
			p.VX += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
			p.VY += (rand.Float32() - 0.5) * cfg.TurbulenceY * dt
			p.VZ += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
		case MoveGravity:
			p.VY -= cfg.Gravity * dt
			p.VX += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
			p.VZ += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
		case MoveRadial:
			p.VX += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
			p.VY += (rand.Float32() - 0.5) * cfg.TurbulenceY * dt
			p.VZ += (rand.Float32() - 0.5) * cfg.TurbulenceXZ * dt
		}

		// Clamp speed.
		speed := float32(math.Sqrt(float64(p.VX*p.VX + p.VY*p.VY + p.VZ*p.VZ)))
		if speed > cfg.MaxSpeed {
			scale := cfg.MaxSpeed / speed
			p.VX *= scale
			p.VY *= scale
			p.VZ *= scale
		}

		p.X += p.VX * dt
		p.Y += p.VY * dt
		p.Z += p.VZ * dt

		// Boundary bounce.
		if cfg.BounceAtBounds {
			if p.X < em.MinX {
				p.VX = float32(math.Abs(float64(p.VX)))
			}
			if p.X > em.MaxX {
				p.VX = -float32(math.Abs(float64(p.VX)))
			}
			if p.Z < em.MinZ {
				p.VZ = float32(math.Abs(float64(p.VZ)))
			}
			if p.Z > em.MaxZ {
				p.VZ = -float32(math.Abs(float64(p.VZ)))
			}
			if p.Y < em.BaseY+em.HeightMin {
				p.VY = float32(math.Abs(float64(p.VY)))
			}
			if p.Y > em.BaseY+em.HeightMax {
				p.VY = -float32(math.Abs(float64(p.VY)))
			}
		}

		em.Particles[n] = em.Particles[i]
		n++
	}
	em.Particles = em.Particles[:n]

	// Spawn new particles.
	switch cfg.Spawn {
	case SpawnContinuous:
		em.spawnAccum += cfg.SpawnRate * dt
		for em.spawnAccum >= 1 && len(em.Particles) < cfg.MaxParticles {
			em.Particles = append(em.Particles, em.spawnParticle())
			em.spawnAccum -= 1
		}
		if len(em.Particles) >= cfg.MaxParticles {
			em.spawnAccum = 0
		}
	case SpawnBurst:
		if !em.burstFired {
			em.burstFired = true
			for i := 0; i < cfg.BurstCount && len(em.Particles) < cfg.MaxParticles; i++ {
				em.Particles = append(em.Particles, em.spawnParticle())
			}
		}
		if !cfg.Loop && len(em.Particles) == 0 {
			em.Done = true
		}
	}
}

// BuildInstances converts live particles into GPU instance data.
func (em *ParticleEmitter) BuildInstances(nightFactor float32) []renderer.ParticleInstance {
	cfg := em.Config

	if cfg.NightOnly && nightFactor < cfg.NightMin {
		return nil
	}
	if len(em.Particles) == 0 {
		return nil
	}

	out := make([]renderer.ParticleInstance, 0, len(em.Particles))
	for i := range em.Particles {
		p := &em.Particles[i]
		age := p.MaxLife - p.Life

		var alpha float32
		switch cfg.Fade {
		case FadePulse:
			pulse := float32(0.5 + 0.5*math.Sin(float64(em.elapsed*cfg.PulseSpeed+p.Phase)))
			fade := float32(1.0)
			if cfg.FadeInTime > 0 && age < cfg.FadeInTime {
				fade = age / cfg.FadeInTime
			}
			if cfg.FadeOutTime > 0 && p.Life < cfg.FadeOutTime {
				fade *= p.Life / cfg.FadeOutTime
			}
			alpha = pulse * fade * cfg.AlphaMax
		case FadePlain:
			fade := float32(1.0)
			if cfg.FadeInTime > 0 && age < cfg.FadeInTime {
				fade = age / cfg.FadeInTime
			}
			if cfg.FadeOutTime > 0 && p.Life < cfg.FadeOutTime {
				fade *= p.Life / cfg.FadeOutTime
			}
			alpha = fade * cfg.AlphaMax
		case FadeFlash:
			t := age / p.MaxLife
			alpha = (1 - t*t) * cfg.AlphaMax
		}

		if cfg.NightOnly {
			alpha *= nightFactor
		}

		if alpha < 0.01 {
			continue
		}

		out = append(out, renderer.ParticleInstance{
			X: p.X, Y: p.Y, Z: p.Z,
			Size: p.Size,
			R:    p.R, G: p.G, B: p.B,
			A: alpha,
		})
	}
	return out
}

// --- Preset configs ---

// FireflyConfig returns an emitter config matching the original hardcoded firefly behavior.
func FireflyConfig() *EmitterConfig {
	return &EmitterConfig{
		Spawn:        SpawnContinuous,
		SpawnRate:    60,
		MaxParticles: 40,

		LifeMin: 4.0,
		LifeMax: 8.0,

		SizeMin: 0.03,
		SizeMax: 0.06,

		RMin: 0.8, RMax: 1.0,
		GMin: 0.9, GMax: 1.0,
		BMin: 0.3, BMax: 0.6,

		Move:           MoveDrift,
		InitialSpeedXZ: 0.1,
		InitialSpeedY:  0.05,
		MaxSpeed:       0.3,
		TurbulenceXZ:   0.3,
		TurbulenceY:    0.15,
		BounceAtBounds: true,

		Fade:        FadePulse,
		FadeInTime:  0.5,
		FadeOutTime: 1.0,
		PulseSpeed:  2.0,
		AlphaMax:    0.8,

		NightOnly: true,
		NightMin:  0.1,

		Loop: true,
	}
}

// SparkConfig returns an emitter config for fire spark particles.
func SparkConfig() *EmitterConfig {
	return &EmitterConfig{
		Spawn:        SpawnContinuous,
		SpawnRate:    20,
		MaxParticles: 30,

		LifeMin: 0.5,
		LifeMax: 1.5,

		SizeMin: 0.02,
		SizeMax: 0.04,

		RMin: 0.9, RMax: 1.0,
		GMin: 0.3, GMax: 0.6,
		BMin: 0.0, BMax: 0.1,

		Move:           MoveGravity,
		InitialSpeedXZ: 0.5,
		InitialSpeedY:  2.0,
		MaxSpeed:       3.0,
		TurbulenceXZ:   0.5,
		Gravity:        4.0,
		BounceAtBounds: false,

		Fade:        FadePlain,
		FadeInTime:  0.05,
		FadeOutTime: 0.3,
		AlphaMax:    1.0,

		Loop: true,
	}
}

// SpellImpactConfig returns an emitter config for a burst of spell impact particles.
func SpellImpactConfig() *EmitterConfig {
	return &EmitterConfig{
		Spawn:        SpawnBurst,
		BurstCount:   20,
		MaxParticles: 20,

		LifeMin: 0.3,
		LifeMax: 0.8,

		SizeMin: 0.04,
		SizeMax: 0.08,

		RMin: 0.5, RMax: 0.8,
		GMin: 0.7, GMax: 1.0,
		BMin: 0.9, BMax: 1.0,

		Move:           MoveRadial,
		RadialSpeed:    3.0,
		InitialSpeedY:  1.0,
		MaxSpeed:       4.0,
		TurbulenceXZ:   0.3,
		TurbulenceY:    0.3,
		BounceAtBounds: false,

		Fade:     FadeFlash,
		AlphaMax: 1.0,

		Loop: false,
	}
}
