package main

import (
	"log"
	"net/http"

	"github.com/tokopedia/panics"
)

func main() {
	panics.SetOptions(&panics.Options{
		Env:             "TEST",
		Filepath:        "./",
		SlackWebhookURL: "https://hooks.slack.com/services/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",
	})

	http.HandleFunc("/", panics.CaptureHandler(func(w http.ResponseWriter, r *http.Request) {
		panic("Duh aku panik nih guys")
	}))

	log.Fatal(http.ListenAndServe(":9000", nil))
}
