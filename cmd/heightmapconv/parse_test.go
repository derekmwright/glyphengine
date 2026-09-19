package main

import "testing"

func TestParseWxH(t *testing.T) {
	w, h, err := parseWxH("129x65")
	if err != nil || w != 129 || h != 65 {
		t.Errorf("parseWxH(129x65) = %d,%d,%v, want 129,65,nil", w, h, err)
	}
	if _, _, err := parseWxH("1x1"); err == nil {
		t.Error("parseWxH(1x1) succeeded, want an error (grid must be at least 2x2)")
	}
	if _, _, err := parseWxH("bogus"); err == nil {
		t.Error("parseWxH(bogus) succeeded, want an error")
	}
}

func TestParseXY(t *testing.T) {
	a, b, err := parseXY("-1.5,2.25")
	if err != nil || a != -1.5 || b != 2.25 {
		t.Errorf("parseXY(-1.5,2.25) = %g,%g,%v, want -1.5,2.25,nil", a, b, err)
	}
	if _, _, err := parseXY("1"); err == nil {
		t.Error("parseXY(1) succeeded, want an error")
	}
}

func TestParseBounds(t *testing.T) {
	minX, minZ, maxX, maxZ, err := parseBounds("-10,-5,10,5")
	if err != nil || minX != -10 || minZ != -5 || maxX != 10 || maxZ != 5 {
		t.Errorf("parseBounds = %g,%g,%g,%g,%v, want -10,-5,10,5,nil", minX, minZ, maxX, maxZ, err)
	}
	if _, _, _, _, err := parseBounds("1,2,3"); err == nil {
		t.Error("parseBounds(1,2,3) succeeded, want an error (needs 4 numbers)")
	}
}

func TestResolveGrid(t *testing.T) {
	w, h, err := resolveGrid("65x33", 0, 100, 100)
	if err != nil || w != 65 || h != 33 {
		t.Errorf("resolveGrid with -grid = %d,%d,%v, want 65,33,nil", w, h, err)
	}

	// -cell 2 over a 100x50 world -> 51x26 grid (100/2+1, 50/2+1).
	w, h, err = resolveGrid("", 2, 100, 50)
	if err != nil || w != 51 || h != 26 {
		t.Errorf("resolveGrid with -cell 2 over 100x50 = %d,%d,%v, want 51,26,nil", w, h, err)
	}

	if _, _, err := resolveGrid("", 0, 100, 100); err == nil {
		t.Error("resolveGrid with neither -grid nor -cell succeeded, want an error")
	}
}
