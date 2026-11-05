package tfregistry

import (
	"encoding/json/v2"
	"slices"
	"testing"
)

// oracleVersions is the struct unmarshal fetchVersions used before versionList.
func oracleVersions(data []byte) ([]string, error) {
	var doc struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range doc.Versions {
		out = append(out, v.Version)
	}
	for _, m := range doc.Modules {
		for _, v := range m.Versions {
			out = append(out, v.Version)
		}
	}
	return out, nil
}

var versionListSeeds = []string{
	providerVersions,
	moduleVersions,
	awsShapedVersions(),
	`{}`, `null`, `{"versions":null,"modules":null}`, `{"versions":[null,{}]}`,
	`{"modules":[null,{"versions":null},{"source":"x","versions":[{"version":"1"}]}]}`,
	`{"versions":[{"version":null}],"modules":[{"versions":[{"version":"2","x":[1,{"a":2}]}]}]}`,
	`{"versions":[{"version":"1"}],"modules":[{"versions":[{"version":"2"}]}]}`,
	`{"versions":5}`, `{"versions":[5]}`, `{"versions":[{"version":5}]}`, `{"modules":{}}`,
	`{"versions":[{"version":"1","version":"2"}]}`, `{"Versions":[{"Version":"1"}]}`,
	`{"versions":[`, `[]`, `"x"`, ``,
}

func checkVersionListParity(t *testing.T, data []byte) {
	want, wantErr := oracleVersions(data)
	var got versionList
	err := json.Unmarshal(data, &got)
	if (err != nil) != (wantErr != nil) {
		t.Fatalf("%q: err = %v, oracle err = %v", data, err, wantErr)
	}
	if err == nil && !slices.Equal(versions(got), want) {
		t.Fatalf("%q: versions = %q, oracle %q", data, versions(got), want)
	}
}

func TestVersionListMatchesStructUnmarshal(t *testing.T) {
	for _, s := range versionListSeeds {
		checkVersionListParity(t, []byte(s))
	}
}

func FuzzVersionListMatchesStructUnmarshal(f *testing.F) {
	for _, s := range versionListSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(checkVersionListParity)
}
