// Go template command
package main

/*
Are there any examples of wanting more than one type exported from the
same package? Possibly for functional type utilities.

Could import multiple types from the same package and the builder
would do the right thing.

Path generation for generated files could do with work - args may have
spaces in, may have upper and lower case characters which will fold
together on Windows.

Detect dupliace template definitions so we don't write them multiple times

write some test

manage all the generated files - find them - delete stale ones, etg

Put comment in generated file, generated by gotemplate from xyz on date?

do replacements in comments too?
*/

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
	"strings"
)

// Globals
var (
	// Flags
	verbose = flag.Bool("v", false, "Verbose - print lots of stuff")
)

// Logging function
var logf = log.Printf

// Log then fatal error
func fatalf(format string, args ...interface{}) {
	logf(format, args...)
	os.Exit(1)
}

// Log if -v set
func debugf(format string, args ...interface{}) {
	if *verbose {
		logf(format, args...)
	}
}

// Convert a Fileset and Ast into a string
//
// The file is passed through go format
func gofmtFile(fset *token.FileSet, f *ast.File) ([]byte, error) {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Ouput the go formatted file
//
// Exits with a fatal error on error
func outputFile(fset *token.FileSet, f *ast.File, path string) {
	source, err := gofmtFile(fset, f)
	if err != nil {
		fatalf("Failed to output '%s': %s", path, err)
	}
	err = ioutil.WriteFile(path, source, 0777)
	if err != nil {
		fatalf("Failed to write '%s': %s", path, err)
	}
}

// Parses a file into a Fileset and Ast
//
// Dies with a fatal error on error
func parseFile(path string) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet() // positions are relative to fset

	// Parse the file containing this very example
	// but stop after processing the imports.
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		fatalf("Failed to parse file: %s", err)
	}
	return fset, f
}

// Holds the desired templateInstantiation
type templateInstantiation struct {
	Package    string
	Name       string
	Args       []string
	NewPackage string
	Dir        string
}

// Parse the arguments string in Template(A, B, C)
//
// FIXME use the Go parser for this?
func parseArgs(s string) (args []string) {
	for _, arg := range strings.Split(s, ",") {
		arg = strings.TrimSpace(arg)
		args = append(args, arg)
	}
	return
}

