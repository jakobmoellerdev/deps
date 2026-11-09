package toolrun

import (
	"context"
	"os"
)

// patchedKey is the context key of WithPatched.
type patchedKey struct{}

// WithPatched returns ctx carrying files, the content a build gives the files it patches, by
// absolute path, for the post-processors it runs: what they read of other managers' files
// through ReadFile is the branch's (cargo reads the rust-toolchain file a grouped toolchain
// update moves). Without files ctx is returned as is.
func WithPatched(ctx context.Context, files map[string][]byte) context.Context {
	if len(files) == 0 {
		return ctx
	}
	return context.WithValue(ctx, patchedKey{}, files)
}

// ReadFile returns the content of the file at the absolute path p: as the build patched it
// (WithPatched), else as on disk.
func ReadFile(ctx context.Context, p string) ([]byte, error) {
	if files, ok := ctx.Value(patchedKey{}).(map[string][]byte); ok {
		if b, ok := files[p]; ok {
			return b, nil
		}
	}
	return os.ReadFile(p)
}
