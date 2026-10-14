package extract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

type mapRangeAppend struct {
	where   string
	mapName string
}

// findMapRangeAppends reports `for _, v := range someMap { slice = append(slice, ...) }`
// over a map built with make(map[...]...), which is the shape that produced
// randomized row order.
func findMapRangeAppends(t *testing.T) []mapRangeAppend {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["extract"]
	if !ok {
		t.Fatal("package extract not found")
	}

	var out []mapRangeAppend
	for _, file := range pkg.Files {
		mapVars := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(as.Rhs) {
					continue
				}
				call, ok := as.Rhs[i].(*ast.CallExpr)
				if !ok {
					continue
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "make" || len(call.Args) == 0 {
					continue
				}
				if _, isMap := call.Args[0].(*ast.MapType); isMap {
					mapVars[id.Name] = true
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			rng, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			id, ok := rng.X.(*ast.Ident)
			if !ok || !mapVars[id.Name] {
				return true
			}
			if !bodyAppends(rng.Body) {
				return true
			}
			out = append(out, mapRangeAppend{
				where:   fmt.Sprintf("%s:%d", fset.Position(rng.Pos()).Filename, fset.Position(rng.Pos()).Line),
				mapName: id.Name,
			})
			return true
		})
	}
	return out
}

func bodyAppends(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "append" {
			found = true
		}
		return true
	})
	return found
}