// Returns true if haystack contains needle
func containsString(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// "template type Set(A)"
var matchTemplateType = regexp.MustCompile(`^/[*/]\s+template\s+type\s+(\w+)\((.*?)\)\s*$`)

// Parses the template file
func (ti *templateInstantiation) parse(inputFile string) {
	newIsPublic := ast.IsExported(ti.Name)

	fset, f := parseFile(inputFile)

	// Inspect the comments
	templateName := ""
	templateArgs := []string{}
	for _, cg := range f.Comments {
		for _, x := range cg.List {
			matches := matchTemplateType.FindStringSubmatch(x.Text)
			if matches != nil {
				if templateName != "" {
					fatalf("Found multiple template definitions in %s", inputFile)
				}
				templateName = matches[1]
				templateArgs = parseArgs(matches[2])
			}
		}
	}
	if templateName == "" {
		fatalf("Didn't find template definition in %s", inputFile)
	}
	if len(templateArgs) != len(ti.Args) {
		fatalf("Wrong number of arguments - template is expecting %d but %d supplied", len(ti.Args), len(templateArgs))
	}
	debugf("templateName = %v, templateArgs = %v", templateName, templateArgs)
	// debugf("Decls = %#v", f.Decls)
	// Find names which need to be adjusted
	namesToMangle := []string{}
	newDecls := []ast.Decl{}
	for _, Decl := range f.Decls {
		remove := false
		switch d := Decl.(type) {
		case *ast.GenDecl:
			// A general definition
			switch d.Tok {
			case token.IMPORT:
				// Ignore imports
			case token.CONST, token.VAR:
				if len(d.Specs) != 1 {
					log.Fatal("Unexpected specs on CONST/VAR")
				}
				v := d.Specs[0].(*ast.ValueSpec)
				for _, name := range v.Names {
					debugf("VAR or CONST %v", name.Name)
					namesToMangle = append(namesToMangle, name.Name)
				}
			case token.TYPE:
				if len(d.Specs) != 1 {
					log.Fatal("Unexpected specs on TYPE")
				}
				t := d.Specs[0].(*ast.TypeSpec)
				debugf("Type %v", t.Name.Name)
				namesToMangle = append(namesToMangle, t.Name.Name)
				// Remove type A if it is a template definition
				remove = containsString(t.Name.Name, templateArgs)
			default:
				logf("Unknown type %s", d.Tok)
			}
			debugf("GenDecl = %#v", d)
		case *ast.FuncDecl:
			// A function definition
			if d.Recv != nil {
				// No receiver == method - ignore this function
			} else {
				//debugf("FuncDecl = %#v", d)
				debugf("FuncDecl = %s", d.Name.Name)
				namesToMangle = append(namesToMangle, d.Name.Name)
				// Remove func A() if it is a template definition
				remove = containsString(d.Name.Name, templateArgs)
			}
		default:
			fatalf("Unknown Decl %#v", Decl)
		}
		if !remove {
			newDecls = append(newDecls, Decl)
		}
	}
	debugf("Names to mangle = %#v", namesToMangle)

	// Remove the stub type definitions "type A int" from the package
	f.Decls = newDecls

	// Make the name mappings
	mappings := make(map[string]string)

	// Map the type definitions A -> string, B -> int
	for i := range ti.Args {
		mappings[templateArgs[i]] = ti.Args[i]
	}

	// FIXME factor to method
	// FIXME put mappings as member
	addMapping := func(name string) {
		replacementName := ""
		if !strings.Contains(name, templateName) {
			// If name doesn't contain template name then just prefix it
			replacementName = name + ti.Name
			debugf("Top level definition '%s' doesn't contain template name '%s', using '%s'", name, templateName, replacementName)
		} else {
			replacementName = strings.Replace(name, templateName, ti.Name, 1)
		}
		// If new template name is not public then make sure
		// the exported name is not public too
		if !newIsPublic && ast.IsExported(replacementName) {
			replacementName = strings.ToLower(replacementName[:1]) + replacementName[1:]
		}
		mappings[name] = replacementName
	}

	found := false
	for _, name := range namesToMangle {
		if name == templateName {
			found = true
			addMapping(name)
		} else if _, found := mappings[name]; !found {
			addMapping(name)
		}

	}
	if !found {
		fatalf("No definition for template type '%s'", templateName)
	}
	debugf("mappings = %#v", mappings)

	newFile := f
	for name, replacement := range mappings {
		newFile = rewriteFile(fset, parseExpr(name, "pattern"), parseExpr(replacement, "replacement"), newFile)
	}

	// Change the package to the local package name
	f.Name.Name = ti.NewPackage

	// Output
	outputFileName := "gotemplate_" + ti.Name + ".go"
	outputFile(fset, newFile, outputFileName)
	logf("Written '%s'", outputFileName)
}

// Instantiate the template package
func (ti *templateInstantiation) instantiate() {
	p, err := build.Default.Import(ti.Package, ti.Dir, build.ImportMode(0))
	if err != nil {
		fatalf("Import %s failed: %s", ti.Package, err)
	}
	//debugf("package = %#v", p)
	debugf("Dir = %#v", p.Dir)
	// FIXME CgoFiles ?
	debugf("Go files = %#v", p.GoFiles)

	if len(p.GoFiles) == 0 {
		fatalf("No go files found for package '%s'", ti.Package)
	}
	// FIXME
	if len(p.GoFiles) != 1 {
		fatalf("Found more than one go file in '%s' - can only cope with 1 for the moment, sorry", ti.Package)
	}

	templateFilePath := path.Join(p.Dir, p.GoFiles[0])
	ti.parse(templateFilePath)
}

// usage prints the syntax and exists
func usage() {
	BaseName := path.Base(os.Args[0])
	fmt.Fprintf(os.Stderr,
		"Syntax: %s [flags] package_name parameter\n\n"+
			"Flags:\n\n",
		BaseName)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

var matchTemplateWithArgs = regexp.MustCompile(`^(\w+)\((.*?)\)\s*$`)

// Parse the arguments string Template(A, B, C)
//
// FIXME use the Go parser for this?
func parseTemplateAndArgs(s string) (name string, args []string) {
	matches := matchTemplateWithArgs.FindStringSubmatch(s)
	if matches == nil {
		fatalf("Bad template replacement string %q", s)
	}
	return matches[1], parseArgs(matches[2])
}

// findPackageName reads all the go packages in the curent directory
// and finds which package they are in
func findPackageName() string {
	p, err := build.Default.Import(".", ".", build.ImportMode(0))
	if err != nil {
		fatalf("Faile to read packages in current directory: %v", err)
	}
	return p.Name
}

func main() {
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) != 2 {
		fatalf("Need 2 arguments, package and parameters")
	}
	pkg := args[0]
	name, templateArgs := parseTemplateAndArgs(args[1])

	currentPackageName := findPackageName()

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Couldn't get wd: %v", err)
	}
	ti := &templateInstantiation{
		Package:    pkg,
		Name:       name,
		Args:       templateArgs,
		NewPackage: currentPackageName,
		Dir:        cwd,
	}
	logf("%s: substituting %q with %s(%s) into package %s", os.Args[0], ti.Package, ti.Name, strings.Join(ti.Args, ","), ti.NewPackage)

	ti.instantiate()
}
