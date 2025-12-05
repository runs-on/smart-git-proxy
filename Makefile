GO ?= mise exec -- go
BIN := bin/smart-git-proxy
PKG := ./...

.PHONY: all build lint test fmt tidy upload deploy bump

all: build

build:
	$(GO) build -o $(BIN) ./cmd/proxy

lint:
	golangci-lint run ./...

test:
	$(GO) test $(PKG)

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy

upload:
	AWS_PROFILE=runs-on-releaser aws s3 cp cloudformation/smart-git-proxy.yaml s3://runs-on/cloudformation/smart-git-proxy.yaml
	@echo "https://runs-on.s3.eu-west-1.amazonaws.com/cloudformation/smart-git-proxy.yaml"

deploy:
ifndef VPC_ID
	$(error VPC_ID is required)
endif
ifndef SUBNET_IDS
	$(error SUBNET_IDS is required)
endif
ifndef CLIENT_SG
	$(error CLIENT_SG is required. Usage: make deploy VPC_ID=vpc-xxx SUBNET_IDS=subnet-aaa,subnet-bbb CLIENT_SG=sg-xxx)
endif
	aws cloudformation deploy \
		--template-file cloudformation/smart-git-proxy.yaml \
		--stack-name smart-git-proxy \
		--parameter-overrides \
			VpcId=$(VPC_ID) \
			SubnetIds=$(SUBNET_IDS) \
			ClientSecurityGroupId=$(CLIENT_SG) \
			$(if $(PUBLIC_IP),AssignPublicIp=$(PUBLIC_IP),) \
			$(if $(INSTANCE_TYPE),InstanceType=$(INSTANCE_TYPE),) \
			$(if $(ROOT_VOLUME_SIZE),RootVolumeSize=$(ROOT_VOLUME_SIZE),) \
		--capabilities CAPABILITY_IAM

bump:
ifndef TAG
	$(error TAG is required. Usage: make bump TAG=0.2.0)
endif
	@echo "Bumping version to $(TAG)..."
	sed -i '' 's/Version: "[^"]*"/Version: "$(TAG)"/' cloudformation/smart-git-proxy.yaml
	git add cloudformation/smart-git-proxy.yaml
	git commit -m "Bump version to $(TAG)"
	git tag -a "v$(TAG)" -m "Release $(TAG)"
	git push origin main --tags
