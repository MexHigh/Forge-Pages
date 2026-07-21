package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"code.leon.wtf/leon/forge-pages/pathsafe"
	"github.com/google/uuid"
)

const protectionFlag = ".protect"

// ErrPathTraversal is returned (wrapped) when a storage path or tar entry
// would resolve outside its intended containment root.
var ErrPathTraversal = errors.New("path traversal detected")

// ErrTotalSizeExceeded is returned (wrapped) when the cumulative
// uncompressed bytes written across all entries of a single deployment's
// tar.gz would exceed the configured ceiling (see maxTotalExtractedBytesLimit).
// This guards against a request that stays under every existing per-file
// (maxTarEntrySizeBytes) and per-archive-file-count (maxNumOfTarEntries)
// limit individually but still totals an enormous amount of disk (e.g.
// 2500 files just under 5 MiB each is ~12 GiB).
var ErrTotalSizeExceeded = errors.New("cumulative extracted size exceeds configured maximum")

// ErrEntryTooLarge is returned (wrapped) when a single tar entry's declared
// size exceeds maxTarEntrySizeBytes (a zip-bomb-style guard on a single
// file, independent of the cumulative-total guard above).
var ErrEntryTooLarge = errors.New("tar entry exceeds maximum size")

// ErrTooManyEntries is returned (wrapped) when a tar.gz archive contains
// more than maxNumOfTarEntries entries, guarding against archives that try
// to exhaust resources via sheer file count rather than any single file's
// size.
var ErrTooManyEntries = errors.New("tar archive has too many entries")

type ForgePage struct {
	StoragePath string
	Owner       string
	Repo        string
}

// NewForgePage builds a ForgePage for owner/repo/addBasePath, validating each
// component as a single safe path label and confirming the resulting storage
// path stays contained within config.ServePath. owner, repo, and
// addBasePath are all attacker-influenced (deploy/delete API params, or the
// request subdomain/URL path on the read side) so every component must be
// validated before it is used to build a filesystem path.
func NewForgePage(owner, repo, addBasePath string) (*ForgePage, error) {
	if err := pathsafe.SafeLabel(owner); err != nil {
		return nil, fmt.Errorf("%w: invalid owner: %s", ErrPathTraversal, err)
	}
	if err := pathsafe.SafeLabel(repo); err != nil {
		return nil, fmt.Errorf("%w: invalid repo: %s", ErrPathTraversal, err)
	}

	lowerOwner := strings.ToLower(owner)
	lowerRepo := strings.ToLower(repo)
	lowerAddBasePath := "__ROOT__" // default, if no additional base path is provided
	if addBasePath != "" {
		if err := pathsafe.SafeLabel(addBasePath); err != nil {
			return nil, fmt.Errorf("%w: invalid additional_base_path: %s", ErrPathTraversal, err)
		}
		lowerAddBasePath = strings.ToLower(addBasePath)
	}

	storagePath, err := pathsafe.SecureJoin(
		config.ServePath,
		lowerOwner,
		lowerRepo,
		lowerAddBasePath,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPathTraversal, err)
	}

	fp := &ForgePage{
		StoragePath: storagePath,
		Owner:       lowerOwner,
		Repo:        lowerRepo,
	}
	return fp, nil
}

func (fp *ForgePage) Exists() bool {
	entries, err := os.ReadDir(fp.StoragePath)
	if err != nil {
		return false
	}
	if len(entries) > 0 {
		return true
	}
	return false
}

// AddProtectionFlag creates the empty ".protect" marker file inside the
// page's storage path, returning any error. It must fail closed: a caller
// that requested protection must abort (never publish an unprotected page)
// if the flag cannot be written, and the created descriptor is always closed
// rather than leaked.
func (fp *ForgePage) AddProtectionFlag() error {
	f, err := os.Create(path.Join(fp.StoragePath, protectionFlag))
	if err != nil {
		return err
	}
	return f.Close()
}

func (fp *ForgePage) HasProtectionFlag() bool {
	_, err := os.Stat(path.Join(fp.StoragePath, protectionFlag))
	return err == nil
}

func (fp *ForgePage) Purge() error {
	return os.RemoveAll(fp.StoragePath)
}

// deployLock is a single per-target lock entry: the mutex that serializes
// operations on the target, plus a refcount of how many goroutines are
// currently holding or waiting on it. The refcount lets lockDeployTarget
// remove the entry from deployLocks once nobody references it, keeping the
// map bounded rather than growing once per distinct target ever deployed.
type deployLock struct {
	mu   sync.Mutex
	refs int
}

