package ecs

import "reflect"

// Component is the interface that all components must implement.
type Component interface{}

// componentStore holds all components of a single type, keyed by entity.
// Legacy storage — new code should use the generic Store[T] instead.
type componentStore struct {
	components map[Entity]Component
}

func newComponentStore() *componentStore {
	return &componentStore{
		components: make(map[Entity]Component),
	}
}

func (s *componentStore) set(entity Entity, component Component) {
	s.components[entity] = component
}

func (s *componentStore) get(entity Entity) (Component, bool) {
	c, ok := s.components[entity]
	return c, ok
}

func (s *componentStore) remove(entity Entity) {
	delete(s.components, entity)
}

// componentTypeKey returns a unique key for a component type.
func componentTypeKey(c Component) reflect.Type {
	return reflect.TypeOf(c)
}

// --- storeI implementation for bridge compatibility ---

func (s *componentStore) getAny(entity Entity) (Component, bool) {
	return s.get(entity)
}

func (s *componentStore) setAny(entity Entity, component Component) {
	s.set(entity, component)
}

func (s *componentStore) removeEntity(entity Entity) {
	s.remove(entity)
}

func (s *componentStore) hasEntity(entity Entity) bool {
	_, ok := s.components[entity]
	return ok
}

func (s *componentStore) eachEntity(fn func(Entity)) {
	for e := range s.components {
		fn(e)
	}
}

func (s *componentStore) length() int {
	return len(s.components)
}
