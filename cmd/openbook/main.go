package main

import (
	"fmt"
	"io"
	"os"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		RunVersion(os.Stdout)
	case "doctor":
		os.Exit(RunDoctor(os.Stdout, os.Getenv))
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func printUsage(writer io.Writer) {
	_, _ = io.WriteString(writer, "用法: openbook <version|doctor>\n")
}
