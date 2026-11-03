package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

// profiler writes CPU and heap profiles for the hidden --cpuprofile/--memprofile flags.
type profiler struct {
	cpu, mem string
	cpuFile  *os.File
}

func (p *profiler) start() error {
	if p.cpu == "" {
		return nil
	}
	f, err := os.Create(p.cpu)
	if err != nil {
		return fmt.Errorf("cpuprofile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return fmt.Errorf("cpuprofile: %w", err)
	}
	p.cpuFile = f
	return nil
}

func (p *profiler) stop() error {
	if p.cpuFile != nil {
		pprof.StopCPUProfile()
		if err := p.cpuFile.Close(); err != nil {
			return fmt.Errorf("cpuprofile: %w", err)
		}
		p.cpuFile = nil
	}
	if p.mem == "" {
		return nil
	}
	f, err := os.Create(p.mem)
	if err != nil {
		return fmt.Errorf("memprofile: %w", err)
	}
	defer f.Close()
	runtime.GC()
	if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
		return fmt.Errorf("memprofile: %w", err)
	}
	return nil
}
