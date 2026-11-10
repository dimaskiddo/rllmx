BUILD_CGO_ENABLED  := 0
BUILD_TAGS         := nomsgpack
SERVICE_NAME       := rllmx
REBASE_URL         := "github.com/dimaskiddo/rllmx"
COMMIT_MSG         := "update improvement"

# A dependency that needs a newer Go must fail the build, not download a toolchain.
export GOTOOLCHAIN := local

VERSION_GIT := $(shell git describe --tags --abbrev=0 2>/dev/null | sed -e 's|^v||g')
VERSION     := $(if $(VERSION_GIT),$(VERSION_GIT),dev)

COMMIT_GIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
COMMIT      := $(if $(COMMIT_GIT),$(COMMIT_GIT),none)

.PHONY: init init-dist deps deps-download deps-verify tidy release publish build docker-build run vet test test-race test-pg mutant clean-dist clean-build clean-vendor clean-cache clean commit rebase push pull

.SILENT:

init:
	make clean
	GO111MODULE=on go mod init $(REBASE_URL)

init-dist:
	mkdir -p dist

deps:
	make clean-vendor
	make tidy
	make deps-download
	make deps-verify

deps-download:
	GO111MODULE=on go mod download

deps-verify:
	GO111MODULE=on go mod verify

tidy:
	GO111MODULE=on go mod tidy

release:
	make deps
	make clean-dist
	goreleaser release --parallelism 1 --clean --snapshot --skip=publish
	echo "Release '$(SERVICE_NAME)' complete, please check dist directory."

publish:
	make deps
	make clean-dist
	GITHUB_TOKEN=$(GITHUB_TOKEN) goreleaser release --parallelism 1 --clean
	echo "Publish '$(SERVICE_NAME)' complete, please check your repository releases."

build:
	make deps
	make init-dist
	CGO_ENABLED=$(BUILD_CGO_ENABLED) go build -tags $(BUILD_TAGS) -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -trimpath -o dist/$(SERVICE_NAME) .
	echo "Build '$(SERVICE_NAME)' complete."

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t dimaskiddo/rllmx:v$(VERSION) .
	echo "Docker Build '$(SERVICE_NAME)' complete."

run:
	go run -tags $(BUILD_TAGS) .

vet:
	go vet -tags $(BUILD_TAGS) ./...

test:
	CGO_ENABLED=$(BUILD_CGO_ENABLED) go test -tags $(BUILD_TAGS) -count=1 ./...

test-race:
	CGO_ENABLED=1 go test -tags $(BUILD_TAGS) -race -count=1 ./...

test-pg:
	@test -n "$(TEST_PG_DSN)" || { echo "TEST_PG_DSN is not set"; exit 1; }
	CGO_ENABLED=1 TEST_PG_DSN="$(TEST_PG_DSN)" go test -tags $(BUILD_TAGS) -race -count=1 ./...

mutant:
	tools/mutant.sh "$(LABEL)" "$(FILE)" "$(OLD)" "$(NEW)"

clean-dist:
	rm -rf dist

clean-build:
	rm -rf storage/app storage/logs storage/framework
	rm -f $(SERVICE_NAME)

clean-vendor:
	rm -rf vendor

clean-cache:
	go clean -cache -testcache

clean:
	make clean-dist
	make clean-build
	make clean-vendor
	make clean-cache

commit:
	make tidy
	make clean
	git add .
	git commit -am $(COMMIT_MSG)

rebase:
	rm -rf .git
	find . -type f -iname "*.go*" -exec sed -i -e "s%github.com/dimaskiddo/rllmx%$(REBASE_URL)%g" {} \;
	git init
	git remote add origin https://$(REBASE_URL).git

push:
	git push origin master

pull:
	git pull origin master
