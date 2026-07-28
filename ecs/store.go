package ecs

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// storeI is the internal interface for polymorphic component storage.
// Both the legacy componentStore and generic Store[T] implement it.
type storeI interface {
	getAny(Entity) (Component, bool)
	setAny(Entity, Component)
	removeEntity(Entity)
	hasEntity(Entity) bool
	eachEntity(func(Entity))
	length() int
}

// lockable is implemented by stores that support concurrent access mode.
type lockable interface {
	setConcurrent(bool)
}

// Store is a type-safe, generic component store that eliminates interface
// boxing. Direct access via Get/Set involves no reflection or type assertions.
//
// Concurrency: by default, access is lock-free (single-threaded fast path).
// Call SetConcurrent(true) before parallel access and SetConcurrent(false)
// after. When concurrent, Get takes a read lock and Set/Remove take a write lock.
type Store[T any] struct {
	components map[Entity]*T
	mu         sync.RWMutex
	concurrent atomic.Bool
}

// NewStore creates a typed component store and registers it with the World.
// Must be called before any entities use this component type.
func NewStore[T any](w *World) *Store[T] {
	s := &Store[T]{
		components: make(map[Entity]*T),
	}
	key := reflect.TypeOf((*T)(nil))
	w.stores[key] = s
	return s
}

func (s *Store[T]) setConcurrent(on bool) {
	s.concurrent.Store(on)
}

// Get retrieves a component pointer with zero boxing or reflection.
func (s *Store[T]) Get(entity Entity) (*T, bool) {
	if s.concurrent.Load() {
		s.mu.RLock()
		c, ok := s.components[entity]
		s.mu.RUnlock()
		return c, ok
	}
	c, ok := s.components[entity]
	return c, ok
}

// Set attaches a component to an entity with zero boxing.
func (s *Store[T]) Set(entity Entity, c *T) {
	if s.concurrent.Load() {
		s.mu.Lock()
		s.components[entity] = c
		s.mu.Unlock()
		return
	}
	s.components[entity] = c
}

// Remove detaches a component from an entity.
func (s *Store[T]) Remove(entity Entity) {
	if s.concurrent.Load() {
		s.mu.Lock()
		delete(s.components, entity)
		s.mu.Unlock()
		return
	}
	delete(s.components, entity)
}

// Has returns true if the entity has this component type.
func (s *Store[T]) Has(entity Entity) bool {
	if s.concurrent.Load() {
		s.mu.RLock()
		_, ok := s.components[entity]
		s.mu.RUnlock()
		return ok
	}
	_, ok := s.components[entity]
	return ok
}

// Each calls fn for every entity that has this component type.
func (s *Store[T]) Each(fn func(Entity, *T)) {
	if s.concurrent.Load() {
		s.mu.RLock()
		for e, c := range s.components {
			fn(e, c)
		}
		s.mu.RUnlock()
		return
	}
	for e, c := range s.components {
		fn(e, c)
	}
}

// Len returns the number of entities with this component type.
func (s *Store[T]) Len() int {
	if s.concurrent.Load() {
		s.mu.RLock()
		n := len(s.components)
		s.mu.RUnlock()
		return n
	}
	return len(s.components)
}

// --- storeI bridge (allows old World.Get/Set/Each to access typed storage) ---

func (s *Store[T]) getAny(entity Entity) (Component, bool) {
	c, ok := s.components[entity]
	if !ok {
		return nil, false
	}
	return c, true
}

func (s *Store[T]) setAny(entity Entity, component Component) {
	s.components[entity] = component.(*T)
}

func (s *Store[T]) removeEntity(entity Entity) {
	delete(s.components, entity)
}

func (s *Store[T]) hasEntity(entity Entity) bool {
	_, ok := s.components[entity]
	return ok
}

func (s *Store[T]) eachEntity(fn func(Entity)) {
	for e := range s.components {
		fn(e)
	}
}

func (s *Store[T]) length() int {
	return len(s.components)
}
