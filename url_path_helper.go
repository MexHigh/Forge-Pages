package main

import (
	"strings"
)

type URLPathHelper struct {
	NumOfElements int
	originalURL   string
	allParts      []string
}

func NewURLPathHelper(url string) *URLPathHelper {
	split := splitURLPath(url)
	u := &URLPathHelper{
		originalURL:   url,
		allParts:      split,
		NumOfElements: len(split),
	}
	return u
}

func (u *URLPathHelper) HasElement(elemIndex int) bool {
	return u.NumOfElements > elemIndex
}

func (u *URLPathHelper) GetElement(elemIndex int) string {
	if !u.HasElement(elemIndex) {
		return ""
	}
	return u.allParts[elemIndex]
}

func (u *URLPathHelper) GetElementsStartingFromElement(elemIndex int) string {
	return strings.Join(u.allParts[elemIndex:], "")
}

func splitURLPath(urlPath string) []string {
	urlPath = strings.TrimSpace(urlPath)
	urlPath = strings.Trim(urlPath, "/")

	if urlPath == "" {
		return []string{}
	}

	return strings.Split(urlPath, "/")
}
