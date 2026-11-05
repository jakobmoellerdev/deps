package versioning

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
)

// Extractor is a compiled extractVersion expression (Renovate's `extractVersion`): an RE2
// regexp with a `version` group that maps a datasource's release name (a tag such as
// `jq-1.7.1` or `kustomize/v5.4.3`) to the version written into files (`1.7.1`,
// `v5.4.3`). Both (?P<version>…) and (?<version>…) syntax work.
type Extractor struct {
	re  *regexp.Regexp
	grp int
}

// extractors caches compiled expressions; expressions come from config and managers and
// are few.
var extractors sync.Map // string -> *Extractor

// CompileExtract compiles expr once per process.
func CompileExtract(expr string) (*Extractor, error) {
	if e, ok := extractors.Load(expr); ok {
		return e.(*Extractor), nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid RE2 regexp: %w", err)
	}
	grp := re.SubexpIndex("version")
	if grp < 0 {
		return nil, errors.New("needs a (?<version>…) group")
	}
	e, _ := extractors.LoadOrStore(expr, &Extractor{re: re, grp: grp})
	return e.(*Extractor), nil
}

// Version returns the version in release name, false when the expression does not match
// or the version group is empty.
func (e *Extractor) Version(name string) (string, bool) {
	m := e.re.FindStringSubmatchIndex(name)
	if len(m) <= 2*e.grp+1 || m[2*e.grp] < 0 || m[2*e.grp] == m[2*e.grp+1] {
		return "", false
	}
	return name[m[2*e.grp]:m[2*e.grp+1]], true
}
