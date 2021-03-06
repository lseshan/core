#
# test: run unit tests with coverage turned on.
# vet: run go vet for catching subtle errors.
# lint: run golint.
#

services = $$GOPATH/bin/root\
		   $$GOPATH/bin/agent\
		   $$GOPATH/bin/tenant\
		   $$GOPATH/bin/ipam\
		   $$GOPATH/bin/romana\
		   $$GOPATH/bin/policy\
		   $$GOPATH/bin/listener\
		   $$GOPATH/bin/watchnodes\
		   $$GOPATH/bin/doc\
		   $$GOPATH/bin/topology

UPX_VERSION := $(shell upx --version 2>/dev/null)

install:
	go list -f '{{.ImportPath}}' "./..." | \
		grep -v /vendor/ | xargs go install -ldflags \
		"-X github.com/romana/core/common.buildInfo=`git describe --always` \
		-X github.com/romana/core/common.buildTimeStamp=`date -u '+%Y-%m-%d_%I:%M:%S%p'`"

all: install fmt vet test

test:
	go list -f '{{.ImportPath}}' "./..." | \
		grep -v /vendor/ | xargs go test -timeout=30s -cover

testv:
	go list -f '{{.ImportPath}}' "./..." | \
		grep -v /vendor/ | xargs go test -v -timeout=30s -cover

vet:
	go list -f '{{.ImportPath}}' "./..." | \
		grep -v /vendor/ | xargs go vet

fmt:
	go list -f '{{.ImportPath}}' "./..." | \
		grep -v /vendor/ | xargs go fmt

lint:
	go list -f '{{.Dir}}' "./..." | \
		grep -v /vendor/ | xargs -n 1 golint

upx:
ifndef UPX_VERSION
	$(error "No upx in $(PATH), consider doing apt-get install upx")
endif
	upx $(services)

clean:
	rm $(services)

doc_main_file = $(shell go list -f '{{.Dir}}' "./..." |fgrep github.com/romana/core/tools/doc)/doc.go
root_dir = $(shell go list -f '{{.Root}}' "./..." | head -1)
swagger_dir_tmp = $(shell mktemp -d)
index=$(shell for svc in agent ipam root tenant topology policy; do echo '<li><a href=\"../index.html?url=/romana/'$$svc/$$svc.yaml'\">'$$svc'</a></li>'; echo; done)

swagger:	
	cd $(swagger_dir_tmp)
	echo In `pwd`
	go run $(doc_main_file) $(root_dir) > swagger.out 2>&1
	# If the above succeeded, we do not need to keep the output,
	# so remove it.
	rm swagger.out
	echo '<html><body><h1>Romana services</h1>' > doc/index.html
	echo '<ul>' >> doc/index.html
	echo "$(index)" >> doc/index.html
	echo '</ul>' >> doc/index.html	
	echo '</body></html>' >> doc/index.html
	cat index.html
	aws s3 sync --acl public-read doc s3://swagger.romana.io/romana
	echo Latest doc uploaded to http://swagger.romana.io.s3-website-us-west-1.amazonaws.com/romana/index.html
	rm -rf $(swagger_dir_tmp)

.PHONY: test vet lint all install clean fmt upx testv
