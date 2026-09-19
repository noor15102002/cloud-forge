package selection

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestSelectionRejectsUnsafePathsAndFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "http"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "apps", "http", "Containerfile")
	if err := os.WriteFile(file, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := model.BuildSelection{App: "apps/http", Context: ".", Dockerfile: "apps/http/Containerfile"}
	if _, err := Resolve(root, good); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"", "../outside", "apps/../http", "/tmp", "https://example.com", "git@host:repo", "C:\\temp", "-f", "apps/--flag", "apps\nforged", "$(id)", ".git", strings.Repeat("a", 513)} {
		for _, field := range []string{"app", "dockerfile", "context"} {
			t.Run(field+"/"+unsafe, func(t *testing.T) {
				value := good
				switch field {
				case "app":
					value.App = unsafe
				case "dockerfile":
					value.Dockerfile = unsafe
				case "context":
					value.Context = unsafe
				}
				if _, err := Resolve(root, value); err == nil {
					t.Fatal("unsafe selection accepted")
				}
			})
		}
	}
	outside := t.TempDir()
	for _, target := range []string{outside, filepath.Join(root, "apps")} {
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		for _, value := range []model.BuildSelection{{App: "link", Context: ".", Dockerfile: good.Dockerfile}, {App: good.App, Context: "link", Dockerfile: good.Dockerfile}, {App: good.App, Context: ".", Dockerfile: "link/http/Containerfile"}} {
			if _, err := Resolve(root, value); err == nil {
				t.Fatal("symlink accepted")
			}
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, good); err == nil {
		t.Fatal("FIFO accepted")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, make([]byte, (2<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, good); err == nil {
		t.Fatal("oversized Dockerfile accepted")
	}
}

func TestSelectionNormalizesAndAllowsIndependentDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"apps/web app", "build context", "definitions"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "definitions/Production"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(root, model.BuildSelection{App: "./apps/web app/", Context: "./build context/", Dockerfile: "./definitions/Production"})
	if err != nil {
		t.Fatal(err)
	}
	if got.App != "apps/web app" || got.Context != "build context" || got.Dockerfile != "definitions/Production" {
		t.Fatalf("noncanonical selection: %#v", got)
	}
}
