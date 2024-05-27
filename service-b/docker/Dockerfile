# Dockerfile for Service A
FROM golang:1.22

WORKDIR /app
COPY . .

RUN go mod download
RUN go build -o serviceB .

CMD ["./serviceB"]