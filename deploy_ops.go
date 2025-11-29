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

const protectionFlag = ".protect"

type ForgePage struct {
	BasePath string
}

func NewForgePage(owner, repo string) *ForgePage {
	return &ForgePage{
		BasePath: filepath.Join(config.ServePath, owner, filepath.Clean(repo)),
	}
}

func (fp *ForgePage) Exists() bool {
	entries, err := os.ReadDir(fp.BasePath)
	if err != nil {
		return false
	}
	if len(entries) > 0 {
		return true
	}
	return false
}

func (fp *ForgePage) AddProtectionFlag() {
	os.Create(path.Join(fp.BasePath, protectionFlag))
}

func (fp *ForgePage) HasProtectionFlag() bool {
	_, err := os.Stat(path.Join(fp.BasePath, protectionFlag))
	return err == nil
}

func (fp *ForgePage) Purge() error {
	return os.RemoveAll(fp.BasePath)
}

const maxTarEntrySizeBytes = 5_242_880 // 5 MB

func extractTarGz(r io.Reader, dest string) error {
	// gzip reader
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	// tar reader
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
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
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
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
	}

	return nil
}
