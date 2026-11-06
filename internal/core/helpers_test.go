package core

import (
	"reflect"
	"testing"
)

// Helpers shared by the tests of this package.

func mkField(name string, ft FieldType) DocField {
	return DocField{Name: name, Label: name, Type: ft}
}

func mkDocType(fields ...DocField) *DocType {
	return &DocType{Name: "thing", Label: "Thing", Fields: fields}
}

func named(name string, isChild bool, fields ...DocField) *DocType {
	d := mkDocType(fields...)
	d.Name = name
	d.IsChild = isChild
	return d
}

func linkField(name, target string) DocField {
	return mkField(name, FieldType{Kind: FieldLink, Target: target})
}

func tableField(name, target string) DocField {
	return mkField(name, FieldType{Kind: FieldTable, Target: target})
}

func dataField(name string, max uint32) DocField {
	return mkField(name, FieldType{Kind: FieldData, MaxLength: max})
}

func intField(name string) DocField { return mkField(name, FieldType{Kind: FieldInt}) }

func textField(name string) DocField { return mkField(name, FieldType{Kind: FieldText}) }

func selectField(name string, options ...string) DocField {
	return mkField(name, FieldType{Kind: FieldSelect, Options: options})
}

func decimalField(name string, precision, scale uint8) DocField {
	return mkField(name, FieldType{Kind: FieldDecimal, Precision: precision, Scale: scale})
}

func sameValue(t *testing.T, got, want any, label string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", label, got, want)
	}
}

// defErrs is the DefinitionErrors of err, which must be a *MetaError of kind MetaInvalid.
func invalidOf(t *testing.T, text string) []DefinitionError {
	t.Helper()
	_, err := ParseDocType(text)
	me, ok := err.(*MetaError)
	if !ok || me.Kind != MetaInvalid {
		t.Fatalf("expected an invalid-definition error, got %v", err)
	}
	return me.Problems
}

func mustDocType(t *testing.T, text string) *DocType {
	t.Helper()
	d, err := ParseDocType(text)
	if err != nil {
		t.Fatalf("ParseDocType: %v\n%s", err, text)
	}
	return d
}

func isParseError(err error) bool {
	me, ok := err.(*MetaError)
	return ok && me.Kind == MetaParse
}

func hasErr(errs []DefinitionError, want DefinitionError) bool {
	for _, e := range errs {
		if reflect.DeepEqual(e, want) {
			return true
		}
	}
	return false
}

func hasKind(errs []DefinitionError, kind DefinitionErrorKind) bool {
	for _, e := range errs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
