package main

import (
	"reflect"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

func TestRunResultMergesBases(t *testing.T) {
	wMain := engine.Warning{Dep: dep.Dep{Name: "a"}, Kind: engine.WarnDeprecated, Reason: "package deprecated"}
	wRel := engine.Warning{Dep: dep.Dep{Name: "b"}, Kind: engine.WarnAbandoned, Reason: "no release since 2020-02-11"}
	tests := []struct {
		name  string
		bases []bot.Base
		want  []engine.Warning
	}{
		{"one base", []bot.Base{{Name: "main", Res: &engine.Result{Warnings: []engine.Warning{wMain}}}}, []engine.Warning{wMain}},
		{"two bases", []bot.Base{
			{Name: "main", Res: &engine.Result{Warnings: []engine.Warning{wMain}}},
			{Name: "release/1", Res: &engine.Result{Warnings: []engine.Warning{wRel}}},
		}, []engine.Warning{wMain, wRel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := runResult(tt.bases, "deps/", len(tt.bases) > 1, nil)
			if !reflect.DeepEqual(res.Warnings, tt.want) {
				t.Errorf("Warnings = %+v, want %+v", res.Warnings, tt.want)
			}
		})
	}
}
