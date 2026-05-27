FROM golang:1.26.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/e2e/h2c-backend ./test/e2e/h2c-backend
RUN go build -o /out/h2c-echo ./test/e2e/h2c-backend

FROM alpine:3.22
COPY --from=build /out/h2c-echo /h2c-echo
EXPOSE 8080
ENTRYPOINT ["/h2c-echo"]
