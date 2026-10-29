FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /duck-hunt .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /duck-hunt /duck-hunt
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/duck-hunt"]
