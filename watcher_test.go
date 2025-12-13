//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rjeczalik/notify"
)

func TestNativeWatcherDetectsFileChanges(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "watcher-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Track detected events
	var mu sync.Mutex
	detectedFiles := make(map[string]bool)

	// Create event channel
	c := make(chan notify.EventInfo, 100)

	// Watch the temp directory recursively
	if err := notify.Watch(tmpDir+"/...", c, notify.All); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer notify.Stop(c)

	// Start event listener in background
	done := make(chan bool)
	go func() {
		timeout := time.After(5 * time.Second)
		for {
			select {
			case ei := <-c:
				event := ei.Event()
				if event&notify.Write != 0 || event&notify.Create != 0 {
					mu.Lock()
					relPath, _ := filepath.Rel(tmpDir, ei.Path())
					detectedFiles[relPath] = true
					t.Logf("Detected: %s (event: %s)", relPath, event.String())
					mu.Unlock()
				}
			case <-timeout:
				close(done)
				return
			}
		}
	}()

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Test 1: Create a new file in root
	testFile1 := filepath.Join(tmpDir, "test1.go")
	if err := os.WriteFile(testFile1, []byte("package main"), 0644); err != nil {
		t.Fatalf("failed to create test file 1: %v", err)
	}
	t.Log("Created test1.go")

	// Test 2: Create a file in subdirectory
	testFile2 := filepath.Join(subDir, "test2.sql")
	if err := os.WriteFile(testFile2, []byte("SELECT 1;"), 0644); err != nil {
		t.Fatalf("failed to create test file 2: %v", err)
	}
	t.Log("Created subdir/test2.sql")

	// Test 3: Modify existing file
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(testFile1, []byte("package main\n// modified"), 0644); err != nil {
		t.Fatalf("failed to modify test file 1: %v", err)
	}
	t.Log("Modified test1.go")

	// Test 4: Create a new subdirectory and file in it
	newSubDir := filepath.Join(tmpDir, "newsubdir")
	if err := os.MkdirAll(newSubDir, 0755); err != nil {
		t.Fatalf("failed to create new subdir: %v", err)
	}
	testFile3 := filepath.Join(newSubDir, "test3.graphql")
	if err := os.WriteFile(testFile3, []byte("type Query { test: String }"), 0644); err != nil {
		t.Fatalf("failed to create test file 3: %v", err)
	}
	t.Log("Created newsubdir/test3.graphql")

	// Wait for events to be processed
	<-done

	// Verify detections
	mu.Lock()
	defer mu.Unlock()

	t.Logf("\nDetected files: %v", detectedFiles)

	// Check that files were detected by looking for the filename in the paths
	expectedSuffixes := []string{
		"test1.go",
		"test2.sql",
		"test3.graphql",
	}

	for _, suffix := range expectedSuffixes {
		found := false
		for path := range detectedFiles {
			if filepath.Base(path) == suffix || filepath.Base(path) == filepath.Base(suffix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected to detect a file ending with %s but didn't", suffix)
		}
	}

	if len(detectedFiles) == 0 {
		t.Error("No files were detected at all - watcher may not be working")
	}
}

func TestNativeWatcherPerformance(t *testing.T) {
	// Create a temporary directory with many subdirectories
	tmpDir, err := os.MkdirTemp("", "watcher-perf-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create 100 subdirectories (simulating a medium-sized project)
	for i := 0; i < 100; i++ {
		subDir := filepath.Join(tmpDir, "dir"+string(rune('a'+i%26))+string(rune('0'+i/26)))
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create subdir %d: %v", i, err)
		}
		// Create a file in each directory
		testFile := filepath.Join(subDir, "file.go")
		if err := os.WriteFile(testFile, []byte("package test"), 0644); err != nil {
			t.Fatalf("failed to create test file in subdir %d: %v", i, err)
		}
	}

	// Measure time to set up watcher
	c := make(chan notify.EventInfo, 100)

	start := time.Now()
	if err := notify.Watch(tmpDir+"/...", c, notify.All); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	setupTime := time.Since(start)
	defer notify.Stop(c)

	t.Logf("FSEvents watcher setup time for 100 directories: %v", setupTime)

	// FSEvents should set up very quickly (typically < 10ms)
	if setupTime > 500*time.Millisecond {
		t.Errorf("Watcher setup took too long: %v (expected < 500ms)", setupTime)
	}

	// Verify it can detect changes
	detectedCount := 0
	done := make(chan bool)

	go func() {
		timeout := time.After(3 * time.Second)
		for {
			select {
			case ei := <-c:
				event := ei.Event()
				if event&notify.Write != 0 || event&notify.Create != 0 {
					detectedCount++
				}
			case <-timeout:
				close(done)
				return
			}
		}
	}()

	// Give watcher time to start
	time.Sleep(50 * time.Millisecond)

	// Create 10 files rapidly
	for i := 0; i < 10; i++ {
		testFile := filepath.Join(tmpDir, "rapid"+string(rune('0'+i))+".go")
		if err := os.WriteFile(testFile, []byte("package rapid"), 0644); err != nil {
			t.Fatalf("failed to create rapid test file %d: %v", i, err)
		}
	}

	<-done

	t.Logf("Detected %d file events", detectedCount)

	if detectedCount < 5 {
		t.Errorf("Expected to detect at least 5 rapid file changes, got %d", detectedCount)
	}
}

func TestWatcherFiltersByExtension(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "watcher-filter-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	var mu sync.Mutex
	detectedFiles := make(map[string]string) // path -> extension

	c := make(chan notify.EventInfo, 100)

	if err := notify.Watch(tmpDir+"/...", c, notify.All); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer notify.Stop(c)

	done := make(chan bool)
	go func() {
		timeout := time.After(3 * time.Second)
		for {
			select {
			case ei := <-c:
				event := ei.Event()
				if event&notify.Write != 0 || event&notify.Create != 0 {
					mu.Lock()
					ext := filepath.Ext(ei.Path())
					relPath, _ := filepath.Rel(tmpDir, ei.Path())
					detectedFiles[relPath] = ext
					mu.Unlock()
				}
			case <-timeout:
				close(done)
				return
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create files with different extensions
	testCases := map[string]string{
		"app.go":           ".go",
		"schema.graphql":   ".graphql",
		"migration.sql":    ".sql",
		"template.gohtml":  ".gohtml",
		"config.env":       ".env",
		"readme.md":        ".md",
		"image.png":        ".png",
	}

	for name := range testCases {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	<-done

	mu.Lock()
	defer mu.Unlock()

	t.Logf("Detected files with extensions: %v", detectedFiles)

	// Check that we detected the important file types
	importantExts := []string{".go", ".graphql", ".sql", ".gohtml", ".env"}
	for _, ext := range importantExts {
		found := false
		for _, detectedExt := range detectedFiles {
			if detectedExt == ext {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected to detect a %s file", ext)
		}
	}
}
