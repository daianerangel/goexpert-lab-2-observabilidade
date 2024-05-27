FROM golang:1.22 as builder
WORKDIR /app
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -v -o serviceA

FROM scratch
WORKDIR /app
COPY --from=builder /app/serviceA .
ENTRYPOINT ["./serviceA"]