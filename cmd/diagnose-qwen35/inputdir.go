package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Enumerate file names only; read/validate selected JSONs later through LoadFiles.
// Review files, nested folders and annotations are never opened here.
func externalInputs(directory, selection string) ([]string, []string, error) {
	fail := errors.New("invalid_external_input_selection")
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, nil, fail
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fail
	}
	files := map[string]string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, nil, fail
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if id == "" || seen[strings.ToLower(id)] {
			return nil, nil, fail
		}
		seen[strings.ToLower(id)] = true
		files[id] = filepath.Join(root, entry.Name())
	}
	first := []string{"syn-01", "syn-02", "syn-03", "syn-04", "syn-05", "syn-06"}
	ids := []string{}
	switch selection {
	case "first-six":
		ids = first
	case "remaining":
		for _, id := range first {
			if files[id] == "" {
				return nil, nil, fail
			}
		}
		for id := range files {
			if id != "syn-01" && id != "syn-02" && id != "syn-03" && id != "syn-04" && id != "syn-05" && id != "syn-06" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	default:
		ids = []string{selection}
	}
	if len(ids) == 0 || len(ids) > 18 {
		return nil, nil, fail
	}
	paths := []string{}
	for _, id := range ids {
		if files[id] == "" {
			return nil, nil, fail
		}
		paths = append(paths, files[id])
	}
	return ids, paths, nil
}
