package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Version is a plain MAJOR.MINOR.PATCH release tag with an optional
// prefix such as "v" or "app-v". Pre-release and build suffixes are not
// treated as releases.
type Version struct {
	Prefix              string
	Major, Minor, Patch int
}

var tagRe = regexp.MustCompile(`^(.*?)(\d+)\.(\d+)\.(\d+)$`)

func parseTag(tag string) (Version, bool) {
	m := tagRe.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return Version{}, false
	}
	maj, _ := strconv.Atoi(m[2])
	min, _ := strconv.Atoi(m[3])
	pat, _ := strconv.Atoi(m[4])
	return Version{Prefix: m[1], Major: maj, Minor: min, Patch: pat}, true
}

func (v Version) String() string {
	return fmt.Sprintf("%s%d.%d.%d", v.Prefix, v.Major, v.Minor, v.Patch)
}

func (v Version) less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Bump kinds, in the order they are offered.
const (
	Patch = "patch"
	Minor = "minor"
	Major = "major"
)

var bumpKinds = []string{Patch, Minor, Major}

func (v Version) Bump(kind string) Version {
	switch kind {
	case Major:
		return Version{Prefix: v.Prefix, Major: v.Major + 1}
	case Minor:
		return Version{Prefix: v.Prefix, Major: v.Major, Minor: v.Minor + 1}
	default:
		return Version{Prefix: v.Prefix, Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}

// latestVersion picks the highest release tag. With prefix set, only tags
// with exactly that prefix count; otherwise tags prefixed with "v" or
// nothing do, so unrelated tags like "app-1.0.0" are ignored.
func latestVersion(tags []string, prefix string, prefixSet bool) (Version, string, bool) {
	var found []Version
	names := map[Version]string{}
	for _, t := range tags {
		v, ok := parseTag(t)
		if !ok {
			continue
		}
		if prefixSet && v.Prefix != prefix {
			continue
		}
		if !prefixSet && v.Prefix != "v" && v.Prefix != "" {
			continue
		}
		found = append(found, v)
		names[v] = t
	}
	if len(found) == 0 {
		return Version{}, "", false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].less(found[j]) })
	best := found[len(found)-1]
	return best, names[best], true
}
