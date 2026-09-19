// Package selection resolves one workload's build inputs within a repository.
package selection

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/noor15102002/cloud-forge/internal/safefile"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Validate checks portable path syntax without accessing the filesystem.
func Validate(value model.BuildSelection) error {
	for _, name := range []string{value.App, value.Dockerfile, value.Context} {
		if name == "" || len(name) > 512 || strings.ContainsAny(name, "\\:$`\"*?[]{}<>|") || strings.HasPrefix(name, "/") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return errors.New("build paths must be bounded, repository-relative paths without URL, shell or control syntax")
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." || strings.HasPrefix(part, "-") || part == ".git" {
				return errors.New("build paths cannot contain parent traversal, option-like components or .git")
			}
		}
	}
	if path.Clean(value.Dockerfile) == "." {
		return errors.New("build.dockerfile must select a regular file")
	}
	return nil
}

// Resolve rejects symlinks in selected paths and validates metadata without
// opening FIFOs or executing build instructions. All returned paths are relative
// to root, independent of configuration-file placement and checkout location.
func Resolve(root string, value model.BuildSelection) (model.BuildSelection, error) {
	if err := Validate(value); err != nil {
		return model.BuildSelection{}, err
	}
	value.App, value.Dockerfile, value.Context = path.Clean(value.App), path.Clean(value.Dockerfile), path.Clean(value.Context)
	directory, err := os.OpenRoot(root)
	if err != nil {
		return model.BuildSelection{}, errors.New("cannot open repository boundary")
	}
	defer func() { _ = directory.Close() }()
	for _, item := range []struct {
		field, name string
		directory   bool
	}{{"app", value.App, true}, {"context", value.Context, true}, {"dockerfile", value.Dockerfile, false}} {
		prefix := ""
		for _, component := range strings.Split(item.name, "/") {
			prefix = path.Join(prefix, component)
			info, err := directory.Lstat(filepath.FromSlash(prefix))
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return model.BuildSelection{}, fmt.Errorf("build.%s must exist within the repository without symlink components", item.field)
			}
			if prefix != item.name && !info.IsDir() {
				return model.BuildSelection{}, fmt.Errorf("build.%s has a non-directory parent", item.field)
			}
			if prefix == item.name && ((item.directory && !info.IsDir()) || (!item.directory && !info.Mode().IsRegular())) {
				return model.BuildSelection{}, fmt.Errorf("build.%s has the wrong file type", item.field)
			}
		}
	}
	if _, err := safefile.Read(root, filepath.FromSlash(value.Dockerfile), 2<<20); err != nil {
		return model.BuildSelection{}, errors.New("selected Dockerfile must be a readable regular file of at most 2 MiB")
	}
	return value, nil
}
