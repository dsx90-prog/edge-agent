package filemanager

import (
	"os"
	"testing"
)

func TestLocalFileManager(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "filemanager_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fm, err := NewFileManager(Config{BasePath: tempDir})
	if err != nil {
		t.Fatalf("Failed to create file manager: %v", err)
	}

	// Test Upload
	testData := []byte("hello world")
	testPath := "subdir/test.txt"
	err = fm.UploadFile(testPath, testData)
	if err != nil {
		t.Errorf("UploadFile failed: %v", err)
	}

	// Test Download
	downloaded, err := fm.DownloadFile(testPath)
	if err != nil {
		t.Errorf("DownloadFile failed: %v", err)
	}
	if string(downloaded) != string(testData) {
		t.Errorf("Downloaded data mismatch: expected %s, got %s", string(testData), string(downloaded))
	}

	// Test List
	nodes, err := fm.ListDevFolders()
	if err != nil {
		t.Errorf("ListDevFolders failed: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("ListDevFolders returned empty list")
	}

	found := false
	for _, n := range nodes {
		if n.Name == "subdir" && n.IsDir {
			found = true
			if len(n.Children) == 0 || n.Children[0].Name != "test.txt" {
				t.Errorf("ListDevFolders child mismatch: %+v", n.Children)
			}
		}
	}
	if !found {
		t.Error("ListDevFolders did not find 'subdir'")
	}
}
