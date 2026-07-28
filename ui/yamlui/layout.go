package yamlui

// Rect is an axis-aligned rectangle in screen coordinates (Y-down).
type Rect struct{ X, Y, W, H float32 }

// Contains returns true if the point (x, y) is inside the rectangle.
func (r Rect) Contains(x, y float32) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}

// naturalHeight computes the intrinsic content height for a node based on its
// children, padding, and gap. Used when no explicit height or flex is set.
func naturalHeight(def *WidgetDef, scale float32) float32 {
	if def.Height > 0 {
		return def.Height * scale
	}
	if len(def.Children) == 0 {
		if def.FontSize > 0 {
			return def.FontSize * scale
		}
		// Leaf widgets (e.g. icon) with no explicit height or font_size:
		// use width as height to stay square, otherwise 0.
		if def.Width > 0 {
			return def.Width * scale
		}
		return 0
	}

	pad := def.Padding * scale
	gap := def.Gap * scale

	switch def.Layout {
	case "horizontal":
		var maxH float32
		for i := range def.Children {
			ch := naturalHeight(&def.Children[i], scale)
			if ch > maxH {
				maxH = ch
			}
		}
		return 2*pad + maxH
	default: // vertical
		var total float32
		var count int
		for i := range def.Children {
			child := &def.Children[i]
			if child.Overlay != "" {
				continue
			}
			count++
			if child.Flex > 0 {
				continue // flex children contribute 0 to natural height
			}
			total += naturalHeight(child, scale)
		}
		if count > 1 {
			total += gap * float32(count-1)
		}
		return 2*pad + total
	}
}

// layoutChildren dispatches to horizontal or vertical layout based on node.Def.Layout.
// bindings is used to resolve visibility templates so hidden children don't consume space.
func layoutChildren(node *Node, parentRect Rect, scale float32, bindings ...map[string]string) {
	var b map[string]string
	if len(bindings) > 0 {
		b = bindings[0]
	}
	switch node.Def.Layout {
	case "horizontal":
		layoutHorizontal(node, parentRect, scale, b)
	default:
		layoutVerticalInner(node, parentRect, scale, b)
	}
}

// isHidden returns true if the node's Visible field resolves to false.
func isHidden(def *WidgetDef, bindings map[string]string) bool {
	if def.Visible == "" || bindings == nil {
		return false
	}
	return !resolveBool(def.Visible, bindings)
}

// layoutVerticalInner positions children in a vertical stack within parentRect.
// It handles padding, gap, the overlay mechanism, and flex proportional sizing.
func layoutVerticalInner(node *Node, parentRect Rect, scale float32, bindings map[string]string) {
	pad := node.Def.Padding * scale
	gap := node.Def.Gap * scale

	innerX := parentRect.X + pad
	innerW := parentRect.W - 2*pad
	innerH := parentRect.H - 2*pad

	// First pass: sum fixed heights and total flex (skip overlays and hidden).
	var fixedTotal float32
	var totalFlex float32
	var nonOverlayCount int
	for _, child := range node.Children {
		if child.Def.Overlay != "" || isHidden(child.Def, bindings) {
			continue
		}
		nonOverlayCount++
		if child.Def.Height > 0 {
			fixedTotal += child.Def.Height * scale
		} else if child.Def.Flex > 0 {
			totalFlex += child.Def.Flex
		} else {
			fixedTotal += naturalHeight(child.Def, scale)
		}
	}

	totalGap := gap * float32(max(0, nonOverlayCount-1))
	remaining := innerH - fixedTotal - totalGap
	if remaining < 0 {
		remaining = 0
	}

	// Second pass: assign rects.
	cursorY := parentRect.Y + pad
	siblingY := make(map[string]float32) // id -> Y position for overlay references

	for _, child := range node.Children {
		// Skip hidden children entirely — don't consume layout space.
		if isHidden(child.Def, bindings) {
			child.Rect = Rect{}
			continue
		}

		childH := child.Def.Height * scale
		if childH <= 0 {
			if child.Def.Flex > 0 && totalFlex > 0 {
				childH = child.Def.Flex / totalFlex * remaining
			} else {
				childH = naturalHeight(child.Def, scale)
			}
		}

		childW := innerW
		if child.Def.Width > 0 {
			childW = child.Def.Width * scale
		}

		if child.Def.Overlay != "" {
			// Overlay: position at the same Y as the referenced sibling.
			if refY, ok := siblingY[child.Def.Overlay]; ok {
				child.Rect = Rect{X: innerX, Y: refY, W: childW, H: childH}
			} else {
				child.Rect = Rect{X: innerX, Y: cursorY, W: childW, H: childH}
			}
		} else {
			child.Rect = Rect{X: innerX, Y: cursorY, W: childW, H: childH}
			cursorY += childH + gap
		}

		if child.ID != "" {
			siblingY[child.ID] = child.Rect.Y
		}

		// Recurse for containers.
		if len(child.Children) > 0 {
			layoutChildren(child, child.Rect, scale, bindings)
		}
	}
}

// layoutHorizontal positions children in a horizontal row within parentRect.
// Fixed children: Width > 0. Flex children: Width == 0 && Flex > 0, share remaining space
// proportionally. Neither: gets 0 width (spacer with no size).
func layoutHorizontal(node *Node, parentRect Rect, scale float32, bindings map[string]string) {
	pad := node.Def.Padding * scale
	gap := node.Def.Gap * scale

	innerX := parentRect.X + pad
	innerY := parentRect.Y + pad
	innerW := parentRect.W - 2*pad
	innerH := parentRect.H - 2*pad

	// First pass: sum fixed widths and total flex (skip hidden).
	var fixedTotal float32
	var totalFlex float32
	var visibleCount int
	for _, child := range node.Children {
		if isHidden(child.Def, bindings) {
			continue
		}
		visibleCount++
		if child.Def.Width > 0 {
			fixedTotal += child.Def.Width * scale
		} else if child.Def.Flex > 0 {
			totalFlex += child.Def.Flex
		}
	}

	totalGap := gap * float32(max(0, visibleCount-1))
	remaining := innerW - fixedTotal - totalGap
	if remaining < 0 {
		remaining = 0
	}

	cursorX := innerX
	for _, child := range node.Children {
		if isHidden(child.Def, bindings) {
			child.Rect = Rect{}
			continue
		}

		var childW float32
		if child.Def.Width > 0 {
			childW = child.Def.Width * scale
		} else if child.Def.Flex > 0 && totalFlex > 0 {
			childW = child.Def.Flex / totalFlex * remaining
		}

		childH := innerH
		if child.Def.Height > 0 {
			childH = child.Def.Height * scale
		}

		child.Rect = Rect{X: cursorX, Y: innerY, W: childW, H: childH}
		cursorX += childW + gap

		// Recurse for containers.
		if len(child.Children) > 0 {
			layoutChildren(child, child.Rect, scale, bindings)
		}
	}
}
