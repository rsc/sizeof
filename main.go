// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Sizeof prints the size of named Go types in a package.
//
// Usage:
//
//	sizeof [-c] [-f] [-p path] [-v] [name...]
//
// Sizeof prints the size of Go types in a given package.
//
// If the -p option is given, sizeof compiles the package named by the import path.
// Otherwise it compiles the package in the current directory.
//
// If type names are given on the command line, sizeof prints the size of those types.
// Otherwise it prints the size of all named types in the package.
//
// If the -f option is given, sizeof also prints field locations for each type.
//
// If the -c option is given, sizeof ignores types and instead prints the values of integer constants.
//
// If the -v option is given, sizeof prints information about its internal operations.
//
// Sizeof builds the package using ``go build,'' so it uses the same operating system
// and architecture as ``go build'' does. To find the size on a different system,
// set GOOS and/or GOARCH.
//
// Example
//
// To find the size of regexp's Regexp:
//
//	cd $GOROOT/src/regexp
//	sizeof Regexp
//
// To find the size of regexp/syntax's Regexp, without going into that directory:
//
//	sizeof -p regexp/syntax Regexp
//
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	goroot   = runtime.GOROOT()
	compiler string
	runRE    *regexp.Regexp
)

var (
	flagConst   = flag.Bool("c", false, "show constant values")
	flagField   = flag.Bool("f", false, "show field offsets")
	flagPkg     = flag.String("p", "", "look up types in package named by `path`")
	flagVerbose = flag.Bool("v", false, "print debugging information")

	want []string
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: sizeof [-c] [-f] [-p path] [type...]\n")
	fmt.Fprintf(os.Stderr, "options:\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("sizeof: ")
	flag.Usage = usage
	flag.Parse()
	want = flag.Args()

	// Resolve -p option.
	dir := "."
	if *flagPkg != "" {
		out, err := exec.Command("go", "list", "-f", "{{.Dir}}", *flagPkg).CombinedOutput()
		if err != nil {
			if len(out) > 0 {
				log.Fatalf("%s", out)
			}
			log.Fatalf("go list: %v", err)
		}
		dir = strings.TrimSpace(string(out))
	}

	// Find information about package.
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}\n{{.Stale}}\n{{.SFiles}}\n{{.Name}}")
	cmd.Dir = dir
	outb, err := cmd.CombinedOutput()
	if err != nil {
		if len(outb) > 0 {
			log.Fatalf("%s", outb)
		}
		log.Fatalf("go list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(outb)), "\n")
	if len(lines) < 4 {
		log.Fatalf("go list: unexpected output")
	}
	pkg := lines[0]
	stale := lines[1] == "true"
	haveSFiles := lines[2] != "[]"
	packageName := lines[3]

	// Figure out how to get the asm header file.
	var tmp *os.File
	args := []string{"build"}
	if haveSFiles {
		// Go command already writes asmhdr file. Use that one.
		if *flagVerbose {
			log.Print("package has .s files; using -work")
		}
		args = append(args, "-work")
	} else {
		// Add -asmhdr explicitly.
		// This is used for every package being built,
		// but ours is built last and only after all the others,
		// so the repeated smashing of the file before then
		// is okay.
		if *flagVerbose {
			log.Print("package has no .s files; using -asmhdr")
		}
		f, err := ioutil.TempFile("", "rsc-io-sizeof-")
		if err != nil {
			log.Fatal(err)
		}
		tmp = f
		args = append(args, "-gcflags", "-asmhdr="+tmp.Name())
	}

	// Figure out how to force the build of the package.
	cleanup := ""
	if !stale {
		cleanup = filepath.Join(dir, "xxx_rsc_io_sizeof_tmp_.go")
		if *flagVerbose {
			log.Printf("package is not stale; writing %v", cleanup)
		}
		err := ioutil.WriteFile(cleanup, []byte("package "+packageName), 0666)
		if err != nil {
			if *flagVerbose {
				log.Printf("write failed: %v", err)
			}
			args = append(args, "-a")
		}
	}

	// Build.
	if *flagVerbose {
		log.Printf("go %v", strings.Join(args, " "))
	}
	cmd = exec.Command("go", args...)
	cmd.Dir = dir
	outb, err = cmd.CombinedOutput()
	if false && cleanup != "" {
		os.Remove(cleanup)
	}
	out := string(outb)
	workdir := ""
	if strings.HasPrefix(out, "WORK=") {
		i := strings.Index(out, "\n")
		if i >= 0 {
			workdir = out[len("WORK="):i]
			out = out[i+1:]
		}
	}
	if err != nil {
		if workdir != "" {
			os.RemoveAll(workdir)
		}
		if len(out) > 0 {
			log.Fatalf("%s", out)
		}
		log.Fatalf("go build: %v", err)
	}

	var data []byte
	if haveSFiles {
		if workdir == "" {
			log.Fatal("go build: cannot find work directory")
		}
		// Parse go_asm.h file left in work directory.
		hdr := workdir + "/" + pkg + "/_obj/go_asm.h"
		data, err = ioutil.ReadFile(hdr)
		//os.RemoveAll(workdir)
	} else {
		// Parse go_asm.h file written to f.
		data, err = ioutil.ReadFile(tmp.Name())
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if err != nil {
		log.Fatal(err)
	}

	inType := ""
	match := false
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "#define" {
			continue
		}
		val := f[2]
		if *flagConst {
			if strings.HasPrefix(f[1], "const_") {
				name := strings.TrimPrefix(f[1], "const_")
				if matchName(name) {
					fmt.Printf("%s %s\n", name, val)
				}
			}
			continue
		}
		if strings.HasSuffix(f[1], "__size") {
			inType = strings.TrimSuffix(f[1], "__size")
			match = matchName(inType)
			if match {
				fmt.Printf("%s %s\n", inType, val)
			}
			continue
		}
		if match && *flagField && strings.HasPrefix(f[1], inType+"_") {
			fmt.Printf("%s.%s %s\n", inType, f[1][len(inType)+1:], val)
		}
	}

	status := 0
	for _, name := range want {
		if name != "" {
			log.Printf("cannot find type %s", name)
			status = 1
		}
	}
	os.Exit(status)
}

func matchName(name string) bool {
	if len(want) == 0 {
		return true
	}
	for i, x := range want {
		if name == x {
			want[i] = ""
			return true
		}
	}
	return false
}
