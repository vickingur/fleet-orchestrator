.PHONY: check verify fmt vet lint test e2e build privacy hooks

check: lint verify

verify: fmt vet test privacy

fmt:
	@test -z "$$(gofmt -l cmd internal test)" || { gofmt -l cmd internal test; exit 1; }

vet:
	go vet ./...
	go vet -tags e2e ./test/e2e/

lint:
	golangci-lint run ./...

test:
	go test -race -cover -timeout 5m ./...
	python3 -B -m unittest discover -s scripts -p 'test_*.py'

privacy:
	python3 scripts/check-shareable.py --tracked

e2e:
	go test -tags e2e -count=1 -timeout 10m ./test/e2e/

build:
	go build -trimpath -o dist/fleet ./cmd/fleet

hooks:
	git config core.hooksPath .githooks
