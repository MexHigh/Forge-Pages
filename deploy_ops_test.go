package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestLockDeployTarget_MapStaysBounded proves the keyed-lock table does not
// grow unboundedly: after N sequential lock/unlock cycles on distinct targets
// the map returns to its baseline size, because each entry is refcounted and
// removed once no goroutine references it.
func TestLockDeployTarget_MapStaysBounded(t *testing.T) {
	deployLocksMu.Lock()
	baseline := len(deployLocks)
	deployLocksMu.Unlock()

	for i := 0; i < 1000; i++ {
		unlock := lockDeployTarget(fmt.Sprintf("/serve/owner/repo/target-%d", i))
		unlock()
	}

	deployLocksMu.Lock()
	got := len(deployLocks)
	deployLocksMu.Unlock()
	if got != baseline {
		t.Fatalf("deployLocks size = %d after 1000 unique lock/unlock cycles, want baseline %d", got, baseline)
	}
}

// TestLockDeployTarget_SerializesSameKey proves that two goroutines contending
// on the same target still serialize on the same underlying mutex while the
// refcount is above zero: the critical sections never overlap. Run with -race
// to also catch any data race in the refcount bookkeeping.
func TestLockDeployTarget_SerializesSameKey(t *testing.T) {
	const target = "/serve/owner/repo/same"
	var mu sync.Mutex // guards inside, detects overlap
	inside := 0
	overlap := false

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockDeployTarget(target)
			defer unlock()
			mu.Lock()
			inside++
			if inside != 1 {
				overlap = true
			}
			mu.Unlock()
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if overlap {
		t.Fatal("two goroutines held the same-key deploy lock concurrently")
	}
	deployLocksMu.Lock()
	_, present := deployLocks[target]
	deployLocksMu.Unlock()
	if present {
		t.Fatal("deployLocks entry for the contended key was not removed after all holders released")
	}
}

// TestAddProtectionFlag_ReturnsErrorOnCreateFailure proves AddProtectionFlag
// fails closed: when the ".protect" marker cannot be created (here because
// the storage path does not exist, so os.Create fails) it returns an error
// rather than silently discarding it, and creates no file. This is the
// property the deploy handler relies on to abort promotion instead of
// publishing an unprotected page.
func TestAddProtectionFlag_ReturnsErrorOnCreateFailure(t *testing.T) {
	// StoragePath points at a directory that does not exist, so os.Create of
	// "<missing>/.protect" fails with ENOENT.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	fp := &ForgePage{StoragePath: missing}

	if err := fp.AddProtectionFlag(); err == nil {
		t.Fatal("expected AddProtectionFlag to return an error when the flag cannot be created, got nil")
	}
	if _, err := os.Stat(filepath.Join(missing, protectionFlag)); !os.IsNotExist(err) {
		t.Fatalf("expected no .protect file to exist after a failed create, stat err=%v", err)
	}
}

