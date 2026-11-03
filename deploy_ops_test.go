package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeTarGz(w io.Writer, files map[string]string) error {
	gw := gzip.NewWriter(w)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

func TestDeployCreatesNewPage(t *testing.T) {
	config = &Config{ServePath: t.TempDir()}
	fp := NewForgePage("owner", "repo", "")

	var buf bytes.Buffer
	if err := writeTarGz(&buf, map[string]string{"index.html": "hello"}); err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	if err := fp.Deploy(&buf, false); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(fp.StoragePath, "index.html"))
	if err != nil {
		t.Fatalf("failed to read deployed file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("unexpected content: %s", string(content))
	}
}

func TestDeployReplacesExistingPage(t *testing.T) {
	config = &Config{ServePath: t.TempDir()}
	fp := NewForgePage("owner", "repo", "")

	var first bytes.Buffer
	if err := writeTarGz(&first, map[string]string{"index.html": "old"}); err != nil {
		t.Fatalf("failed to create first tar.gz: %v", err)
	}
	if err := fp.Deploy(&first, false); err != nil {
		t.Fatalf("first deploy failed: %v", err)
	}

	var second bytes.Buffer
	if err := writeTarGz(&second, map[string]string{"index.html": "new"}); err != nil {
		t.Fatalf("failed to create second tar.gz: %v", err)
	}
	if err := fp.Deploy(&second, false); err != nil {
		t.Fatalf("second deploy failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(fp.StoragePath, "index.html"))
	if err != nil {
		t.Fatalf("failed to read deployed file: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("unexpected content after replace: %s", string(content))
	}

	if _, err := os.Stat(fp.StoragePath + stagingSuffix); !os.IsNotExist(err) {
		t.Fatalf("staging directory was not cleaned up: %s", fp.StoragePath+stagingSuffix)
	}
	if _, err := os.Stat(fp.StoragePath + backupSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup directory was not cleaned up: %s", fp.StoragePath+backupSuffix)
	}
}

func TestDeployFailedExtractionKeepsOldPage(t *testing.T) {
	config = &Config{ServePath: t.TempDir()}
	fp := NewForgePage("owner", "repo", "")

	var first bytes.Buffer
	if err := writeTarGz(&first, map[string]string{"index.html": "old"}); err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}
	if err := fp.Deploy(&first, false); err != nil {
		t.Fatalf("first deploy failed: %v", err)
	}

	broken := bytes.NewReader([]byte("not a tar.gz"))
	if err := fp.Deploy(broken, false); err == nil {
		t.Fatalf("expected deploy to fail with broken tar.gz")
	}

	content, err := os.ReadFile(filepath.Join(fp.StoragePath, "index.html"))
	if err != nil {
		t.Fatalf("failed to read deployed file after failed deploy: %v", err)
	}
	if string(content) != "old" {
		t.Fatalf("existing page was modified by failed deploy: %s", string(content))
	}
}

func TestDeployAddsProtectionFlag(t *testing.T) {
	config = &Config{ServePath: t.TempDir()}
	fp := NewForgePage("owner", "repo", "")

	var buf bytes.Buffer
	if err := writeTarGz(&buf, map[string]string{"index.html": "secret"}); err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	if err := fp.Deploy(&buf, true); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	if !fp.HasProtectionFlag() {
		t.Fatalf("protection flag was not created")
	}
}

func TestDeployKeepsProtectionFlagFromArchive(t *testing.T) {
	config = &Config{ServePath: t.TempDir()}
	fp := NewForgePage("owner", "repo", "")

	var buf bytes.Buffer
	if err := writeTarGz(&buf, map[string]string{
		"index.html":   "secret",
		protectionFlag: "",
	}); err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	if err := fp.Deploy(&buf, false); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	if !fp.HasProtectionFlag() {
		t.Fatalf("protection flag from archive was lost")
	}
}
