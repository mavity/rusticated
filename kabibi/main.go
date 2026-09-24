package main

import (
	"log"

	"github.com/mavity/rusticated/kabibi/ui/app"
)

func main() {
	appWidget, host, err := app.Boot()
	if err != nil {
		log.Fatalf("kabibi boot error: %v", err)
	}
	_ = appWidget

	errRun := host.Run()
	if errRun != nil {
		log.Fatalf("kabibi run error: %v", errRun)
	}
}
