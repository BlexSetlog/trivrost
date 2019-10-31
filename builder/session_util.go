package main

import (
	"fmt"
	"github.com/codeskyblue/go-sh"
	"path/filepath"
	"runtime"
)

func s() *sh.Session {
	session := sh.NewSession()
	session.ShowCMD = true
	return session
}

func run(session *sh.Session) {
	if err := session.Run(); err != nil {
		_, filePath, line, ok := runtime.Caller(1)
		if ok {
			err = fmt.Errorf("%s:%d: %w", filepath.Base(filePath), line, err)
		}
		panic(err)
	}
}
