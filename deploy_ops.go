package main

import (
	"os"
	"path"
	"path/filepath"
)

const protectionFlag = ".protected"

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
