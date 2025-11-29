package main

import "regexp"

func InsertStringAfterSubstring(str, subString, insertString string) string {
	schemeRe := regexp.MustCompile(subString)
	return schemeRe.ReplaceAllString(str, "${1}"+insertString)
}
