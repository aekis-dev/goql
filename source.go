package goql

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Registered source trees, consulted before the filesystem when locating a lambda.
//
// Each is anchored at the directory of the file that registered it. Without that anchor, two
// modules that both embed a file called main.go are indistinguishable — a lookup matches on
// the suffix, registration order decides, and one module's lambda is sought in the other's
// source. That was reproduced before this field existed.
type sourceTree struct {
	fsys fs.FS
	root string // directory of the caller of RegisterSource, as the runtime reports paths
}

var (
	sourceMu sync.RWMutex
	sources  []sourceTree
)

// RegisterSource makes a source tree available to the lambda parser, so a binary can read
// the lambdas it was compiled from without the source being on disk beside it.
//
// One directive at the module root covers a whole project, including nested packages:
//
//	//go:embed *.go internal
//	var goqlSource embed.FS
//
//	func init() { goql.RegisterSource(goqlSource) }
//
// This is an alternative to the ahead-of-time registry: with the source embedded there is
// nothing to generate and nothing that can fall out of step, because the embedded bytes are
// the bytes the compiler compiled.
func RegisterSource(fsys fs.FS) {
	// The caller's own file locates the tree: an embedded FS is rooted at the directory of
	// the package that declares it, and runtime paths are reported the same way here as they
	// are for a lambda — absolute normally, module-relative under -trimpath — so the two
	// always agree.
	root := ""
	if _, file, _, ok := runtime.Caller(1); ok {
		root = path.Dir(filepath.ToSlash(file))
	}

	sourceMu.Lock()
	sources = append(sources, sourceTree{fsys: fsys, root: root})
	sourceMu.Unlock()
}

// readSource returns the contents of the file the runtime named, preferring a registered
// tree and falling back to the filesystem.
//
// The runtime reports an absolute path normally and "<module>/<relpath>" under -trimpath.
// runtime.Caller inside RegisterSource reports paths the same way, so anchoring a tree at
// its registering package's directory makes the two directly comparable under either.
func readSource(file string) ([]byte, error) {
	sourceMu.RLock()
	trees := make([]sourceTree, len(sources))
	copy(trees, sources)
	sourceMu.RUnlock()

	want := filepath.ToSlash(file)
	for _, tree := range trees {
		if data, ok := tree.find(want); ok {
			return data, nil
		}
	}

	data, err := os.ReadFile(file)
	if err != nil && len(trees) > 0 {
		return nil, fmt.Errorf(
			"goql: %s is neither in a registered source tree nor on disk — if this binary "+
				"embeds its sources, check the //go:embed pattern covers this package: %w",
			file, err)
	}
	return data, err
}

// find resolves a runtime-reported path against this tree by rebuilding the absolute path
// each embedded entry would have had: the registering package's directory plus the entry's
// path within the tree. Comparing whole paths, rather than a suffix, is what keeps two
// modules' identically-named files apart.
func (t sourceTree) find(want string) ([]byte, bool) {
	if t.root == "" {
		return nil, false
	}
	if !strings.HasPrefix(want, t.root+"/") {
		return nil, false
	}
	rel := strings.TrimPrefix(want, t.root+"/")

	data, err := fs.ReadFile(t.fsys, rel)
	if err != nil {
		return nil, false
	}
	return data, true
}
