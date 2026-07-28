package ecs

import "testing"

type Position struct {
	X, Y, Z float64
}

type Velocity struct {
	X, Y, Z float64
}

func TestSpawnAndAlive(t *testing.T) {
	w := NewWorld()

	e := w.Spawn()
	if !w.Alive(e) {
		t.Fatal("expected entity to be alive")
	}
	if w.EntityCount() != 1 {
		t.Fatalf("expected 1 entity, got %d", w.EntityCount())
	}
}

func TestDespawn(t *testing.T) {
	w := NewWorld()

	e := w.Spawn()
	w.Set(e, Position{1, 2, 3})
	w.Despawn(e)

	if w.Alive(e) {
		t.Fatal("expected entity to be dead")
	}
	if w.EntityCount() != 0 {
		t.Fatalf("expected 0 entities, got %d", w.EntityCount())
	}
}

func TestSetGetComponent(t *testing.T) {
	w := NewWorld()
	e := w.Spawn()

	w.Set(e, Position{10, 20, 30})

	comp, ok := w.Get(e, Position{})
	if !ok {
		t.Fatal("expected component to exist")
	}
	pos := comp.(Position)
	if pos.X != 10 || pos.Y != 20 || pos.Z != 30 {
		t.Fatalf("unexpected position: %+v", pos)
	}
}

func TestMultipleComponentTypes(t *testing.T) {
	w := NewWorld()
	e := w.Spawn()

	w.Set(e, Position{1, 2, 3})
	w.Set(e, Velocity{4, 5, 6})

	_, ok := w.Get(e, Position{})
	if !ok {
		t.Fatal("expected Position component")
	}
	_, ok = w.Get(e, Velocity{})
	if !ok {
		t.Fatal("expected Velocity component")
	}
}

func TestRemoveComponent(t *testing.T) {
	w := NewWorld()
	e := w.Spawn()

	w.Set(e, Position{1, 2, 3})
	w.Remove(e, Position{})

	_, ok := w.Get(e, Position{})
	if ok {
		t.Fatal("expected component to be removed")
	}
}

func TestGetMissing(t *testing.T) {
	w := NewWorld()
	e := w.Spawn()

	_, ok := w.Get(e, Position{})
	if ok {
		t.Fatal("expected no component")
	}
}

type Health struct {
	HP int
}

func TestEachSingleType(t *testing.T) {
	w := NewWorld()
	e1 := w.Spawn()
	e2 := w.Spawn()
	w.Set(e1, Position{1, 2, 3})
	w.Set(e2, Position{4, 5, 6})

	var found []Entity
	w.Each(func(e Entity) {
		found = append(found, e)
	}, Position{})

	if len(found) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(found))
	}
}

func TestEachMultipleTypes(t *testing.T) {
	w := NewWorld()
	e1 := w.Spawn()
	e2 := w.Spawn()
	e3 := w.Spawn()

	w.Set(e1, Position{1, 2, 3})
	w.Set(e1, Velocity{1, 0, 0})
	w.Set(e2, Position{4, 5, 6})
	// e2 has no Velocity
	w.Set(e3, Velocity{0, 1, 0})
	// e3 has no Position

	var found []Entity
	w.Each(func(e Entity) {
		found = append(found, e)
	}, Position{}, Velocity{})

	if len(found) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(found))
	}
	if found[0] != e1 {
		t.Fatalf("expected entity %d, got %d", e1, found[0])
	}
}

func TestEachNoMatchingEntities(t *testing.T) {
	w := NewWorld()
	e1 := w.Spawn()
	w.Set(e1, Position{1, 2, 3})

	var count int
	w.Each(func(e Entity) {
		count++
	}, Velocity{})

	if count != 0 {
		t.Fatalf("expected 0 entities, got %d", count)
	}
}

func TestEachEmptyWorld(t *testing.T) {
	w := NewWorld()

	var count int
	w.Each(func(e Entity) {
		count++
	}, Position{})

	if count != 0 {
		t.Fatalf("expected 0 entities, got %d", count)
	}
}

func TestEachNoTypes(t *testing.T) {
	w := NewWorld()
	e1 := w.Spawn()
	w.Set(e1, Position{1, 2, 3})

	var count int
	w.Each(func(e Entity) {
		count++
	})

	if count != 0 {
		t.Fatalf("expected 0 entities with no types, got %d", count)
	}
}

func TestUniqueEntityIDs(t *testing.T) {
	w := NewWorld()
	ids := make(map[Entity]struct{})
	for i := 0; i < 1000; i++ {
		e := w.Spawn()
		if _, exists := ids[e]; exists {
			t.Fatalf("duplicate entity ID: %d", e)
		}
		ids[e] = struct{}{}
	}
}
