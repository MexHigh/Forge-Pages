package main

import (
	"os"
	"path"
	"path/filepath"
)

const protectionFlag = ".forge_protection"

type ForgePage struct {
	BasePath string
}

func NewForgePage(owner, repo string) *ForgePage {
	return &ForgePage{
		BasePath: filepath.Join(config.ServePath, owner, filepath.Clean(repo)),
	}
}

func (fp *ForgePage) AddProtectionFlag() {
	os.Create(path.Join(fp.BasePath, protectionFlag))
}

func (fp *ForgePage) HasProtectionFlag() bool {
	_, err := os.Stat(path.Join(fp.BasePath, protectionFlag))
	return err == nil
}