// deployLocksMu guards deployLocks, a keyed-mutex table serializing
// deploy/delete operations per target StoragePath. Concurrent requests for
// different targets proceed independently; concurrent requests for the same
// target are serialized so a staged deploy's extract-then-swap sequence
// (see createStagingDir/promoteStaging) can never interleave with another
// deploy or a delete of the same target.
//
// Each entry is refcounted (see deployLock): the count is bumped under
// deployLocksMu on acquire and dropped under deployLocksMu on release, and
// the entry is deleted when it reaches zero. Doing both the count update and
// the deletion under deployLocksMu -- while the acquiring goroutine already
// holds a reference -- avoids the classic keyed-mutex race: a second
// goroutine that looks the key up sees refs>0 and reuses the same
// *deployLock, so the entry can never be deleted out from under a waiter and
// a third goroutine can never create a *second* lock for the same key while
// the first is still in use.
var (
	deployLocksMu sync.Mutex
	deployLocks   = map[string]*deployLock{}
)

// lockDeployTarget acquires (creating if necessary) the per-target lock for
// target and returns a function that releases it and drops the reference,
// removing the entry from deployLocks when no goroutine references it any
// longer. Callers must defer the returned function.
func lockDeployTarget(target string) func() {
	deployLocksMu.Lock()
	l, ok := deployLocks[target]
	if !ok {
		l = &deployLock{}
		deployLocks[target] = l
	}
	l.refs++
	deployLocksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		deployLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(deployLocks, target)
		}
		deployLocksMu.Unlock()
	}
}

// siblingCandidate computes one pathsafe.SecureJoin-validated candidate name
// for a sibling of target -- i.e. a path in target's parent directory --
// named ".<target-basename>.<kind>-<random>", without creating it or checking
// whether it already exists (each caller applies its own existence strategy
// and retries with a fresh candidate on collision). The random suffix is
// derived from a v4 UUID (separator-free, unguessable), so collision is
// astronomically unlikely; the callers' retry loops keep them correct rather
// than merely probably-correct.
//
// The leading dot is load-bearing: a real deployment directory name is a
// SafeLabel, which requires a leading alphanumeric, so it can never start with
// ".". That makes these temp names impossible to collide with any legitimate
// additional-base-path directory (e.g. a user's own "preview.old-release"),
// which the stale-sibling sweep must not delete. kind is expected to be
// "staging" or "old".
func siblingCandidate(target, kind string) (string, error) {
	parent := filepath.Dir(target)
	base := filepath.Base(target)
	suffix := strings.ReplaceAll(uuid.New().String(), "-", "")
	name := fmt.Sprintf(".%s.%s-%s", base, kind, suffix)
	candidate, err := pathsafe.SecureJoin(parent, name)
	if err != nil {
		return "", fmt.Errorf("computing %s sibling path: %w", kind, err)
	}
	return candidate, nil
}

// allocateSiblingDirName returns a dot-prefixed sibling path of target (see
// siblingCandidate) without creating it, retrying with a fresh suffix if the
// candidate name is already taken. It uses a Lstat-then-return existence
// check because the caller (promoteStaging's ".old-*" allocation) only needs
// a name it can rename onto, not an atomically-reserved directory.
func allocateSiblingDirName(target, kind string) (string, error) {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidate, err := siblingCandidate(target, kind)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not allocate unique %s sibling path for %s after %d attempts", kind, target, maxAttempts)
}

// createStagingDir creates and returns a sibling staging directory for
// target -- ".<target-basename>.staging-<random>" (see siblingCandidate) --
// on the same filesystem as target (a prerequisite for the atomic os.Rename
// swap in promoteStaging). It ensures target's parent directory exists first,
// since on a brand new deploy neither target nor its parent exist yet.
//
// Unlike allocateSiblingDirName, it reserves the name with an atomic os.Mkdir
// and retries on os.IsExist: the create IS the existence check, closing the
// TOCTOU window between "name is free" and "we created it" that a
// Lstat-then-create would open. Do not convert this to Lstat -- the atomic
// create is deliberate and race-safe.
func createStagingDir(target string) (string, error) {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0750); err != nil {
		return "", fmt.Errorf("creating parent directory %s: %w", parent, err)
	}
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidate, err := siblingCandidate(target, "staging")
		if err != nil {
			return "", err
		}
		if err := os.Mkdir(candidate, 0750); err == nil {
			return candidate, nil
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("creating staging directory: %w", err)
		}
	}
	return "", fmt.Errorf("could not allocate unique staging directory for %s after %d attempts", target, maxAttempts)
}

