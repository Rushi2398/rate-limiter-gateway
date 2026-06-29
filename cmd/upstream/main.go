package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/get", getHandler)
	mux.HandleFunc("/users", usersHandler)
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:    ":8081",
		Handler: mux,
	}

	log.Println("upstream listening on :8081")
	log.Fatal(server.ListenAndServe())
}
