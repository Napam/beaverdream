// Command mockllm is a deterministic OpenAI-compatible server for local
// development. Point OPENAI_BASE_URL at it and the board mutates without a real
// model: mentions of "matrix", "pink" or "brutalist" pick a canned theme.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/Napam/beaverdream/lib/mockllm"
)

func main() {
	addr := flag.String("addr", "localhost:6700", "listen address")
	flag.Parse()

	log.Printf("mock OpenAI server on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mockllm.Handler()); err != nil {
		log.Fatal(err)
	}
}
