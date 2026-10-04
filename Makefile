.PHONY: build test vet run

build:
	go build -o budgeteer ./cmd/budgeteer

test:
	go test ./...

vet:
	go vet ./...

# make run ARGS="account list"
run:
	go run ./cmd/budgeteer $(ARGS)
