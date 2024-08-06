package main

import (
	"log"
	"os/exec"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		panic(err)
	}

	err := exec.Command("tern", "migrate", "--migrations", "./internal/store/pgstore/migrations", "--config", "./internal/store/pgstore/migrations/tern.conf").Run()
	if err != nil {
		log.Println("Error running tern migrate: ", err)
		panic(err)
	}
}
