package main

import (
	"errors"
	"fmt"
	"go/types"
	"log"
	"net/http"
	"os"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

func main() {
	patterns := os.Args[1:]
	if len(patterns) == 0 {
		patterns = []string{"."} // point this at a main package
	}

	pkgs, _ := loadPackages(patterns)
	prog, ssaPkgs, userPkgs := constructSSA(pkgs)

	// --- Stage 3: roots = init + main of every main package ---
	mains := ssautil.MainPackages(ssaPkgs)
	if len(mains) == 0 {
		log.Fatal("no main package found (closed-world analysis needs an entry point)")
	}
	var roots []*ssa.Function
	for _, m := range mains {
		for _, name := range []string{"init", "main"} {
			if fn := m.Func(name); fn != nil {
				roots = append(roots, fn)
			}
		}
	}

	// --- Stage 4: RTA, then read off the three results ---
	res := rta.Analyze(roots, true)

	// (a) THE CALL GRAPH — this feeds your visualizer.
	_ = res.CallGraph // *callgraph.Graph: res.CallGraph.Nodes, node.Out edges, etc.

	// (b) DEAD FUNCTIONS — user funcs RTA never proved reachable.
	fmt.Println("== possibly dead functions ==")
	for fn := range ssautil.AllFunctions(prog) {
		if fn.Pkg == nil || !userPkgs[fn.Pkg] {
			continue // skip deps and package-less synthetic funcs
		}
		if _, reachable := res.Reachable[fn]; !reachable {
			fmt.Printf("  %s\n", fn)
		}
	}

	// (c) INSTANTIATED TYPES — for your "unused struct" signal.
	instantiated := make(map[types.Type]bool)
	res.RuntimeTypes.Iterate(func(t types.Type, _ any) {
		instantiated[t] = true
	})
	// A user-defined named struct absent from `instantiated` was never created.
}

func GraphHandler(w http.ResponseWriter, r *http.Request) {

}

// Load packages into memory, if error then returns nil.
func loadPackages(patterns []string) ([]*packages.Package, error) {
	// --- Stage 1: load + type-check ---
	fmt.Println("=== Stage 1: Load + type check ===")
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		// Dir:   "",    // defaults to cwd
		Tests: false, // flip on if you want _test.go code in the graph
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		log.Fatalf("load: %v", err)
		return pkgs, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		log.Fatal("packages contain type errors; aborting") // silent poison otherwise
		return nil, errors.New("packages contain error")
	}

	return pkgs, err
}

func constructSSA(pkgs []*packages.Package) (*ssa.Program, []*ssa.Package, map[*ssa.Package]bool) {
	// --- Stage 2: build SSA for the whole program (incl. deps) ---
	fmt.Println("=== Stage 2: Build SSA ===")
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	// Track which SSA packages are *user code* (the initial set), so later
	// you can ignore stdlib/deps when reporting "dead" things.
	userPkgs := make(map[*ssa.Package]bool)
	for _, p := range ssaPkgs {
		if p != nil {
			userPkgs[p] = true
		}
	}

	return prog, ssaPkgs, userPkgs
}
