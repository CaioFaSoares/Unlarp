package sync

import (
	"os"
	"testing"
	"time"
)

func TestNormalizeSymlinkTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		rootDir  string
		expected string
	}{
		{
			name:     "Absolute target inside root",
			target:   "/a/b/c/d/file.txt",
			rootDir:  "/a/b/c",
			expected: "[root]/d/file.txt",
		},
		{
			name:     "Absolute target inside root with trailing slash root",
			target:   "/a/b/c/d/file.txt",
			rootDir:  "/a/b/c/",
			expected: "[root]/d/file.txt",
		},
		{
			name:     "Target is identical to root",
			target:   "/a/b/c",
			rootDir:  "/a/b/c",
			expected: "[root]",
		},
		{
			name:     "Target has substring prefix but not folder boundary",
			target:   "/a/b/c-other/file.txt",
			rootDir:  "/a/b/c",
			expected: "/a/b/c-other/file.txt",
		},
		{
			name:     "Absolute target outside root",
			target:   "/x/y/z",
			rootDir:  "/a/b/c",
			expected: "/x/y/z",
		},
		{
			name:     "Relative target",
			target:   "../other/file.txt",
			rootDir:  "/a/b/c",
			expected: "../other/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeSymlinkTarget(tt.target, tt.rootDir)
			if result != tt.expected {
				t.Errorf("normalizeSymlinkTarget(%q, %q) = %q; want %q", tt.target, tt.rootDir, result, tt.expected)
			}
		})
	}
}

func TestFileEntryChanged(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	tests := []struct {
		name     string
		fe1      FileEntry
		fe2      FileEntry
		expected bool
	}{
		{
			name: "Regular files identical",
			fe1: FileEntry{
				Mode:    0644,
				Size:    100,
				ModTime: now,
			},
			fe2: FileEntry{
				Mode:    0644,
				Size:    100,
				ModTime: now,
			},
			expected: false,
		},
		{
			name: "Regular files different size",
			fe1: FileEntry{
				Mode:    0644,
				Size:    100,
				ModTime: now,
			},
			fe2: FileEntry{
				Mode:    0644,
				Size:    200,
				ModTime: now,
			},
			expected: true,
		},
		{
			name: "Regular files different ModTime",
			fe1: FileEntry{
				Mode:    0644,
				Size:    100,
				ModTime: now,
			},
			fe2: FileEntry{
				Mode:    0644,
				Size:    100,
				ModTime: now.Add(time.Second),
			},
			expected: true,
		},
		{
			name: "One symlink, one regular file",
			fe1: FileEntry{
				Mode:          os.ModeSymlink | 0777,
				SymlinkTarget: "[root]/a",
			},
			fe2: FileEntry{
				Mode: 0644,
				Size: 100,
			},
			expected: true,
		},
		{
			name: "Symlinks identical",
			fe1: FileEntry{
				Mode:          os.ModeSymlink | 0777,
				SymlinkTarget: "[root]/a",
			},
			fe2: FileEntry{
				Mode:          os.ModeSymlink | 0777,
				SymlinkTarget: "[root]/a",
			},
			expected: false,
		},
		{
			name: "Symlinks different targets",
			fe1: FileEntry{
				Mode:          os.ModeSymlink | 0777,
				SymlinkTarget: "[root]/a",
			},
			fe2: FileEntry{
				Mode:          os.ModeSymlink | 0777,
				SymlinkTarget: "[root]/b",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fe1.Changed(tt.fe2)
			if result != tt.expected {
				t.Errorf("fe1.Changed(fe2) = %v; want %v", result, tt.expected)
			}
		})
	}
}
