package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	protectionFlag = ".protect"
	stagingSuffix  = ".staging"
	backupSuffix   = ".backup"
)

type ForgePage struct {
	StoragePath string
	Owner       string
	Repo        string
}

func NewForgePage(owner, repo, addBasePath string) *ForgePage {
	lowerOwner := strings.ToLower(owner)
	lowerRepo := strings.ToLower(repo)
	lowerAddBasePath := "__ROOT__" // default, if no additional base path is provided
	if addBasePath != "" {
		lowerAddBasePath = strings.ToLower(addBasePath)
	}

	fp := &ForgePage{
		StoragePath: filepath.Join(
			config.ServePath,
			lowerOwner,
			lowerRepo,
			lowerAddBasePath,
		),
		Owner: lowerOwner,
		Repo:  lowerRepo,
	}
	return fp
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

func (fp *ForgePage) AddProtectionFlag() {
	os.Create(path.Join(fp.StoragePath, protectionFlag))
}

func (fp *ForgePage) HasProtectionFlag() bool {
	_, err := os.Stat(path.Join(fp.StoragePath, protectionFlag))
	return err == nil
}

func (fp *ForgePage) Purge() error {
	return os.RemoveAll(fp.StoragePath)
}

// Deploy extracts the uploaded tar.gz into a temporary staging directory
// and atomically swaps it with the current deployment. If anything fails
// during extraction, the existing deployment is left untouched.
func (fp *ForgePage) Deploy(r io.Reader, protect bool) error {
	staging := fp.StoragePath + stagingSuffix
	backup := fp.StoragePath + backupSuffix

	// clean up stale staging / backup leftovers from a previous crash
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(backup)

	// create parent directory if it does not exist yet
	if err := os.MkdirAll(filepath.Dir(staging), 0750); err != nil {
		return fmt.Errorf("could not create staging parent directory: %w", err)
	}

	// extract into staging area
	if err := extractTarGz(r, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}

	// honour explicit protect request
	if protect && !hasProtectionFlag(staging) {
		if _, err := os.Create(filepath.Join(staging, protectionFlag)); err != nil {
			_ = os.RemoveAll(staging)
			return fmt.Errorf("could not add protection flag: %w", err)
		}
	}

	// atomically swap staging into the live path
	if err := atomicReplace(fp.StoragePath, staging); err != nil {
		return fmt.Errorf("could not activate staged deployment: %w", err)
	}

	return nil
}

func hasProtectionFlag(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, protectionFlag))
	return err == nil
}

// atomicReplace moves source to dest. If dest already exists it is first
// moved to a backup location; the backup is removed after a successful swap.
// On failure the backup is restored so the previous content remains available.
func atomicReplace(dest, source string) error {
	// if there is no current deployment, a simple rename is enough
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return os.Rename(source, dest)
	}

	// move current deployment out of the way
	backup := dest + backupSuffix
	_ = os.RemoveAll(backup)
	if err := os.Rename(dest, backup); err != nil {
		return fmt.Errorf("failed to backup current deployment: %w", err)
	}

	// move staged deployment to live path
	if err := os.Rename(source, dest); err != nil {
		// try to restore the previous deployment
		if rbErr := os.Rename(backup, dest); rbErr != nil {
			return fmt.Errorf("failed to activate new deployment (%v) and rollback failed (%v)", err, rbErr)
		}
		_ = os.RemoveAll(source)
		return fmt.Errorf("failed to activate new deployment: %w", err)
	}

	// cleanup backup
	if err := os.RemoveAll(backup); err != nil {
		log.Printf("Warning: failed to remove deployment backup %s: %s", backup, err)
	}
	return nil
}

const (
	maxTarEntrySizeBytes = 5_242_880 // 5 MB
	maxNumOfTarEntries   = 2_500
)

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
			return fmt.Errorf("upacked more than %d files, stopping deployment", maxNumOfTarEntries)
		}

		// check single entry size (to prevent zip bombs)
		if header.Size > maxTarEntrySizeBytes {
			log.Printf("Entry %s has %d bytes (too big), stopping deployment", header.Name, header.Size)
			return fmt.Errorf("tar entry too large (%d bytes), stopping deployment", header.Size)
		}

		target := filepath.Join(dest, filepath.Clean(header.Name))
		// checking for path traversal
		if !strings.HasPrefix(target, dest) {
			log.Printf("Possible path traversal detected while unpacking tar.gz entry %s (to: %s), stopping deployment", header.Name, target)
			return fmt.Errorf("possible path traversal detected while unpacking tar.gz entry %s, stopping deployment", header.Name)
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
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
		// silently discards all other types (like symlinks)
	}

	return nil
}
