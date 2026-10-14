package extract

// Guard against a row field that is declared and never populated.
//
// This exists because three of them shipped: OperationData's
// ContractCallsJSON, ContractsInvolved and MaxCallDepth were declared when
// the type was introduced and assigned nowhere, because integrateCallGraph
// was a no-op stub. Every consumer read nil and had no way to tell that from
// a ledger with no cross-contract calls.
//
// A field-by-field comparison between two independent ingesters could not
// catch it: both read the same nil and agreed. Nothing short of reading the
// library's own source would have, so this test reads it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// knownUnpopulated are fields deliberately left nil, with the reason.
//
// Adding an entry here is a decision that a consumer will always read nil
// from this field. Prefer deleting the field, or populating it.
var knownUnpopulated = map[string]string{
	// Superseded by DetailsJSON, which carries the full effect payload for
	// all 50+ effect types. These predate it and are kept so a consumer
	// reading the struct does not break on the field disappearing.
	"EffectData.TrustlineLimit": "legacy; see DetailsJSON",
	"EffectData.AuthorizeFlag":  "legacy; see DetailsJSON",
	"EffectData.ClawbackFlag":   "legacy; see DetailsJSON",
	"EffectData.SignerAccount":  "legacy; see DetailsJSON",
	"EffectData.SignerWeight":   "legacy; see DetailsJSON",
	"EffectData.OfferID":        "legacy; see DetailsJSON",
	"EffectData.SellerAccount":  "legacy; see DetailsJSON",
}

func TestEveryRowFieldIsPopulatedSomewhere(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["extract"]
	if !ok {
		t.Fatalf("package extract not found; parsed %v", keysOf(pkgs))
	}

	files := make([]*ast.File, 0, len(pkg.Files))
	for _, f := range pkg.Files {
		files = append(files, f)
	}

	rowTypes := collectRowTypes(files)
	if len(rowTypes) < 20 {
		t.Fatalf("found only %d row types; the scan is broken, not the code", len(rowTypes))
	}
	returnsType := collectFuncReturnTypes(files)

	var unpopulated []string
	for typeName, fields := range rowTypes {
		assigned := assignedFields(files, typeName, returnsType)
		for _, f := range fields {
			if assigned[f] {
				continue
			}
			key := typeName + "." + f
			if _, known := knownUnpopulated[key]; known {
				continue
			}
			unpopulated = append(unpopulated, key)
		}
	}
	sort.Strings(unpopulated)

	for _, key := range unpopulated {
		t.Errorf("%s is declared but never assigned: every consumer reads nil, "+
			"and cannot distinguish that from the ledger genuinely having no value. "+
			"Populate it, delete it, or record the reason in knownUnpopulated.", key)
	}

	// A stale allowlist is its own problem: it would suppress a regression on
	// a field that has since been wired up.
	for key := range knownUnpopulated {
		typeName, field, _ := strings.Cut(key, ".")
		fields, ok := rowTypes[typeName]
		if !ok {
			t.Errorf("knownUnpopulated names %s, but type %s no longer exists", key, typeName)
			continue
		}
		if !contains(fields, field) {
			t.Errorf("knownUnpopulated names %s, but that field no longer exists", key)
			continue
		}
		if assignedFields(files, typeName, returnsType)[field] {
			t.Errorf("%s is listed as never populated but is now assigned; remove it from knownUnpopulated", key)
		}
	}
}

// collectRowTypes returns the exported fields of every extraction row type.
func collectRowTypes(files []*ast.File) map[string][]string {
	out := map[string][]string{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			name := ts.Name.Name
			if !strings.HasSuffix(name, "Data") && !strings.HasSuffix(name, "RowData") {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			var fields []string
			for _, fld := range st.Fields.List {
				for _, id := range fld.Names {
					if id.IsExported() {
						fields = append(fields, id.Name)
					}
				}
			}
			if len(fields) > 0 {
				out[name] = fields
			}
			return true
		})
	}
	// LedgerInput and friends are inputs, not rows.
	delete(out, "LedgerData")
	return out
}

// collectFuncReturnTypes maps a function name to the row type it returns, so
// that `row := buildThing(...)` followed by `row.Field = x` counts as an
// assignment. Missing this is what made an earlier hand-rolled version of
// this scan report a false positive on ContractEventData.EraID.
func collectFuncReturnTypes(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
				continue
			}
			if name := typeNameOf(fn.Type.Results.List[0].Type); name != "" {
				out[fn.Name.Name] = name
			}
		}
	}
	return out
}

// assignedFields returns the fields of typeName assigned anywhere: as a key
// in a composite literal of that type, or through a selector on a variable
// known to hold one.
func assignedFields(files []*ast.File, typeName string, returnsType map[string]string) map[string]bool {
	assigned := map[string]bool{}

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if typeNameOf(node.Type) != typeName {
					return true
				}
				for _, elt := range node.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							assigned[id.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				for name := range identsHolding(node, typeName, returnsType) {
					collectSelectorAssignments(node, name, assigned)
				}
			}
			return true
		})
	}
	return assigned
}

// identsHolding finds local names bound to typeName within one function:
// composite literals, var declarations, calls to constructors, and
// parameters of pointer type.
func identsHolding(fn *ast.FuncDecl, typeName string, returnsType map[string]string) map[string]bool {
	names := map[string]bool{}

	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			if typeNameOf(p.Type) == typeName {
				for _, id := range p.Names {
					names[id.Name] = true
				}
			}
		}
	}

	ast.Inspect(fn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				if rhsTypeName(node.Rhs[i], returnsType) == typeName {
					names[id.Name] = true
				}
			}
		case *ast.DeclStmt:
			gd, ok := node.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || typeNameOf(vs.Type) != typeName {
					continue
				}
				for _, id := range vs.Names {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	return names
}

func rhsTypeName(expr ast.Expr, returnsType map[string]string) string {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return typeNameOf(e.Type)
	case *ast.UnaryExpr:
		return rhsTypeName(e.X, returnsType)
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok {
			return returnsType[id.Name]
		}
	}
	return ""
}

func collectSelectorAssignments(fn *ast.FuncDecl, varName string, assigned map[string]bool) {
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == varName {
				assigned[sel.Sel.Name] = true
			}
		}
		return true
	})
}

// typeNameOf unwraps pointers and qualified names down to a bare type name.
func typeNameOf(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return typeNameOf(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