// promoteStaging atomically swaps a fully-populated, already-validated
// stagingPath into target's place:
//
//  1. if target already exists, rename it aside to a unique ".old-<random>"
//     sibling
//  2. rename stagingPath onto target -- each rename is individually atomic,
//     so a concurrent reader never observes *mixed* content (part old, part
//     new). It CAN, however, briefly observe target not existing at all:
//     between step 1's rename-away and step 2's rename-into-place, target is
//     absent from disk, so a concurrent page GET during that narrow window
//     sees a 404 rather than stale or new content.
//  3. best-effort remove the ".old-*" backup
//
// On any failure it preserves the previous deployment where possible --
// restoring it back to target by moving the ".old-*" backup back if step 2
// failed after step 1 succeeded, or, if that restore itself also fails,
// leaving it recoverable at the ".old-*" path (see the CRITICAL log branch
// below) -- and returns an error; stagingPath is left untouched on error so
// the caller (which already knows whether the operation failed) is
// responsible for removing it.
func promoteStaging(target, stagingPath string) error {
	_, statErr := os.Lstat(target)
	targetExisted := statErr == nil

	var oldPath string
	if targetExisted {
		var err error
		oldPath, err = allocateSiblingDirName(target, "old")
		if err != nil {
			return fmt.Errorf("allocating old-swap path: %w", err)
		}
		if err := os.Rename(target, oldPath); err != nil {
			return fmt.Errorf("moving current deployment aside: %w", err)
		}
	}

	if err := os.Rename(stagingPath, target); err != nil {
		if targetExisted {
			if rerr := os.Rename(oldPath, target); rerr != nil {
				// Both renames failed: target is missing or in an unknown
				// state, and oldPath is the only remaining intact copy of
				// the previous deployment. Leaving oldPath on disk here is
				// DELIBERATE, not a missed cleanup -- it is the sole
				// recovery path for an operator. A future refactor (e.g.
				// one that adds a "clean up leftover .old-*/.staging-*
				// dirs" pass, such as sweepStaleSiblingDirs below) must not
				// remove oldPath from *this* branch; doing so would destroy
				// the last good copy with nothing left to recover.
				log.Printf("CRITICAL: failed to restore previous deployment at %s after failed staging swap: swap error=%v, restore error=%v (previous deployment left at %s, new deployment left at %s)", target, err, rerr, oldPath, stagingPath)
				return fmt.Errorf("renaming staging into place failed (%w) and restoring the previous deployment also failed (%v); previous deployment preserved at %s, manual recovery required", err, rerr, oldPath)
			}
		}
		return fmt.Errorf("renaming staging into place: %w", err)
	}

	if targetExisted {
		if err := os.RemoveAll(oldPath); err != nil {
			log.Printf("Warning: deployed successfully but failed to remove old deployment backup %s: %v", oldPath, err)
		}
	}
	return nil
}

// sweepStaleSiblingDirs performs a best-effort removal of any pre-existing
// ".<target-basename>.staging-*" and ".<target-basename>.old-*" sibling
// directories of target. These accumulate when a previous handler
// invocation for this exact target was interrupted (process killed, panic
// recovered elsewhere, etc.) before its own cleanup ran -- see the
// panic-safety defer in handleNewDeployment and the best-effort removal in
// promoteStaging, neither of which can run if the process dies first.
//
// It only ever matches the dot-prefixed temp names this package generates
// (see allocateSiblingDirName/createStagingDir); a user's own additional
// base path can never start with "." (SafeLabel requires a leading
// alphanumeric), so a legitimate deployment directory -- even one named like
// "preview.old-release" -- is never swept.
//
// Callers must hold the per-target lock (lockDeployTarget) before calling
// this, since it is only safe to remove these directories when no other
// goroutine could be mid-flight using them for this target. It must only be
// called AFTER a deploy to this target has succeeded (see
// handleNewDeployment): running it earlier could destroy a ".old-*" recovery
// copy that a failed swap+restore deliberately left as the last known-good
// deployment, before the replacement is proven good.
//
// Note: a pre-existing ".old-*" sibling found here most likely means a
// previous promoteStaging call already logged a CRITICAL message and left
// it as the last known-good copy for manual operator recovery (see the
// double-rename-failure branch above). By the time a *new* deploy for the
// same target has succeeded, that recovery window is closed -- this sweep
// then removes it, consistent with its best-effort, never-block-the-deploy
// contract, so stale directories don't accumulate forever. An operator who
// needs to recover from a CRITICAL failure must do so before triggering
// another successful deploy to the same target.
//
// Errors are logged only, never returned: a sweep failure must never block
// a deploy.
func sweepStaleSiblingDirs(target string) {
	parent := filepath.Dir(target)
	base := filepath.Base(target)
	for _, kind := range []string{"staging", "old"} {
		matches, err := filepath.Glob(filepath.Join(parent, "."+base+"."+kind+"-*"))
		if err != nil {
			log.Printf("Warning: failed to glob stale %s siblings of %s: %v", kind, target, err)
			continue
		}
		for _, m := range matches {
			if err := os.RemoveAll(m); err != nil {
				log.Printf("Warning: failed to remove stale %s sibling %s: %v", kind, m, err)
				continue
			}
			log.Printf("Swept stale %s sibling directory %s", kind, m)
		}
	}
}

