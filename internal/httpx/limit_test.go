package httpx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

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

func TestAcquire(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	// nil is unbounded: no error even for a done context, and a callable release.
	release, err := Acquire(canceled, nil)
	if err != nil || release == nil {
		t.Fatalf("Acquire(nil sem): release nil %t, err %v; want a release and no error", release == nil, err)
	}
	release()

	sem := make(chan struct{}, 1)
	release, err = Acquire(t.Context(), sem)
	if err != nil || len(sem) != 1 {
		t.Fatalf("Acquire = %v, slots taken %d; want nil, 1", err, len(sem))
	}
	if _, err := Acquire(canceled, sem); !errors.Is(err, context.Canceled) {
		t.Errorf("Acquire(full sem, canceled) = %v, want context.Canceled", err)
	}
	release()
	if len(sem) != 0 {
		t.Errorf("slots taken after release = %d, want 0", len(sem))
	}
}

func TestInFlight(t *testing.T) {
	l := NewLimits(4)
	if got := l.InFlight(); got != nil {
		t.Fatalf("idle: %v", got)
	}
	for range 3 {
		l.NPM <- struct{}{}
	}
	l.Default <- struct{}{}
	l.Docker <- struct{}{}
	want := []PoolUse{{"npm", 3}, {"docker", 1}, {"default", 1}}
	if got := l.InFlight(); !reflect.DeepEqual(got, want) {
		t.Errorf("InFlight = %v, want %v", got, want)
	}
}
