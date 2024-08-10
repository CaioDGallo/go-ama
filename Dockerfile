FROM golang:1.22.6-alpine

WORKDIR /app

COPY go.mod go.sum ./

COPY . .

RUN go mod tidy && \
    go build -o main ./cmd/gama && \
    chmod +x main

EXPOSE 8080

CMD [ "./main" ]
