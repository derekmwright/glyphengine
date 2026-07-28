package ecs

import "sync/atomic"

// Entity is a unique identifier for a game object.
type Entity uint64

var nextEntityID atomic.Uint64

func newEntity() Entity {
	return Entity(nextEntityID.Add(1))
}
