package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: go run . <command> [flags]

commands:
  setup    create the index (analyzers, mappings, shards) behind the "wikipedia" alias
  ingest   fetch Wikipedia articles and bulk-index them
  serve    start the search web application (default http://localhost:8080)

environment:
  OPENSEARCH_URL   cluster address (default http://localhost:9200)`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "setup":
		err = cmdSetup(os.Args[2:])
	case "ingest":
		err = cmdIngest(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
