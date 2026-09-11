.PHONY: build test check helm

build:
	go build ./cmd/...

test:
	go test ./...

check:
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"
	go vet ./...
	go test ./...

helm:
	helm lint charts/stayinformed-go --set credentials.existingSecret=example
	helm template example charts/stayinformed-go -f charts/stayinformed-go/examples/values.yaml >/dev/null
