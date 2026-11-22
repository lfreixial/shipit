package main

import (
	"strings"
	"testing"
)

const uvPyproject = `[project]
name = "My_Package"
version = "2.0.1"
requires-python = ">=3.11"
`

const uvLock = `version = 1
requires-python = ">=3.11"

[[package]]
name = "idna"
version = "2.0.1"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "my-package"
version = "2.0.1"
source = { editable = "." }
dependencies = [
    { name = "idna" },
]
`

const initPy = `"""My package."""

__version__ = "2.0.1"

from .core import run
`

func TestPythonUVProject(t *testing.T) {
	dir := repo(t, map[string]string{
		"pyproject.toml":                  uvPyproject,
		"uv.lock":                         uvLock,
		"src/my_package/__init__.py":      initPy,
		"src/my_package/core/__init__.py": "__version__ = '2.0.1'\n", // too deep: not a version file
		"tests/__init__.py":               "",                        // no __version__: ignored
	})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Major: 2, Patch: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "pyproject.toml src/my_package/__init__.py uv.lock" {
		t.Fatalf("files = %q", got)
	}
	bumpAll(t, dir, files, "2.1.0")
	if got := read(t, dir, "uv.lock"); got != strings.Replace(uvLock, "name = \"my-package\"\nversion = \"2.0.1\"", "name = \"my-package\"\nversion = \"2.1.0\"", 1) {
		t.Errorf("uv.lock (idna must keep 2.0.1):\n%s", got)
	}
	if got := read(t, dir, "src/my_package/__init__.py"); got != strings.Replace(initPy, "2.0.1", "2.1.0", 1) {
		t.Errorf("__init__.py:\n%s", got)
	}
}

func TestPythonSetuptools(t *testing.T) {
	cfg := "[metadata]\nname = legacy\nversion = 0.9.0\n\n[options]\npackages = find:\n"
	py := "from setuptools import setup\n\nsetup(\n    name=\"legacy\",\n    version=\"0.9.0\",\n    python_requires=\">=3.8\",\n)\n"
	dir := repo(t, map[string]string{"setup.cfg": cfg, "setup.py": py, "legacy/__about__.py": "__version__: str = '0.9.0'\n"})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Minor: 9}, true)
	if err != nil || paths(files) != "legacy/__about__.py setup.cfg setup.py" {
		t.Fatalf("%q %v", paths(files), err)
	}
	bumpAll(t, dir, files, "1.0.0")
	if got := read(t, dir, "setup.cfg"); got != strings.Replace(cfg, "0.9.0", "1.0.0", 1) {
		t.Errorf("setup.cfg:\n%s", got)
	}
	if got := read(t, dir, "setup.py"); got != strings.Replace(py, "0.9.0", "1.0.0", 1) {
		t.Errorf("setup.py:\n%s", got)
	}
	if got := read(t, dir, "legacy/__about__.py"); got != "__version__: str = '1.0.0'\n" {
		t.Errorf("__about__.py: %q", got)
	}
}

func TestPythonDynamicVersion(t *testing.T) {
	// setuptools-scm / hatch-vcs: the version comes from the tag, so
	// there's nothing to bump.
	dir := repo(t, map[string]string{
		"pyproject.toml":  "[project]\nname = \"scm\"\ndynamic = [\"version\"]\n",
		"setup.cfg":       "[metadata]\nversion = attr: scm.__version__\n",
		"scm/__init__.py": "from importlib.metadata import version\n__version__ = version(\"scm\")\n",
	})
	files, err := findVersionFiles(dir, Config{}, Version{Prefix: "v", Major: 1}, true)
	if err != nil || len(files) != 0 {
		t.Fatalf("%+v %v", files, err)
	}
}

func TestNormalisePyName(t *testing.T) {
	if got := normalisePyName("My__Weird.Package-name"); got != "my-weird-package-name" {
		t.Errorf("got %q", got)
	}
}

func TestNeverDowngrade(t *testing.T) {
	// main already carries the next major version (httpx-style): a patch
	// release must not rewrite it backwards.
	m := model{
		repo:  RepoState{Latest: Version{Minor: 28, Patch: 1}, LatestTag: "0.28.1", HasLatest: true, Branch: "main"},
		files: []*VersionFile{{Path: "httpx/__version__.py", Kind: "py-version", Current: "1.0.0"}},
	}
	if b := m.blockers(m.newPlan(Patch)); len(b) == 0 || !strings.Contains(b[0], "newer than 0.28.2") {
		t.Fatalf("patch should be blocked, got %v", b)
	}
	if b := m.blockers(m.newPlan(Major)); len(b) != 0 {
		t.Fatalf("major (1.0.0) should be allowed, got %v", b)
	}
}
