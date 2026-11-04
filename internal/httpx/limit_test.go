package httpx

import "testing"

func TestNewLimits(t *testing.T) {
	for _, c := range []struct {
		n                               int
		def, npm, docker, osvCap, maven int
	}{
		{1, 1, 2, 1, 2, 2},
		{3, 3, 6, 1, 6, 6},
		{32, 32, 64, 16, 64, 64},
	} {
		l := NewLimits(c.n)
		want := [6]int{c.def, c.npm, c.docker, c.osvCap, c.osvCap, c.maven}
		if got := [6]int{cap(l.Default), cap(l.NPM), cap(l.Docker), cap(l.OSV), cap(l.Crates), cap(l.Maven)}; got != want {
			t.Errorf("NewLimits(%d) capacities (default, npm, docker, osv, crates, maven) = %v, want %v", c.n, got, want)
		}
	}
}

// TestLimitsIndependent checks that a pool with every slot taken doesn't block the others.
func TestLimitsIndependent(t *testing.T) {
	l := NewLimits(2)
	pools := []chan struct{}{l.Default, l.NPM, l.Docker, l.OSV, l.Crates, l.Maven}
	for full := range pools {
		for range cap(pools[full]) {
			pools[full] <- struct{}{}
		}
		for i, p := range pools {
			if i == full {
				continue
			}
			select {
			case p <- struct{}{}:
				<-p
			default:
				t.Errorf("pool %d blocked while pool %d is full", i, full)
			}
		}
		for range cap(pools[full]) {
			<-pools[full]
		}
	}
}
