package filemanager

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// Config holds configuration for the file manager.
type Config struct {
	BasePath string `yaml:"base_path"`
}

// FileNode represents a file or directory in the file system tree.
type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"` // relative path
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size,omitempty"`
	ModTime  string     `json:"mod_time,omitempty"`
	Children []FileNode `json:"children,omitempty"`
}

// FileManager defines the interface for file operations.
type FileManager interface {
	// ListDevFolders returns a tree of files and folders starting from BasePath.
	ListDevFolders() ([]FileNode, error)
	// DownloadFile reads the file at the given relative path and returns its bytes.
	DownloadFile(relPath string) ([]byte, error)
	// UploadFile writes data to the given relative path, creating directories as needed.
	UploadFile(relPath string, data []byte) error
}

// LocalFileManager implements FileManager using the local filesystem.
type LocalFileManager struct {
	basePath string
}

// NewFileManager creates a new FileManager based on the provided configuration.
func NewFileManager(cfg Config) (FileManager, error) {
	if cfg.BasePath == "" {
		return nil, errors.New("base_path must be provided")
	}
	// Ensure the base path exists.
	info, err := os.Stat(cfg.BasePath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("base_path is not a directory")
	}
	return &LocalFileManager{basePath: cfg.BasePath}, nil
}

// ListDevFolders returns a tree of files and folders starting from BasePath.
func (fm *LocalFileManager) ListDevFolders() ([]FileNode, error) {
	return fm.listRecursive(fm.basePath, "", 0)
}

func (fm *LocalFileManager) listRecursive(absRoot, relPath string, depth int) ([]FileNode, error) {
	// Limit recursion depth
	if depth > 10 {
		return nil, nil
	}

	absPath := filepath.Join(absRoot, relPath)
	entries, err := ioutil.ReadDir(absPath)
	if err != nil {
		return nil, err
	}

	var nodes []FileNode
	for _, e := range entries {
		// Skip hidden files/folders
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		node := FileNode{
			Name:    e.Name(),
			Path:    filepath.Join(relPath, e.Name()),
			IsDir:   e.IsDir(),
			Size:    e.Size(),
			ModTime: e.ModTime().Format("2006-01-02 15:04:05"),
		}

		if e.IsDir() {
			children, err := fm.listRecursive(absRoot, node.Path, depth+1)
			if err == nil {
				node.Children = children
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// DownloadFile reads the file at the relative path.
func (fm *LocalFileManager) DownloadFile(relPath string) ([]byte, error) {
	// Prevent path traversal.
	cleanPath := filepath.Clean(relPath)
	if cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return nil, errors.New("invalid relative path")
	}
	absPath := filepath.Join(fm.basePath, cleanPath)
	return os.ReadFile(absPath)
}

// UploadFile writes data to the given relative path, creating directories as needed.
func (fm *LocalFileManager) UploadFile(relPath string, data []byte) error {
	cleanPath := filepath.Clean(relPath)
	if cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return errors.New("invalid relative path")
	}
	absPath := filepath.Join(fm.basePath, cleanPath)
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(absPath, data, 0644)
}