const (
	maxTarEntrySizeBytes = 5_242_880 // 5 MB
	maxNumOfTarEntries   = 2_500
)

// maxTotalExtractedBytesLimit returns the effective cumulative extraction
// ceiling: the configured config.MaxTotalExtractedBytes if it has been set
// to a positive value, otherwise defaultMaxTotalExtractedBytes. Reading the
// fallback here (rather than relying solely on Config.setDefaults having
// run) keeps the guard active for any caller -- including tests -- that
// builds/assigns a *Config directly without going through LoadConfig.
func maxTotalExtractedBytesLimit() int64 {
	if config != nil && config.MaxTotalExtractedBytes > 0 {
		return config.MaxTotalExtractedBytes
	}
	return defaultMaxTotalExtractedBytes
}

func extractTarGz(r io.Reader, dest string) error {
	// gzip reader
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	// tar reader
	tr := tar.NewReader(gz)

	totalFiles := 0
	var totalWritten int64
	totalLimit := maxTotalExtractedBytesLimit()
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		totalFiles++
		if totalFiles > maxNumOfTarEntries {
			log.Printf("Unpacked more than %d files, stopping deployment", maxNumOfTarEntries)
			return fmt.Errorf("%w: unpacked more than %d files, stopping deployment", ErrTooManyEntries, maxNumOfTarEntries)
		}

		// check single entry size (to prevent zip bombs)
		if header.Size > maxTarEntrySizeBytes {
			log.Printf("Entry %s has %d bytes (too big), stopping deployment", header.Name, header.Size)
			return fmt.Errorf("%w: tar entry too large (%d bytes), stopping deployment", ErrEntryTooLarge, header.Size)
		}

		// resolve the entry's target path, rejecting any entry (absolute
		// path, "..", or a mix of segments) that would resolve outside of
		// dest. Directory entries conventionally end in "/" (a trailing
		// empty segment after split), so trim that first; an entry that is
		// only slashes ("/", "//", ...) or empty resolves to dest itself.
		trimmedName := strings.TrimRight(header.Name, "/")
		target := dest
		if trimmedName != "" {
			entryParts := strings.Split(trimmedName, "/")
			t, err := pathsafe.SecureJoin(dest, entryParts...)
			if err != nil {
				log.Printf("Possible path traversal detected while unpacking tar.gz entry %s, stopping deployment: %s", header.Name, err)
				return fmt.Errorf("%w: tar entry %s: %s", ErrPathTraversal, header.Name, err)
			}
			target = t
		}
		log.Printf("Unpacking to %s", target)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return err
			}

			// Enforce the cumulative ceiling BEFORE writing, so disk can
			// overshoot by at most a single probe byte rather than by a
			// whole entry. The per-entry checks above (maxTarEntrySizeBytes,
			// maxNumOfTarEntries) each bound a single dimension but compose
			// to allow a huge total (2500 entries just under 5 MiB each is
			// ~12 GiB); this closes that gap.
			//
			// remaining is the budget left before the ceiling. Copying via
			// io.CopyN with a limit of remaining+1 writes at most one byte
			// past the budget: if the entry fits it copies fewer bytes and
			// returns io.EOF; if it is over budget it copies exactly
			// remaining+1 bytes, which then trips the check below. Net effect
			// on disk across the whole deploy: at most totalLimit+1 bytes.
			remaining := totalLimit - totalWritten
			if remaining <= 0 {
				log.Printf("Cumulative extracted size already at configured maximum %d bytes, stopping deployment", totalLimit)
				return fmt.Errorf("%w: cumulative extracted size exceeds maximum %d bytes", ErrTotalSizeExceeded, totalLimit)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			n, err := io.CopyN(f, tr, remaining+1)
			f.Close()
			if err != nil && err != io.EOF {
				return err
			}
			totalWritten += n
			if totalWritten > totalLimit {
				log.Printf("Cumulative extracted size exceeds configured maximum %d bytes, stopping deployment", totalLimit)
				return fmt.Errorf("%w: cumulative extracted size exceeds maximum %d bytes", ErrTotalSizeExceeded, totalLimit)
			}
		}
		// Silently discards all other types (symlinks, hardlinks, device
		// nodes, etc). This is load-bearing, not just tidy: SecureJoin's
		// containment check above is purely lexical (it validates the
		// *target* path string, it never resolves symlinks on disk). If a
		// future change re-enables extracting tar.TypeSymlink/TypeLink
		// entries, a two-step "zip-slip via symlink" becomes possible --
		// entry 1 plants a symlink whose target points outside dest (which
		// passes the lexical check because the symlink's own path is fine),
		// then entry 2 writes through that symlink to escape containment.
		// Re-enabling link extraction requires resolving the final path
		// with the symlink followed (e.g. os.Root / filepath.EvalSymlinks
		// against dest) before repeating this containment check.
	}

	return nil
}
