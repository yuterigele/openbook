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
	case "init":
		os.Exit(RunInit(os.Stdout, os.Args[2:]))
	case "migrate":
		os.Exit(RunMigrate(os.Stdout, os.Args[2:]))
	case "seed":
		os.Exit(RunSeed(os.Stdout, os.Args[2:]))
	case "backup":
		os.Exit(RunBackup(os.Stdout, os.Args[2:]))
	case "restore":
		os.Exit(RunRestore(os.Stdout, os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func printUsage(writer io.Writer) {
	_, _ = io.WriteString(writer, "用法: openbook <version|doctor|init|migrate|seed|backup|restore>\n")
}
