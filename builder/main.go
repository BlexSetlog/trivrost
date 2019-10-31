package main

import (
	"fmt"
	"os"
)

func main() {
	defer report()
	Generate()
}

func report() {
	if r := recover(); r != nil {
		fmt.Println(r)
		os.Exit(1)
	}
}
