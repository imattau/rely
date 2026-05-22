package consensus

import (
	"sync"
	"time"
)

type State struct {
	Round int64              `json:"round"`
	Rep   map[string]float64 `json:"rep"`
}

type incomingMsg struct {
	state  *State
	weight float64
}

type Diffuser struct {
	mu          sync.RWMutex
	round       int64
	rep         map[string]float64
	dirty       map[string]float64
	broadcast   func(msgType string, payload interface{})
	onRecompute func()
	incoming    chan incomingMsg
}

func NewDiffuser(broadcast func(string, interface{}), onRecompute func()) *Diffuser {
	return &Diffuser{
		rep:         make(map[string]float64),
		dirty:       make(map[string]float64),
		broadcast:   broadcast,
		onRecompute: onRecompute,
		incoming:    make(chan incomingMsg, 64),
	}
}

func (d *Diffuser) Run(tick time.Duration, done <-chan struct{}) {
	if tick <= 0 {
		tick = time.Second
	}

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			d.mu.Lock()
			d.round++
			snapshot := d.snapshotLocked()
			d.mu.Unlock()

			if d.broadcast != nil {
				d.broadcast("consensus", snapshot)
			}
		case msg := <-d.incoming:
			d.MergeState(msg.state, msg.weight)
		}
	}
}

func (d *Diffuser) Enqueue(s *State, weight ...float64) {
	if s == nil {
		return
	}

	w := 1.0
	if len(weight) > 0 {
		w = weight[0]
	}

	select {
	case d.incoming <- incomingMsg{state: s, weight: w}:
	default:
	}
}

func (d *Diffuser) MergeState(neighbour *State, weight ...float64) {
	if neighbour == nil {
		return
	}

	w := 1.0
	if len(weight) > 0 {
		w = weight[0]
	}
	if w < 0 {
		w = 0
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.rep == nil {
		d.rep = make(map[string]float64)
	}
	if d.dirty == nil {
		d.dirty = make(map[string]float64)
	}

	nextRound := d.round
	if neighbour.Round > nextRound {
		nextRound = neighbour.Round
	}
	d.round = nextRound + 1
	if d.round < nextRound {
		d.round = nextRound
	}

	for k, v := range neighbour.Rep {
		local := d.rep[k]
		merged := (local + v*w) / (1.0 + w)
		clamped := clamp(merged, -1, 1)
		if clamped != local {
			d.rep[k] = clamped
			d.dirty[k] = clamped
		}
	}
}

func (d *Diffuser) Snapshot() *State {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rep := make(map[string]float64, len(d.dirty))
	for k, v := range d.dirty {
		rep[k] = v
	}
	return &State{Round: d.round, Rep: rep}
}

func (d *Diffuser) snapshotLocked() *State {
	rep := make(map[string]float64, len(d.dirty))
	for k, v := range d.dirty {
		rep[k] = v
	}
	// Clear the delta after taking the snapshot so the next broadcast stays bounded.
	d.dirty = make(map[string]float64)
	return &State{Round: d.round, Rep: rep}
}

func (d *Diffuser) GetRound() int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.round
}

func (d *Diffuser) SetRound(r int64) {
	d.mu.Lock()
	d.round = r
	d.mu.Unlock()
}

func (d *Diffuser) GetReputation(pubkey string) float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.rep[pubkey]
}

func (d *Diffuser) SetReputation(pubkey string, score float64) {
	d.mu.Lock()
	if d.rep == nil {
		d.rep = make(map[string]float64)
	}
	if d.dirty == nil {
		d.dirty = make(map[string]float64)
	}
	value := clamp(score, -1, 1)
	d.rep[pubkey] = value
	d.dirty[pubkey] = value
	d.mu.Unlock()
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
