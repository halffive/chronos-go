STATICCHECK := honnef.co/go/tools/cmd/staticcheck@2026.2.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0

.PHONY: check fmt-check vet lint test vuln cover

check: fmt-check vet lint test vuln

fmt-check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

vet:
	go vet ./...

lint:
	go run $(STATICCHECK) ./...

test:
	go test -race ./...

vuln:
	go run $(GOVULNCHECK) ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out
