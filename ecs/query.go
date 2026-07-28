package ecs

// Query2 iterates all entities that have both component types, calling fn
// with the entity and both component pointers. Iterates the smaller store
// and checks the larger for O(min(|A|,|B|)) work.
func Query2[A, B any](a *Store[A], b *Store[B], fn func(Entity, *A, *B)) {
	if a.Len() <= b.Len() {
		for e, av := range a.components {
			if bv, ok := b.components[e]; ok {
				fn(e, av, bv)
			}
		}
	} else {
		for e, bv := range b.components {
			if av, ok := a.components[e]; ok {
				fn(e, av, bv)
			}
		}
	}
}

// Query3 iterates all entities with all three component types.
func Query3[A, B, C any](a *Store[A], b *Store[B], c *Store[C], fn func(Entity, *A, *B, *C)) {
	aLen, bLen, cLen := a.Len(), b.Len(), c.Len()

	if aLen <= bLen && aLen <= cLen {
		for e, av := range a.components {
			bv, ok := b.components[e]
			if !ok {
				continue
			}
			cv, ok := c.components[e]
			if !ok {
				continue
			}
			fn(e, av, bv, cv)
		}
	} else if bLen <= cLen {
		for e, bv := range b.components {
			av, ok := a.components[e]
			if !ok {
				continue
			}
			cv, ok := c.components[e]
			if !ok {
				continue
			}
			fn(e, av, bv, cv)
		}
	} else {
		for e, cv := range c.components {
			av, ok := a.components[e]
			if !ok {
				continue
			}
			bv, ok := b.components[e]
			if !ok {
				continue
			}
			fn(e, av, bv, cv)
		}
	}
}

// Query4 iterates all entities with all four component types.
func Query4[A, B, C, D any](a *Store[A], b *Store[B], c *Store[C], d *Store[D], fn func(Entity, *A, *B, *C, *D)) {
	// Find the smallest store to iterate.
	stores := [4]int{a.Len(), b.Len(), c.Len(), d.Len()}
	smallest := 0
	for i := 1; i < 4; i++ {
		if stores[i] < stores[smallest] {
			smallest = i
		}
	}

	check := func(e Entity) (*A, *B, *C, *D, bool) {
		av, ok := a.components[e]
		if !ok {
			return nil, nil, nil, nil, false
		}
		bv, ok := b.components[e]
		if !ok {
			return nil, nil, nil, nil, false
		}
		cv, ok := c.components[e]
		if !ok {
			return nil, nil, nil, nil, false
		}
		dv, ok := d.components[e]
		if !ok {
			return nil, nil, nil, nil, false
		}
		return av, bv, cv, dv, true
	}

	switch smallest {
	case 0:
		for e := range a.components {
			if av, bv, cv, dv, ok := check(e); ok {
				fn(e, av, bv, cv, dv)
			}
		}
	case 1:
		for e := range b.components {
			if av, bv, cv, dv, ok := check(e); ok {
				fn(e, av, bv, cv, dv)
			}
		}
	case 2:
		for e := range c.components {
			if av, bv, cv, dv, ok := check(e); ok {
				fn(e, av, bv, cv, dv)
			}
		}
	case 3:
		for e := range d.components {
			if av, bv, cv, dv, ok := check(e); ok {
				fn(e, av, bv, cv, dv)
			}
		}
	}
}
