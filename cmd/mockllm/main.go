// Command mockllm is a deterministic OpenAI-compatible server for local
// development. Point OPENAI_BASE_URL at it and the board mutates without a real
// model: mentions of "matrix", "pink" or "brutalist" pick a canned theme.
// Streamed replies arrive in small chunks with a pause between them, so the
// admin panel shows a turn being written.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/Napam/beaverdream/lib/mockllm"
)

func main() {
	addr := flag.String("addr", "localhost:6701", "listen address")
	delay := flag.Duration("delay", 15*time.Millisecond, "pause before each streamed chunk")
	flag.Parse()

	log.Printf("mock OpenAI server on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mockllm.SlowHandler(*delay)); err != nil {
		log.Fatal(err)
	}
}
