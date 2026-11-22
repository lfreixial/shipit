package main

import (
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// tagWorkflows reports whether any GitHub Actions workflow in the repo runs
// when a tag is pushed. known is false when that can't be worked out (an
// unreadable file), in which case shipit watches anyway.
func tagWorkflows(root string) (any, known bool) {
	var files []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", pat))
		files = append(files, m...)
	}
	known = true
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			known = false
			continue
		}
		var wf struct {
			On yaml.Node `yaml:"on"`
		}
		if err := yaml.Unmarshal(data, &wf); err != nil {
			known = false
			continue
		}
		if runsOnTagPush(&wf.On) {
			return true, true
		}
	}
	return false, known
}

// runsOnTagPush follows GitHub's rules: a push trigger with no filters, or
// with tag filters, fires for tags; one filtered only by branch doesn't.
func runsOnTagPush(on *yaml.Node) bool {
	switch on.Kind {
	case yaml.ScalarNode:
		return on.Value == "push"
	case yaml.SequenceNode:
		for _, n := range on.Content {
			if n.Value == "push" {
				return true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if on.Content[i].Value != "push" {
				continue
			}
			push := on.Content[i+1]
			if push.Kind != yaml.MappingNode {
				return true // `push:` with no filters
			}
			hasBranches, hasTags := false, false
			for j := 0; j+1 < len(push.Content); j += 2 {
				switch push.Content[j].Value {
				case "branches", "branches-ignore":
					hasBranches = true
				case "tags", "tags-ignore":
					hasTags = true
				}
			}
			return hasTags || !hasBranches
		}
	}
	return false
}
