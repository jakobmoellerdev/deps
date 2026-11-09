package engine

import (
	vcpkgds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/vcpkg"
)

// vcpkgBaselines returns the index in todo of every vcpkg registry baseline dep by its
// vcpkg.BaselineKey (the dep.Dep.Baseline of the ports read at it); nil without any.
func vcpkgBaselines(todo []dep.Dep) map[string]int {
	var m map[string]int
	for i, d := range todo {
		if d.Manager != vcpkg.Name || d.Datasource == vcpkgds.Name {
			continue
		}
		if m == nil {
			m = map[string]int{}
		}
		m[vcpkg.BaselineKey(d)] = i
	}
	return m
}

// portAtBaseline resolves the vcpkg port d at the new commit of its registry baseline when
// the baseline moves in this run (the digest update among baseUps), so a baseline bump and
// the port versions it unlocks arrive in one run. They must also land in one PR, as a port
// version newer than the baseline does not install with the old one: when the port's updates
// at the new commit do not all share the baseline update's group (the built-in vcpkg group
// unless a rule sets another) and bucket, or carry advisories (which leave groups), d is
// resolved at its current baseline instead, as it is when the baseline does not move.
func portAtBaseline(d dep.Dep, baseUps []dep.Update, resolve func(dep.Dep) (Trace, []dep.Update, resolver)) (Trace, []dep.Update, resolver) {
	var moved *dep.Update
	for k := range baseUps {
		if u := &baseUps[k]; u.Type == dep.Digest && u.NewDigest != "" {
			moved = u
			break
		}
	}
	if moved == nil || moved.Group == "" {
		return resolve(d)
	}
	at := d
	at.Source = vcpkgds.RawBase(moved.Dep.Source, moved.NewDigest)
	t, ups, r := resolve(at)
	for _, u := range ups {
		if u.Group != moved.Group || u.Bucket != moved.Bucket || len(u.Advisories) > 0 {
			return resolve(d)
		}
	}
	return t, ups, r
}
