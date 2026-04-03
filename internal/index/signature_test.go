package index

import (
	"testing"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func TestBuildMethodSignatureInfo_Symlinks(t *testing.T) {
	// Mock symbols for parameters
	param1 := &sdb.SymbolInformation{
		Symbol:      "local0",
		DisplayName: "arg1",
		Signature: &sdb.Signature{
			SealedValue: &sdb.Signature_ValueSignature{
				ValueSignature: &sdb.ValueSignature{
					Tpe: &sdb.Type{
						SealedValue: &sdb.Type_TypeRef{
							TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"},
						},
					},
				},
			},
		},
	}
	param2 := &sdb.SymbolInformation{
		Symbol:      "local1",
		DisplayName: "arg2",
		Signature: &sdb.Signature{
			SealedValue: &sdb.Signature_ValueSignature{
				ValueSignature: &sdb.ValueSignature{
					Tpe: &sdb.Type{
						SealedValue: &sdb.Type_TypeRef{
							TypeRef: &sdb.TypeRef{Symbol: "int#"},
						},
					},
				},
			},
		},
	}

	lookup := map[string]*sdb.SymbolInformation{
		"local0": param1,
		"local1": param2,
	}

	// Method signature with symlinks
	methodSig := &sdb.MethodSignature{
		ReturnType: &sdb.Type{
			SealedValue: &sdb.Type_TypeRef{
				TypeRef: &sdb.TypeRef{Symbol: "void#"},
			},
		},
		ParameterLists: []*sdb.Scope{
			{
				Symlinks: []string{"local0", "local1"},
			},
		},
	}

	sig := &sdb.Signature{
		SealedValue: &sdb.Signature_MethodSignature{
			MethodSignature: methodSig,
		},
	}

	info := buildSignatureInfo("myMethod", sig, lookup)

	expectedLabel := "void myMethod(String arg1, int arg2)"
	if info.Label != expectedLabel {
		t.Errorf("expected label %q, got %q", expectedLabel, info.Label)
	}

	if !info.HasParams {
		t.Error("expected HasParams to be true")
	}

	if len(info.Params) != 2 {
		t.Errorf("expected 2 params, got %d", len(info.Params))
	}

	if info.Params[0].Name != "arg1" || info.Params[0].Type != "String" {
		t.Errorf("param 0 mismatch: %+v", info.Params[0])
	}

	if info.Params[1].Name != "arg2" || info.Params[1].Type != "int" {
		t.Errorf("param 1 mismatch: %+v", info.Params[1])
	}
}

func TestBuildMethodSignatureInfo_Hardlinks(t *testing.T) {
	// Mock symbols for parameters
	param1 := &sdb.SymbolInformation{
		Symbol:      "local0",
		DisplayName: "arg1",
		Signature: &sdb.Signature{
			SealedValue: &sdb.Signature_ValueSignature{
				ValueSignature: &sdb.ValueSignature{
					Tpe: &sdb.Type{
						SealedValue: &sdb.Type_TypeRef{
							TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"},
						},
					},
				},
			},
		},
	}

	// Method signature with hardlinks
	methodSig := &sdb.MethodSignature{
		ReturnType: &sdb.Type{
			SealedValue: &sdb.Type_TypeRef{
				TypeRef: &sdb.TypeRef{Symbol: "void#"},
			},
		},
		ParameterLists: []*sdb.Scope{
			{
				Hardlinks: []*sdb.SymbolInformation{param1},
			},
		},
	}

	sig := &sdb.Signature{
		SealedValue: &sdb.Signature_MethodSignature{
			MethodSignature: methodSig,
		},
	}

	info := buildSignatureInfo("myMethod", sig, nil)

	expectedLabel := "void myMethod(String arg1)"
	if info.Label != expectedLabel {
		t.Errorf("expected label %q, got %q", expectedLabel, info.Label)
	}
}
