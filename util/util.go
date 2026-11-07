package util

import (
	"strings"
	"time"
)

func Now() time.Time {
	return time.Now().UTC().Truncate(time.Second)
}

func NowPtr() *time.Time {
	v := time.Now().UTC().Truncate(time.Second)
	return &v
}

func MSI(args ...interface{}) map[string]interface{} {
	output := make(map[string]interface{})

	if len(args)%2 != 0 {
		panic("invalid number of arguments")
	}

	for i := 0; i < len(args); i += 2 {
		output[args[i].(string)] = args[i+1]
	}

	return output
}

func StrPtr(s string) *string {
	return &s
}

func IntPtr(i int) *int {
	return &i
}

func StrPtrOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func StrFromPtr(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

/*
func ValidFile(filename string) bool {
	fi, err := os.Stat(filename)

	if !fi.IsDir() {
	os.IsNotExist(err)
}
*/
