package gotool

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/zclconf/go-cty/cty"
)

const injectedTypes = `
type Issue struct {
	Code       string  ` + "`json:\"code\"`" + `
	Message    string  ` + "`json:\"message\"`" + `
	Path       *string ` + "`json:\"path,omitempty\"`" + `
	RepairHint *string ` + "`json:\"repair_hint,omitempty\"`" + `
}

type ToolResponse[T any] struct {
	Accepted bool    ` + "`json:\"accepted\"`" + `
	Output   *T      ` + "`json:\"output,omitempty\"`" + `
	Issues   []Issue ` + "`json:\"issues,omitempty\"`" + `
}
`

type Analysis struct {
	InputType  cty.Type
	OutputType cty.Type
}

func Analyze(source string) (Analysis, error) {
	if containsPackageClause(source) {
		return Analysis{}, fmt.Errorf("package clause is not allowed")
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "tool.go", "package main\n"+source+injectedTypes, parser.AllErrors)
	if err != nil {
		return Analysis{}, fmt.Errorf("parsing inline Go tool: %w", err)
	}
	if err = validateDeclarations(file); err != nil {
		return Analysis{}, err
	}
	if err = validateImports(file); err != nil {
		return Analysis{}, err
	}

	configuration := types.Config{Importer: importer.Default()}
	checked, err := configuration.Check("r42.inline.tool", fileSet, []*ast.File{file}, nil)
	if err != nil {
		return Analysis{}, fmt.Errorf("type checking inline Go tool: %w", err)
	}

	input, err := requiredNamedType(checked, "Input")
	if err != nil {
		return Analysis{}, err
	}
	output, err := requiredNamedType(checked, "Output")
	if err != nil {
		return Analysis{}, err
	}
	if err = validateInvoke(checked, input, output); err != nil {
		return Analysis{}, err
	}
	for _, name := range []string{"Issue", "ToolResponse"} {
		typeName, ok := checked.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			return Analysis{}, fmt.Errorf("injected type %s is unavailable", name)
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			return Analysis{}, fmt.Errorf("injected type %s is invalid", name)
		}
		if err = rejectCustomWireMethods(named, name); err != nil {
			return Analysis{}, err
		}
	}

	mapper := typeMapper{pkg: checked, visiting: make(map[types.Type]bool)}
	inputType, err := mapper.mapType(input, "Input")
	if err != nil {
		return Analysis{}, err
	}
	outputType, err := mapper.mapType(output, "Output")
	if err != nil {
		return Analysis{}, err
	}
	return Analysis{InputType: inputType, OutputType: outputType}, nil
}

func containsPackageClause(source string) bool {
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("tool.go", -1, len(source))
	var sourceScanner scanner.Scanner
	sourceScanner.Init(file, []byte(source), nil, scanner.ScanComments)
	for {
		_, scanned, _ := sourceScanner.Scan()
		if scanned == token.PACKAGE {
			return true
		}
		if scanned == token.EOF {
			return false
		}
	}
}

func validateDeclarations(file *ast.File) error {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == "main" {
			return fmt.Errorf("main function is not allowed")
		}
	}
	return nil
}

func validateImports(file *ast.File) error {
	for _, importSpec := range file.Imports {
		path, err := strconv.Unquote(importSpec.Path.Value)
		if err != nil {
			return fmt.Errorf("invalid import path %s: %w", importSpec.Path.Value, err)
		}
		imported, err := build.Default.Import(path, "", build.FindOnly)
		if err != nil || !imported.Goroot {
			return fmt.Errorf("import must be from the Go standard library: %q", path)
		}
	}
	return nil
}

func requiredNamedType(pkg *types.Package, name string) (*types.Named, error) {
	object := pkg.Scope().Lookup(name)
	if object == nil {
		return nil, fmt.Errorf("type %s is required", name)
	}
	typeName, ok := object.(*types.TypeName)
	if !ok || typeName.IsAlias() {
		return nil, fmt.Errorf("%s must be a defined type", name)
	}
	named, ok := typeName.Type().(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%s must be a defined type", name)
	}
	return named, nil
}

