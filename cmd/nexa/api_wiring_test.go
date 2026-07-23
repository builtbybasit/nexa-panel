package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestLockoutGuardIsWired pins the one piece of the confirm-or-revert design
// that unit tests cannot reach. The firewall and services modules take their
// guard as an option, and every test in those packages supplies one, so a build
// that forgets to construct the guard here still passes every package suite —
// and then refuses lockout-capable changes outright on the real node, because
// both modules fail closed when the guard is nil. The API runner is not
// constructible in a test (it binds sockets and an agent), so the wiring is
// asserted against the source that performs it.
func TestLockoutGuardIsWired(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "api.go", nil, 0)
	if err != nil {
		t.Fatalf("parse api.go: %v", err)
	}

	constructed := false
	guarded := map[string]bool{"firewall": false, "services": false}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, name, ok := qualifiedCallee(call)
		if !ok {
			return true
		}
		if pkg == "safeguard" && name == "New" {
			constructed = true
			return true
		}
		if name != "New" {
			return true
		}
		if _, tracked := guarded[pkg]; !tracked {
			return true
		}
		for _, argument := range call.Args {
			optionCall, ok := argument.(*ast.CallExpr)
			if !ok {
				continue
			}
			optionPkg, optionName, ok := qualifiedCallee(optionCall)
			if ok && optionPkg == pkg && optionName == "WithLockoutGuard" {
				guarded[pkg] = true
			}
		}
		return true
	})

	if !constructed {
		t.Error("api.go never calls safeguard.New, so no armed revert can be recorded or reconciled on boot")
	}
	for pkg, ok := range guarded {
		if !ok {
			t.Errorf("api.go builds the %s module without %s.WithLockoutGuard, so lockout-capable changes are refused instead of applied with a timed revert", pkg, pkg)
		}
	}
}

// qualifiedCallee splits a pkg.Func(...) callee into its two halves.
func qualifiedCallee(call *ast.CallExpr) (pkg, name string, ok bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	return identifier.Name, selector.Sel.Name, true
}
