.PHONY: example check verify fmt vet lint test e2e build docs-check privacy hooks

# This target never replaces a customized deployment inventory.
example:
	@test -f fleet.toml || cp fleet.example.toml fleet.toml
	@cmp -s fleet.example.toml fleet.toml || { echo "fleet.toml is customized; use a separate checkout for checks"; exit 1; }
	go run ./cmd/fleet docs

check: lint verify

verify: fmt vet test docs-check privacy

fmt:
	@test -z "$$(gofmt -l cmd internal policy test)" || { gofmt -l cmd internal policy test; exit 1; }

vet:
	go vet ./...
	go vet -tags e2e ./test/e2e/

lint:
	golangci-lint run ./...

test:
	go test -race -cover -timeout 5m ./...
	python3 -B -m unittest discover -s scripts -p 'test_*.py'

docs-check:
	go run ./cmd/fleet docs --check

privacy:
	python3 scripts/check-shareable.py --tracked

e2e:
	go test -tags e2e -count=1 -timeout 10m ./test/e2e/

build:
	go build -trimpath -o dist/fleet ./cmd/fleet

hooks:
	git config core.hooksPath .githooks
