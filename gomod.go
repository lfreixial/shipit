package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	goModuleRe = regexp.MustCompile(`(?m)^\s*module\s+"?([^\s"]+)"?`)
	majorSufRe = regexp.MustCompile(`/v([0-9]+)$`)
)

// goModule returns the module path in the repo's root go.mod, if any.
func goModule(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goModuleRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// goMajorProblem explains why Go would reject a tag for this module, or
// returns "". From v2 on, Go only accepts vN.x.y tags from a module whose
// path ends in /vN (https://go.dev/doc/modules/major-version).
func goMajorProblem(module string, next Version) string {
	if module == "" || strings.HasPrefix(module, "gopkg.in/") {
		return "" // gopkg.in encodes the major version as .vN
	}
	if next.Prefix != "v" {
		return fmt.Sprintf("Go only recognises tags starting with \"v\", so %s won't be usable as a version of %s", next, module)
	}
	have := 1
	if m := majorSufRe.FindStringSubmatch(module); m != nil {
		have, _ = strconv.Atoi(m[1])
	}
	want := max(next.Major, 1)
	if have == want {
		return ""
	}
	base := majorSufRe.ReplaceAllString(module, "")
	if want == 1 {
		return fmt.Sprintf("go.mod says module %s, which Go only accepts v%d tags for", module, have)
	}
	return fmt.Sprintf("Go needs module %s/v%d for a %s tag; update go.mod and its imports first", base, want, next)
}
