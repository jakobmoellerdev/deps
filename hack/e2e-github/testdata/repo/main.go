package main

import "github.com/spf13/cobra"

func main() { _ = (&cobra.Command{Use: "e2e"}).Execute() }
