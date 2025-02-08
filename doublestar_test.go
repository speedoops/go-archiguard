package main

import (
	"fmt"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

func TestMatch(t *testing.T) {
	patterns := []string{
		"**/app/**",
		"internal/**/app",
	}
	testPaths := []string{
		"app",
		"app/xxx",
		"/var/app",
		"../internal/some/nested/app",
		"/app/somefile.txt",
		"internal/app",
	}

	for _, pattern := range patterns {
		fmt.Printf("Pattern: %s\n", pattern)
		for _, path := range testPaths {
			matched, err := doublestar.Match(pattern, path)
			if err != nil {
				t.Errorf("invalid pattern: %v", err)
			} else {
				fmt.Printf("  `%s`: %v\n", path, matched)
			}

		}
		fmt.Println()
	}
}