// TestAddProtectionFlag_Succeeds proves the happy path: the marker file is
// created inside the storage path and the returned error is nil.
func TestAddProtectionFlag_Succeeds(t *testing.T) {
	dir := t.TempDir()
	fp := &ForgePage{StoragePath: dir}

	if err := fp.AddProtectionFlag(); err != nil {
		t.Fatalf("AddProtectionFlag() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, protectionFlag)); err != nil {
		t.Fatalf("expected .protect file to exist: %v", err)
	}
}

// buildTarGzEntries builds an in-memory .tar.gz archive of regular files
// named by names, each filled with size bytes of 'A', in the given order.
// Mirrors makeTarGz in deploy_api_test.go but lives here so extractTarGz
// unit tests don't need the full HTTP handler machinery.
func buildTarGzEntries(t *testing.T, names []string, sizes map[string]int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		size := sizes[name]
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(size)}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%q): %v", name, err)
		}
		if _, err := tw.Write(bytes.Repeat([]byte{'A'}, size)); err != nil {
			t.Fatalf("tar Write(%q): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

// withConfig temporarily swaps the global config for the duration of the
// test, restoring the previous value on cleanup.
func withConfig(t *testing.T, c *Config) {
	t.Helper()
	prev := config
	config = c
	t.Cleanup(func() { config = prev })
}

// TestMaxTotalExtractedBytesLimit_DefaultFallback proves the effective
// ceiling falls back to defaultMaxTotalExtractedBytes whenever config is nil
// or MaxTotalExtractedBytes is unset (zero value), and otherwise uses the
// configured value. This fallback matters because several existing tests
// (and any future ones) build a *Config directly without calling
// setDefaults, and the cumulative-size guard must still be active for them.
func TestMaxTotalExtractedBytesLimit_DefaultFallback(t *testing.T) {
	t.Run("nil config falls back to default", func(t *testing.T) {
		withConfig(t, nil)
		if got := maxTotalExtractedBytesLimit(); got != defaultMaxTotalExtractedBytes {
			t.Errorf("maxTotalExtractedBytesLimit() = %d, want default %d", got, defaultMaxTotalExtractedBytes)
		}
	})

	t.Run("zero-value MaxTotalExtractedBytes falls back to default", func(t *testing.T) {
		withConfig(t, &Config{})
		if got := maxTotalExtractedBytesLimit(); got != defaultMaxTotalExtractedBytes {
			t.Errorf("maxTotalExtractedBytesLimit() = %d, want default %d", got, defaultMaxTotalExtractedBytes)
		}
	})

	t.Run("configured value is used when positive", func(t *testing.T) {
		withConfig(t, &Config{MaxTotalExtractedBytes: 123})
		if got := maxTotalExtractedBytesLimit(); got != 123 {
			t.Errorf("maxTotalExtractedBytesLimit() = %d, want 123", got)
		}
	})
}

// TestExtractTarGz_CumulativeSizeLimit exercises extractTarGz directly
// (rather than through the full HTTP handler) so the ceiling can be
// pinpointed exactly, without writing hundreds of megabytes to disk in a
// unit test. All file sizes stay well under maxTarEntrySizeBytes (5 MiB) so
// only the new cumulative guard -- never the pre-existing per-file limit --
// is what trips.
func TestExtractTarGz_CumulativeSizeLimit(t *testing.T) {
	const ceiling = 10_000_000 // 10 MB custom ceiling, for a fast test
	withConfig(t, &Config{MaxTotalExtractedBytes: ceiling})

	t.Run("rejects when cumulative total exceeds the ceiling", func(t *testing.T) {
		dest := t.TempDir()
		names := []string{"a.bin", "b.bin", "c.bin"}
		sizes := map[string]int{"a.bin": 5_000_000, "b.bin": 5_000_000, "c.bin": 5_000_000} // 15 MB total > 10 MB ceiling
		body := buildTarGzEntries(t, names, sizes)

		err := extractTarGz(bytes.NewReader(body), dest)
		if err == nil {
			t.Fatal("expected error for cumulative size over ceiling, got nil")
		}
		if !errors.Is(err, ErrTotalSizeExceeded) {
			t.Errorf("expected errors.Is(err, ErrTotalSizeExceeded), got: %v", err)
		}
	})

	t.Run("succeeds when cumulative total is exactly at the ceiling", func(t *testing.T) {
		dest := t.TempDir()
		names := []string{"a.bin", "b.bin"}
		sizes := map[string]int{"a.bin": 5_000_000, "b.bin": 5_000_000} // exactly 10 MB == ceiling
		body := buildTarGzEntries(t, names, sizes)

		if err := extractTarGz(bytes.NewReader(body), dest); err != nil {
			t.Fatalf("expected success at exactly the ceiling, got error: %v", err)
		}
	})

	t.Run("fails when cumulative total is one byte over the ceiling", func(t *testing.T) {
		dest := t.TempDir()
		names := []string{"a.bin", "b.bin"}
		sizes := map[string]int{"a.bin": 5_000_000, "b.bin": 5_000_001} // 10,000,001 bytes, 1 over ceiling
		body := buildTarGzEntries(t, names, sizes)

		err := extractTarGz(bytes.NewReader(body), dest)
		if err == nil {
			t.Fatal("expected error for cumulative size one byte over ceiling, got nil")
		}
		if !errors.Is(err, ErrTotalSizeExceeded) {
			t.Errorf("expected errors.Is(err, ErrTotalSizeExceeded), got: %v", err)
		}
	})

	t.Run("succeeds when cumulative total is comfortably under the ceiling", func(t *testing.T) {
		dest := t.TempDir()
		names := []string{"a.bin", "b.bin"}
		sizes := map[string]int{"a.bin": 1_000_000, "b.bin": 1_000_000} // 2 MB, well under 10 MB ceiling
		body := buildTarGzEntries(t, names, sizes)

		if err := extractTarGz(bytes.NewReader(body), dest); err != nil {
			t.Fatalf("expected success comfortably under ceiling, got error: %v", err)
		}
	})

	t.Run("over-limit extraction writes at most ceiling+1 bytes to disk", func(t *testing.T) {
		dest := t.TempDir()
		// Three 5 MB entries (15 MB) against a 10 MB ceiling: the guard fires
		// before the whole third entry is written, so disk must overshoot by
		// at most a single probe byte (ceiling+1), never by a full entry.
		names := []string{"a.bin", "b.bin", "c.bin"}
		sizes := map[string]int{"a.bin": 5_000_000, "b.bin": 5_000_000, "c.bin": 5_000_000}
		body := buildTarGzEntries(t, names, sizes)

		err := extractTarGz(bytes.NewReader(body), dest)
		if !errors.Is(err, ErrTotalSizeExceeded) {
			t.Fatalf("expected ErrTotalSizeExceeded, got: %v", err)
		}
		var onDisk int64
		if werr := filepath.Walk(dest, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				onDisk += info.Size()
			}
			return nil
		}); werr != nil {
			t.Fatalf("walking dest: %v", werr)
		}
		if onDisk > ceiling+1 {
			t.Fatalf("on-disk bytes = %d, want at most ceiling+1 = %d", onDisk, ceiling+1)
		}
	})
}
