package ecs

import "reflect"

// World manages entities and their components.
type World struct {
	entities map[Entity]struct{}
	stores   map[reflect.Type]storeI
}

// NewWorld creates an empty ECS world with no entities or component stores.
func NewWorld() *World {
	return &World{
		entities: make(map[Entity]struct{}),
		stores:   make(map[reflect.Type]storeI),
	}
}

// Spawn creates a new entity and returns its ID.
func (w *World) Spawn() Entity {
	e := newEntity()
	w.entities[e] = struct{}{}
	return e
}

// Despawn removes an entity and all its components.
func (w *World) Despawn(entity Entity) {
	delete(w.entities, entity)
	for _, store := range w.stores {
		store.removeEntity(entity)
	}
}

// Alive checks if an entity exists.
func (w *World) Alive(entity Entity) bool {
	_, ok := w.entities[entity]
	return ok
}

// Set attaches a component to an entity.
// Prefer Store[T].Set for hot paths — this method uses reflection.
func (w *World) Set(entity Entity, component Component) {
	key := componentTypeKey(component)
	store, ok := w.stores[key]
	if !ok {
		store = newComponentStore()
		w.stores[key] = store
	}
	store.setAny(entity, component)
}

// Get retrieves a component from an entity.
// Prefer Store[T].Get for hot paths — this method uses reflection.
func (w *World) Get(entity Entity, componentType Component) (Component, bool) {
	key := componentTypeKey(componentType)
	store, ok := w.stores[key]
	if !ok {
		return nil, false
	}
	return store.getAny(entity)
}

// Remove detaches a component from an entity.
func (w *World) Remove(entity Entity, componentType Component) {
	key := componentTypeKey(componentType)
	store, ok := w.stores[key]
	if !ok {
		return
	}
	store.removeEntity(entity)
}

// Each calls fn for every entity that has ALL of the specified component types.
// Prefer Query2/Query3 for hot paths — this method uses reflection.
func (w *World) Each(fn func(Entity), types ...Component) {
	if len(types) == 0 {
		return
	}

	// Stack-allocated array avoids heap allocation for up to 4 types.
	var buf [4]storeI
	stores := buf[:len(types)]
	for i, t := range types {
		key := componentTypeKey(t)
		s, ok := w.stores[key]
		if !ok {
			return
		}
		stores[i] = s
	}

	// Pick the smallest store to iterate over.
	smallest := 0
	for i := 1; i < len(stores); i++ {
		if stores[i].length() < stores[smallest].length() {
			smallest = i
		}
	}

	// Iterate the smallest store and check presence in the others.
	stores[smallest].eachEntity(func(entity Entity) {
		for i, s := range stores {
			if i == smallest {
				continue
			}
			if !s.hasEntity(entity) {
				return
			}
		}
		fn(entity)
	})
}

// EnableLocking switches all registered typed stores into concurrent mode.
// Call before parallel access, and DisableLocking after.
func (w *World) EnableLocking() {
	for _, s := range w.stores {
		if l, ok := s.(lockable); ok {
			l.setConcurrent(true)
		}
	}
}

// DisableLocking switches all registered typed stores back to lock-free mode.
func (w *World) DisableLocking() {
	for _, s := range w.stores {
		if l, ok := s.(lockable); ok {
			l.setConcurrent(false)
		}
	}
}

// EntityCount returns the number of living entities.
func (w *World) EntityCount() int {
	return len(w.entities)
}
