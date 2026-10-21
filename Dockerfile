FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/chainwatch ./cmd/bot

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/chainwatch /chainwatch
ENV APP_ENV=production
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/chainwatch"]
