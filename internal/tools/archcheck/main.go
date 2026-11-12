// Command archcheck type-checks the module for 32-bit and other non-host
// targets without a C cross-compiler.
//
// openmanetd ships to linux/mipsle routers, where int is 32 bits. A plain
// GOARCH=mipsle build needs a cross gcc plus target copies of libopus, PAM,
// hidapi and friends, so CI never compiled for it and 32-bit-only errors
// (for example an untyped constant that overflows int) reached the firmware
// build. archcheck lists the packages with `go list` for each target, then
// type-checks them from source with go/types using that target's sizes.
// `import "C"` is faked, so cgo packages still check their Go code. Only
// errors in this module's own packages are reported.
//
// Usage:
//
//	go run ./internal/tools/archcheck [goos/goarch ...]
//
// With no arguments it checks linux/mipsle, the 32-bit firmware target.
// (amd64 and arm64 are covered by the normal host builds and tests.)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// listedPackage is the subset of `go list -json` output archcheck reads.
type listedPackage struct {
	ImportMap  map[string]string
	Module     *struct{ Main bool }
	Error      *struct{ Err string }
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
}

func main() {
	targets := os.Args[1:]
	if len(targets) == 0 {
		targets = []string{"linux/mipsle"}
	}

	failed := false

	for _, target := range targets {
		n, err := checkTarget(".", target, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "archcheck %s: %v\n", target, err)
			os.Exit(2)
		}

		if n > 0 {
			failed = true

			continue
		}

		fmt.Printf("archcheck %s: ok\n", target)
	}

	if failed {
		os.Exit(1)
	}
}

// checkTarget type-checks every package of the module in dir for target and
// writes each error in a main-module package to w. It returns the error count.
func checkTarget(dir, target string, w io.Writer) (int, error) {
	goos, goarch, ok := strings.Cut(target, "/")
	if !ok {
		return 0, fmt.Errorf("target %q is not goos/goarch", target)
	}

	sizes := types.SizesFor("gc", goarch)
	if sizes == nil {
		return 0, fmt.Errorf("unknown goarch %q", goarch)
	}

	pkgs, err := listPackages(dir, goos, goarch)
	if err != nil {
		return 0, err
	}

	c := &checker{
		fset:  token.NewFileSet(),
		sizes: sizes,
		done:  make(map[string]*types.Package, len(pkgs)),
		w:     w,
	}

	// go list -deps prints dependencies before the packages that import them.
	for _, p := range pkgs {
		c.check(target, p)
	}

	return c.errs, nil
}

func listPackages(dir, goos, goarch string) ([]*listedPackage, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-e", "-deps", "-json", "./...")
	cmd.Dir = dir

	cmd.Env = append(os.Environ(),
		"GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=1", "GOFLAGS=-tags=noasm")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, stderr.String())
	}

	var pkgs []*listedPackage

	dec := json.NewDecoder(bytes.NewReader(out))

	for {
		p := new(listedPackage)

		err := dec.Decode(p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}

		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}

		pkgs = append(pkgs, p)
	}
}

type checker struct {
	fset  *token.FileSet
	sizes types.Sizes
	done  map[string]*types.Package
	w     io.Writer
	errs  int
}

func (c *checker) check(target string, p *listedPackage) {
	if p.ImportPath == "unsafe" {
		c.done[p.ImportPath] = types.Unsafe

		return
	}

	main := p.Module != nil && p.Module.Main

	if p.Error != nil && main {
		c.errs++
		fmt.Fprintf(c.w, "%s: %s: %s\n", target, p.ImportPath, p.Error.Err)
	}

	names := make([]string, 0, len(p.GoFiles)+len(p.CgoFiles))
	names = append(names, p.GoFiles...)
	names = append(names, p.CgoFiles...)

	files := make([]*ast.File, 0, len(names))

	for _, name := range names {
		f, err := parser.ParseFile(c.fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			if main {
				c.errs++
				fmt.Fprintf(c.w, "%s: %v\n", target, err)
			}

			continue
		}

		files = append(files, f)
	}

	conf := types.Config{
		Importer:    importerFunc(func(path string) (*types.Package, error) { return c.lookup(p, path) }),
		Sizes:       c.sizes,
		FakeImportC: len(p.CgoFiles) > 0,
		Error: func(err error) {
			// With import "C" faked, constants and types that come from C
			// are invalid; errors that mention them are not real.
			if main && !strings.Contains(err.Error(), "invalid type") {
				c.errs++
				fmt.Fprintf(c.w, "%s: %v\n", target, err)
			}
		},
	}

	// Errors go through conf.Error; the package is kept even when incomplete
	// so its importers can still be checked.
	pkg, _ := conf.Check(p.ImportPath, c.fset, files, nil) //nolint:errcheck // reported via conf.Error
	c.done[p.ImportPath] = pkg
}

func (c *checker) lookup(from *listedPackage, path string) (*types.Package, error) {
	if mapped, ok := from.ImportMap[path]; ok {
		path = mapped
	}

	if pkg, ok := c.done[path]; ok && pkg != nil {
		return pkg, nil
	}

	return nil, fmt.Errorf("package %s not checked before %s", path, from.ImportPath)
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
