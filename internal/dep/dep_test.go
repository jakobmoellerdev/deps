package dep

import "testing"

func TestFromTo(t *testing.T) {
	const sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	tests := []struct {
		name     string
		u        Update
		from, to string
	}{
		{"plain", Update{Dep: Dep{Version: "v1.0.0"}, NewVersion: "v1.1.0"}, "v1.0.0", "v1.1.0"},
		{
			"pinned to pinned",
			Update{Dep: Dep{Version: "v6", Digest: "1af3b93b6815bc44a9784bd300feb67ff0d1eeb3"}, NewVersion: "v7", NewDigest: sha},
			"v6@1af3b93", "v7@3d3c42e",
		},
		{"tag to digest", Update{Dep: Dep{Version: "v7"}, NewVersion: "v7", NewDigest: sha}, "v7", "v7@3d3c42e"},
		{"short digests", Update{Dep: Dep{Version: "v1", Digest: "abc"}, NewVersion: "v2", NewDigest: "de"}, "v1@abc", "v2@de"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to := tt.u.FromTo()
			if from != tt.from || to != tt.to {
				t.Fatalf("FromTo() = %q, %q; want %q, %q", from, to, tt.from, tt.to)
			}
		})
	}
}
