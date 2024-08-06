package gen

//go:generate go run ./cmd/terndotenv/main.go
//go:generate sqlc generate -f ./internal/store/pgstore/sqlc.yaml