func validateInvoke(pkg *types.Package, input, output *types.Named) error {
	object := pkg.Scope().Lookup("Invoke")
	function, ok := object.(*types.Func)
	if !ok {
		return fmt.Errorf("Invoke must have signature func(context.Context, Input) (ToolResponse[Output], error)")
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() != nil || signature.Variadic() || signature.Params().Len() != 2 || signature.Results().Len() != 2 {
		return fmt.Errorf("Invoke must have signature func(context.Context, Input) (ToolResponse[Output], error)")
	}

	var contextPackage *types.Package
	for _, imported := range pkg.Imports() {
		if imported.Path() == "context" {
			contextPackage = imported
			break
		}
	}
	if contextPackage == nil {
		return fmt.Errorf("Invoke must have signature func(context.Context, Input) (ToolResponse[Output], error)")
	}
	contextType := contextPackage.Scope().Lookup("Context").Type()
	if !types.Identical(signature.Params().At(0).Type(), contextType) ||
		!types.Identical(signature.Params().At(1).Type(), input) ||
		!types.Identical(signature.Results().At(1).Type(), types.Universe.Lookup("error").Type()) {
		return fmt.Errorf("Invoke must have signature func(context.Context, Input) (ToolResponse[Output], error)")
	}

	response, ok := signature.Results().At(0).Type().(*types.Named)
	if !ok || response.TypeArgs() == nil || response.TypeArgs().Len() != 1 ||
		!types.Identical(response.TypeArgs().At(0), output) {
		return fmt.Errorf("Invoke must return ToolResponse[Output]")
	}
	responseDeclaration, ok := pkg.Scope().Lookup("ToolResponse").(*types.TypeName)
	if !ok {
		return fmt.Errorf("Invoke must return ToolResponse[Output]")
	}
	responseType, ok := responseDeclaration.Type().(*types.Named)
	if !ok || response.Origin() != responseType {
		return fmt.Errorf("Invoke must return ToolResponse[Output]")
	}
	return nil
}

type typeMapper struct {
	pkg      *types.Package
	visiting map[types.Type]bool
}

func (m typeMapper) mapType(source types.Type, path string) (cty.Type, error) {
	if m.visiting[source] {
		return cty.NilType, fmt.Errorf("%s: recursive Go types are not supported", path)
	}
	m.visiting[source] = true
	defer delete(m.visiting, source)

	if named, ok := source.(*types.Named); ok {
		if named.Obj().Pkg() != m.pkg {
			return cty.NilType, fmt.Errorf("%s: imported named Go types are not supported", path)
		}
		if err := rejectCustomWireMethods(named, path); err != nil {
			return cty.NilType, err
		}
		source = named.Underlying()
	}

	switch value := source.(type) {
	case *types.Basic:
		return mapBasic(value, path)
	case *types.Slice:
		if basic, ok := value.Elem().Underlying().(*types.Basic); ok && basic.Kind() == types.Byte {
			return cty.NilType, fmt.Errorf("%s: []byte has JSON string semantics and is not supported", path)
		}
		element, err := m.mapType(value.Elem(), path+"[]")
		if err != nil {
			return cty.NilType, err
		}
		return cty.List(element), nil
	case *types.Array:
		element, err := m.mapType(value.Elem(), path+"[]")
		if err != nil {
			return cty.NilType, err
		}
		elements := make([]cty.Type, value.Len())
		for index := range elements {
			elements[index] = element
		}
		return cty.Tuple(elements), nil
	case *types.Map:
		key, err := m.mapType(value.Key(), path+".<key>")
		if err != nil {
			return cty.NilType, err
		}
		if !key.Equals(cty.String) {
			return cty.NilType, fmt.Errorf("%s: map keys must be strings", path)
		}
		element, err := m.mapType(value.Elem(), path+".*")
		if err != nil {
			return cty.NilType, err
		}
		return cty.Map(element), nil
	case *types.Struct:
		return m.mapStruct(value, path)
	default:
		return cty.NilType, fmt.Errorf("%s: unsupported Go type %s", path, source.String())
	}
}

func rejectCustomWireMethods(named *types.Named, path string) error {
	wireMethods := map[string]struct{}{
		"MarshalJSON":   {},
		"UnmarshalJSON": {},
		"MarshalText":   {},
		"UnmarshalText": {},
	}
	for _, receiver := range []types.Type{named, types.NewPointer(named)} {
		methods := types.NewMethodSet(receiver)
		for method := range methods.Methods() {
			if _, changesWireShape := wireMethods[method.Obj().Name()]; changesWireShape {
				return fmt.Errorf("%s: custom JSON or text methods are not supported", path)
			}
		}
	}
	return nil
}

func mapBasic(value *types.Basic, path string) (cty.Type, error) {
	switch value.Kind() {
	case types.Bool:
		return cty.Bool, nil
	case types.String:
		return cty.String, nil
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr,
		types.Float32, types.Float64:
		return cty.Number, nil
	default:
		return cty.NilType, fmt.Errorf("%s: unsupported Go type %s", path, value.Name())
	}
}

func (m typeMapper) mapStruct(value *types.Struct, path string) (cty.Type, error) {
	attributes := make(map[string]cty.Type)
	optional := make([]string, 0)
	for index := range value.NumFields() {
		field := value.Field(index)
		if !field.Exported() || field.Anonymous() {
			return cty.NilType, fmt.Errorf("%s.%s: fields must be exported and non-embedded", path, field.Name())
		}
		name, omitted, omitempty, err := jsonField(field.Name(), value.Tag(index), path)
		if err != nil {
			return cty.NilType, err
		}
		if omitted {
			continue
		}
		fieldType := field.Type()
		if pointer, ok := fieldType.(*types.Pointer); ok {
			fieldType = pointer.Elem()
			omitempty = true
		}
		if _, exists := attributes[name]; exists {
			return cty.NilType, fmt.Errorf("%s: duplicate JSON field %q", path, name)
		}
		mapped, err := m.mapType(fieldType, path+"."+name)
		if err != nil {
			return cty.NilType, err
		}
		attributes[name] = mapped
		if omitempty {
			optional = append(optional, name)
		}
	}
	return cty.ObjectWithOptionalAttrs(attributes, optional), nil
}

func jsonField(fallback, tag, path string) (string, bool, bool, error) {
	value, ok := reflect.StructTag(tag).Lookup("json")
	if !ok {
		return fallback, false, false, nil
	}
	if value == "-" {
		return "", true, false, nil
	}
	parts := strings.Split(value, ",")
	name := parts[0]
	if !validJSONFieldName(name) {
		name = fallback
	}
	for _, option := range parts[1:] {
		if option != "" && option != "omitempty" {
			return "", false, false, fmt.Errorf("%s.%s: unsupported JSON tag option %q", path, name, option)
		}
	}
	return name, false, slices.Contains(parts[1:], "omitempty"), nil
}

func validJSONFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		allowedPunctuation := strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", character)
		if !allowedPunctuation && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}
