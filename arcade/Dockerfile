FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
COPY games ./games
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /arcade .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /arcade /arcade
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/arcade"]
