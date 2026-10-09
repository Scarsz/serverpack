// Command serverpack manages Git-tracked Minecraft server definitions and builds them into Docker images.
package main

import (
	"os"

	"github.com/Scarsz/serverpack/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
